//go:build realdujiao

package realdujiao

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestTelegramOrderNavigation(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	uid := strconv.FormatInt(buyerID, 10)
	f.command("/start")
	f.waitText("欢迎")
	// Seven real orders, six immediately cancelled via the normal API. No
	// gateway records/funds: enough pages in both all and cancelled filters.
	var pending dujiao.OrderCreated
	for i := 0; i < 7; i++ {
		o, err := c.CreateOrder(ctx, dujiao.OrderRequest{TelegramIdentity: dujiao.TelegramIdentity{ChannelUserID: uid, Locale: "zh-CN"}, Items: []dujiao.OrderItemRequest{{ProductID: f.config.Product, SKUID: f.config.SKU, Quantity: 1}}})
		o = must(t, "navigation synthetic order", o, err)
		if i < 6 {
			_, err = c.CancelOrder(ctx, uid, o.OrderID)
			must(t, "navigation fixture cancellation", true, err)
		} else {
			pending = o
		}
	}
	f.command("/orders")
	menu := f.waitButton("orderfind:a:1")
	assertCommerceKeyboard(t, menu, "", "orderfind:", "orderfilters:", "orders:a:1", "orders:p:1", "cats", "home")
	if !strings.Contains(menu.Text, "1/2") || !strings.Contains(menu.Text, "共 7 单") {
		t.Fatal("list missing count/page")
	}
	f.click(menu, button(menu, "orders:p:1", ""))
	menu = f.waitButton("orderfind:p:1")
	if !strings.Contains(menu.Text, "共 1 单") {
		t.Fatal("one-tap pending filter incorrect")
	}
	f.click(menu, button(menu, "orders:a:1", ""))
	menu = f.waitButton("orderfind:a:1")
	f.click(menu, button(menu, "orders:a:2", ""))
	menu = f.waitButton("orderfind:a:2")
	viewData := button(menu, "order:", ":a:2")
	f.click(menu, viewData)
	view := f.waitButton(viewData)
	assertCommerceKeyboard(t, view, "", "order:", "support:", "orders:a:2", "home")
	if !strings.Contains(view.Text, "付款：") || !strings.Contains(view.Text, "交付：") {
		t.Fatal("detail did not separate payment/delivery")
	}
	f.click(view, button(view, "orders:a:2", ""))
	menu = f.waitButton("orderfind:a:2")
	f.click(menu, button(menu, "orderfilters:", ""))
	filters := f.waitButton("orders:x:1")
	f.click(filters, button(filters, "orders:x:1", ""))
	menu = f.waitButton("orderfind:x:1")
	if !strings.Contains(menu.Text, "共 6 单") || !strings.Contains(menu.Text, "订单已关闭") {
		t.Fatal("exact cancelled filter incorrect")
	}
	f.click(menu, button(menu, "orders:x:2", ""))
	menu = f.waitButton("orderfind:x:2")
	f.click(menu, button(menu, "orderfind:", ""))
	f.waitText("请发送完整订单号")
	s.docker("restart", f.bot)
	f.healthy()
	// A draft survives Redis/Bot restart; lookup deliberately ignores the
	// cancelled filter but its return button preserves cancelled page two.
	f.command(pending.OrderNo)
	view = f.waitButton("resume:")
	if !strings.Contains(view.Text, pending.OrderNo) || button(view, "orders:x:2", "") == "" || strings.Contains(view.Text, "交付：已交付") {
		t.Fatal("restarted search lost owner/context or fabricated delivery")
	}
	f.click(view, button(view, "orders:x:2", ""))
	f.waitButton("orderfind:x:2")
	f.command("/orders " + pending.OrderNo)
	view = f.waitButton("resume:")
	if button(view, fmt.Sprintf("order:%d:", pending.OrderID), "") == "" {
		t.Fatal("direct command lookup failed")
	}
	f.command("/orders NO-SUCH-SYNTHETIC-ORDER")
	f.waitText("未找到你名下")
	_, err := c.ProvisionIdentity(ctx, dujiao.TelegramIdentity{ChannelUserID: "910000002", FirstName: "Other synthetic buyer", Locale: "zh-CN"})
	must(t, "navigation other identity", true, err)
	_, err = c.OrderByOrderNo(ctx, "910000002", "zh-CN", pending.OrderNo)
	var denied *dujiao.APIError
	if !errors.As(err, &denied) || denied.HTTPStatus != 404 || denied.ErrorCode != "order_not_found" {
		t.Fatal("order number bypassed user isolation")
	}
	f.command("/orders")
	menu = f.waitButton("orderfind:a:1")
	f.click(menu, button(menu, "orderfilters:", ""))
	filters = f.waitButton("orders:c:1")
	f.click(filters, button(filters, "orders:c:1", ""))
	menu = f.waitButton("orderfind:c:1")
	if !strings.Contains(menu.Text, "当前筛选下没有订单") || button(menu, "orderfind:", "") == "" {
		t.Fatal("empty state is a dead end")
	}
	final, err := c.Orders(ctx, uid, "zh-CN", "", 1, 20)
	final = must(t, "navigation final orders", final, err)
	if final.Total != 7 || len(f.state().Payments) != 0 {
		t.Fatal("read-only navigation created financial objects")
	}
	count := map[string]int{}
	for _, o := range final.Items {
		count[o.Status]++
	}
	if count["pending_payment"] != 1 || count["canceled"] != 6 {
		t.Fatal("navigation changed order status")
	}
	f.assertWallet(c, "0.00", 0)
	t.Log("PASS: real owner-scoped number lookup, filtered pagination/context, restarted search prompt, direct command, empty states and no payment/ledger writes")
}
