package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func formKeyboard(t *testing.T, f *formFixture) models.InlineKeyboardMarkup {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var k models.InlineKeyboardMarkup
	if len(f.markups) == 0 || json.Unmarshal([]byte(f.markups[len(f.markups)-1]), &k) != nil {
		t.Fatal("keyboard missing")
	}
	return k
}

func assertCompactKeyboard(t *testing.T, k models.InlineKeyboardMarkup) {
	t.Helper()
	for _, row := range k.InlineKeyboard {
		if len(row) == 0 || len(row) > 2 {
			t.Fatal("invalid compact column count")
		}
		for _, b := range row {
			actions := 0
			if b.CallbackData != "" {
				actions++
			}
			if b.URL != "" {
				actions++
			}
			if b.CopyText != nil {
				actions++
			}
			if actions != 1 || b.Text == "" || len(b.CallbackData) > 64 {
				t.Fatal("invalid SDK button")
			}
		}
	}
}

func TestWalletAndRechargeResultsHaveDirectCompactNavigation(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		a.showWallet(context.Background(), 42, 42, false, 0)
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].CallbackData != "recharge" || len(k.InlineKeyboard[1]) != 2 || k.InlineKeyboard[1][0].CallbackData != "walletlog:1" || k.InlineKeyboard[1][1].CallbackData != "giftcard" || k.InlineKeyboard[2][0].CallbackData != "cats" || k.InlineKeyboard[2][1].CallbackData != "home" {
			t.Fatal("wallet primary/secondary/navigation hierarchy changed")
		}
		result := rechargeResultKeyboard(locale)
		assertCompactKeyboard(t, result)
		for _, callback := range []string{"wallet", "walletlog:1", "cats", "orders:a:1"} {
			if findCallback(result, callback) != callback {
				t.Fatal("missing recharge shortcut", callback)
			}
		}
		if len(f.rechargeRequests) != 0 || len(f.orders) != 0 {
			t.Fatal("rendering navigation wrote business state")
		}
	}
}

func TestRechargeChannelGridKeepsExplicitOneTimeSubmission(t *testing.T) {
	a, f := newFormFixture(t, nil)
	f.rechargeChannels = []dujiao.PaymentChannel{{ID: 1, Name: "渠道 A"}, {ID: 2, Name: "渠道 B"}, {ID: 3, Name: strings.Repeat("长渠道", 8)}}
	a.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(a, "100.00")
	k := formKeyboard(t, f)
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard[0]) != 2 || len(k.InlineKeyboard[1]) != 1 || k.InlineKeyboard[1][0].Text != f.rechargeChannels[2].Name || len(f.rechargeRequests) != 0 {
		t.Fatal("channel grid clipped a provider or submitted without selection")
	}
	rechargePay(a, 2)
	if len(f.rechargeRequests) != 1 || f.rechargeRequests[0].ChannelID != 2 {
		t.Fatal("channel identity changed")
	}
}

func TestOrderListButtonsAreNumberedAndKeepContext(t *testing.T) {
	a, f := newPendingFixture(t)
	for i := 0; i < 5; i++ {
		f.list.Items = append(f.list.Items, dujiao.OrderListItem{OrderID: uint(i + 7), OrderNo: fmt.Sprintf("DJ%d", i+7), ProductTitle: "商品" + strings.Repeat("长", 50), Status: "pending_payment", TotalAmount: "1.00", Currency: "CNY"})
	}
	f.list.Total, f.list.TotalPages = 10, 2
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		pendingClick(a, 42, 42, 10, "orders:p:2")
		k := f.markups[len(f.markups)-1]
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 7 || len(k.InlineKeyboard[2]) != 2 {
			t.Fatal("order list is not compact")
		}
		for i := range 5 {
			callback := fmt.Sprintf("order:%d:p:2", i+7)
			if findCallback(k, callback) != callback {
				t.Fatal("order context or identity changed")
			}
		}
		if !(strings.HasPrefix(k.InlineKeyboard[2][0].Text, "1 · ") || k.InlineKeyboard[2][0].Text == "View order 1") || strings.Contains(k.InlineKeyboard[2][0].Text, "商品") || findCallback(k, "orders:a:1") != "orders:a:1" || findCallback(k, "orders:p:1") != "orders:p:1" || findCallback(k, "cats") != "cats" {
			t.Fatal("list labels or navigation unclear")
		}
	}
	if f.posts+f.cancels != 0 {
		t.Fatal("list layout performed a write")
	}
}

func TestOrderDetailGroupsToolsAndKeepsPrimaryAndSafeCancel(t *testing.T) {
	a, f := newPendingFixture(t)
	a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}})
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		pendingClick(a, 42, 42, 10, "order:7:p:2")
		k := f.markups[len(f.markups)-1]
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].CallbackData != "resume:7" || findCallback(k, "order:7:p:2") != "order:7:p:2" || findCallback(k, "orders:p:2") != "orders:p:2" || findCallback(k, "support:7:p:2:g") == "" || findCallback(k, "cancelask:7") == "" || findCallback(k, "cancelgo:") != "" {
			t.Fatal("detail lost context/primary or bypassed cancellation review")
		}
	}
	pendingClick(a, 42, 42, 10, "cancelask:7")
	k := f.markups[len(f.markups)-1]
	if len(k.InlineKeyboard) != 2 || len(k.InlineKeyboard[0]) != 1 || len(k.InlineKeyboard[1]) != 1 || !strings.HasPrefix(k.InlineKeyboard[0][0].CallbackData, "cancelback:") || !strings.HasPrefix(k.InlineKeyboard[1][0].CallbackData, "cancelgo:") || f.cancels != 0 {
		t.Fatal("cancel confirmation merged with navigation or submitted early")
	}
}

func TestPaymentChoicesAndExistingInvoiceStayExplicit(t *testing.T) {
	a, f := newPendingFixture(t)
	a.presentPaymentMethods(context.Background(), 42, 10, 42, f.order, dujiao.PaymentChannelList{Items: []dujiao.PaymentChannel{{ID: 12, Name: "渠道 A"}, {ID: 14, Name: "渠道 B"}}})
	k := f.markups[len(f.markups)-1]
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard[0]) != 2 || f.posts != 0 {
		t.Fatal("selection not paired or auto-submitted")
	}
	f.balance = "0.00"
	pendingClick(a, 42, 42, 10, k.InlineKeyboard[0][0].CallbackData)
	k = f.markups[len(f.markups)-1]
	if findCallback(k, "recharge") != "recharge" || findCallback(k, "resume:7") != "resume:7" || f.posts != 0 {
		t.Fatal("insufficient balance shortcut missing or money requested")
	}
	p := dujiao.PaymentResult{PaymentID: 8, OrderID: 7, Status: "pending", PayURL: "https://payments.example.invalid/original", QRCode: "SYNTHETIC-QR", Amount: "11.25", Currency: "CNY", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)}
	a.presentExistingPayment(context.Background(), 42, 10, f.order, p, false, false)
	k = f.markups[len(f.markups)-1]
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard[0]) != 1 || k.InlineKeyboard[0][0].URL != p.PayURL || findCallback(k, "payqr:7:8") != "payqr:7:8" || findCallback(k, "order:7") != "order:7" || findCallback(k, "cancelask:7") != "cancelask:7" || f.posts != 0 || f.photos != 0 {
		t.Fatal("invoice shortcuts created money/QR or changed payment record")
	}
	if !strings.Contains(f.texts[len(f.texts)-1], "应付：") || strings.Contains(f.texts[len(f.texts)-1], "实付：") {
		t.Fatal("pending invoice sounds paid")
	}
}

func TestDeliveryReceiptKeepsResendSeparateFromNavigation(t *testing.T) {
	a, f := newDeliveryFixture(t)
	a.showDeliveryReceipt(context.Background(), 42, 10, "zh-CN", 7, delivery.Receipt{Status: "sent", Token: strings.Repeat("A", 26), NotBefore: time.Now().Add(-time.Second)})
	var k models.InlineKeyboardMarkup
	_ = json.Unmarshal([]byte(f.markups[len(f.markups)-1]), &k)
	assertCompactKeyboard(t, k)
	if len(k.InlineKeyboard) != 2 || len(k.InlineKeyboard[0]) != 1 || !strings.HasPrefix(k.InlineKeyboard[0][0].CallbackData, "fileretry:7:") || len(k.InlineKeyboard[1]) != 2 || findCallback(k, "order:7") != "order:7" || findCallback(k, "home") != "home" || len(f.files) != 0 {
		t.Fatal("resend confirmation lost or ran during navigation")
	}
}
