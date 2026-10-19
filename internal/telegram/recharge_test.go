package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

type rechargeTestRequest struct {
	ChannelUserID string `json:"channel_user_id"`
	Amount        string `json:"amount"`
	ChannelID     uint   `json:"channel_id"`
}

func rechargePick(app *App, value string) {
	draft := app.sessions.Get(42).Recharge
	formCallback(app, 42, draft.MessageID, "rechargepick:"+draft.ID+":"+value)
}

func rechargePay(app *App, channelID uint) {
	draft := app.sessions.Get(42).Recharge
	formCallback(app, 42, draft.MessageID, fmt.Sprintf("rechargepay:%s:%d", draft.ID, channelID))
}

func TestRechargePresetsFeesAndConcurrentSubmission(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.rechargePayment = &dujiao.WalletRechargePayment{Amount: "103.50", FeeAmount: "3.50", Currency: "CNY", Status: "success"}
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	draft := app.sessions.Get(42).Recharge
	if draft == nil || draft.MessageID == 0 || draft.Currency != "CNY" {
		t.Fatalf("recharge menu state = %#v", draft)
	}
	var keyboard models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(fixture.markups[0]), &keyboard); err != nil {
		t.Fatal(err)
	}
	var amounts []string
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if len(button.CallbackData) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
			if strings.HasPrefix(button.CallbackData, "rechargepick:") {
				amounts = append(amounts, strings.Split(button.CallbackData, ":")[2])
			}
		}
	}
	if strings.Join(amounts, ",") != "10.00,50.00,100.00,200.00,500.00,custom" {
		t.Fatalf("presets = %v", amounts)
	}
	rechargePick(app, "100.00")
	text := strings.Join(fixture.texts, "\n")
	for _, want := range []string{"100.00 CNY", "2.50% + 1.00 CNY", "3.50 CNY", "并非最终应付报价"} {
		if !strings.Contains(text, want) {
			t.Errorf("channel preview missing %q", want)
		}
	}
	draft = app.sessions.Get(42).Recharge
	data := "rechargepay:" + draft.ID + ":2"
	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() { formCallback(app, 42, draft.MessageID, data) })
	}
	wait.Wait()
	if len(fixture.rechargeRequests) != 1 || fixture.rechargeRequests[0] != (rechargeTestRequest{ChannelUserID: "42", Amount: "100.00", ChannelID: 2}) {
		t.Fatalf("recharge writes = %+v", fixture.rechargeRequests)
	}
	if app.sessions.Get(42).Recharge != nil {
		t.Fatal("submitted recharge retained")
	}
	text = strings.Join(fixture.texts, "\n")
	for _, want := range []string{"充值到账金额：100.00 CNY", "渠道手续费：3.50 CNY", "实付金额：103.50 CNY"} {
		if !strings.Contains(text, want) {
			t.Errorf("receipt missing %q", want)
		}
	}
	if strings.Contains(text, "107.00") {
		t.Fatal("fee was added twice")
	}
}

func TestRechargeCustomAmountAndValidation(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(app, "custom")
	draft := app.sessions.Get(42).Recharge
	if !draft.Custom || draft.MessageID == 0 || !strings.Contains(fixture.markups[len(fixture.markups)-1], `"force_reply":true`) {
		t.Fatal("custom amount did not request a reply")
	}
	for _, test := range []struct {
		text   string
		prompt int
	}{
		{"12.34", draft.MessageID - 1}, {"0", draft.MessageID}, {"-1", draft.MessageID}, {"1.234", draft.MessageID}, {"NaN", draft.MessageID}, {"1000000.01", draft.MessageID},
	} {
		app.handleUpdate(context.Background(), nil, giftCardMessage(test.text, test.prompt, 42, 42))
		if state := app.sessions.Get(42); state.Flow != "recharge_amount" || state.Recharge.Amount != "" {
			t.Fatalf("accepted invalid custom input %q", test.text)
		}
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("12.34", draft.MessageID, 42, 42))
	if got := app.sessions.Get(42); got.Flow != "recharge_channels" || got.Recharge.Amount != "12.34" || got.Recharge.MessageID == draft.MessageID {
		t.Fatalf("custom amount state = %#v", got)
	}
	rechargePay(app, 1)
	if len(fixture.rechargeRequests) != 1 || fixture.rechargeRequests[0].Amount != "12.34" {
		t.Fatalf("custom recharge requests = %+v", fixture.rechargeRequests)
	}
}

func TestRechargeRejectsStaleForeignAndForgedCallbacks(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	old := app.sessions.Get(42).Recharge
	rechargePick(app, "100.00")
	draft := app.sessions.Get(42).Recharge
	for _, data := range []string{"rechargepay:1:100.00", "rechargepay:" + old.ID + ":1", "rechargepay:" + draft.ID + ":99", "rechargepay:" + draft.ID + ":-1", "rechargepay:" + draft.ID + ":1.0"} {
		formCallback(app, 42, draft.MessageID, data)
	}
	formCallback(app, 43, draft.MessageID, "rechargepay:"+draft.ID+":1")
	formCallback(app, 42, draft.MessageID+1, "rechargepay:"+draft.ID+":1")
	if len(fixture.rechargeRequests) != 0 {
		t.Fatal("stale/foreign/forged callback created a recharge")
	}
	app.sessions.Update(42, func(state *session.State) { state.Recharge.ExpiresAt = time.Now().Add(-time.Second) })
	formCallback(app, 42, draft.MessageID, "rechargepay:"+draft.ID+":1")
	if len(fixture.rechargeRequests) != 0 || app.sessions.Get(42).Recharge != nil {
		t.Fatal("expired recharge was submitted")
	}
}

func TestRechargeNavigationAndPrivateChat(t *testing.T) {
	for _, command := range []string{"/cancel", "/wallet", "/language"} {
		t.Run(command, func(t *testing.T) {
			app, fixture := newFormFixture(t, nil)
			app.askRechargeAmount(context.Background(), 42, 42, false, 0)
			rechargePick(app, "50.00")
			draft := app.sessions.Get(42).Recharge
			app.handleUpdate(context.Background(), nil, giftCardMessage(command, 0, 42, 42))
			formCallback(app, 42, draft.MessageID, "rechargepay:"+draft.ID+":1")
			if app.sessions.Get(42).Recharge != nil || len(fixture.rechargeRequests) != 0 {
				t.Fatal("navigation retained a payable draft")
			}
		})
	}
	app, _ := newFormFixture(t, nil)
	app.askRechargeAmount(context.Background(), -100, 42, false, 0)
	if app.sessions.Get(42).Recharge != nil {
		t.Fatal("group chat opened recharge")
	}
}

func TestRechargeChannelFailureCanRetryButWriteFailureCannot(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	fixture.rechargeChannelsFail = true
	rechargePick(app, "50.00")
	if draft := app.sessions.Get(42).Recharge; draft == nil || draft.Submitting || draft.Amount != "" {
		t.Fatal("channel lookup failure stuck draft")
	}
	fixture.rechargeChannelsFail = false
	channels := fixture.rechargeChannels
	fixture.rechargeChannels = nil
	rechargePick(app, "50.00")
	if draft := app.sessions.Get(42).Recharge; draft == nil || draft.Submitting || draft.Amount != "" {
		t.Fatal("empty channels stuck draft")
	}
	fixture.rechargeChannels = channels
	rechargePick(app, "50.00")
	draft := app.sessions.Get(42).Recharge
	fixture.rechargeFailure = true
	data := "rechargepay:" + draft.ID + ":1"
	formCallback(app, 42, draft.MessageID, data)
	formCallback(app, 42, draft.MessageID, data)
	if len(fixture.rechargeRequests) != 1 || app.sessions.Get(42).Recharge != nil {
		t.Fatal("uncertain write was retried")
	}
	if !strings.Contains(strings.Join(fixture.texts, "\n"), "避免重复付款") {
		t.Fatal("uncertain result did not warn against duplicate payment")
	}
}

func TestRechargeReceiptFeePoliciesCurrenciesAndEscaping(t *testing.T) {
	for _, tc := range []struct {
		name, pay, fee, currency, status, want string
		absorbed                               bool
	}{
		{"free", "100.00", "0.00", "CNY", "success", "实付金额：100.00 CNY", false},
		{"customer", "103.50", "3.50", "CNY", "pending", "应付金额：103.50 CNY", false},
		{"merchant", "100.00", "3.50", "CNY", "paid", "实付金额：100.00 CNY", true},
		{"converted", "14.00", "3.50", "USD", "pending", "应付金额：14.00 USD", false},
		{"missing fee", "100.00", "", "CNY", "pending", "渠道手续费：商店未返回", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := dujiao.WalletRechargeResult{RechargeNo: "<unsafe>", Payment: dujiao.WalletRechargePayment{Amount: tc.pay, FeeAmount: tc.fee, Currency: tc.currency, Status: tc.status}}
			text := rechargeReceipt("zh-CN", "100.00", "CNY", result)
			if !strings.Contains(text, tc.want) || strings.Contains(text, "商家承担") != tc.absorbed || strings.Contains(text, "<unsafe>") || !strings.Contains(text, "充值到账金额：100.00 CNY") {
				t.Fatalf("receipt = %s", text)
			}
		})
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		if text := rechargeFeeRule(locale, "100.00", "CNY", dujiao.PaymentChannel{FeeRate: "bad", FixedFee: "1.00"}); text == "" || strings.Contains(text, "bad") {
			t.Errorf("invalid rule display: %s", text)
		}
	}
}

func TestRechargePendingPaymentRetainsServerPayableAndLink(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.rechargePayment = &dujiao.WalletRechargePayment{Amount: "103.50", FeeAmount: "3.50", Currency: "CNY", Status: "pending", PayURL: "https://pay.example.com/recharge"}
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(app, "100.00")
	rechargePay(app, 2)
	if text := fixture.texts[len(fixture.texts)-1]; !strings.Contains(text, "应付金额：103.50 CNY") || strings.Contains(text, "充值成功") {
		t.Fatalf("pending receipt = %s", text)
	}
	if !strings.Contains(fixture.markups[len(fixture.markups)-1], "https://pay.example.com/recharge") {
		t.Fatal("pending recharge payment link missing")
	}
}

func TestRechargeChannelPaginationKeepsMessagesBounded(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.rechargeChannels = nil
	for id := uint(1); id <= 12; id++ {
		fixture.rechargeChannels = append(fixture.rechargeChannels, dujiao.PaymentChannel{ID: id, Name: strings.Repeat("<", 1000), FeeRate: "2.50", FixedFee: "1.00"})
	}
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(app, "100.00")
	draft := app.sessions.Get(42).Recharge
	formCallback(app, 42, draft.MessageID, "rechargepage:"+draft.ID+":99")
	if app.sessions.Get(42).Recharge.ID != draft.ID {
		t.Fatal("invalid page changed draft")
	}
	formCallback(app, 42, draft.MessageID, "rechargepage:"+draft.ID+":1")
	next := app.sessions.Get(42).Recharge
	if next.Page != 1 || next.ID == draft.ID {
		t.Fatal("channel page did not advance or invalidate old buttons")
	}
	if text := fixture.texts[len(fixture.texts)-1]; len(text) > 4096 || strings.Count(text, "2.50%") != 5 {
		t.Fatalf("unbounded channel page: %d bytes", len(text))
	}
	formCallback(app, 42, draft.MessageID, "rechargepay:"+draft.ID+":1")
	if len(fixture.rechargeRequests) != 0 {
		t.Fatal("old page button created recharge")
	}
	rechargePay(app, 7)
	if len(fixture.rechargeRequests) != 1 || fixture.rechargeRequests[0].ChannelID != 7 {
		t.Fatal("channel selection on second page failed")
	}
}

func TestRechargeDirectInputAndRestart(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	app.handleUpdate(context.Background(), nil, giftCardMessage("0.01", 0, 42, 42))
	draft := app.sessions.Get(42).Recharge
	if draft.Amount != "0.01" || app.sessions.Get(42).Flow != "recharge_channels" {
		t.Fatal("direct amount input did not work")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/recharge", 0, 42, 42))
	next := app.sessions.Get(42).Recharge
	if next == nil || next.ID == draft.ID || next.Amount != "" {
		t.Fatal("restart did not invalidate old amount")
	}
	formCallback(app, 42, draft.MessageID, "rechargepay:"+draft.ID+":1")
	if len(fixture.rechargeRequests) != 0 {
		t.Fatal("restarted draft could still create recharge")
	}
}
