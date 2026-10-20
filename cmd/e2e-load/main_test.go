package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestClassifySeparatesDenialsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		retry, body string
		health      bool
		want        string
	}{
		{"healthy", 200, "", `{"status":"ok"}`, true, "ok"},
		{"unhealthy", 503, "1", `{"status":"session_unavailable"}`, true, "unexpected"},
		{"sent", 200, "", `{"status":"sent"}`, false, "ok"},
		{"duplicate", 200, "", `{"status":"duplicate_or_obsolete"}`, false, "ok"},
		{"fake success", 200, "", `{"status":"notify_failed"}`, false, "unexpected"},
		{"rate", 503, "1", `{"status":"rate_limited"}`, false, "limited"},
		{"HTTP rate", 429, "1", `{"status":"rate_limited"}`, false, "limited"},
		{"missing cooldown", 503, "", `{"status":"rate_limited"}`, false, "unexpected"},
		{"invalid cooldown", 503, "NaN", `{"status":"rate_limited"}`, false, "unexpected"},
		{"busy", 503, "2", `{"status":"delivery_unavailable"}`, false, "retry"},
		{"SDK429", 503, "1", `{"status":"telegram_rate_limited"}`, false, "retry"},
		{"SDK500", 503, "1", `{"status":"notify_failed"}`, false, "retry"},
		{"Redis receipt", 503, "1", `{"status":"receipt_unavailable"}`, false, "unexpected"},
		{"Redis limiter", 503, "1", `{"status":"limiter_unavailable"}`, false, "unexpected"},
		{"generic 503", 503, "1", `{"status":"unknown"}`, false, "unexpected"},
		{"auth", 401, "", `{"status":"invalid_signature"}`, false, "unexpected"},
		{"bad JSON", 503, "1", `<html>`, false, "unexpected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.status, tc.retry, []byte(tc.body), tc.health); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBoundedMetricsFailClosed(t *testing.T) {
	var m metrics
	for range 4800 {
		m.add(outcome{true, "ok", time.Millisecond})
	}
	for range 200 {
		m.add(outcome{false, "ok", 10 * time.Millisecond})
	}
	for range 1000 {
		m.add(outcome{false, "limited", time.Millisecond})
	}
	if !m.valid(time.Minute) || m.p99() != 25 {
		t.Fatal("normal load thresholds failed")
	}
	for _, mutate := range []func(*metrics){
		func(m *metrics) { m.Unexpected++ }, func(m *metrics) { m.Dropped++ }, func(m *metrics) { m.Retryable = 17 }, func(m *metrics) { m.Limited = 0 }, func(m *metrics) { m.Accepted = m.Health }, func(m *metrics) { m.Requests = 5000 }, func(m *metrics) { m.hist = [201]int64{}; m.hist[80] = m.Health },
	} {
		copy := m
		mutate(&copy)
		if copy.valid(time.Minute) {
			t.Fatal("bad load accepted")
		}
	}
	data, _ := json.Marshal(m)
	if strings.Contains(string(data), "hist") {
		t.Fatal("unbounded/internal metrics exposed")
	}
}

func TestRequestsUseSignedNotificationOnlyPaths(t *testing.T) {
	for id := 0; id < 3; id++ {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, event := eventFor(id)
			if r.Method != "POST" || r.URL.Path != "/internal/"+kind {
				t.Error("load attempted a non-notification write")
			}
			var body bytes.Buffer
			_, _ = body.ReadFrom(r.Body)
			stamp, _ := strconv.ParseInt(r.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
			if !dujiao.Verify("secret", r.Method, r.URL.Path, stamp, body.Bytes(), r.Header.Get("Dujiao-Next-Channel-Signature")) {
				t.Error("unsigned load")
			}
			var decoded callback.Event
			if json.Unmarshal(body.Bytes(), &decoded) != nil || decoded != event {
				t.Error("incorrect synthetic event")
			}
			_, _ = w.Write([]byte(`{"status":"sent"}`))
		}))
		r := runner{client: s.Client(), bot: s.URL, key: "key", secret: "secret"}
		if o := r.request(context.Background(), false, id); o.category != "ok" {
			t.Fatal("synthetic request failed")
		}
		s.Close()
	}
}

func TestCanceledLoadTerminatesWithoutAcceptingPartialRun(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	defer s.Close()
	r := runner{client: s.Client(), bot: s.URL, peer: s.URL, key: "key", secret: "secret"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.run(ctx, time.Minute, json.NewEncoder(&bytes.Buffer{})); err == nil {
		t.Fatal("canceled load was marked passed")
	}
}
