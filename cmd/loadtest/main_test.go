package main

import "testing"

func TestExpectedRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		retry  string
		body   string
		want   bool
	}{
		{"ingress", 429, "1", `{"status":"rate_limited"}`, true},
		{"signed user retry", 503, "2", `{"status":"rate_limited"}`, true},
		{"missing cooldown", 503, "", `{"status":"rate_limited"}`, false},
		{"receipt outage", 503, "2", `{"status":"receipt_unavailable"}`, false},
		{"limiter outage", 503, "1", `{"status":"limiter_unavailable"}`, false},
		{"telegram throttle", 503, "2", `{"status":"telegram_rate_limited"}`, false},
		{"generic server error", 500, "1", `{"status":"rate_limited"}`, false},
		{"invalid JSON", 429, "1", `{"status":"rate_limited"`, false},
		{"unrelated response", 429, "1", `<html>rate_limited</html>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := expectedRateLimit(tc.status, tc.retry, []byte(tc.body)); got != tc.want {
				t.Fatalf("expectedRateLimit = %v; want %v", got, tc.want)
			}
		})
	}
}
