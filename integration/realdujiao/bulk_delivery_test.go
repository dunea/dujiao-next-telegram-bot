//go:build realdujiao

package realdujiao

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestTelegramBulkDeliveryTXT(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	var category, product struct{ ID uint }
	s.admin("POST", "/categories", s.login(), map[string]any{"slug": "bulk-txt", "name": map[string]string{"zh-CN": "批量卡密 TXT"}}, &category)
	s.admin("POST", "/products", s.login(), map[string]any{"category_id": category.ID, "slug": "bulk-txt-item", "title": map[string]string{"zh-CN": "合成批量卡密"}, "price_amount": 0.01, "fulfillment_type": "auto", "is_active": true}, &product)
	p, err := c.Product(ctx, "zh-CN", "", product.ID)
	p = must(t, "bulk TXT product", p, err)
	if len(p.SKUs) != 1 {
		t.Fatal("default bulk SKU missing")
	}
	for batch := 0; batch < 5; batch++ {
		secrets := make([]string, 500)
		for i := range secrets {
			secrets[i] = fmt.Sprintf("SYNTHETIC-BULK-%05d<&>", batch*500+i)
		}
		var imported struct{ Created int }
		s.admin("POST", "/card-secrets/batch", s.login(), map[string]any{"product_id": product.ID, "sku_id": p.SKUs[0].ID, "secrets": secrets}, &imported)
		if imported.Created != 500 {
			t.Fatal("bulk import incomplete")
		}
	}
	f.command("/start")
	f.waitText("欢迎")
	f.command("/products")
	menu := f.waitButton("cat:")
	f.click(menu, button(menu, fmt.Sprintf("cat:%d:", category.ID), ""))
	menu = f.waitButton("product:")
	f.click(menu, button(menu, "product:", fmt.Sprint(product.ID)))
	menu = f.waitButton("qtyinput:")
	f.click(menu, button(menu, "qtyinput:", ""))
	f.waitText("请输入购买数量")
	f.command("2500")
	menu = f.waitButton("buy:")
	f.click(menu, button(menu, "buy:", ":2500"))
	menu = f.waitButton("formconfirm:")
	f.click(menu, button(menu, "formconfirm:", ""))
	menu = f.waitButton("pay:")
	f.click(menu, button(menu, "pay:", fmt.Sprintf(":%d:0", f.paymentChannel)))
	s.wait(func() bool { return len(f.state().Payments) == 1 })
	f.pay(0, "success")
	f.waitText("订单已完成交付")
	orders, err := c.Orders(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	orders = must(t, "bulk delivered orders", orders, err)
	if orders.Total != 1 {
		t.Fatal("bulk checkout duplicated order")
	}
	var order dujiao.OrderDetail
	s.wait(func() bool {
		order, err = c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orders.Items[0].OrderID)
		return err == nil && order.Status == "completed"
	})
	payload := deliveryPayload(order)
	if len(strings.Split(payload, "\n")) != 2500 {
		t.Fatal("official bulk delivery lost cards")
	}
	f.command("/orders")
	menu = f.waitButton("order:")
	f.click(menu, button(menu, "order:", fmt.Sprint(order.OrderID)))
	view := f.waitButton("file:")
	if strings.Contains(view.Text, "SYNTHETIC-BULK-") || len(view.Text) > 4096 {
		t.Fatal("bulk summary exposed/truncated cards")
	}
	f.click(view, button(view, "file:", ""))
	file := f.waitMessage(func(m tgMessage) bool { return m.Method == "sendDocument" })
	if len(order.Children) != 1 {
		t.Fatal("expected official single child")
	}
	expected := "Order / 订单: " + order.OrderNo + "\n\n=== Sub-order / 子订单: " + order.Children[0].OrderNo + " ===\n" + payload + "\n\n"
	digest := sha256.Sum256([]byte(expected))
	if file.FileBytes != len(expected) || file.FileSHA256 != fmt.Sprintf("%x", digest) {
		t.Fatal("2500-card TXT incomplete/escaped/truncated")
	}
	f.waitText("TXT 已发送")
	f.click(view, button(view, "file:", ""))
	f.waitText("TXT 已发送")
	s.docker("restart", f.bot)
	f.healthy()
	f.command("/orders")
	menu = f.waitButton("order:")
	f.click(menu, button(menu, "order:", fmt.Sprint(order.OrderID)))
	view = f.waitButton("file:")
	f.click(view, button(view, "file:", ""))
	f.waitText("TXT 已发送")
	files := 0
	for _, m := range f.state().Messages {
		if m.Method == "sendDocument" {
			files++
		}
	}
	if files != 1 {
		t.Fatal("bulk receipt lost across duplicate click/restart")
	}
	f.assertStock(product.ID, p.SKUs[0].ID, 0, 0, 2500)
	f.assertWallet(c, "0.00", 0)
	audit := s.docker("exec", f.bot, "cat", "/tmp/audit.jsonl")
	if strings.Contains(audit, "SYNTHETIC-BULK-") || !strings.Contains(audit, "delivery.txt") {
		t.Fatal("bulk audit missing or leaking card data")
	}
	t.Log("PASS: 2500 real automatic cards exported byte-complete via SDK multipart; duplicate clicks and restart send one TXT, stock/money unchanged")
}
