//go:build realdujiao

package realdujiao

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// One buyer, one wallet and the real official store: this closes the gaps
// between independently passing feature suites, using only synthetic goods.
func TestTelegramReleaseJourney(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	f.supportConfig("https://t.me/synthetic_support")
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	uid := strconv.FormatInt(buyerID, 10)
	var category, manual, automatic struct{ ID uint }
	s.admin("POST", "/categories", s.login(), map[string]any{"slug": "release-journey", "name": map[string]string{"zh-CN": "整体验收"}}, &category)
	s.admin("POST", "/products", s.login(), map[string]any{"category_id": category.ID, "slug": "journey-manual", "title": map[string]string{"zh-CN": "合成人工商品"}, "price_amount": 10, "fulfillment_type": "manual", "manual_stock_total": 50, "is_active": true, "manual_form_schema": map[string]any{"fields": []map[string]any{{"key": "email", "label": map[string]string{"zh-CN": "交付邮箱"}, "type": "email", "required": true}}}}, &manual)
	s.admin("POST", "/products", s.login(), map[string]any{"category_id": category.ID, "slug": "journey-auto", "title": map[string]string{"zh-CN": "合成自动商品"}, "price_amount": 3.25, "fulfillment_type": "auto", "is_active": true}, &automatic)
	s.admin("POST", "/coupons", s.login(), map[string]any{"code": "JOURNEY1", "type": "fixed", "value": 1, "scope_ref_ids": []uint{manual.ID}, "is_active": true, "usage_limit": 10, "per_user_limit": 1}, nil)
	p, err := c.Product(ctx, "zh-CN", "", automatic.ID)
	p = must(t, "journey automatic SKU", p, err)
	secrets := []string{"SYNTHETIC-JOURNEY-A<&>", "SYNTHETIC-JOURNEY-B"}
	s.admin("POST", "/card-secrets/batch", s.login(), map[string]any{"product_id": automatic.ID, "sku_id": p.SKUs[0].ID, "secrets": secrets}, nil)
	f.command("/start")
	home := f.waitText("欢迎")
	var homeMenu struct {
		Rows [][]supportButton `json:"inline_keyboard"`
	}
	if json.Unmarshal(home.Keyboard, &homeMenu) != nil {
		t.Fatal("invalid home keyboard sent through Telegram SDK")
	}
	hasPair, hasShop := false, false
	for _, row := range homeMenu.Rows {
		if len(row) == 0 || len(row) > 2 {
			t.Fatal("home menu has invalid column count")
		}
		hasPair = hasPair || len(row) == 2
		for _, b := range row {
			if b.Data == "cats" {
				hasShop = true
				if len(row) != 1 {
					t.Fatal("shopping entry is not full width")
				}
			}
		}
	}
	if !hasPair || !hasShop {
		t.Fatal("home layout is not compact or lost shopping")
	}
	f.command("/recharge")
	menu := f.waitButton("rechargepick:")
	f.command("100")
	menu = f.waitButton("rechargepay:")
	if menu.Method != "sendMessage" || menu.ID <= f.inputID {
		t.Fatal("typed recharge amount next step is hidden above input")
	}
	f.click(menu, button(menu, "rechargepay:", fmt.Sprint(f.paymentChannel)))
	f.waitText("103.50 CNY")
	s.wait(func() bool { return len(f.state().Payments) == 1 })
	f.pay(0, "bad_signature")
	f.assertWallet(c, "0.00", 0)
	f.pay(0, "success")
	f.waitText("余额充值成功")
	f.pay(0, "success")
	f.assertWallet(c, "100.00", 1)
	openProduct := func(id uint) tgMessage {
		f.command("/products")
		view := f.waitButton("cat:")
		f.click(view, button(view, fmt.Sprintf("cat:%d:", category.ID), ""))
		view = f.waitButton("product:")
		f.click(view, button(view, "product:", fmt.Sprint(id)))
		return f.waitButton("qtyinput:")
	}
	menu = openProduct(manual.ID)
	f.click(menu, button(menu, "qtyinput:", ""))
	f.waitText("请输入购买数量")
	f.command("2")
	menu = f.waitButton("buy:")
	f.click(menu, button(menu, "buy:", ":2"))
	prompt := f.waitText("交付邮箱")
	f.command("invalid-email", prompt)
	f.waitText("填写内容不符合要求")
	prompt = f.waitText("交付邮箱")
	f.command("journey@example.invalid", prompt)
	preview := f.waitButton("formconfirm:")
	staleConfirm := button(preview, "formconfirm:", "")
	f.click(preview, button(preview, "coupon:", ":edit"))
	prompt = f.waitText("输入优惠码")
	f.command("JOURNEY-BAD", prompt)
	bad := f.waitText("优惠码不可用")
	f.click(bad, button(bad, "coupon:", ":edit"))
	prompt = f.waitText("输入优惠码")
	f.command("JOURNEY1", prompt)
	preview = f.waitButton("formconfirm:")
	if preview.ID <= f.inputID {
		t.Fatal("coupon result is hidden in an earlier preview")
	}
	if !strings.Contains(preview.Text, "19.00 CNY") || !strings.Contains(preview.Text, "journey@example.invalid") {
		t.Fatal("coupon retry lost form/quantity or price")
	}
	f.click(preview, staleConfirm)
	f.waitText("失效")
	orders, err := c.Orders(ctx, uid, "zh-CN", "", 1, 20)
	orders = must(t, "journey before confirmation", orders, err)
	if orders.Total != 0 {
		t.Fatal("invalid input/old confirmation created an order")
	}
	f.click(preview, button(preview, "formconfirm:", ""))
	selection := f.waitButton("pay:")
	walletPay := button(selection, "pay:", ":1")
	f.click(selection, walletPay)
	f.waitText("订单详情")
	orders, err = c.Orders(ctx, uid, "zh-CN", "", 1, 20)
	orders = must(t, "journey paid manual", orders, err)
	if orders.Total != 1 || orders.Items[0].TotalAmount != "19.00" {
		t.Fatal("discounted order missing or duplicated")
	}
	manualID := orders.Items[0].OrderID
	f.click(selection, walletPay)
	f.waitText("订单详情")
	f.assertWallet(c, "81.00", 2)
	var adminOrder struct {
		Items []struct {
			Submission map[string]any `json:"manual_form_submission"`
		} `json:"items"`
	}
	s.admin("GET", fmt.Sprintf("/orders/%d", manualID), s.login(), nil, &adminOrder)
	if len(adminOrder.Items) == 0 || adminOrder.Items[0].Submission["email"] != "journey@example.invalid" {
		t.Fatal("purchase data did not reach official order")
	}
	detail, err := c.Order(ctx, uid, "zh-CN", manualID)
	detail = must(t, "manual detail", detail, err)
	fulfillID := manualID
	if len(detail.Children) > 0 {
		fulfillID = detail.Children[0].OrderID
	}
	s.admin("POST", "/fulfillments", s.login(), map[string]any{"order_id": fulfillID, "payload": "SYNTHETIC-JOURNEY-MANUAL"}, nil)
	notice := f.waitText("订单已完成交付")
	f.click(notice, button(notice, "file:", ""))
	f.waitMessage(func(m tgMessage) bool {
		return m.Method == "sendDocument" && m.Filename == fmt.Sprintf("delivery-%d.txt", manualID)
	})
	f.waitText("TXT 已发送")
	menu = openProduct(automatic.ID)
	f.click(menu, button(menu, "qtyinput:", ""))
	f.waitText("请输入购买数量")
	f.command("2")
	menu = f.waitButton("buy:")
	f.click(menu, button(menu, "buy:", ":2"))
	preview = f.waitButton("formconfirm:")
	f.click(preview, button(preview, "formconfirm:", ""))
	selection = f.waitButton("pay:")
	f.click(selection, button(selection, "pay:", ":"+strconv.FormatUint(uint64(f.paymentChannel), 36)+":0"))
	card := f.waitButton("payqr:")
	originalLink := paymentLink(card)
	f.click(card, button(card, "resume:", ""))
	card = f.waitButton("payqr:")
	if paymentLink(card) != originalLink {
		t.Fatal("continue payment changed invoice")
	}
	f.click(card, button(card, "payqr:", ""))
	f.waitButton("payqr:")
	s.wait(func() bool { return len(f.state().Payments) == 2 })
	f.pay(1, "success")
	notice = f.waitText("订单已完成交付")
	orders, err = c.Orders(ctx, uid, "zh-CN", "", 1, 20)
	orders = must(t, "journey automatic order", orders, err)
	if orders.Total != 2 {
		t.Fatal("continue payment created extra orders")
	}
	autoID := orders.Items[0].OrderID
	detail, err = c.Order(ctx, uid, "zh-CN", autoID)
	detail = must(t, "journey automatic detail", detail, err)
	if detail.Status != "completed" || detail.TotalAmount != "6.50" || detail.OnlinePaidAmount != "6.50" {
		t.Fatal("automatic payment or delivery incomplete")
	}
	for _, secret := range secrets {
		if !strings.Contains(deliveryPayload(detail), secret) {
			t.Fatal("automatic delivery missing purchased code")
		}
	}
	f.click(notice, button(notice, "file:", ""))
	f.waitMessage(func(m tgMessage) bool {
		return m.Method == "sendDocument" && m.Filename == fmt.Sprintf("delivery-%d.txt", autoID)
	})
	f.waitText("TXT 已发送")
	s.docker("restart", f.bot)
	f.healthy()
	f.command("/orders " + detail.OrderNo)
	view := f.waitButton("support:")
	f.click(view, button(view, "support:", ":g"))
	help := f.waitText("订单售后")
	goodDraft := false
	for _, b := range supportButtons(help) {
		if b.URL != "" {
			u, _ := url.Parse(b.URL)
			if u != nil && u.Path == "/synthetic_support" && strings.Contains(u.Query().Get("text"), detail.OrderNo) {
				goodDraft = true
			}
		}
	}
	if !goodDraft || strings.Contains(help.Text, "SYNTHETIC-JOURNEY") || strings.Contains(help.Text, "journey@example.invalid") {
		t.Fatal("support context missing or private data leaked")
	}
	f.click(help, button(help, "order:", ""))
	view = f.waitButton("file:")
	f.click(view, button(view, "file:", ""))
	f.waitText("TXT 已发送")
	f.assertWallet(c, "81.00", 2)
	files := 0
	for _, m := range f.state().Messages {
		if m.Method == "sendDocument" {
			files++
		}
	}
	if files != 2 || len(f.state().Payments) != 2 {
		t.Fatal("restart/support navigation replayed file or payment")
	}
	var coupons []struct {
		Code      string
		UsedCount int `json:"used_count"`
	}
	s.admin("GET", "/coupons", s.login(), nil, &coupons)
	if len(coupons) != 1 || coupons[0].UsedCount != 1 {
		t.Fatal("coupon usage duplicated")
	}
	audit := s.docker("exec", f.bot, "cat", "/tmp/audit.jsonl")
	for _, secret := range append(secrets, "journey@example.invalid", "JOURNEY1", "SYNTHETIC-JOURNEY-MANUAL") {
		if strings.Contains(audit, secret) {
			t.Fatal("private checkout/delivery content in audit")
		}
	}
	t.Log("PASS: same buyer recharge+fees -> typed quantity+manual form -> invalid/valid coupon and stale confirmation -> one wallet debit -> manual TXT -> online automatic codes -> invoice reuse -> reboot/number lookup/support -> no duplicate files, money or coupon usage")
}
