//go:build realdujiao

package realdujiao

import (
	"encoding/json"
	"strings"
	"testing"
)

func assertCommerceKeyboard(t *testing.T, m tgMessage, primary string, required ...string) {
	t.Helper()
	var keyboard struct {
		Rows [][]supportButton `json:"inline_keyboard"`
	}
	if json.Unmarshal(m.Keyboard, &keyboard) != nil || len(keyboard.Rows) == 0 {
		t.Fatal("missing commerce keyboard")
	}
	for _, row := range keyboard.Rows {
		if len(row) == 0 || len(row) > 2 {
			t.Fatal("commerce keyboard is not compact")
		}
	}
	if primary != "" {
		row := keyboard.Rows[0]
		if len(row) != 1 || (primary == "url" && row[0].URL == "") || (primary != "url" && !strings.HasPrefix(row[0].Data, primary)) {
			t.Fatal("primary action is not separate")
		}
	}
	for _, prefix := range required {
		if button(m, prefix, "") == "" {
			t.Fatal("commerce shortcut missing", prefix)
		}
	}
}
