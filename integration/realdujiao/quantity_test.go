//go:build realdujiao

package realdujiao

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestTelegramBulkQuantity(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s := f.s
	var category struct{ ID uint }
	s.admin("POST", "/categories", s.login(), map[string]any{"slug": "bulk-fixture", "name": map[string]string{"zh-CN": "批量数量测试"}}, &category)
	var product struct{ ID uint }
	s.admin("POST", "/products", s.login(), map[string]any{"category_id": category.ID, "slug": "bulk-item", "title": map[string]string{"zh-CN": "合成批量商品"}, "price_amount": 0.01, "fulfillment_type": "manual", "manual_stock_total": 10000, "is_active": true}, &product)
	f.command("/start")
	f.waitText("欢迎")
	f.command("/products")
	menu := f.waitButton("cat:")
	assertCommerceKeyboard(t, menu, "", "cat:", "home")
	f.click(menu, button(menu, fmt.Sprintf("cat:%d:", category.ID), ""))
	menu = f.waitButton("product:")
	assertCommerceKeyboard(t, menu, "", "product:", "cats", "home")
	f.click(menu, button(menu, "product:", fmt.Sprint(product.ID)))
	menu = f.waitButton("qtyinput:")
	assertQuantityKeyboard(t, menu)
	if button(menu, "buy:", ":1") == "" {
		t.Fatal("single SKU required an extra option-selection screen")
	}
	f.click(menu, button(menu, "qtyinput:", ""))
	f.waitText("请输入购买数量")
	f.command("0")
	validation := f.waitText("请输入正整数")
	if validation.Method != "sendMessage" || validation.ID <= f.inputID {
		t.Fatal("invalid quantity response is hidden above user input")
	}
	f.command("2500")
	menu = f.waitButton("buy:")
	assertQuantityKeyboard(t, menu)
	if menu.Method != "sendMessage" || menu.ID <= f.inputID {
		t.Fatal("typed quantity next step is hidden above user input")
	}
	if !strings.Contains(menu.Text, "25.00 CNY") || button(menu, "buy:", ":2500") == "" {
		t.Fatal("typed bulk quantity or estimate not displayed")
	}
	f.click(menu, button(menu, "buy:", ":2500"))
	menu = f.waitButton("formconfirm:")
	assertCommerceKeyboard(t, menu, "formconfirm:", "qtyedit:", "coupon:", "product:", "home")
	c := s.channel(f.config, false)
	orders, err := c.Orders(context.Background(), strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	if err != nil || orders.Total != 0 {
		t.Fatal("typing/previewing quantity created an order")
	}
	oldConfirm := button(menu, "formconfirm:", "")
	f.click(menu, button(menu, "qtyedit:", ""))
	f.waitText("请输入购买数量")
	f.command("5000")
	menu = f.waitButton("formconfirm:")
	assertCommerceKeyboard(t, menu, "formconfirm:", "qtyedit:", "coupon:", "product:", "home")
	if menu.ID <= f.inputID {
		t.Fatal("edited quantity preview is hidden above user input")
	}
	if !strings.Contains(menu.Text, "50.00 CNY") {
		t.Fatal("edited quantity did not reprice preview")
	}
	f.click(menu, oldConfirm) // Old revision must not create an order.
	f.waitText("已失效")
	orders, err = c.Orders(context.Background(), strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	if err != nil || orders.Total != 0 {
		t.Fatal("old quantity confirmation created an order")
	}
	f.click(menu, button(menu, "formconfirm:", ""))
	f.waitButton("pay:")
	orders, err = c.Orders(context.Background(), strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	if err != nil || orders.Total != 1 || orders.Items[0].TotalAmount != "50.00" {
		t.Fatal("bulk final confirmation was not one correctly-priced order")
	}
	if len(f.state().Payments) != 0 {
		t.Fatal("bulk quantity test unexpectedly started a payment")
	}
	t.Log("PASS: single-SKU direct quantity card, typed 2500 -> preview edit 5000, real reprice, stale confirmation rejected, one explicit order and no payment")
}
