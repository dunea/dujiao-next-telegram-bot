package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func newWithdrawalFixture(t *testing.T) (*App, *formFixture) {
	t.Helper()
	app, fixture := newFormFixture(t, nil)
	fixture.affiliate.dashboard = dujiao.AffiliateDashboard{Opened: true, AffiliateCode: "AFF42", AvailableCommission: "100.00", PendingCommission: "50.00", WithdrawnCommission: "0.00", MinWithdrawAmount: json.Number("10"), WithdrawChannels: []string{"alipay", "usdt"}}
	return app, fixture
}

func withdrawalReply(app *App, text string) {
	draft := app.sessions.Get(42).Withdrawal
	app.handleUpdate(context.Background(), nil, giftCardMessage(text, draft.MessageID, 42, 42))
}

func withdrawalAction(app *App, action string) {
	draft := app.sessions.Get(42).Withdrawal
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":"+action)
}

func readyWithdrawal(app *App, account string) {
	formCallback(app, 42, 10, "affapply")
	withdrawalReply(app, "20")
	withdrawalAction(app, "channel:0")
	withdrawalReply(app, account)
}

func TestWithdrawalReviewAndConcurrentConfirmWritesOnce(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	account := "alice@example.com Alice <test>"
	readyWithdrawal(app, account)
	draft := app.sessions.Get(42).Withdrawal
	if draft == nil || draft.Step != "confirm" || draft.Amount != "20.00" || len(fixture.affiliate.withdrawalRequests) != 0 {
		t.Fatal("request submitted before confirmation")
	}
	text := fixture.texts[len(fixture.texts)-1]
	for _, want := range []string{"20.00 CNY", "alipay", "alice@example.com Alice &lt;test&gt;", "不代表已打款"} {
		if !strings.Contains(text, want) {
			t.Errorf("review missing %q: %s", want, text)
		}
	}
	var keyboard models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(fixture.markups[len(fixture.markups)-1]), &keyboard); err != nil {
		t.Fatal(err)
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if len(button.CallbackData) > 64 || strings.Contains(button.CallbackData, "alice") {
				t.Fatal("unsafe callback payload")
			}
		}
	}
	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() { formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm") })
	}
	wait.Wait()
	requests := fixture.affiliate.withdrawalRequests
	if len(requests) != 1 || requests[0] != (dujiao.AffiliateWithdrawalRequest{ChannelUserID: "42", Amount: "20.00", Channel: "alipay", Account: account}) {
		t.Fatalf("writes=%+v", requests)
	}
	state := app.sessions.Get(42)
	if state.Withdrawal != nil || state.WithdrawalGuard != nil {
		t.Fatal("sensitive draft or guard retained after success")
	}
	text = strings.Join(fixture.texts, "\n")
	if !strings.Contains(text, "提现申请已记录") || !strings.Contains(text, "待审核") {
		t.Fatal("missing submission receipt/status")
	}
}

func TestWithdrawalAmountAndReceivingValidation(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	formCallback(app, 42, 10, "affapply")
	for _, input := range []string{"0", "-1", "+10", "9.99", "100.01", "1.234", "1e2", "NaN", "1,000", "9999999999", ""} {
		withdrawalReply(app, input)
		if app.sessions.Get(42).Withdrawal.Step != "amount" {
			t.Fatalf("accepted invalid amount %q", input)
		}
	}
	withdrawalReply(app, "10.00")
	channel := app.sessions.Get(42).Withdrawal
	for _, action := range []string{"channel:-1", "channel:999", "channel:alipay", "confirm"} {
		formCallback(app, 42, channel.MessageID, "wd:"+channel.ID+":"+action)
	}
	if app.sessions.Get(42).Withdrawal.Step != "channel" {
		t.Fatal("forged channel or early confirm accepted")
	}
	withdrawalAction(app, "channel:1")
	for _, input := range []string{" ", "a\nb", "a\tb", "a\x00b", "a\u202eb", strings.Repeat("中", 201)} {
		withdrawalReply(app, input)
		if app.sessions.Get(42).Withdrawal.Step != "account" {
			t.Fatalf("accepted invalid account %q", input)
		}
	}
	withdrawalReply(app, "TRC20 T-example-address 收款人 Alice")
	if app.sessions.Get(42).Withdrawal.Step != "confirm" || len(fixture.affiliate.withdrawalRequests) != 0 {
		t.Fatal("receiving information bypassed second confirmation")
	}
}

func TestWithdrawalStaleForeignMessagesAndEdit(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	formCallback(app, 42, 10, "affapply")
	first := app.sessions.Get(42).Withdrawal
	app.handleUpdate(context.Background(), nil, giftCardMessage("20", first.MessageID+1, 42, 42))
	app.handleUpdate(context.Background(), nil, giftCardMessage("20", first.MessageID, 42, -100))
	if app.sessions.Get(42).Withdrawal.Step != "amount" {
		t.Fatal("wrong reply or group input accepted")
	}
	withdrawalReply(app, "20")
	withdrawalAction(app, "channel:0")
	withdrawalReply(app, "first@example.com")
	old := app.sessions.Get(42).Withdrawal
	formCallback(app, 43, old.MessageID, "wd:"+old.ID+":confirm")
	formCallback(app, 42, old.MessageID+1, "wd:"+old.ID+":confirm")
	withdrawalAction(app, "back")
	current := app.sessions.Get(42).Withdrawal
	if current.Step != "account" || current.Account != "" || current.ID == old.ID {
		t.Fatal("account edit retained old confirmation")
	}
	formCallback(app, 42, old.MessageID, "wd:"+old.ID+":confirm")
	withdrawalReply(app, "second@example.com")
	withdrawalAction(app, "restart")
	if draft := app.sessions.Get(42).Withdrawal; draft.Step != "amount" || draft.Account != "" || draft.Channel != "" || draft.Amount != "" {
		t.Fatal("start over retained sensitive data")
	}
	if len(fixture.affiliate.withdrawalRequests) != 0 {
		t.Fatal("stale or foreign confirmation created request")
	}
}

func TestWithdrawalNavigationExpiryAndCancellationInvalidateConfirmation(t *testing.T) {
	for _, action := range []string{"/cancel", "/wallet", "/affiliate", "/language", "button_cancel", "expire"} {
		t.Run(action, func(t *testing.T) {
			app, fixture := newWithdrawalFixture(t)
			readyWithdrawal(app, "alice@example.com")
			draft := app.sessions.Get(42).Withdrawal
			switch action {
			case "button_cancel":
				withdrawalAction(app, "cancel")
			case "expire":
				app.sessions.Update(42, func(state *session.State) { state.Withdrawal.ExpiresAt = time.Now().Add(-time.Second) })
			default:
				app.handleUpdate(context.Background(), nil, giftCardMessage(action, 0, 42, 42))
			}
			formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
			if state := app.sessions.Get(42); state.Withdrawal != nil || state.Flow == "withdrawal" || len(fixture.affiliate.withdrawalRequests) != 0 {
				t.Fatal("cancelled/expired draft submitted")
			}
		})
	}
}

func TestWithdrawalPreflightRevalidatesFundsMinimumChannelAndCurrency(t *testing.T) {
	for _, change := range []string{"balance", "minimum", "channel", "currency", "profile", "read_failure"} {
		t.Run(change, func(t *testing.T) {
			app, fixture := newWithdrawalFixture(t)
			readyWithdrawal(app, "alice@example.com")
			fixture.affiliate.mu.Lock()
			switch change {
			case "balance":
				fixture.affiliate.dashboard.AvailableCommission = "19.99"
			case "minimum":
				fixture.affiliate.dashboard.MinWithdrawAmount = "25"
			case "channel":
				fixture.affiliate.dashboard.WithdrawChannels = []string{"wechat"}
			case "profile":
				fixture.affiliate.dashboard.Opened = false
			case "read_failure":
				fixture.affiliate.failureCode = "unavailable"
				fixture.affiliate.failureStatus = 503
			}
			fixture.affiliate.mu.Unlock()
			if change == "currency" {
				fixture.mu.Lock()
				fixture.walletCurrency = "USD"
				fixture.mu.Unlock()
			}
			withdrawalAction(app, "confirm")
			state := app.sessions.Get(42)
			if len(fixture.affiliate.withdrawalRequests) != 0 || state.WithdrawalGuard != nil || state.Withdrawal != nil {
				t.Fatal("changed eligibility created request or left a guard")
			}
		})
	}
}

func TestWithdrawalBusinessRejectionsClearDraftWithoutRetryOrAccountLogs(t *testing.T) {
	for _, code := range []string{"affiliate_withdraw_insufficient", "affiliate_withdraw_amount_invalid", "affiliate_withdraw_channel_invalid", "validation_error", "affiliate_not_opened", "affiliate_disabled", "user_disabled"} {
		t.Run(code, func(t *testing.T) {
			app, fixture := newWithdrawalFixture(t)
			var logs bytes.Buffer
			app.logger = slog.New(slog.NewTextHandler(&logs, nil))
			account := "PRIVATE_ACCOUNT_12345"
			readyWithdrawal(app, account)
			fixture.affiliate.withdrawalFailureCode = code
			fixture.affiliate.withdrawalFailureStatus = 400
			draft := app.sessions.Get(42).Withdrawal
			withdrawalAction(app, "confirm")
			formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
			if state := app.sessions.Get(42); state.Withdrawal != nil || state.WithdrawalGuard != nil || len(fixture.affiliate.withdrawalRequests) != 1 {
				t.Fatal("rejection retained draft or repeated write")
			}
			if strings.Contains(logs.String(), account) || strings.Contains(fixture.texts[len(fixture.texts)-2], account) {
				t.Fatal("private account leaked through server error")
			}
		})
	}
}

func TestWithdrawalUnknownResultRequiresHistoryReviewAndExplicitUnlock(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			app, fixture := newWithdrawalFixture(t)
			readyWithdrawal(app, "PRIVATE_ACCOUNT_12345")
			if incomplete {
				fixture.affiliate.withdrawalResponse = &dujiao.AffiliateWithdrawal{}
			} else {
				fixture.affiliate.withdrawalFailureCode = "unavailable"
				fixture.affiliate.withdrawalFailureStatus = 503
			}
			withdrawalAction(app, "confirm")
			state := app.sessions.Get(42)
			if state.Withdrawal != nil || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "unknown" {
				t.Fatal("uncertain result did not retain account-free guard")
			}
			encoded, _ := json.Marshal(state)
			if strings.Contains(string(encoded), "PRIVATE_ACCOUNT") {
				t.Fatal("account retained after uncertain result")
			}
			guardID := state.WithdrawalGuard.ID
			app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
			formCallback(app, 42, 20, "affapply")
			formCallback(app, 42, 0, "wdack:"+guardID)
			if app.sessions.Get(42).WithdrawalGuard == nil || app.sessions.Get(42).Withdrawal != nil || len(fixture.affiliate.withdrawalRequests) != 1 {
				t.Fatal("navigation bypassed unknown-result lock")
			}
			formCallback(app, 42, 200, "affwithdraws:1")
			if !strings.Contains(fixture.markups[len(fixture.markups)-1], "wdack:"+guardID) {
				t.Fatal("history lacks deliberate recovery action")
			}
			formCallback(app, 43, 200, "wdack:"+guardID)
			formCallback(app, 42, 201, "wdack:"+guardID)
			if app.sessions.Get(42).WithdrawalGuard == nil {
				t.Fatal("foreign or stale history unlocked guard")
			}
			formCallback(app, 42, 200, "wdack:"+guardID)
			if app.sessions.Get(42).WithdrawalGuard != nil || len(fixture.affiliate.withdrawalRequests) != 1 {
				t.Fatal("review acknowledgement retried a write")
			}
			formCallback(app, 42, 20, "affapply")
			if app.sessions.Get(42).Withdrawal == nil || len(fixture.affiliate.withdrawalRequests) != 1 {
				t.Fatal("explicit recovery failed or submitted automatically")
			}
		})
	}
}

func TestWithdrawalInFlightGuardSurvivesCancelAndConcurrentRestart(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	readyWithdrawal(app, "alice@example.com")
	fixture.affiliate.withdrawalStarted = make(chan struct{}, 1)
	fixture.affiliate.withdrawalRelease = make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(fixture.affiliate.withdrawalRelease) }) }
	t.Cleanup(unblock)
	draft := app.sessions.Get(42).Withdrawal
	done := make(chan struct{})
	go func() { defer close(done); formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm") }()
	select {
	case <-fixture.affiliate.withdrawalStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("write not started")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
	formCallback(app, 42, 20, "affapply")
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	if state := app.sessions.Get(42); state.Withdrawal != nil || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "posting" {
		t.Fatal("in-flight guard lost on navigation")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submission did not finish")
	}
	if len(fixture.affiliate.withdrawalRequests) != 1 || app.sessions.Get(42).WithdrawalGuard != nil {
		t.Fatal("in-flight request duplicated or guard not cleared")
	}
}

func TestWithdrawalCustomChannelAndInvalidRulesFailClosed(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	fixture.affiliate.dashboard.WithdrawChannels = nil
	formCallback(app, 42, 10, "affapply")
	withdrawalReply(app, "20")
	if app.sessions.Get(42).Withdrawal.Step != "custom_channel" {
		t.Fatal("unrestricted channels not supported")
	}
	withdrawalReply(app, "PayPal")
	withdrawalReply(app, "alice@example.com")
	withdrawalAction(app, "confirm")
	if len(fixture.affiliate.withdrawalRequests) != 1 || fixture.affiliate.withdrawalRequests[0].Channel != "PayPal" {
		t.Fatal("custom channel not sent")
	}
	for _, bad := range []string{"not_opened", "missing_minimum", "bad_available", "bad_channel", "below_minimum"} {
		t.Run(bad, func(t *testing.T) {
			app, fixture := newWithdrawalFixture(t)
			switch bad {
			case "not_opened":
				fixture.affiliate.dashboard.Opened = false
			case "missing_minimum":
				fixture.affiliate.dashboard.MinWithdrawAmount = ""
			case "bad_available":
				fixture.affiliate.dashboard.AvailableCommission = "bad"
			case "bad_channel":
				fixture.affiliate.dashboard.WithdrawChannels = []string{"bad\nchannel"}
			case "below_minimum":
				fixture.affiliate.dashboard.AvailableCommission = "9.99"
			}
			formCallback(app, 42, 10, "affapply")
			if app.sessions.Get(42).Withdrawal != nil || len(fixture.affiliate.withdrawalRequests) != 0 {
				t.Fatal("invalid rules or insufficient commission opened draft")
			}
		})
	}
}

func TestWithdrawalTransportCancellationKeepsUnknownGuard(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	readyWithdrawal(app, "alice@example.com")
	fixture.affiliate.withdrawalStarted = make(chan struct{}, 1)
	fixture.affiliate.withdrawalRelease = make(chan struct{})
	t.Cleanup(func() { close(fixture.affiliate.withdrawalRelease) })
	draft := app.sessions.Get(42).Withdrawal
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.handleCallback(ctx, &models.CallbackQuery{ID: "test", From: models.User{ID: 42}, Data: "wd:" + draft.ID + ":confirm", Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: draft.MessageID, Chat: models.Chat{ID: 42}}}})
	}()
	select {
	case <-fixture.affiliate.withdrawalStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("POST did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request did not return")
	}
	state := app.sessions.Get(42)
	if state.Withdrawal != nil || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "unknown" {
		t.Fatal("transport cancellation allowed unsafe automatic retry")
	}
	if aCancelled := app.cancelWithdrawalInput(42, draft.ID); aCancelled {
		t.Fatal("in-flight write was reported as unsubmitted")
	}
}
