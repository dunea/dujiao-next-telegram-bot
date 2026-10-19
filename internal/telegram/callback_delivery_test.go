package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestRealSDKCallbackDeliveryFaults(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     int
		drop     bool
		want     int
		attempts int64
	}{
		{"429", 429, false, 503, 2}, {"5xx", 500, false, 503, 2}, {"blocked", 403, false, 200, 1}, {"accepted response lost", 0, true, 503, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls, accepted atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/getMe"):
					fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Test"}}`)
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					n := calls.Add(1)
					if n == 1 && test.drop {
						accepted.Add(1)
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
						return
					}
					if n == 1 && test.code != 0 {
						w.WriteHeader(test.code)
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": test.code, "description": "PRIVATE_SDK_ERROR_BODY", "parameters": map[string]int{"retry_after": 1}})
						return
					}
					accepted.Add(1)
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"synthetic"}}`)
				default:
					t.Error("callback attempted merchant/financial API")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			var logs auditBuffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			api, _ := dujiao.NewClient(server.URL, "key", "secret", time.Second)
			app, err := New("999:test-token", api, dujiao.BotConfig{}, "zh-CN", time.Second, server.URL, logger)
			if err != nil {
				t.Fatal(err)
			}
			h := callback.New("key", "secret", app, logger, app.CallbackOptions())
			invoke := func() int {
				body := []byte(`{"order_id":7,"telegram_user_id":"42"}`)
				r := httptest.NewRequest("POST", "/internal/order-paid", bytes.NewReader(body))
				stamp := time.Now().Unix()
				r.Header.Set("Dujiao-Next-Channel-Key", "key")
				r.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(stamp, 10))
				r.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign("secret", "POST", r.URL.Path, stamp, body))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code
			}
			if invoke() != test.want {
				t.Fatal("incorrect first delivery result")
			}
			invoke()
			if calls.Load() != 1 {
				t.Fatal("duplicate ignored callback cooldown")
			}
			if test.attempts == 2 {
				time.Sleep(1100 * time.Millisecond)
			}
			if invoke() != 200 || calls.Load() != test.attempts {
				t.Fatal("SDK recovery/rejection mismatch")
			}
			if test.drop && accepted.Load() != 2 {
				t.Fatal("response-loss ambiguity was hidden instead of documented")
			}
			if strings.Contains(logs.text(), "PRIVATE_SDK") || strings.Contains(logs.text(), "test-token") {
				t.Fatal("SDK error leaked secrets")
			}
		})
	}
}
