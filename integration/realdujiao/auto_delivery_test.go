//go:build realdujiao

package realdujiao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type stockCard struct {
	ID      uint   `json:"id"`
	Secret  string `json:"secret"`
	Status  string `json:"status"`
	OrderID uint   `json:"order_id"`
}

func TestTelegramAutoDelivery(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	var category struct {
		ID uint `json:"id"`
	}
	s.admin("POST", "/categories", s.login(), map[string]any{"slug": "auto-fixture", "name": map[string]string{"zh-CN": "合成自动卡密"}}, &category)
	var product struct {
		ID uint `json:"id"`
	}
	s.admin("POST", "/products", s.login(), map[string]any{"category_id": category.ID, "slug": "auto-item", "title": map[string]string{"zh-CN": "合成自动卡密"}, "price_amount": 3.25, "fulfillment_type": "auto", "is_active": true}, &product)
	p, err := c.Product(ctx, "zh-CN", "", product.ID)
	p = must(t, "automatic product", p, err)
	if p.FulfillmentType != "auto" || len(p.SKUs) != 1 {
		t.Fatal("automatic default SKU missing")
	}
	sku := p.SKUs[0].ID
	secrets := []string{"SYNTHETIC-AUTO-A<&>", "SYNTHETIC-AUTO-B", "SYNTHETIC-AUTO-C"}
	var imported struct {
		Created int `json:"created"`
	}
	s.admin("POST", "/card-secrets/batch", s.login(), map[string]any{"product_id": product.ID, "sku_id": sku, "secrets": secrets}, &imported)
	if imported.Created != 3 {
		t.Fatal("synthetic stock import mismatch")
	}
	f.assertStock(product.ID, sku, 3, 0, 0)
	f.command("/start")
	f.waitText("欢迎")
	var previous []string
	for index, quantity := range []int{2, 1} {
		// All checkout operations come from actual returned Telegram buttons.
		f.command("/products")
		menu := f.waitButton("cat:")
		f.click(menu, button(menu, fmt.Sprintf("cat:%d:", category.ID), ""))
		menu = f.waitButton("product:")
		f.click(menu, button(menu, "product:", fmt.Sprint(product.ID)))
		menu = f.waitButton("qty:")
		f.click(menu, button(menu, fmt.Sprintf("qty:%d:%d:", product.ID, sku), ":1"))
		menu = f.waitButton("buy:")
		if quantity == 2 {
			f.click(menu, button(menu, "qty:", ":2"))
			menu = f.waitButton("buy:")
		}
		f.click(menu, button(menu, "buy:", fmt.Sprintf(":%d", quantity)))
		menu = f.waitButton("formconfirm:")
		f.click(menu, button(menu, "formconfirm:", ""))
		menu = f.waitButton("pay:")
		orders, err := c.Orders(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
		orders = must(t, "automatic checkout orders", orders, err)
		if orders.Total != int64(index+1) {
			t.Fatal("automatic checkout created extra/missing order")
		}
		orderID := orders.Items[0].OrderID
		detail, err := c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
		detail = must(t, "unpaid automatic order", detail, err)
		if deliveryPayload(detail) != "" {
			t.Fatal("cards exposed before payment")
		}
		f.assertStock(product.ID, sku, 3-len(previous)-quantity, quantity, len(previous))
		// A second bound identity cannot read the first buyer's cards/order.
		if index == 0 {
			_, err := c.ProvisionIdentity(ctx, dujiao.TelegramIdentity{ChannelUserID: "910000002", FirstName: "Other synthetic buyer", Locale: "zh-CN"})
			must(t, "other synthetic identity", true, err)
			_, err = c.Order(ctx, "910000002", "zh-CN", orderID)
			var denied *dujiao.APIError
			if !errors.As(err, &denied) || denied.HTTPStatus != 404 || denied.ErrorCode != "order_not_found" {
				t.Fatal("cross-user order access was not explicitly denied")
			}
		}
		f.click(menu, button(menu, "pay:", fmt.Sprintf(":%d:0", f.paymentChannel)))
		s.wait(func() bool { return len(f.state().Payments) == index+1 })
		// Provider failures cannot reveal cards or consume reserved stock.
		f.pay(index, "bad_signature")
		detail, err = c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
		detail = must(t, "rejected auto payment", detail, err)
		if detail.Status != "pending_payment" || deliveryPayload(detail) != "" {
			t.Fatal("invalid payment delivered cards")
		}
		f.pay(index, "success")
		s.wait(func() bool {
			detail, err = c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
			return err == nil && detail.Status == "completed" && deliveryPayload(detail) != ""
		})
		f.waitText("订单已完成交付")
		if detail.PaidAmount != map[int]string{2: "6.50", 1: "3.25"}[quantity] || detail.WalletPaidAmount != "0.00" {
			t.Fatal("automatic order payment amount mismatch")
		}
		payload := deliveryPayload(detail)
		lines := strings.Split(payload, "\n")
		if len(lines) != quantity {
			t.Fatal("automatic delivery quantity mismatch")
		}
		seen := map[string]bool{}
		for _, line := range lines {
			known := false
			for _, secret := range secrets {
				if line == secret {
					known = true
				}
			}
			if !known || seen[line] {
				t.Fatal("unknown or duplicate card delivered")
			}
			for _, old := range previous {
				if line == old {
					t.Fatal("card allocated across two orders")
				}
			}
			seen[line] = true
		}
		cards := f.cards(product.ID, sku)
		ownedOrders := map[uint]bool{orderID: true}
		for _, child := range detail.Children {
			if child.Fulfillment == nil || child.Fulfillment.Type != "auto" {
				t.Fatal("automatic child fulfillment missing")
			}
			ownedOrders[child.OrderID] = true
		}
		used := 0
		for _, card := range cards {
			if seen[card.Secret] {
				used++
				if card.Status != "used" || !ownedOrders[card.OrderID] {
					t.Fatal("delivered card not associated with an order")
				}
			}
		}
		if used != quantity {
			t.Fatal("card allocation missing")
		}
		previous = append(previous, lines...)
		f.assertStock(product.ID, sku, 3-len(previous), 0, len(previous))
		before, _ := json.Marshal(cards)
		// Duplicate gateway callbacks must not allocate another card or fulfillment.
		f.pay(index, "success")
		f.pay(index, "success")
		again := f.cards(product.ID, sku)
		after, _ := json.Marshal(again)
		if string(before) != string(after) {
			t.Fatal("duplicate provider callback changed card allocation")
		}
		replayed, err := c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
		replayed = must(t, "automatic replay", replayed, err)
		if replayed.Status != "completed" || deliveryPayload(replayed) != payload {
			t.Fatal("duplicate provider callback changed fulfillment")
		}
		f.command("/orders")
		menu = f.waitButton("order:")
		f.click(menu, button(menu, "order:", fmt.Sprint(orderID)))
		view := f.waitMessage(func(m tgMessage) bool { return strings.Contains(m.Text, html.EscapeString(lines[0])) })
		for _, line := range lines {
			if !strings.Contains(view.Text, html.EscapeString(line)) {
				t.Fatal("Telegram omitted/failed to escape delivered card")
			}
		}
		for _, secret := range secrets {
			if !seen[secret] && strings.Contains(view.Text, html.EscapeString(secret)) {
				t.Fatal("Telegram leaked another order/unallocated card")
			}
		}
		if index == 0 {
			s.docker("restart", f.bot)
			f.healthy()
			f.command("/orders")
			menu = f.waitButton("order:")
			f.click(menu, button(menu, "order:", fmt.Sprint(orderID)))
			f.waitMessage(func(m tgMessage) bool { return strings.Contains(m.Text, html.EscapeString(lines[0])) })
		}
	}
	f.command("/products")
	menu := f.waitButton("cat:")
	f.click(menu, button(menu, fmt.Sprintf("cat:%d:", category.ID), ""))
	menu = f.waitButton("product:")
	f.click(menu, button(menu, "product:", fmt.Sprint(product.ID)))
	soldOut := f.waitMessage(func(m tgMessage) bool {
		var keyboard struct {
			Rows [][]struct{ Text string } `json:"inline_keyboard"`
		}
		_ = json.Unmarshal(m.Keyboard, &keyboard)
		for _, row := range keyboard.Rows {
			for _, b := range row {
				if b.Text == "已售罄" {
					return true
				}
			}
		}
		return false
	})
	if button(soldOut, "qty:", "") != "" || button(soldOut, "buy:", "") != "" {
		t.Fatal("sold out product still offers checkout")
	}
	f.assertStock(product.ID, sku, 0, 0, 3)
	f.assertWallet(c, "0.00", 0)
	fulfilled := 0
	for _, message := range f.state().Messages {
		if strings.Contains(message.Text, "订单已完成交付") {
			fulfilled++
		}
	}
	if fulfilled != 2 {
		t.Fatal("automatic fulfilled callback duplicated/missing")
	}
	audit := s.docker("exec", f.bot, "cat", "/tmp/audit.jsonl")
	if strings.Contains(audit, "SYNTHETIC-AUTO-") {
		t.Fatal("card contents leaked into audit")
	}
	t.Log("PASS: Telegram -> online payment -> official automatic fulfillment; 2+1 distinct cards, replay isolation, restart retrieval, exhausted stock and audit privacy")
}

func deliveryPayload(d dujiao.OrderDetail) string {
	if text, ok := d.FulfillmentResult.(string); ok && text != "" {
		return text
	}
	for _, child := range d.Children {
		if child.Fulfillment != nil {
			if text, ok := child.Fulfillment.Payload.(string); ok {
				return text
			}
		}
	}
	return ""
}
func (f *telegramFlow) cards(product, sku uint) []stockCard {
	var cards []stockCard
	f.s.admin("GET", fmt.Sprintf("/card-secrets?product_id=%d&sku_id=%d&page_size=20", product, sku), f.s.login(), nil, &cards)
	if len(cards) != 3 {
		f.s.t.Fatal("unexpected stock cardinality")
	}
	return cards
}
func (f *telegramFlow) assertStock(product, sku uint, available, reserved, used int) {
	var stats struct{ Total, Available, Reserved, Used int }
	f.s.admin("GET", fmt.Sprintf("/card-secrets/stats?product_id=%d&sku_id=%d", product, sku), f.s.login(), nil, &stats)
	if stats.Total != 3 || stats.Available != available || stats.Reserved != reserved || stats.Used != used {
		f.s.t.Fatal("automatic stock counters mismatch")
	}
}
