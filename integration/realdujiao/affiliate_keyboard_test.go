//go:build realdujiao

package realdujiao

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestTelegramAffiliateWithdrawal(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	s, ctx := f.s, context.Background()
	c := s.channel(f.config, false)
	uid := fmt.Sprint(buyerID)
	s.admin("PUT", "/settings/affiliate", s.login(), map[string]any{"enabled": true, "commission_rate": 10, "confirm_days": 0, "min_withdraw_amount": 0.5, "withdraw_channels": []string{"test-only", "test-two"}}, nil)
	f.command("/start")
	f.waitText("欢迎")
	f.command("/affiliate")
	view := f.waitButton("affopen")
	assertCommerceKeyboard(t, view, "affopen", "affiliate", "home")
	dashboard, err := c.AffiliateDashboard(ctx, uid)
	if err != nil || dashboard.Opened {
		t.Fatal("viewing affiliate page opened a profile")
	}
	f.click(view, button(view, "affopen", ""))
	view = f.waitButton("affcomm:")
	assertCommerceKeyboard(t, view, "url", "affapply", "affwithdraws:1", "affiliate", "home")
	var copied, shared string
	for _, b := range supportButtons(view) {
		if b.Copy != nil {
			copied = b.Copy.Text
		}
		if u, parseErr := url.Parse(b.URL); parseErr == nil && u.Host == "t.me" && u.Path == "/share/url" {
			shared = u.Query().Get("url")
		}
	}
	if copied == "" || copied != shared || !strings.Contains(copied, "?start=aff_") {
		t.Fatal("referral copy and share do not match")
	}
	dashboard, err = c.AffiliateDashboard(ctx, uid)
	dashboard = must(t, "opened affiliate", dashboard, err)
	// Earn commission from a different synthetic buyer's real paid order.
	// No SQL-seeded commission, merchant approval or payout call is used.
	other := dujiao.TelegramIdentity{ChannelUserID: "910000002", FirstName: "Synthetic referred buyer", Locale: "zh-CN"}
	_, err = c.ProvisionIdentity(ctx, other)
	if err != nil {
		t.Fatal("synthetic referred identity unavailable")
	}
	s.admin("POST", "/gift-cards/generate", s.login(), map[string]any{"name": "synthetic-affiliate", "quantity": 1, "amount": "100.00"}, nil)
	var cards []struct{ Code string }
	s.admin("GET", "/gift-cards?status=active", s.login(), nil, &cards)
	if len(cards) != 1 {
		t.Fatal("synthetic gift card missing")
	}
	_, err = c.RedeemGiftCard(ctx, other.ChannelUserID, cards[0].Code)
	if err != nil {
		t.Fatal("synthetic referred wallet funding failed")
	}
	o, err := c.CreateOrder(ctx, dujiao.OrderRequest{TelegramIdentity: other, Items: []dujiao.OrderItemRequest{{ProductID: f.config.Product, SKUID: f.config.SKU, Quantity: 1}}, AffiliateCode: dashboard.AffiliateCode})
	o = must(t, "synthetic referred order", o, err)
	p, err := c.CreatePayment(ctx, other.ChannelUserID, o.OrderID, 0, true)
	if err != nil || !p.OrderPaid {
		t.Fatal("synthetic referred payment failed")
	}
	s.wait(func() bool {
		d, readErr := c.AffiliateDashboard(ctx, uid)
		return readErr == nil && d.AvailableCommission == "0.73"
	})
	f.click(view, button(view, "affcomm:", ""))
	view = f.waitText("佣金明细")
	assertCommerceKeyboard(t, view, "", "affapply", "affwithdraws:1", "affiliate", "home")
	if !strings.Contains(view.Text, "0.73 CNY") {
		t.Fatal("real earned commission not displayed")
	}
	prepare := func(entry tgMessage) tgMessage {
		t.Helper()
		f.click(entry, button(entry, "affapply", ""))
		prompt := f.waitText("请回复本条消息输入提现金额")
		f.command("0.10", prompt)
		f.waitText("金额无效")
		f.command("0.50", prompt)
		channels := f.waitButton("wd:")
		assertCommerceKeyboard(t, channels, "", "wd:")
		f.click(channels, button(channels, "wd:", ":channel:1"))
		prompt = f.waitText("填写收款账号或地址")
		f.command("SYNTHETIC-NO-PAYOUT", prompt)
		review := f.waitText("请核对后再确认提交")
		assertCommerceKeyboard(t, review, "wd:", "wd:", "affwithdraws:1")
		if !strings.Contains(review.Text, "0.50 CNY") || !strings.Contains(review.Text, "test-two") {
			t.Fatal("withdrawal review changed amount or channel identity")
		}
		history, readErr := c.AffiliateWithdrawals(ctx, uid, 1)
		if readErr != nil || history.Total != 0 {
			t.Fatal("input or review submitted a withdrawal")
		}
		return review
	}
	review := prepare(view)
	stale := button(review, "wd:", ":confirm")
	f.click(review, button(review, "affwithdraws:1", ""))
	view = f.waitText("当前页没有提现记录")
	assertCommerceKeyboard(t, view, "", "affapply", "affcomm:1", "affiliate", "home")
	f.click(review, stale)
	f.waitText("这组提现按钮已失效")
	review = prepare(view)
	confirm := button(review, "wd:", ":confirm")
	f.click(review, confirm)
	receipt := f.waitText("提现申请已记录")
	assertCommerceKeyboard(t, receipt, "affwithdraws:1", "affiliate", "home")
	f.click(review, confirm)
	f.waitText("这组提现按钮已失效")
	s.docker("restart", f.bot)
	f.healthy()
	f.click(review, confirm)
	f.waitText("这组提现按钮已失效")
	f.click(receipt, button(receipt, "affwithdraws:1", ""))
	view = f.waitText("提现记录")
	assertCommerceKeyboard(t, view, "", "affapply", "affcomm:1", "affiliate", "home")
	if !strings.Contains(view.Text, "待审核") || strings.Contains(view.Text, "SYNTHETIC-NO-PAYOUT") {
		t.Fatal("withdrawal status missing or receiving details unmasked")
	}
	history, err := c.AffiliateWithdrawals(ctx, uid, 1)
	if err != nil || history.Total != 1 || history.Items[0].Amount != "0.50" || history.Items[0].Channel != "test-two" || history.Items[0].Status != "pending_review" {
		t.Fatal("exit, repeat or restart duplicated or paid a withdrawal")
	}
	dashboard, err = c.AffiliateDashboard(ctx, uid)
	if err != nil || dashboard.AvailableCommission != "0.23" || len(f.state().Payments) != 0 {
		t.Fatal("commission reservation or gateway state incorrect")
	}
	f.assertWallet(c, "0.00", 0)
	t.Log("PASS: compact affiliate copy/share -> real earned commission -> direct history/apply -> amount validation -> channel grid -> review/exit invalidates old confirm -> one pending-review request across repeat/restart; no payout or wallet charge")
}
