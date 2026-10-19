package telegram

import "testing"

func TestRealChannelCanceledStatus(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		if got := orderStatus(locale, "canceled"); got == "canceled" || got != orderStatus(locale, "cancelled") {
			t.Fatalf("missing canceled translation for %s", locale)
		}
	}
}
