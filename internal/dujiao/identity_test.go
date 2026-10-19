package dujiao

import (
	"encoding/json"
	"testing"
)

func TestIdentityResultRealChannelShape(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       IdentityResult
		invalid    bool
	}{
		{"real nested", `{"bound":true,"created":true,"user":{"id":42,"email":"synthetic@example.invalid","display_name":"测试","locale":"zh-CN"},"identity":{"provider":"telegram"}}`, IdentityResult{Bound: true, Created: true, UserID: 42, Email: "synthetic@example.invalid", Nickname: "测试", Locale: "zh-CN"}, false},
		{"flat adapter", `{"bound":true,"user_id":7,"nickname":"adapter"}`, IdentityResult{Bound: true, UserID: 7, Nickname: "adapter"}, false},
		{"nested authoritative", `{"user_id":7,"email":"wrong@example.invalid","member_level":"other","wallet_balance":"999.00","user":{"id":42}}`, IdentityResult{UserID: 42}, false},
		{"unbound resets old result", `{"bound":false}`, IdentityResult{}, false},
		{"invalid user", `{"bound":true,"user":{"id":"wrong"}}`, IdentityResult{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := IdentityResult{UserID: 99, Bound: true, Email: "stale@example.invalid"}
			err := json.Unmarshal([]byte(tc.body), &r)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid nested ID accepted")
				}
				return
			}
			if err != nil || r != tc.want {
				t.Fatalf("identity decode mismatch: %+v, %v", r, err)
			}
		})
	}
}

func TestRealOrderCancellationTimestamp(t *testing.T) {
	for _, field := range []string{"canceled_at", "cancelled_at"} {
		body := []byte(`{"order_id":7,"status":"canceled","` + field + `":"2026-10-20T00:00:00Z"}`)
		var cancelled OrderCancelled
		var detail OrderDetail
		if err := json.Unmarshal(body, &cancelled); err != nil || cancelled.CancelledAt != "2026-10-20T00:00:00Z" || cancelled.OrderID != 7 {
			t.Fatal("cancellation timestamp not decoded", err)
		}
		if err := json.Unmarshal(body, &detail); err != nil || detail.CancelledAt != cancelled.CancelledAt || detail.OrderID != 7 {
			t.Fatal("detail cancellation timestamp not decoded", err)
		}
	}
}
