package callback

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type recordingNotifier struct {
	called bool
	userID int64
	event  string
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
