package telegram

import (
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestParseCommand(t *testing.T) {
	command, argument := parseCommand("/start@store_bot invite-123")
	if command != "/start" || argument != "invite-123" {
		t.Fatalf("got %q %q", command, argument)
	}
}

func TestNormalizeLocale(t *testing.T) {
	tests := map[string]string{
		"zh-CN":   "zh-CN",
		"zh-Hant": "zh-TW",
		"zh-HK":   "zh-TW",
		"en":      "en-US",
		"de-DE":   "en-US",
	}
	for input, want := range tests {
		if got := normalizeLocale(input); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("独角数卡", 3); got != "独角…" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestConfiguredMenuRowsHonorsEnabledItems(t *testing.T) {
	rows := configuredMenuRows([]dujiao.BotMenuItem{
		{Key: "my_orders", Enabled: true, Order: 2, Label: dujiao.LocalizedText{"en-US": "Orders"}},
		{Key: "shop_home", Enabled: false, Order: 1, Label: dujiao.LocalizedText{"en-US": "Shop"}},
		{Key: "docs", Enabled: true, Order: 3, Label: dujiao.LocalizedText{"en-US": "Docs"}, Action: dujiao.BotMenuAction{Type: "url", Value: "https://example.com"}},
	}, "en-US")
	if len(rows) != 2 || rows[0][0].CallbackData != "orders:1" || rows[1][0].URL != "https://example.com" {
		t.Fatalf("rows = %#v", rows)
	}
}
