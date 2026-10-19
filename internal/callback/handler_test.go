package callback

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

type recordingNotifier struct {
	called bool
	userID int64
	event  string
}

func signedRequest(uid string) *http.Request {
	body := `{"order_id":7,"telegram_user_id":"` + uid + `"}`
	r := httptest.NewRequest(http.MethodPost, "/internal/order-paid", strings.NewReader(body))
	timestamp := time.Now().Unix()
	r.Header.Set("Dujiao-Next-Channel-Key", "key")
	r.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
	r.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign("secret", http.MethodPost, r.URL.Path, timestamp, []byte(body)))
	return r
}

func TestCallbackPerUserLimitAndUntrustedForwardedHeader(t *testing.T) {
	notifier := &recordingNotifier{}
	policy := ratelimit.DefaultPolicy()
	policy.Callback.Limit = 1
	policy.HTTP.Limit = 2
	policy.Callback.Window = time.Hour
	policy.HTTP.Window = time.Hour
	h := New("key", "secret", notifier, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Policy: policy})
	for i, want := range []int{200, 503, 429} {
		r := signedRequest("42")
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("X-Forwarded-For", fmt.Sprint(i))
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if response.Code != want {
			t.Fatalf("request %d: got %d, want %d", i, response.Code, want)
		}
		if i > 0 && response.Header().Get("Retry-After") == "" {
			t.Fatal("missing rate denial/retry header", response.Code)
		}
	}
}

type unavailableLimiter struct{}

func (unavailableLimiter) Allow(context.Context, string, ratelimit.Rule) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, ratelimit.ErrUnavailable
}

func TestCallbackLimiterFailureReturns503WithoutNotifying(t *testing.T) {
	notifier := &recordingNotifier{}
	h := New("key", "secret", notifier, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Policy: ratelimit.DefaultPolicy(), Limiter: unavailableLimiter{}})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, signedRequest("42"))
	if response.Code != 503 || notifier.called || response.Header().Get("Retry-After") != "1" {
		t.Fatal("limiter failure was not fail-closed", response.Code)
	}
}

func TestCallbackRejectsOversizedBody(t *testing.T) {
	h := New("key", "secret", &recordingNotifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := httptest.NewRequest(http.MethodPost, "/internal/order-paid", strings.NewReader(strings.Repeat("x", (64<<10)+1)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal(w.Code)
	}
}

func (n *recordingNotifier) Notify(_ context.Context, userID int64, event string, _ Event) error {
	n.called, n.userID, n.event = true, userID, event
	return nil
}

func TestHandlerAcceptsSignedCallback(t *testing.T) {
	notifier := &recordingNotifier{}
	handler := New("key", "secret", notifier, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1700000000, 0)
	handler.now = func() time.Time { return now }
	body := `{"order_id":7,"telegram_user_id":"42"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/order-paid", strings.NewReader(body))
	request.Header.Set("Dujiao-Next-Channel-Key", "key")
	request.Header.Set("Dujiao-Next-Channel-Timestamp", "1700000000")
	request.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign("secret", http.MethodPost, request.URL.Path, now.Unix(), []byte(body)))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !notifier.called || notifier.userID != 42 || notifier.event != "order-paid" {
		t.Fatalf("response=%d notifier=%#v", response.Code, notifier)
	}
}

func TestHandlerRejectsBadSignature(t *testing.T) {
	handler := New("key", "secret", &recordingNotifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.now = func() time.Time { return time.Unix(1700000000, 0) }
	request := httptest.NewRequest(http.MethodPost, "/internal/order-paid", strings.NewReader(`{"telegram_user_id":"42"}`))
	request.Header.Set("Dujiao-Next-Channel-Key", "key")
	request.Header.Set("Dujiao-Next-Channel-Timestamp", "1700000000")
	request.Header.Set("Dujiao-Next-Channel-Signature", "bad")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}
