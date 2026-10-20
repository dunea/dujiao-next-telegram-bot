package telegram

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func TestAffiliateCompactDashboardCopyAndRecordsShortcuts(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newWithdrawalFixture(t)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		formCallback(a, 42, 10, "affiliate")
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 4 || len(k.InlineKeyboard[0]) != 1 || len(k.InlineKeyboard[1]) != 2 {
			t.Fatal("affiliate primary and tools are not grouped")
		}
		share, err := url.Parse(k.InlineKeyboard[0][0].URL)
		copy := k.InlineKeyboard[1][0].CopyText
		if err != nil || share.Host != "t.me" || share.Path != "/share/url" || copy == nil || copy.Text != affiliateLink("test_bot", "AFF42") || share.Query().Get("url") != copy.Text || k.InlineKeyboard[1][1].CallbackData != "affapply" {
			t.Fatal("copy/share destinations or explicit withdrawal action changed")
		}
		for i := range 7 {
			f.affiliate.commissions = append(f.affiliate.commissions, dujiao.AffiliateCommission{ID: uint(i + 1), CommissionAmount: "1.00"})
			f.affiliate.withdrawals = append(f.affiliate.withdrawals, dujiao.AffiliateWithdrawal{ID: uint(i + 1), Amount: "1.00", Account: "PRIVATE-ACCOUNT"})
		}
		for _, kind := range []string{"affcomm", "affwithdraws"} {
			formCallback(a, 42, 10, kind+":2")
			k = formKeyboard(t, f)
			assertCompactKeyboard(t, k)
			if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 2 || findCallback(k, "affapply") != "affapply" || findCallback(k, kind+":1") != kind+":1" || findCallback(k, "affiliate") != "affiliate" || findCallback(k, "home") != "home" {
				t.Fatal("records lost direct apply, pagination or compact navigation")
			}
			other := "affwithdraws:1"
			if kind == "affwithdraws" {
				other = "affcomm:1"
			}
			if findCallback(k, other) != other || strings.Contains(f.texts[len(f.texts)-1], "PRIVATE-ACCOUNT") {
				t.Fatal("records lost switching or exposed account")
			}
		}
		if len(f.affiliate.opens) != 0 || len(f.affiliate.withdrawalRequests) != 0 || len(f.orders) != 0 {
			t.Fatal("rendering affiliate navigation performed business writes")
		}
	}
}

func TestAffiliateGuardedLayoutsNeverOfferReapplyOrUnlockImplicitly(t *testing.T) {
	for _, status := range []string{"checking", "posting", "unknown"} {
		a, f := newWithdrawalFixture(t)
		a.sessions.Update(42, func(s *session.State) { s.WithdrawalGuard = &session.WithdrawalGuard{ID: "guard", Status: status} })
		for _, callback := range []string{"affiliate", "affcomm:1", "affwithdraws:1"} {
			formCallback(a, 42, 200, callback)
			k := formKeyboard(t, f)
			assertCompactKeyboard(t, k)
			if findCallback(k, "affapply") != "" || a.sessions.Get(42).WithdrawalGuard == nil || len(f.affiliate.withdrawalRequests) != 0 {
				t.Fatal("guarded navigation offered reapply, cleared guard or submitted")
			}
			unlock := findCallback(k, "wdack:")
			if (unlock != "") != (callback == "affwithdraws:1" && status == "unknown") {
				t.Fatal("unlock no longer requires explicit history review")
			}
			for _, row := range k.InlineKeyboard {
				for _, b := range row {
					if strings.HasPrefix(b.CallbackData, "wdack:") && len(row) != 1 {
						t.Fatal("retry protection action packed beside navigation")
					}
				}
			}
		}
	}
}

func TestWithdrawalCompactChannelsReviewAndExitInvalidateOldConfirm(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newWithdrawalFixture(t)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		f.affiliate.dashboard.WithdrawChannels = []string{"channel A", "channel B", strings.Repeat("长渠道", 8)}
		formCallback(a, 42, 10, "affapply")
		withdrawalReply(a, "20")
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 2 || len(k.InlineKeyboard[1]) != 1 || k.InlineKeyboard[1][0].Text != f.affiliate.dashboard.WithdrawChannels[2] {
			t.Fatal("withdrawal channel grid clipped a long channel")
		}
		withdrawalAction(a, "channel:1")
		withdrawalReply(a, "SYNTHETIC-ACCOUNT")
		d := a.sessions.Get(42).Withdrawal
		k = formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if d.Channel != "channel B" || len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].CallbackData != "wd:"+d.ID+":confirm" || len(k.InlineKeyboard[2]) != 2 || findCallback(k, "affwithdraws:1") != "affwithdraws:1" || len(f.affiliate.withdrawalRequests) != 0 {
			t.Fatal("review lost channel identity, explicit confirmation or exit shortcut")
		}
		formCallback(a, 42, d.MessageID, "affwithdraws:1")
		formCallback(a, 42, d.MessageID, "wd:"+d.ID+":confirm")
		if a.sessions.Get(42).Withdrawal != nil || len(f.affiliate.withdrawalRequests) != 0 {
			t.Fatal("exit to history retained an actionable old confirmation")
		}
		result := withdrawalResultKeyboard(locale)
		assertCompactKeyboard(t, result)
		if len(result.InlineKeyboard) != 2 || len(result.InlineKeyboard[0]) != 1 || len(result.InlineKeyboard[1]) != 2 || findCallback(result, "affapply") != "" {
			t.Fatal("withdrawal result lost primary history or encourages blind resubmit")
		}
	}
}

func TestSupportCompactFooterAndIssueLabelsKeepFullDraftAndContext(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newPendingFixture(t)
		a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}})
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		for _, reason := range []string{"p", "d", "r", "g"} {
			pendingClick(a, 42, 42, 10, "support:7:p:2:"+reason)
			k := f.markups[len(f.markups)-1]
			assertCompactKeyboard(t, k)
			if len(k.InlineKeyboard) != 5 || len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].URL == "" || len(k.InlineKeyboard[4]) != 2 || k.InlineKeyboard[4][0].CallbackData != "support:7:p:2:"+reason || k.InlineKeyboard[4][1].CallbackData != "order:7:p:2" || !strings.Contains(f.texts[len(f.texts)-1], supportReason(locale, reason)) {
				t.Fatal("compact support lost primary, full issue draft or list context")
			}
			for i, key := range []string{"p", "d", "r", "g"} {
				b := k.InlineKeyboard[2+i/2][i%2]
				want := supportIssueLabel(locale, key)
				if key == reason {
					want = "✓ " + want
				}
				if b.Text != want || b.CallbackData != fmt.Sprintf("support:7:p:2:%s", key) {
					t.Fatal("short issue label changed selection or reason code")
				}
			}
		}
		if f.posts+f.cancels+f.photos != 0 {
			t.Fatal("support navigation wrote payment or order state")
		}
	}
}
