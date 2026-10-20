package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/checkout"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

type pendingFixture struct {
	mu                            sync.Mutex
	order                         dujiao.OrderDetail
	latest                        *dujiao.PaymentResult
	posts, cancels, photos, reads int
	texts                         []string
	markups                       []models.InlineKeyboardMarkup
	postFailure                   bool
	cancelFailure                 bool
	latestFailure                 bool
	postLost                      bool
	postInvisible                 bool
	balance                       string
	walletSuccess                 bool
	logs                          auditBuffer
	list                          dujiao.OrderList
	listCalls                     []string
	lookupCalls, lookupFailure    int
	lookupMismatch                bool
}

func newPendingFixture(t *testing.T) (*App, *pendingFixture) {
	t.Helper()
	f := &pendingFixture{order: dujiao.OrderDetail{OrderID: 7, OrderNo: "DJ7", Status: "pending_payment", TotalAmount: "10.00", Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}, balance: "100.00"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/bot") {
			method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if method == "getMe" {
				fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"test"}}`)
				return
			}
			_ = r.ParseMultipartForm(8 << 20)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			if method == "sendMessage" || method == "editMessageText" {
				f.texts = append(f.texts, r.FormValue("text"))
				var k models.InlineKeyboardMarkup
				_ = json.Unmarshal([]byte(r.FormValue("reply_markup")), &k)
				f.markups = append(f.markups, k)
			}
			if method == "sendPhoto" {
				f.photos++
			}
			if method == "answerCallbackQuery" || method == "deleteMessage" {
				fmt.Fprint(w, `{"ok":true,"result":true}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":91,"chat":{"id":42,"type":"private"}}}`)
			return
		}
		fail := func(code int, kind string) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": code, "error_code": kind, "msg": "PRIVATE-PAYMENT-ERROR"})
		}
		uid := r.URL.Query().Get("channel_user_id")
		var req struct {
			ChannelUserID string `json:"channel_user_id"`
			OrderID       uint   `json:"order_id"`
			ChannelID     uint   `json:"channel_id"`
			UseBalance    bool   `json:"use_balance"`
		}
		if r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&req)
			uid = req.ChannelUserID
		}
		if uid != "42" {
			fail(404, "order_not_found")
			return
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/channel/orders":
			f.listCalls = append(f.listCalls, r.URL.Query().Get("status")+":"+r.URL.Query().Get("page"))
			if r.URL.Query().Get("page_size") != "5" {
				t.Error("unexpected order page size")
			}
			data = f.list
		case "/api/v1/channel/orders/by-order-no/DJ7":
			f.lookupCalls++
			if f.lookupFailure != 0 {
				fail(f.lookupFailure, "order_not_found")
				return
			}
			order := f.order
			if f.lookupMismatch {
				order.OrderNo = "OTHER-PRIVATE-NUMBER"
			}
			data = order
		case "/api/v1/channel/orders/7":
			f.reads++
			data = f.order
		case "/api/v1/channel/payments/latest":
			if f.latestFailure {
				fail(503, "unavailable")
				return
			}
			if f.latest == nil {
				fail(404, "payment_not_found")
				return
			}
			data = f.latest
		case "/api/v1/channel/payment-channels":
			data = dujiao.PaymentChannelList{Items: []dujiao.PaymentChannel{{ID: 12, Name: "Synthetic"}}}
		case "/api/v1/channel/wallet":
			data = dujiao.Wallet{Balance: f.balance, Currency: "CNY"}
		case "/api/v1/channel/payments":
			f.posts++
			if req.UseBalance && f.walletSuccess {
				f.order.Status = "paid"
				f.balance = "90.00"
				data = dujiao.PaymentResult{OrderPaid: true}
				break
			}
			if f.postFailure {
				fail(400, "payment_create_failed")
				return
			}
			p := dujiao.PaymentResult{PaymentID: 8, OrderID: 7, Status: "pending", InteractionMode: "qr", Amount: "11.25", Currency: "CNY", PayURL: "https://payments.example.invalid/PRIVATE-URL", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)}
			if !f.postInvisible {
				f.latest = &p
			}
			data = p
			if f.postLost {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = conn.Close()
				}
				return
			}
		case "/api/v1/channel/orders/7/cancel":
			f.cancels++
			if f.cancelFailure {
				fail(503, "unavailable")
				return
			}
			f.order.Status = "canceled"
			data = dujiao.OrderCancelled{OrderID: 7, Status: "canceled"}
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/channel/orders/by-order-no/") {
				fail(404, "order_not_found")
				return
			}
			t.Errorf("unexpected API: %s", r.URL.Path)
			fail(500, "unexpected")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": data})
	}))
	t.Cleanup(srv.Close)
	api, e := dujiao.NewClient(srv.URL, "key", "secret", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	a, e := New("999:test-private-token", api, dujiao.BotConfig{}, "zh-CN", time.Second, srv.URL, slog.New(slog.NewJSONHandler(&f.logs, nil)))
	if e != nil {
		t.Fatal(e)
	}
	run, cancel := context.WithCancel(context.Background())
	a.runCtx = run
	t.Cleanup(cancel)
	return a, f
}
func pendingClick(a *App, uid, chat int64, mid int, data string) {
	a.handleCallback(context.Background(), &models.CallbackQuery{ID: "pending-test", From: models.User{ID: uid}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: mid, Chat: models.Chat{ID: chat}}}})
}
func pendingAction(t *testing.T, f *pendingFixture, prefix string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.markups[len(f.markups)-1].InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, prefix) {
				return b.CallbackData
			}
		}
	}
	t.Fatal("button absent", prefix)
	return ""
}
func pendingPayAction(t *testing.T, f *pendingFixture) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.markups[len(f.markups)-1].InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "pay:") && strings.HasSuffix(b.CallbackData, ":0") {
				if len(b.CallbackData) > 64 {
					t.Fatal("callback too long")
				}
				return b.CallbackData
			}
		}
	}
	t.Fatal("online payment absent")
	return ""
}
func TestPendingContinueReusesPaymentAndRestartEntry(t *testing.T) {
	a, f := newPendingFixture(t)
	pendingClick(a, 42, 42, 10, "resume:7")
	pay := pendingPayAction(t, f)
	pendingClick(a, 42, 42, 10, pay)
	pendingClick(a, 42, 42, 10, pay)
	pendingClick(a, 42, 42, 11, "resume:7")
	f.mu.Lock()
	if f.posts != 1 || f.photos != 1 {
		t.Fatal("resume created payment/QR again", f.posts, f.photos)
	}
	f.mu.Unlock()
	a.stopPaymentPoll(context.Background(), 42, 7) // Same entry after polling state is lost.
	pendingClick(a, 42, 42, 12, "resume:7")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatal("restart entry created new payment")
	}
	if strings.Contains(f.logs.text(), "PRIVATE-") {
		t.Fatal("payment data logged")
	}
}
func TestPendingConcurrentPaymentAndUnknownResults(t *testing.T) {
	for _, mode := range []string{"concurrent", "lost-visible", "lost-invisible", "gateway-failed"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newPendingFixture(t)
			pendingClick(a, 42, 42, 10, "resume:7")
			pay := pendingPayAction(t, f)
			f.postLost = strings.HasPrefix(mode, "lost-")
			f.postInvisible = mode == "lost-invisible"
			f.postFailure = mode == "gateway-failed"
			var wg sync.WaitGroup
			for range 6 {
				wg.Go(func() { pendingClick(a, 42, 42, 10, pay) })
			}
			wg.Wait()
			pendingClick(a, 42, 42, 11, "resume:7")
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 1 {
				t.Fatal("duplicate/unknown payment replayed", f.posts)
			}
		})
	}
}
func TestPendingDeadlinesAndLatestFailuresDoNotWrite(t *testing.T) {
	for _, mode := range []string{"expired", "paid", "foreign", "group", "lookup-failed", "wrong-message", "wallet-insufficient", "expired-selection", "wrong-channel"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newPendingFixture(t)
			pendingClick(a, 42, 42, 10, "resume:7")
			pay := pendingPayAction(t, f)
			uid, chat, mid := int64(42), int64(42), 10
			switch mode {
			case "expired":
				f.order.ExpiresAt = time.Now().Add(-time.Minute).Format(time.RFC3339)
			case "paid":
				f.order.Status = "paid"
			case "foreign":
				uid, chat = 43, 43
			case "group":
				chat = -100
			case "lookup-failed":
				f.latestFailure = true
			case "wrong-message":
				mid = 11
			case "wallet-insufficient":
				f.balance = "0.00"
				pay = pendingAction(t, f, "pay:")
			case "expired-selection":
				_, e := a.checkout.Update(context.Background(), checkout.Key(42, 7), func(s *checkout.State) error { s.SelectionUntil = time.Now().Add(-time.Minute); return nil })
				if e != nil {
					t.Fatal(e)
				}
			case "wrong-channel":
				pay = strings.Replace(pay, ":c:0", ":d:0", 1)
			}
			pendingClick(a, uid, chat, mid, pay)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts != 0 || f.cancels != 0 {
				t.Fatal("invalid payment wrote upstream")
			}
			if mode == "expired" && !strings.Contains(f.texts[len(f.texts)-1], "时限已到") {
				t.Fatal("expired order missing explicit notice")
			}
		})
	}
}
func TestPendingStaleAndLegacyPayCannotRenewExpiredPayment(t *testing.T) {
	a, f := newPendingFixture(t)
	pendingClick(a, 42, 42, 10, "resume:7")
	pay := pendingPayAction(t, f)
	pendingClick(a, 42, 42, 10, pay)
	f.mu.Lock()
	f.latest = nil
	f.mu.Unlock()
	pendingClick(a, 42, 42, 10, pay)
	pendingClick(a, 42, 42, 10, "pay:7:12:0") // Old version button is read-only navigation.
	newPay := pendingPayAction(t, f)
	if pay == newPay {
		t.Fatal("renewal reused consumed nonce")
	}
	pendingClick(a, 42, 42, 10, newPay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 2 {
		t.Fatal("explicit fresh selection failed")
	}
}
func TestPendingCancelReviewBackAndRepeat(t *testing.T) {
	a, f := newPendingFixture(t)
	pendingClick(a, 42, 42, 10, "cancelask:7")
	old := pendingAction(t, f, "cancelgo:")
	back := pendingAction(t, f, "cancelback:")
	pendingClick(a, 42, 42, 10, back)
	pendingClick(a, 42, 42, 10, old)
	pendingClick(a, 42, 42, 10, "cancelgo:7:")
	f.mu.Lock()
	if f.cancels != 0 {
		t.Fatal("back did not invalidate confirmation")
	}
	f.mu.Unlock()
	pendingClick(a, 42, 42, 10, "cancelorder:7") // Legacy confirmation opens a review.
	confirm := pendingAction(t, f, "cancelgo:")
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { pendingClick(a, 42, 42, 10, confirm) })
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 || f.order.Status != "canceled" {
		t.Fatal("duplicate cancel", f.cancels)
	}
}
func TestPendingCancelRacesAndUnknownDoNotReplay(t *testing.T) {
	for _, mode := range []string{"paid", "expired-review", "wrong-message", "foreign", "group", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newPendingFixture(t)
			pendingClick(a, 42, 42, 10, "cancelask:7")
			confirm := pendingAction(t, f, "cancelgo:")
			uid, chat, mid := int64(42), int64(42), 10
			switch mode {
			case "paid":
				f.order.Status = "paid"
			case "expired-review":
				_, e := a.checkout.Update(context.Background(), checkout.Key(42, 7), func(s *checkout.State) error { s.CancelUntil = time.Now().Add(-time.Minute); return nil })
				if e != nil {
					t.Fatal(e)
				}
			case "wrong-message":
				mid = 11
			case "foreign":
				uid, chat = 43, 43
			case "group":
				chat = -100
			case "unknown":
				f.cancelFailure = true
			}
			pendingClick(a, uid, chat, mid, confirm)
			pendingClick(a, uid, chat, mid, confirm)
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 0
			if mode == "unknown" {
				want = 1
			}
			if f.cancels != want {
				t.Fatal("unsafe cancellation", f.cancels)
			}
		})
	}
}
func TestPaymentPollDeadlineNeverExtendsPastExpiry(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, delta := range []time.Duration{-time.Minute, 0, time.Minute, time.Hour} {
		got := paymentPollDeadline(now, now.Add(delta).Format(time.RFC3339))
		want := now.Add(min(delta, 15*time.Minute))
		if !got.Equal(want) {
			t.Fatal("incorrect deadline", delta, got, want)
		}
	}
	if paymentPollDeadline(now, "invalid") != now.Add(15*time.Minute) {
		t.Fatal("missing expiry unbounded")
	}
}

type checkoutFailureStore struct {
	inner         checkout.Store
	calls, failAt int
	after         bool
}

func (s *checkoutFailureStore) Update(ctx context.Context, key string, fn func(*checkout.State) error) (checkout.State, error) {
	s.calls++
	if s.calls == s.failAt {
		if s.after {
			_, _ = s.inner.Update(ctx, key, fn)
		}
		return checkout.State{}, checkout.ErrStorage
	}
	return s.inner.Update(ctx, key, fn)
}
func TestPendingStorageAcknowledgementLossDoesNotReplayMoney(t *testing.T) {
	for _, mode := range []string{"payment-claim", "payment-finish", "cancel-claim", "cancel-finish"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newPendingFixture(t)
			cancelAction := strings.HasPrefix(mode, "cancel")
			action := ""
			if cancelAction {
				pendingClick(a, 42, 42, 10, "cancelask:7")
				action = pendingAction(t, f, "cancelgo:")
			} else {
				pendingClick(a, 42, 42, 10, "resume:7")
				action = pendingPayAction(t, f)
			}
			inner := a.checkout
			failAt := 2
			if mode == "payment-finish" {
				failAt = 3
			}
			if mode == "cancel-claim" {
				failAt = 1
			}
			a.checkout = &checkoutFailureStore{inner: inner, failAt: failAt, after: true}
			pendingClick(a, 42, 42, 10, action)
			a.checkout = inner
			pendingClick(a, 42, 42, 10, action)
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 0
			if strings.HasSuffix(mode, "finish") {
				want = 1
			}
			if cancelAction && f.cancels != want || !cancelAction && f.posts != want {
				t.Fatal("storage failure replayed financial action", f.posts, f.cancels)
			}
		})
	}
}
func TestPendingPollTimeoutIsNotPaymentFailure(t *testing.T) {
	a, f := newPendingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	p := &activePaymentPoll{cancel: func() {}}
	a.polls[7] = p
	a.pollPayment(ctx, p, 42, 42, 7, 10, time.Now().Add(-time.Minute).Format(time.RFC3339))
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) == 0 || !strings.Contains(f.texts[len(f.texts)-1], "不代表付款失败") || f.posts != 0 || f.cancels != 0 || f.order.Status != "pending_payment" {
		t.Fatal("poll timeout changed/claimed financial status")
	}
}

func TestPendingWalletPaymentAndOldConfirmationCannotChargeTwice(t *testing.T) {
	a, f := newPendingFixture(t)
	f.walletSuccess = true
	pendingClick(a, 42, 42, 10, "resume:7")
	pay := pendingAction(t, f, "pay:")
	pendingClick(a, 42, 42, 10, pay)
	pendingClick(a, 42, 42, 10, pay)
	pendingClick(a, 42, 42, 10, "cancelask:7")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || f.cancels != 0 || f.photos != 0 || f.balance != "90.00" || f.order.Status != "paid" {
		t.Fatal("wallet payment repeated or cancellation regressed paid order")
	}
}
