package telegram

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

func TestHomeMenuGroupsShortActionsAndKeepsShoppingPrimary(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, configured := range []bool{false, true} {
			a := &App{}
			cfg := dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: "@synthetic_support"}}
			if configured {
				for i, key := range []string{"shop_home", "my_orders", "my_wallet", "affiliate", "switch_language", "contact_support"} {
					cfg.Menu.Items = append(cfg.Menu.Items, dujiao.BotMenuItem{Key: key, Order: i, Enabled: true, Label: dujiao.LocalizedText{"zh-CN": "商店按钮", "zh-TW": "商店按鈕", "en-US": "Store action"}})
				}
			}
			a.UpdateConfig(cfg)
			keyboard := a.homeKeyboard(locale)
			want := [][]string{{"cats"}, {"orders:1", "wallet"}, {"affiliate", "bcsubscriptions"}, {"lang", "help"}, {"https://t.me/synthetic_support"}}
			if len(keyboard.InlineKeyboard) != len(want) {
				t.Fatalf("%s configured=%t: expected five rows, got %d", locale, configured, len(keyboard.InlineKeyboard))
			}
			for i, row := range keyboard.InlineKeyboard {
				if len(row) != len(want[i]) {
					t.Fatal("unexpected column count", i)
				}
				for j, button := range row {
					action := button.CallbackData
					if button.URL != "" {
						action = button.URL
					}
					if action != want[i][j] || strings.TrimSpace(button.Text) == "" || (button.URL != "" && button.CallbackData != "") {
						t.Fatal("action, label or SDK button contract changed", i, j)
					}
				}
			}
			if !reflect.DeepEqual(a.botConfig(), cfg) {
				t.Fatal("presentation mutated merchant settings")
			}
			encoded, err := json.Marshal(keyboard)
			if err != nil || !json.Valid(encoded) {
				t.Fatal("invalid Telegram markup")
			}
		}
	}
}

func TestHomeMenuPreservesMerchantOrderLabelsAndDisabledItems(t *testing.T) {
	a := &App{}
	a.UpdateConfig(dujiao.BotConfig{Menu: dujiao.BotMenuConfig{Items: []dujiao.BotMenuItem{
		{Key: "my_wallet", Order: 3, Enabled: true, Label: dujiao.LocalizedText{"zh-CN": "自定义钱包"}},
		{Key: "my_orders", Order: 1, Enabled: false, Label: dujiao.LocalizedText{"zh-CN": "隐藏订单"}},
		{Key: "shop_home", Order: 2, Enabled: true, Label: dujiao.LocalizedText{"zh-CN": "自定义购物"}},
		{Key: "external", Order: 0, Enabled: true, Label: dujiao.LocalizedText{"zh-CN": "商店网站"}, Action: dujiao.BotMenuAction{Type: "url", Value: "https://shop.example.invalid/info"}},
		{Key: "invalid", Enabled: true, Label: dujiao.LocalizedText{"zh-CN": "无效按钮"}, Action: dujiao.BotMenuAction{Type: "url", Value: "javascript:alert(1)"}},
	}}})
	var labels []string
	for _, row := range a.homeKeyboard("zh-CN").InlineKeyboard {
		if len(row) > 2 {
			t.Fatal("too many columns")
		}
		for _, button := range row {
			labels = append(labels, button.Text)
		}
	}
	if !reflect.DeepEqual(labels, []string{"商店网站", "自定义购物", "自定义钱包", "🔔 公告订阅"}) {
		t.Fatal("merchant order, visibility, URL filtering or labels changed", labels)
	}
}

func TestHomeMenuLongLabelsKeepFullWidthAndAllActions(t *testing.T) {
	buttons := []models.InlineKeyboardButton{
		{Text: "Shop", CallbackData: "cats"},
		{Text: "Orders", CallbackData: "orders:1"},
		{Text: strings.Repeat("长标题", 12), URL: "https://shop.example.invalid/info"},
		{Text: "Wallet", CallbackData: "wallet"},
		{Text: "Help", CallbackData: "help"},
	}
	rows := compactHomeMenuRows(buttons)
	var flattened []models.InlineKeyboardButton
	for _, row := range rows {
		if len(row) == 0 || len(row) > 2 {
			t.Fatal("invalid row width")
		}
		for _, button := range row {
			if (button.CallbackData == "cats" || len([]rune(button.Text)) > 18) && len(row) != 1 {
				t.Fatal("primary or long label squeezed into a pair")
			}
			flattened = append(flattened, button)
		}
	}
	if !reflect.DeepEqual(buttons, flattened) {
		t.Fatal("layout lost, duplicated or altered an action")
	}
}

func TestHomeMenuDoesNotRestoreDisabledConfiguredActions(t *testing.T) {
	a := &App{}
	a.UpdateConfig(dujiao.BotConfig{Menu: dujiao.BotMenuConfig{Items: []dujiao.BotMenuItem{{Key: "shop_home", Enabled: false, Label: dujiao.LocalizedText{"zh-CN": "隐藏购物"}}}}})
	rows := a.homeKeyboard("zh-CN").InlineKeyboard
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0].CallbackData != "bcsubscriptions" {
		t.Fatal("hidden merchant menu restored fallback buttons")
	}
}
