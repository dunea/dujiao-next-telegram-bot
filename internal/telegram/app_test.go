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

func TestNormalizeMoneyInput(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "50", want: "50.00", ok: true},
		{input: " 12.5 ", want: "12.50", ok: true},
		{input: "0", ok: false},
		{input: "1.234", ok: false},
		{input: "abc", ok: false},
		{input: "1000001", ok: false},
	}
	for _, test := range tests {
		got, ok := normalizeMoneyInput(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("normalizeMoneyInput(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestFormatFulfillmentPayload(t *testing.T) {
	got := formatFulfillmentPayload(map[string]any{"card": "DEMO-123"})
	if got != "{\n  \"card\": \"DEMO-123\"\n}" {
		t.Fatalf("formatFulfillmentPayload = %q", got)
	}
	if got := formatFulfillmentPayload(nil); got != "" {
		t.Fatalf("nil payload = %q", got)
	}
}

func TestOrderFilterRoundTrip(t *testing.T) {
	for status, code := range map[string]string{"": "a", "pending_payment": "p", "completed": "c"} {
		if got := orderFilterCode(status); got != code {
			t.Errorf("orderFilterCode(%q) = %q, want %q", status, got, code)
		}
		if got := orderFilterStatus(code); got != status {
			t.Errorf("orderFilterStatus(%q) = %q, want %q", code, got, status)
		}
	}
}

func TestWalletTransactionPresentation(t *testing.T) {
	if got := walletTransactionSign("out"); got != "−" {
		t.Fatalf("out sign = %q", got)
	}
	if got := walletTransactionType("en-US", "gift_card_redeem"); got != "Gift card redemption" {
		t.Fatalf("type label = %q", got)
	}
}

func TestPurchaseQuantityBounds(t *testing.T) {
	productStock := int64(4)
	skuStock := int64(3)
	product := dujiao.ProductDetail{
		Product:             dujiao.Product{StockStatus: "in_stock", StockCount: &productStock},
		MinPurchaseQuantity: 2,
		MaxPurchaseQuantity: 10,
	}
	minimum, maximum, available := purchaseQuantityBounds(product, 0)
	if minimum != 2 || maximum != 4 || !available {
		t.Fatalf("product bounds = %d, %d, %v", minimum, maximum, available)
	}

	product.SKUs = []dujiao.SKU{{ID: 9, StockStatus: "in_stock", StockCount: &skuStock}}
	minimum, maximum, available = purchaseQuantityBounds(product, 9)
	if minimum != 2 || maximum != 3 || !available {
		t.Fatalf("SKU bounds = %d, %d, %v", minimum, maximum, available)
	}
	if _, _, available = purchaseQuantityBounds(product, 10); available {
		t.Fatal("unknown SKU was available")
	}

	skuStock = 1
	if _, _, available = purchaseQuantityBounds(product, 9); available {
		t.Fatal("stock below minimum was available")
	}

	product.SKUs = nil
	product.ManualFormSchema.Fields = []dujiao.ManualFormField{{Key: "hostname"}}
	if _, _, available = purchaseQuantityBounds(product, 0); available {
		t.Fatal("manual form product bypassed the form flow")
	}
}

func TestClampPurchaseQuantity(t *testing.T) {
	for _, test := range []struct {
		quantity int
		minimum  int
		maximum  int
		want     int
	}{
		{quantity: 1, minimum: 2, maximum: 5, want: 2},
		{quantity: 9, minimum: 2, maximum: 5, want: 5},
		{quantity: 9, minimum: 2, maximum: 0, want: 9},
	} {
		if got := clampPurchaseQuantity(test.quantity, test.minimum, test.maximum); got != test.want {
			t.Errorf("clampPurchaseQuantity(%d, %d, %d) = %d, want %d", test.quantity, test.minimum, test.maximum, got, test.want)
		}
	}
}
