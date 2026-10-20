//go:build realdujiao

package realdujiao

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type supportButton struct {
	URL  string `json:"url"`
	Data string `json:"callback_data"`
	Copy *struct {
		Text string `json:"text"`
	} `json:"copy_text"`
}

func supportButtons(m tgMessage) []supportButton {
	var keyboard struct {
		Rows [][]supportButton `json:"inline_keyboard"`
	}
	_ = json.Unmarshal(m.Keyboard, &keyboard)
	var buttons []supportButton
	for _, row := range keyboard.Rows {
		buttons = append(buttons, row...)
	}
	return buttons
}

func (f *telegramFlow) supportConfig(target string) {
	s := f.s
	var cfg map[string]any
	s.admin("GET", "/settings/telegram-bot", s.login(), nil, &cfg)
	basic, ok := cfg["basic"].(map[string]any)
	if !ok {
		s.t.Fatal("support config missing basic section")
	}
	basic["support_url"] = target
	s.admin("PUT", "/settings/telegram-bot", s.login(), cfg, nil)
	var got map[string]any
	s.admin("GET", "/settings/telegram-bot", s.login(), nil, &got)
	cfg["config_version"] = got["config_version"]
	expected, _ := json.Marshal(cfg)
	actual, _ := json.Marshal(got)
	if string(expected) != string(actual) {
		s.t.Fatal("support update altered unrelated store settings")
	}
	s.docker("restart", f.bot)
	f.healthy()
}

func TestTelegramOrderSupport(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	f.supportConfig("https://t.me/synthetic_support")
	f.command("/start")
	f.waitText("欢迎")
	c, ctx := f.s.channel(f.config, false), context.Background()
	o, err := c.CreateOrder(ctx, dujiao.OrderRequest{TelegramIdentity: dujiao.TelegramIdentity{ChannelUserID: fmt.Sprint(buyerID), Locale: "zh-CN"}, Items: []dujiao.OrderItemRequest{{ProductID: f.config.Product, SKUID: f.config.SKU, Quantity: 1}}})
	o = must(t, "support synthetic order", o, err)
	f.command("/orders " + o.OrderNo)
	view := f.waitButton("support:")
	assertDraft := func(m tgMessage, reason string) {
		t.Helper()
		found := false
		for _, b := range supportButtons(m) {
			if b.URL == "" {
				continue
			}
			u, parseErr := url.Parse(b.URL)
			if parseErr == nil && u.Host == "t.me" && u.Path == "/synthetic_support" && strings.Contains(u.Query().Get("text"), o.OrderNo) && strings.Contains(u.Query().Get("text"), reason) {
				found = true
			}
		}
		if !found {
			t.Fatal("support recipient/order/reason missing in draft URL")
		}
	}
	assertDraft(view, "订单咨询")
	f.click(view, button(view, "support:", ":g"))
	help := f.waitButton("support:")
	assertCommerceKeyboard(t, help, "url", "support:", "order:")
	assertDraft(help, "订单咨询")
	f.click(help, button(help, "support:", ":d"))
	help = f.waitText("交付或使用问题")
	assertCommerceKeyboard(t, help, "url", "support:", "order:")
	assertDraft(help, "交付或使用问题")
	copied := false
	for _, b := range supportButtons(help) {
		if b.Copy != nil && strings.Contains(b.Copy.Text, o.OrderNo) && strings.Contains(b.Copy.Text, "交付或使用问题") {
			copied = true
		}
	}
	if !copied || button(help, fmt.Sprintf("order:%d:", o.OrderID), ":a:1") == "" {
		t.Fatal("copy fallback or order return missing")
	}
	// Reboot preserves no support draft because this is owner-scoped read-only
	// navigation. Every old issue button re-reads the order and current config.
	f.s.docker("restart", f.bot)
	f.healthy()
	f.click(help, button(help, "support:", ":p"))
	help = f.waitText("支付未到账")
	assertDraft(help, "支付未到账")
	for _, target := range []string{"https://support.example.invalid/help?lang=zh", ""} {
		f.supportConfig(target)
		f.click(help, button(help, "support:", ":g"))
		help = f.waitText("订单售后")
		assertCommerceKeyboard(t, help, "", "support:", "order:")
		if target == "" {
			if !strings.Contains(help.Text, "尚未配置") {
				t.Fatal("missing contact not explained")
			}
		} else if !strings.Contains(help.Text, "先复制售后消息") {
			t.Fatal("website fallback misleading")
		}
		for _, b := range supportButtons(help) {
			if b.URL != "" && b.URL != target {
				t.Fatal("fallback URL injected or wrong recipient")
			}
		}
	}
	final, err := c.Order(ctx, fmt.Sprint(buyerID), "zh-CN", o.OrderID)
	final = must(t, "support final order", final, err)
	if final.Status != "pending_payment" || len(f.state().Payments) != 0 {
		t.Fatal("support wrote payment/order state")
	}
	f.assertWallet(c, "0.00", 0)
	t.Log("PASS: owned order direct support draft, issue-specific copy buttons, restart and config changes, website/missing-link fallback; no contact messages, tickets, refunds or financial writes")
}
