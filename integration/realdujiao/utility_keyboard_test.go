//go:build realdujiao

package realdujiao

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTelegramUtilityNavigation(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	f.command("/start")
	f.waitText("欢迎")
	f.command("/help")
	help := f.waitButton("bcsubscriptions")
	assertCommerceKeyboard(t, help, "", "orders:a:1", "wallet", "lang", "bcsubscriptions", "cats", "home")
	assertLanguage := func(m tgMessage, selected string) {
		t.Helper()
		var k struct {
			Rows [][]struct {
				Text string `json:"text"`
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		if json.Unmarshal(m.Keyboard, &k) != nil || len(k.Rows) != 2 || len(k.Rows[0]) != 3 || len(k.Rows[1]) != 2 {
			t.Fatal("language choice row is not compact")
		}
		marked := 0
		for _, b := range k.Rows[0] {
			if strings.HasPrefix(b.Text, "✓ ") {
				marked++
				if b.Data != "setlang:"+selected {
					t.Fatal("language choice marks the wrong preference")
				}
			}
		}
		if marked != 1 || button(m, "help", "") == "" || button(m, "home", "") == "" {
			t.Fatal("language selection or return navigation missing")
		}
	}
	f.click(help, button(help, "lang", ""))
	language := f.waitButton("setlang:")
	assertLanguage(language, "zh-CN")
	f.click(language, "setlang:en-US")
	home := f.waitText("Language updated.")
	f.click(home, button(home, "help", ""))
	help = f.waitButton("bcsubscriptions")
	assertCommerceKeyboard(t, help, "", "orders:a:1", "wallet", "lang", "cats", "home")
	f.click(help, button(help, "bcsubscriptions", ""))
	off := f.waitText("Not subscribed to announcements.")
	assertCommerceKeyboard(t, off, "bcsubscribe", "help", "lang", "cats", "home")
	if off.Method != "editMessageText" || off.ID != help.ID {
		t.Fatal("subscription navigation created unnecessary chat messages")
	}
	f.click(off, button(off, "bcsubscribe", ""))
	on := f.waitText("Announcements subscribed.")
	assertCommerceKeyboard(t, on, "bcunsubscribe", "help", "lang", "cats", "home")
	if on.Method != "editMessageText" || on.ID != off.ID {
		t.Fatal("subscription result did not update the current panel")
	}
	f.click(off, button(off, "bcsubscribe", "")) // Explicit on is idempotent, not a toggle.
	f.waitText("Announcements subscribed.")
	f.s.docker("restart", f.bot)
	f.healthy()
	f.command("/help")
	help = f.waitButton("bcsubscriptions")
	f.click(help, button(help, "bcsubscriptions", ""))
	on = f.waitText("Subscribed to announcements.")
	assertCommerceKeyboard(t, on, "bcunsubscribe", "help", "lang", "cats", "home")
	f.click(on, button(on, "bcunsubscribe", ""))
	off = f.waitText("Announcements unsubscribed.")
	assertCommerceKeyboard(t, off, "bcsubscribe", "help", "lang", "cats", "home")
	f.click(on, button(on, "bcunsubscribe", ""))
	f.waitText("Announcements unsubscribed.")
	f.command("/subscribe")
	on = f.waitText("Announcements subscribed.")
	if on.Method != "sendMessage" {
		t.Fatal("command result is hidden in an older message")
	}
	f.command("/unsubscribe")
	f.waitText("Announcements unsubscribed.")
	f.command("/language")
	language = f.waitButton("setlang:")
	assertLanguage(language, "en-US")
	f.click(language, "setlang:zh-TW")
	f.waitText("語言已切換")
	f.command("/help")
	help = f.waitButton("bcsubscriptions")
	f.click(help, button(help, "bcsubscriptions", ""))
	off = f.waitText("尚未訂閱公告")
	assertCommerceKeyboard(t, off, "bcsubscribe", "help", "lang", "cats", "home")
	// Opening the translated panel did not silently resubscribe.
	f.click(off, button(off, "lang", ""))
	language = f.waitButton("setlang:")
	assertLanguage(language, "zh-TW")
	f.click(language, "setlang:zh-CN")
	f.waitText("语言已切换")
	c := f.s.channel(f.config, false)
	orders, err := c.Orders(context.Background(), fmt.Sprint(buyerID), "zh-CN", "", 1, 20)
	if err != nil || orders.Total != 0 || len(f.state().Payments) != 0 {
		t.Fatal("utility navigation created orders or payments")
	}
	f.assertWallet(c, "0.00", 0)
	for _, m := range f.state().Messages {
		if m.Method == "sendDocument" || strings.HasPrefix(m.Text, "📣 商店公告") {
			t.Fatal("utility navigation delivered files or started a broadcast")
		}
	}
	t.Log("PASS: help one-hop actions -> three-language selection -> localized opt-in panel edited in place -> repeated explicit actions and restart persistence -> command replies below input; no orders, money, files or announcements")
}
