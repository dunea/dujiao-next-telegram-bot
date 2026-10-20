package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func quantityFixture(t *testing.T) (*App, *formFixture) {
	t.Helper()
	a, f := newFormFixture(t, nil)
	f.product.MaxPurchaseQuantity = 10000
	stock := int64(10000)
	f.product.SKUs[0].StockCount = &stock
	return a, f
}
func openQuantityInput(a *App) *session.QuantityInput {
	a.showProduct(context.Background(), 42, 42, 3, true, 10)
	q := a.sessions.Get(42).Quantity
	formCallback(a, 42, q.MessageID, "qtyinput:"+q.ID)
	return a.sessions.Get(42).Quantity
}
func quantityMessage(text string, promptID int) *models.Message {
	m := &models.Message{ID: 500, From: &models.User{ID: 42}, Chat: models.Chat{ID: 42}, Text: text}
	if promptID > 0 {
		m.ReplyToMessage = &models.Message{ID: promptID}
	}
	return m
}
func quantityReply(a *App, text string, promptID int) {
	a.handleUpdate(context.Background(), nil, &models.Update{Message: quantityMessage(text, promptID)})
}

func TestParsePurchaseQuantity(t *testing.T) {
	for _, input := range []string{"1", "2500", "5000", " 10000 ", "0002500", "2147483647"} {
		if _, ok := parsePurchaseQuantity(input); !ok {
			t.Errorf("rejected %q", input)
		}
	}
	for _, input := range []string{"", "0", "-1", "+2", "1.5", "2,500", "1e3", "２５００", "2500\n3", "2147483648", strings.Repeat("9", 1000)} {
		if _, ok := parsePurchaseQuantity(input); ok {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestQuantityCardIsCompactAndKeepsCheckoutExplicit(t *testing.T) {
	a, f := quantityFixture(t)
	a.showProduct(context.Background(), 42, 42, 3, true, 10)
	q := a.sessions.Get(42).Quantity
	if q == nil || q.SKUID != 31 || q.Quantity != 1 || q.MessageID != 10 {
		t.Fatal("single option did not open quantity card directly")
	}
	text, markup := quantityCard("zh-CN", f.product, 31, 2500, q.ID)
	if !strings.Contains(text, "25000.00 CNY") || !strings.Contains(text, "2500") || strings.Contains(text, "SKU-31") {
		t.Fatal("quantity/subtotal presentation incorrect")
	}
	encoded, _ := json.Marshal(markup)
	for _, want := range []string{"输入数量", "1000件", "下一步", "buy:3:31:2500"} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if len(b.CallbackData) > 64 {
				t.Fatal("Telegram callback exceeds 64 bytes")
			}
		}
	}
	f.product.MaxPurchaseQuantity = 30
	_, markup = quantityCard("zh-CN", f.product, 31, 1, q.ID)
	encoded, _ = json.Marshal(markup)
	if strings.Contains(string(encoded), "100件") || strings.Contains(string(encoded), "1000件") {
		t.Fatal("presets exceed purchase bounds")
	}
	if len(f.orders) != 0 || len(f.previews) != 0 {
		t.Fatal("opening selector created/previewed an order")
	}
}

func TestLongProductDetailsAreOptionalAndReturnToSelectedQuantity(t *testing.T) {
	a, f := quantityFixture(t)
	f.product.Description = strings.Repeat("说明", 200) + "DETAIL-END<&>"
	text, keyboard := quantityCard("zh-CN", f.product, 31, 2500, "id")
	encoded, _ := json.Marshal(keyboard)
	if strings.Contains(text, "DETAIL-END") || !strings.Contains(string(encoded), "productinfo:3:31:2500") {
		t.Fatal("long details crowded checkout or became inaccessible")
	}
	formCallback(a, 42, 10, "productinfo:3:31:2500")
	if !strings.Contains(f.texts[len(f.texts)-1], "DETAIL-END&lt;&amp;&gt;") {
		t.Fatal("full details were not safely accessible")
	}
	formCallback(a, 42, 10, "qty:3:31:2500")
	if a.sessions.Get(42).Quantity.Quantity != 2500 {
		t.Fatal("returning from details lost selected quantity")
	}
}

func TestTypedBulkQuantityDoesNotOrderUntilConfirmation(t *testing.T) {
	a, f := quantityFixture(t)
	q := openQuantityInput(a)
	quantityReply(a, "2500", 0) // Plain input is supported; no reply gesture required.
	selected := a.sessions.Get(42).Quantity
	if selected == nil || selected.Quantity != 2500 || selected.ID == q.ID || a.sessions.Get(42).Flow != "" {
		t.Fatal("typed quantity not applied")
	}
	quantityReply(a, "5000", q.PromptID)
	if got := a.sessions.Get(42).Quantity; got == nil || got.Quantity != 2500 {
		t.Fatal("stale reply changed quantity")
	}
	formCallback(a, 42, selected.MessageID, "buy:3:31:2500")
	draft := a.sessions.Get(42).Purchase
	if draft == nil || draft.Quantity != 2500 || !draft.Ready || len(f.orders) != 0 {
		t.Fatal("preview lost bulk quantity or created order early")
	}
	if f.previews[len(f.previews)-1].Items[0].Quantity != 2500 {
		t.Fatal("quantity not forwarded to actual preview request")
	}
	var previewKeyboard models.InlineKeyboardMarkup
	if json.Unmarshal([]byte(f.markups[len(f.markups)-1]), &previewKeyboard) != nil || !strings.HasPrefix(previewKeyboard.InlineKeyboard[0][0].CallbackData, "formconfirm:") {
		t.Fatal("optional coupon displaced primary checkout action")
	}
	if strings.Contains(f.texts[len(f.texts)-1], "原价：") {
		t.Fatal("identical original and total repeated unnecessarily")
	}
	checkoutConfirm(a)
	if len(f.orders) != 1 || f.orders[0].Items[0].Quantity != 2500 {
		t.Fatal("confirmed order quantity incorrect")
	}
	formCallback(a, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
	if len(f.orders) != 1 {
		t.Fatal("old confirmation created duplicate order")
	}
}

func TestTypedQuantityRechecksStockLimitsAndInvalidInput(t *testing.T) {
	for _, input := range []string{"0", "1.5", "2,500", "10001", "999999999999999999999"} {
		t.Run(input, func(t *testing.T) {
			a, f := quantityFixture(t)
			q := openQuantityInput(a)
			quantityReply(a, input, q.PromptID)
			state := a.sessions.Get(42)
			if state.Quantity == nil || state.Quantity.ID != q.ID || state.Flow != "purchase_quantity" || len(f.orders) != 0 || len(f.previews) != 0 {
				t.Fatal("invalid input advanced checkout")
			}
		})
	}
	a, f := quantityFixture(t)
	q := openQuantityInput(a)
	f.mu.Lock()
	stock := int64(100)
	f.product.SKUs[0].StockCount = &stock
	f.mu.Unlock()
	quantityReply(a, "2500", q.PromptID)
	if a.sessions.Get(42).Flow != "purchase_quantity" {
		t.Fatal("stock change silently clamped an exact typed quantity")
	}
	quantityReply(a, "100", q.PromptID)
	if a.sessions.Get(42).Quantity.Quantity != 100 {
		t.Fatal("valid retry failed")
	}
}

func TestQuantityInputIsPrivateFreshCancelableAndExpiring(t *testing.T) {
	for _, mode := range []string{"group", "wrong reply", "old message", "expired", "navigate", "cancel", "back", "sold out back"} {
		t.Run(mode, func(t *testing.T) {
			a, f := quantityFixture(t)
			q := openQuantityInput(a)
			m := quantityMessage("2500", q.PromptID)
			switch mode {
			case "group":
				m.Chat.ID = -100
			case "wrong reply":
				m.ReplyToMessage.ID++
			case "old message":
				m.ID = q.PromptID - 1
			case "expired":
				a.sessions.Update(42, func(s *session.State) { s.Quantity.ExpiresAt = time.Now().Add(-time.Second) })
			case "navigate":
				formCallback(a, 42, 99, "home")
			case "cancel":
				m.Text = "/cancel"
			case "back":
				m.Text = "/back"
			case "sold out back":
				f.product.StockStatus = "out_of_stock"
				formCallback(a, 42, q.PromptID, "qtyback:"+q.ID)
				if a.sessions.Get(42).Flow == "purchase_quantity" {
					t.Fatal("stock change prevented leaving input")
				}
				return
			}
			a.handleUpdate(context.Background(), nil, &models.Update{Message: m})
			state := a.sessions.Get(42)
			if state.Purchase != nil || len(f.orders) != 0 || len(f.previews) != 0 {
				t.Fatal("stale/private input started checkout")
			}
			if state.Quantity != nil && state.Quantity.Quantity == 2500 {
				t.Fatal("invalid context accepted quantity")
			}
			if (mode == "cancel" || mode == "back") && state.Flow == "purchase_quantity" {
				t.Fatal("cancel/back stuck in input")
			}
		})
	}
}

func TestCheckoutQuantityEditKeepsCouponAndDetailsAndInvalidatesOldConfirm(t *testing.T) {
	a, f := quantityFixture(t)
	f.product.ManualFormSchema.Fields = []dujiao.ManualFormField{{Key: "saved", Type: "text", Required: true}}
	formCallback(a, 42, 10, "buy:3:31:1")
	formReply(a, "detail")
	couponAction(a, "edit")
	formReply(a, "SAVE10")
	before := a.sessions.Get(42).Purchase
	formCallback(a, 42, before.PreviewID, fmt.Sprintf("qtyedit:%s:%d", before.ID, before.Revision))
	q := a.sessions.Get(42).Quantity
	formCallback(a, 42, before.PreviewID, fmt.Sprintf("formconfirm:%s:%d", before.ID, before.Revision))
	if len(f.orders) != 0 {
		t.Fatal("editing quantity left old order confirmation active")
	}
	quantityReply(a, "5000", q.PromptID)
	draft := a.sessions.Get(42).Purchase
	if draft.Quantity != 5000 || draft.CouponCode != "SAVE10" || draft.Values["saved"] != "detail" || draft.Revision <= before.Revision || !draft.Ready {
		t.Fatal("quantity edit lost draft data or freshness")
	}
	if f.previews[len(f.previews)-1].Items[0].Quantity != 5000 {
		t.Fatal("changed quantity did not request a new preview")
	}
	checkoutConfirm(a)
	if len(f.orders) != 1 || f.orders[0].Items[0].Quantity != 5000 || f.orders[0].CouponCode != "SAVE10" {
		t.Fatal("edited checkout was not preserved in final order")
	}
	if f.orders[0].ManualFormData["3:31"].(map[string]any)["saved"] != "detail" {
		t.Fatal("typed manual details did not reach final order")
	}
}

func TestConcurrentQuantityRepliesConsumePromptOnce(t *testing.T) {
	a, f := quantityFixture(t)
	q := openQuantityInput(a)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { a.handleQuantityMessage(context.Background(), quantityMessage("2500", q.PromptID)) })
	}
	wg.Wait()
	selected := a.sessions.Get(42).Quantity
	if selected == nil || selected.Quantity != 2500 || a.sessions.Get(42).Flow != "" || len(f.orders) != 0 {
		t.Fatal("concurrent input did not converge on one selection")
	}
}

func TestRedisQuantityInputSurvivesBotRestart(t *testing.T) {
	a, f, restart, _ := persistentWithdrawalFixture(t)
	f.product.MaxPurchaseQuantity = 10000
	q := openQuantityInput(a)
	a = restart()
	if a.sessions.Get(42).Quantity.PromptID != q.PromptID {
		t.Fatal("restart lost quantity prompt")
	}
	quantityReply(a, "2500", q.PromptID)
	if a.sessions.Get(42).Quantity.Quantity != 2500 || len(f.orders) != 0 {
		t.Fatal("restart failed to apply quantity safely")
	}
}
