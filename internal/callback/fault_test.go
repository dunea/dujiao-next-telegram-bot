package callback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type faultNotifier struct {
	calls atomic.Int64
	fn    func() error
}

func (n *faultNotifier) Notify(context.Context, int64, string, Event) error {
	n.calls.Add(1)
	if n.fn != nil {
		return n.fn()
	}
	return nil
}
func fixtureHandler(n Notifier) *Handler {
	return New("key", "secret", n, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}
func callbackRequest(h *Handler, path, body string, stamp int64) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Dujiao-Next-Channel-Key", "key")
	r.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(stamp, 10))
	r.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign("secret", "POST", path, stamp, []byte(body)))
	return r
}
func invoke(h *Handler, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, callbackRequest(h, path, body, h.now().Unix()))
	return w
}

func TestCallbackAuthAndPayloadFaults(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		mutate           func(*http.Request)
		want             int
	}{
		{"malformed JSON", "/internal/order-paid", `{`, nil, 400},
		{"missing resource", "/internal/order-paid", `{"telegram_user_id":"42"}`, nil, 400},
		{"invalid user", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"-1"}`, nil, 400},
		{"missing recharge", "/internal/wallet-recharge-succeeded", `{"telegram_user_id":"42","amount":"10.00","currency":"CNY"}`, nil, 400},
		{"invalid amount", "/internal/wallet-recharge-succeeded", `{"telegram_user_id":"42","recharge_no":"RC1","amount":"NaN","currency":"CNY"}`, nil, 400},
		{"zero amount", "/internal/wallet-recharge-succeeded", `{"telegram_user_id":"42","recharge_no":"RC1","amount":"0","currency":"CNY"}`, nil, 400},
		{"invalid currency", "/internal/wallet-recharge-succeeded", `{"telegram_user_id":"42","recharge_no":"RC1","amount":"10","currency":"<CNY>"}`, nil, 400},
		{"wrong key", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) { r.Header.Set("Dujiao-Next-Channel-Key", "wrong") }, 401},
		{"bad signature", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) { r.Header.Set("Dujiao-Next-Channel-Signature", "wrong") }, 401},
		{"different signed path", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) { r.URL.Path = "/internal/order-fulfilled" }, 401},
		{"body tampered", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"order_id":8,"telegram_user_id":"42"}`))
		}, 401},
		{"expired", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) { r.Header.Set("Dujiao-Next-Channel-Timestamp", "1") }, 401},
		{"extreme timestamp", "/internal/order-paid", `{"order_id":7,"telegram_user_id":"42"}`, func(r *http.Request) { r.Header.Set("Dujiao-Next-Channel-Timestamp", "9223372036854775807") }, 401},
		{"route", "/internal/unknown", `{}`, nil, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &faultNotifier{}
			h := fixtureHandler(n)
			r := callbackRequest(h, tc.path, tc.body, h.now().Unix())
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || n.calls.Load() != 0 {
				t.Fatalf("got %d, calls=%d", w.Code, n.calls.Load())
			}
		})
	}
}

func TestCallbacksDuplicateConcurrentAndOutOfOrder(t *testing.T) {
	for _, kind := range []string{"order-paid", "order-fulfilled", "wallet-recharge-succeeded"} {
		t.Run(kind, func(t *testing.T) {
			n := &faultNotifier{}
			h := fixtureHandler(n)
			body := `{"order_id":7,"telegram_user_id":"42"}`
			if kind == "wallet-recharge-succeeded" {
				body = `{"telegram_user_id":"42","recharge_no":"RC1","amount":"10.00","currency":"CNY"}`
			}
			var wg sync.WaitGroup
			for range 30 {
				wg.Go(func() {
					w := invoke(h, "/internal/"+kind, body)
					if w.Code != 200 && w.Code != 503 {
						t.Errorf("unexpected status %d", w.Code)
					}
				})
			}
			wg.Wait()
			if w := invoke(h, "/internal/"+kind, body); w.Code != 200 || n.calls.Load() != 1 {
				t.Fatal("completed replay sent again")
			}
		})
	}
	n := &faultNotifier{}
	h := fixtureHandler(n)
	body := `{"order_id":7,"telegram_user_id":"42"}`
	if invoke(h, "/internal/order-fulfilled", body).Code != 200 || invoke(h, "/internal/order-paid", body).Code != 200 || n.calls.Load() != 1 {
		t.Fatal("late paid event regressed fulfilled notification")
	}
	if invoke(h, "/internal/order-paid", `{"order_id":7,"telegram_user_id":"43"}`).Code != 200 || n.calls.Load() != 2 {
		t.Fatal("dedupe leaked across users")
	}
}

func TestCallbackTemporaryAndPermanentDeliveryFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    int
		calls   int64
	}{
		{"Telegram 429", &NotifyError{RetryAfter: 3 * time.Second}, 503, 2},
		{"Telegram 5xx", errors.New("private gateway body"), 503, 2},
		{"response lost", context.DeadlineExceeded, 503, 2},
		{"blocked chat", &NotifyError{Permanent: true}, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fail := true
			n := &faultNotifier{fn: func() error {
				if fail {
					return tc.failure
				}
				return nil
			}}
			h := fixtureHandler(n)
			now := time.Now()
			h.now = func() time.Time { return now }
			body := `{"telegram_user_id":"42","recharge_no":"RC1","amount":"10.00","currency":"CNY"}`
			w := invoke(h, "/internal/wallet-recharge-succeeded", body)
			if w.Code != tc.code || strings.Contains(w.Body.String(), "private") {
				t.Fatal("unsafe/unexpected failure response", w.Code)
			}
			invoke(h, "/internal/wallet-recharge-succeeded", body)
			if n.calls.Load() != 1 {
				t.Fatal("retry ignored cooldown or permanent denial")
			}
			now = now.Add(4 * time.Second)
			fail = false
			if invoke(h, "/internal/wallet-recharge-succeeded", body).Code != 200 || n.calls.Load() != tc.calls {
				t.Fatal("redelivery did not recover correctly")
			}
		})
	}
}

type failedReceipts struct {
	inner  Receipts
	calls  int
	failAt int
}

func (f *failedReceipts) Update(c context.Context, k string, fn func(*Receipt) error) (Receipt, error) {
	f.calls++
	if f.calls == f.failAt {
		return Receipt{}, ErrStorage
	}
	return f.inner.Update(c, k, fn)
}
func TestCallbackReceiptFailuresNeverAuthorizeOrForgetSend(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(strconv.Itoa(at), func(t *testing.T) {
			n := &faultNotifier{}
			h := fixtureHandler(n)
			h.options.Receipts = &failedReceipts{inner: NewMemoryReceipts(), failAt: at}
			body := `{"order_id":7,"telegram_user_id":"42"}`
			if invoke(h, "/internal/order-paid", body).Code != 503 || n.calls.Load() != int64(at-1) {
				t.Fatal("receipt failure was not fail-closed")
			}
			if at == 2 {
				invoke(h, "/internal/order-paid", body)
				if n.calls.Load() != 1 {
					t.Fatal("lost completion bypassed posting lease")
				}
			}
		})
	}
}

func TestCallbackChangedRechargeAndCanonicalMoney(t *testing.T) {
	n := &faultNotifier{}
	h := fixtureHandler(n)
	event := Event{TelegramUserID: "42", RechargeNo: "RC1", Amount: "10.00", Currency: "CNY"}
	post := func() int {
		body, _ := json.Marshal(event)
		return invoke(h, "/internal/wallet-recharge-succeeded", string(body)).Code
	}
	if post() != 200 {
		t.Fatal("first recharge failed")
	}
	event.Amount = "10"
	if post() != 200 || n.calls.Load() != 1 {
		t.Fatal("same canonical amount was sent twice")
	}
	event.Amount = "11.00"
	if post() != 409 || n.calls.Load() != 1 {
		t.Fatal("changed amount accepted as a new event")
	}
}

func TestMemoryReceiptCapacityExpiryAndClone(t *testing.T) {
	m := NewMemoryReceipts()
	now := time.Now()
	m.now = func() time.Time { return now }
	ctx := context.Background()
	for i := range ReceiptCapacity {
		key := ReceiptKey(int64(i+1), Event{OrderID: 7})
		m.items[key] = Receipt{Version: 1, ExpiresAt: now.Add(ReceiptLifetime), Events: map[string]Delivery{}}
	}
	key := ReceiptKey(999999, Event{OrderID: 8})
	if _, err := m.Update(ctx, key, func(*Receipt) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatal("capacity evicted live guards")
	}
	now = now.Add(ReceiptLifetime + time.Second)
	r, err := m.Update(ctx, key, func(s *Receipt) error {
		s.Events["order-paid"] = Delivery{Fingerprint: key, Token: strings.Repeat("a", 26), Status: "sent"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	delete(r.Events, "order-paid")
	r, err = m.Update(ctx, key, func(*Receipt) error { return nil })
	if err != nil || len(r.Events) != 1 {
		t.Fatal("returned receipt aliases store")
	}
}

func TestLostCompletionCanRetryAfterLeaseWithoutExactlyOnceClaim(t *testing.T) {
	n := &faultNotifier{}
	h := fixtureHandler(n)
	h.options.Receipts = &failedReceipts{inner: NewMemoryReceipts(), failAt: 2}
	now := time.Now()
	h.now = func() time.Time { return now }
	body := `{"order_id":7,"telegram_user_id":"42"}`
	if invoke(h, "/internal/order-paid", body).Code != 503 {
		t.Fatal("lost completion acknowledged")
	}
	now = now.Add(DeliveryLease + time.Second)
	if invoke(h, "/internal/order-paid", body).Code != 200 || n.calls.Load() != 2 {
		t.Fatal("expired delivery lease did not permit recovery")
	}
}
