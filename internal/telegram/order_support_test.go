package telegram

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func TestOrderSupportTargetDraftAndFallback(t *testing.T) {
	for _, raw := range []string{"@synthetic_support", "t.me/synthetic_support", "https://t.me/synthetic_support", "http://telegram.me/synthetic_support/", "https://telegram.dog/synthetic_support?profile&text=OLD", "tg://resolve?domain=synthetic_support&profile"} {
		target := orderSupportTarget(raw, "订单号：DJ7\nIssue: delivery & usage")
		u, err := url.Parse(target.URL)
		if err != nil || !target.Prefilled || u.Scheme != "https" || u.Host != "t.me" || u.Path != "/synthetic_support" || u.Query().Get("text") != "订单号：DJ7\nIssue: delivery & usage" || u.Query().Has("profile") {
			t.Fatal("incorrect Telegram draft", raw)
		}
	}
	for _, raw := range []string{"https://support.example.invalid/tickets?lang=en#contact", "http://support.example.invalid:8080/help", "https://t.me/SyntheticHelpBot?start=existing", "https://t.me/m/example_slug", "https://t.me/synthetic_support?direct", "https://t.me.evil.example.invalid/support"} {
		target := orderSupportTarget(raw, "DJ7")
		if target.URL != raw || target.Prefilled || strings.Contains(target.URL, "DJ7") {
			t.Fatal("fallback was rewritten or order leaked into arbitrary URL", raw)
		}
	}
	if target := orderSupportTarget("tg://resolve?domain=SyntheticHelpBot&start=existing", "DJ7"); target.URL != "https://t.me/SyntheticHelpBot?start=existing" || target.Prefilled {
		t.Fatal("bot start link corrupted")
	}
	for _, raw := range []string{"", "javascript:alert(1)", "file:///tmp/test", "mailto:test@example.invalid", "https://", "https://x:bad", "https://x:99999", "https://user:pass@example.invalid", "https://t.me/synthetic_support#fragment", "https://t.me/%73ynthetic_support", "https://t.me:443/synthetic_support", "https://t.me/share/url?url=x", "https://t.me/proxy?server=x", "https://t.me/iv?url=x", "tg://proxy?server=x", "tg://resolve?domain=x&domain=y", "https://t.me/x?bad=%zz", "@name/evil", "https://host.example.invalid/a\nb", strings.Repeat("x", 2049)} {
		if target := orderSupportTarget(raw, "DJ7"); target.URL != "" {
			t.Fatal("invalid/system/credential link accepted", raw)
		}
	}
}

func TestOrderSupportMessageIsMinimalBoundedAndLocalized(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, reason := range []string{"p", "d", "r", "g"} {
			text := supportMessage(locale, strings.Repeat("9", 100), reason)
			if utf8.RuneCountInString(text) > 256 || !strings.Contains(text, supportReason(locale, reason)) {
				t.Fatal("copy button limit or reason missing")
			}
			if len(supportCallback(^uint(0), "h", 100000, reason)) > 64 {
				t.Fatal("callback limit")
			}
		}
	}
	for _, invalid := range []string{"", "<private>", strings.Repeat("9", 101), "DJ7\nSECRET"} {
		if supportMessage("zh-CN", invalid, "g") != "" {
			t.Fatal("invalid/truncated order number included")
		}
	}
}

func TestOrderSupportDetailsAndReasonButtonsUseOwnerScopedMinimalContext(t *testing.T) {
	a, f := newPendingFixture(t)
	a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}})
	f.order.Status = "partially_delivered"
	f.order.FulfillmentResult = "SECRET-CARD"
	f.order.FulfillmentInstructions = "SECRET-INSTRUCTIONS"
	f.order.Items = []dujiao.OrderDetailItem{{ProductTitle: "SECRET-TITLE", Instructions: "SECRET-ITEM"}}
	a.showOrderContext(context.Background(), 42, 10, 42, 7, "", "p", 2)
	buttons := f.markups[len(f.markups)-1]
	if findCallback(buttons, "support:") != "support:7:p:2:g" || !strings.Contains(supportURL(buttons), "text=") {
		t.Fatal("detail lacks direct draft/contact help")
	}
	a.sessions.Update(42, func(s *session.State) {
		s.Flow = "manual_form"
		s.Purchase = &session.PurchaseDraft{ID: "keep", ExpiresAt: time.Now().Add(time.Hour)}
		s.WithdrawalGuard = &session.WithdrawalGuard{ID: "keep", Status: "unknown"}
	})
	for _, reason := range []string{"g", "p", "d", "r"} {
		before := f.reads
		pendingClick(a, 42, 42, 10, "support:7:p:2:"+reason)
		if f.reads != before+1 {
			t.Fatal("support did not refresh owner-scoped order")
		}
		text, markup := f.texts[len(f.texts)-1], f.markups[len(f.markups)-1]
		if !strings.Contains(text, "DJ7") || !strings.Contains(text, supportReason("zh-CN", reason)) || !strings.Contains(text, "尚未全部交付") {
			t.Fatal("support context/guidance missing")
		}
		encoded, _ := json.Marshal(markup)
		if strings.Contains(text+string(encoded), "SECRET-") || strings.Contains(text+string(encoded), "10.00") {
			t.Fatal("sensitive order content in support")
		}
		u, err := url.Parse(supportURL(markup))
		if err != nil || u.Query().Get("text") != supportMessage("zh-CN", "DJ7", reason) || findCallback(markup, "order:") != "order:7:p:2" {
			t.Fatal("draft or back context wrong")
		}
		copies := 0
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if button.CopyText != nil {
					copies++
					if button.URL != "" || button.CallbackData != "" || utf8.RuneCountInString(button.CopyText.Text) > 256 {
						t.Fatal("invalid SDK copy button")
					}
				}
			}
		}
		if copies != 2 {
			t.Fatal("copy fallback absent")
		}
	}
	state := a.sessions.Get(42)
	if state.Flow != "manual_form" || state.Purchase == nil || state.WithdrawalGuard == nil || f.posts+f.cancels+f.photos != 0 {
		t.Fatal("support disrupted a draft/guard or executed business writes")
	}
	if strings.Contains(f.logs.text(), "SECRET-") || strings.Contains(f.logs.text(), "DJ7") {
		t.Fatal("support content logged")
	}
}

func supportURL(keyboard models.InlineKeyboardMarkup) string {
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if button.URL != "" {
				return button.URL
			}
		}
	}
	return ""
}

func TestOrderSupportRejectsForeignGroupMalformedAndMismatchingOrders(t *testing.T) {
	a, f := newPendingFixture(t)
	a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}})
	for _, tc := range []struct {
		uid, chat int64
		data      string
	}{{43, 43, "support:7:a:1:g"}, {42, -100, "support:7:a:1:g"}, {42, 42, "support:0:a:1:g"}, {42, 42, "support:bad:a:1:g"}, {42, 42, "support:18446744073709551616:a:1:g"}, {42, 42, "support:7"}} {
		pendingClick(a, tc.uid, tc.chat, 10, tc.data)
	}
	if f.reads != 0 {
		t.Fatal("invalid callback read order")
	}
	for _, text := range f.texts {
		if strings.Contains(text, "DJ7") {
			t.Fatal("unowned order context leaked")
		}
	}
	f.order.OrderID = 8
	pendingClick(a, 42, 42, 10, "support:7:a:1:g")
	if strings.Contains(f.texts[len(f.texts)-1], "DJ7") || supportURL(f.markups[len(f.markups)-1]) != "" {
		t.Fatal("mismatching order response used")
	}
}

func TestOrderSupportMissingWebsiteBotAndChangedConfiguration(t *testing.T) {
	a, f := newPendingFixture(t)
	for _, raw := range []string{"", "javascript:bad", "https://support.example.invalid/help", "https://t.me/SyntheticHelpBot?start=original", "https://t.me/m/test_slug", "@synthetic_support"} {
		a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: raw}})
		f.order.Status = "refunded"
		pendingClick(a, 42, 42, 10, "support:7:v:2:r")
		text := f.texts[len(f.texts)-1]
		if !strings.Contains(text, "退款进度") || !strings.Contains(text, "不自动退款") {
			t.Fatal("refund misleading")
		}
		url := supportURL(f.markups[len(f.markups)-1])
		if raw == "" || raw == "javascript:bad" {
			if url != "" || !strings.Contains(text, "尚未配置") {
				t.Fatal("missing contact became dead/misleading URL")
			}
		} else if url == "" {
			t.Fatal("updated support configuration not used")
		}
	}
	a.showHelp(context.Background(), 42, "zh-CN")
	if supportURL(f.markups[len(f.markups)-1]) != "https://t.me/synthetic_support" || supportURL(a.homeKeyboard("zh-CN")) != "https://t.me/synthetic_support" {
		t.Fatal("help/home do not share current contact")
	}
}

type unavailableSupportSessions struct{ session.Store }

func (s unavailableSupportSessions) Get(int64) session.State {
	return session.State{Err: session.ErrUnavailable}
}

func TestOrderSupportUnavailableStoreDoesNotProduceContext(t *testing.T) {
	a, f := newPendingFixture(t)
	a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}})
	f.orderReadFailure = true
	pendingClick(a, 42, 42, 10, "support:7:p:2:d")
	if strings.Contains(f.texts[len(f.texts)-1], "DJ7") || supportURL(f.markups[len(f.markups)-1]) != "" || strings.Contains(f.logs.text(), "PRIVATE-PAYMENT-ERROR") {
		t.Fatal("failed order lookup generated or logged private context")
	}
	before := f.reads
	a.sessions = unavailableSupportSessions{Store: a.sessions}
	pendingClick(a, 42, 42, 10, "support:7:p:2:d")
	if f.reads != before {
		t.Fatal("session outage did not stop support lookup")
	}
}
