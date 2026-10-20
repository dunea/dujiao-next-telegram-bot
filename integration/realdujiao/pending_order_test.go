//go:build realdujiao

package realdujiao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func paymentLink(m tgMessage) string {
	var keyboard struct {
		Rows [][]struct {
			URL string `json:"url"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal(m.Keyboard, &keyboard)
	for _, row := range keyboard.Rows {
		for _, b := range row {
			if b.URL != "" {
				return b.URL
			}
		}
	}
	return ""
}
func TestTelegramPendingOrder(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	f.command("/start")
	f.waitText("欢迎")
	f.command("/products")
	menu := f.waitButton("cat:")
	f.click(menu, button(menu, "cat:", ""))
	menu = f.waitButton("product:")
	f.click(menu, button(menu, "product:", ""))
	menu = f.waitButton("buy:")
	f.click(menu, button(menu, "buy:", ""))
	menu = f.waitButton("formconfirm:")
	f.click(menu, button(menu, "formconfirm:", ""))
	selection := f.waitButton("pay:")
	pay := button(selection, "pay:", ":"+strconv.FormatUint(uint64(f.paymentChannel), 36)+":0")
	f.click(selection, pay)
	card := f.waitButton("payqr:")
	originalLink := paymentLink(card)
	if originalLink == "" {
		t.Fatal("payment link missing")
	}
	s.wait(func() bool { return len(f.state().Payments) == 1 })
	orders, e := c.Orders(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 10)
	orders = must(t, "pending order list", orders, e)
	if orders.Total != 1 {
		t.Fatal("extra pending orders")
	}
	oid := orders.Items[0].OrderID
	initial, e := c.LatestPayment(ctx, strconv.FormatInt(buyerID, 10), oid)
	initial = must(t, "latest owned payment", initial, e)
	if initial.PaymentID == 0 || initial.PayURL != originalLink {
		t.Fatal("latest payment differs from original")
	}
	_, e = c.ProvisionIdentity(ctx, dujiao.TelegramIdentity{ChannelUserID: "910000002", FirstName: "Other synthetic buyer", Locale: "zh-CN"})
	must(t, "other identity", true, e)
	_, e = c.LatestPayment(ctx, "910000002", oid)
	var denied *dujiao.APIError
	if !errors.As(e, &denied) || denied.HTTPStatus != 404 || denied.ErrorCode != "order_not_found" {
		t.Fatal("latest payment isolation failed")
	}
	// Repeat the old selection and resume after restart: the original provider
	// record and URL must be reused; never generate another invoice or order.
	f.click(selection, pay)
	card = f.waitButton("payqr:")
	if paymentLink(card) != originalLink {
		t.Fatal("repeat selection changed payment")
	}
	s.docker("restart", f.bot)
	f.healthy()
	f.command("/orders")
	menu = f.waitButton("order:")
	f.click(menu, button(menu, "order:", fmt.Sprint(oid)))
	menu = f.waitButton("resume:")
	f.click(menu, button(menu, "resume:", ""))
	card = f.waitButton("payqr:")
	if paymentLink(card) != originalLink || len(f.state().Payments) != 1 {
		t.Fatal("resume after restart created payment")
	}
	f.click(card, button(card, "cancelask:", ""))
	review := f.waitButton("cancelgo:")
	oldCancel := button(review, "cancelgo:", "")
	f.click(review, button(review, "cancelback:", ""))
	card = f.waitButton("payqr:")
	f.click(review, oldCancel)
	f.waitText("暂时无法核对")
	detail, e := c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", oid)
	detail = must(t, "kept pending order", detail, e)
	if detail.Status != "pending_payment" {
		t.Fatal("back allowed stale cancellation")
	}
	f.click(card, button(card, "cancelask:", ""))
	review = f.waitButton("cancelgo:")
	confirm := button(review, "cancelgo:", "")
	f.click(review, confirm)
	f.waitText("订单已关闭")
	f.click(review, confirm)
	f.waitText("订单已关闭")
	s.docker("restart", f.bot)
	f.healthy()
	f.click(review, confirm)
	f.waitText("订单已关闭")
	detail, e = c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", oid)
	detail = must(t, "cancelled order", detail, e)
	if detail.Status != "canceled" && detail.Status != "cancelled" {
		t.Fatal("order cancellation not authoritative")
	}
	final, e := c.Orders(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 10)
	final = must(t, "final order count", final, e)
	if final.Total != 1 || len(f.state().Payments) != 1 {
		t.Fatal("resume/cancel created extra financial objects")
	}
	f.assertWallet(c, "0.00", 0)
	audit := s.docker("exec", f.bot, "cat", "/tmp/audit.jsonl")
	if strings.Contains(audit, originalLink) || !strings.Contains(audit, "order.cancel") {
		t.Fatal("pending action audit missing or leaked payment URL")
	}
	t.Log("PASS: latest owner-scoped payment reused across double-click and restart; cancel back invalidates old confirmation, final cancellation/replay/restart leaves one order/invoice and unchanged wallet")
}
