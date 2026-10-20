package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type receiptFunc func(context.Context, string, func(*Receipt) error) (Receipt, error)

func (f receiptFunc) Update(ctx context.Context, key string, fn func(*Receipt) error) (Receipt, error) {
	return f(ctx, key, fn)
}

func assertCallbackResult(t *testing.T, w *httptest.ResponseRecorder, code int, status, stage string) {
	t.Helper()
	var result struct{ Status, Stage string }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != code || result.Status != status || result.Stage != stage {
		t.Fatalf("unexpected callback result: code=%d status=%q stage=%q", w.Code, result.Status, result.Stage)
	}
	if code == 503 && w.Header().Get("Retry-After") != "2" {
		t.Fatal("missing bounded retry delay")
	}
}

func TestReceiptClaimAndFinalizeFailuresAreDistinctFromBusy(t *testing.T) {
	const body = `{"order_id":7,"telegram_user_id":"42"}`
	for _, at := range []int{1, 2} {
		n := &faultNotifier{}
		h := fixtureHandler(n)
		h.options.Receipts = &failedReceipts{inner: NewMemoryReceipts(), failAt: at}
		stage := "claim"
		if at == 2 {
			stage = "finalize"
		}
		assertCallbackResult(t, invoke(h, "/internal/order-paid", body), 503, "receipt_unavailable", stage)
		if n.calls.Load() != int64(at-1) {
			t.Fatal("storage failure authorized an unconfirmed send")
		}
		if at == 2 {
			assertCallbackResult(t, invoke(h, "/internal/order-paid", body), 503, "delivery_busy", "")
			if n.calls.Load() != 1 {
				t.Fatal("failed finalization bypassed lease")
			}
		}
	}

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	n := &faultNotifier{fn: func() error { close(started); <-release; return nil }}
	h := fixtureHandler(n)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- invoke(h, "/internal/order-paid", body) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first callback never reached notifier")
	}
	busy := invoke(h, "/internal/order-paid", body)
	unblock()
	var completed *httptest.ResponseRecorder
	select {
	case completed = <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first callback did not finish")
	}
	assertCallbackResult(t, busy, 503, "delivery_busy", "")
	assertCallbackResult(t, completed, 200, "sent", "")
	assertCallbackResult(t, invoke(h, "/internal/order-paid", body), 200, "duplicate_or_obsolete", "")
	if n.calls.Load() != 1 {
		t.Fatal("concurrent replay sent twice")
	}
}

func TestAcknowledgedSendFinalizesAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &faultNotifier{fn: func() error { cancel(); return nil }}
	h := fixtureHandler(n)
	r := callbackRequest(h, "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, h.now().Unix()).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertCallbackResult(t, w, 200, "sent", "")
	assertCallbackResult(t, invoke(h, r.URL.Path, `{"order_id":7,"telegram_user_id":"42"}`), 200, "duplicate_or_obsolete", "")
	if n.calls.Load() != 1 {
		t.Fatal("cancellation forgot an acknowledged send")
	}
}

func TestUnavailableDiagnosticsAreSampledAndSurviveCancellation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	n := &faultNotifier{}
	h := New("key", "secret", n, logger)
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		h.options.Receipts = receiptFunc(func(context.Context, string, func(*Receipt) error) (Receipt, error) {
			cancel()
			return Receipt{}, fmt.Errorf("PRIVATE_REDIS_URL: %w", StorageRead)
		})
		r := callbackRequest(h, "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, h.now().Unix()).WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertCallbackResult(t, w, 503, "receipt_unavailable", "claim")
	}
	text := logs.String()
	if strings.Count(text, `"msg":"callback unavailable"`) != 1 || !strings.Contains(text, `"reason":"read_failed"`) || !strings.Contains(text, `"request_context":"canceled"`) || !strings.Contains(text, `"stage":"claim"`) {
		t.Fatal("diagnostics missing, unsampled or cancellation suppressed them")
	}
	if strings.Contains(text, "PRIVATE") || strings.Contains(text, `"order_id"`) || n.calls.Load() != 0 {
		t.Fatal("failure leaked content or authorized a notification")
	}
	for _, err := range []error{errors.New("PRIVATE_ERROR"), StorageFailure("PRIVATE_REASON")} {
		if storageReason(err) != "unknown" {
			t.Fatal("untrusted diagnostic reason escaped allowlist")
		}
	}
	for _, failure := range []StorageFailure{StorageRead, StorageEpoch, StorageInvalid, StorageExpired, StorageWrite, StorageConflict, StorageCapacity} {
		if !errors.Is(failure, ErrStorage) || storageReason(failure) != string(failure) || strings.Contains(failure.Error(), string(failure)) {
			t.Fatal("storage error compatibility or privacy changed")
		}
	}
}

func TestFinalizationDiagnosticRetainsOriginalRequestCancellation(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &faultNotifier{fn: func() error { cancel(); return nil }}
	h := New("key", "secret", n, slog.New(slog.NewJSONHandler(&logs, nil)))
	h.options.Receipts = &failedReceipts{inner: NewMemoryReceipts(), failAt: 2}
	r := callbackRequest(h, "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, h.now().Unix()).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertCallbackResult(t, w, 503, "receipt_unavailable", "finalize")
	if n.calls.Load() != 1 || !strings.Contains(logs.String(), `"stage":"finalize"`) || !strings.Contains(logs.String(), `"request_context":"canceled"`) {
		t.Fatal("detached save context obscured original request cancellation")
	}
}
