package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

type deliveryFixture struct {
	mu                sync.Mutex
	order             dujiao.OrderDetail
	files             [][]byte
	texts             []string
	markups           []string
	names, recipients []string
	lookups           int
	failure           int
	drop              bool
	logs              auditBuffer
}

func newDeliveryFixture(t *testing.T) (*App, *deliveryFixture) {
	t.Helper()
	f := &deliveryFixture{order: dujiao.OrderDetail{OrderID: 7, OrderNo: "DJ7", Status: "completed", FulfillmentResult: "PRIVATE-CARD<&>\nLAST-CARD"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/channel/orders/7" {
			f.lookups++
			if r.URL.Query().Get("channel_user_id") != "42" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"status_code":404,"error_code":"order_not_found","msg":"PRIVATE-CARD-UPSTREAM-ERROR"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": f.order})
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method == "getMe" {
			fmt.Fprint(w, `{"ok":true,"result":{"id":999,"is_bot":true,"first_name":"Test"}}`)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Error(err)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if method == "sendDocument" {
			file, h, err := r.FormFile("document")
			if err != nil {
				t.Error(err)
				return
			}
			data, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				t.Error(err)
			}
			f.files = append(f.files, data)
			f.names = append(f.names, h.Filename)
			f.recipients = append(f.recipients, r.FormValue("chat_id"))
			if f.drop {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			if f.failure != 0 {
				w.WriteHeader(f.failure)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": f.failure, "description": "PRIVATE-SDK-ERROR", "parameters": map[string]int{"retry_after": 1}})
				return
			}
		} else if method == "sendMessage" || method == "editMessageText" {
			f.texts = append(f.texts, r.FormValue("text"))
			f.markups = append(f.markups, r.FormValue("reply_markup"))
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":91,"chat":{"id":42,"type":"private"}}}`)
	}))
	t.Cleanup(srv.Close)
	api, err := dujiao.NewClient(srv.URL, "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New("999:private-test-token", api, dujiao.BotConfig{}, "zh-CN", time.Second, srv.URL, slog.New(slog.NewJSONHandler(&f.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a, f
}

func download(a *App, uid, chat int64, data string) {
	q := &models.CallbackQuery{From: models.User{ID: uid}, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: chat}}}}
	a.handleDeliveryCallback(context.Background(), q, strings.Split(data, ":"))
}

func expireDeliveryCooldown(t *testing.T, a *App) delivery.Receipt {
	t.Helper()
	r, err := a.deliveries.Update(context.Background(), delivery.Key(42, 7), func(r *delivery.Receipt) error { r.NotBefore = time.Now().Add(-time.Second); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDeliveryTXTCompleteBulkAndAllChildren(t *testing.T) {
	var codes strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&codes, "CARD-%05d<&>\r\n", i)
	}
	codes.WriteString("末行-END  ")
	o := dujiao.OrderDetail{OrderNo: "DJ-测试", Status: "completed", FulfillmentResult: codes.String(), FulfillmentInstructions: strings.Repeat("说明", 2000) + "INSTRUCTION-END"}
	data, err := deliveryTXT(o)
	if err != nil || !strings.Contains(string(data), codes.String()) || !strings.Contains(string(data), "INSTRUCTION-END") || strings.Contains(string(data), "&lt;") {
		t.Fatal("bulk payload truncated/escaped/normalized")
	}
	o.FulfillmentResult = "same-card"
	o.Children = []dujiao.OrderChild{
		{OrderNo: "child-A", Fulfillment: &dujiao.OrderFulfillment{Payload: "same-card", Instructions: "CHILD-A-INSTRUCTIONS"}},
		{OrderNo: "child-B", Fulfillment: &dujiao.OrderFulfillment{Payload: "same-card", Instructions: "CHILD-B-INSTRUCTIONS"}},
		{OrderNo: "child-C", Fulfillment: &dujiao.OrderFulfillment{Payload: map[string]any{"code": "THIRD-END"}}},
	}
	data, err = deliveryTXT(o)
	if err != nil || strings.Count(string(data), "same-card") != 3 || !strings.Contains(string(data), "THIRD-END") || !strings.Contains(string(data), "CHILD-B-INSTRUCTIONS") {
		t.Fatal("root/children lost or codes deduplicated")
	}
	o.Children = o.Children[:1]
	data, _ = deliveryTXT(o)
	if strings.Count(string(data), "same-card") != 1 {
		t.Fatal("single-child mirror duplicated")
	}
	o.FulfillmentResult = strings.Repeat("x", maxDeliveryTXT)
	if data, err := deliveryTXT(o); err == nil || data != nil {
		t.Fatal("oversized TXT silently truncated")
	}
	for _, status := range []string{"pending_payment", "cancelled", "canceled", "refunded", ""} {
		o.Status = status
		if hasDelivery(o) {
			t.Fatal("ineligible order exposed payload", status)
		}
	}
}

func TestDeliveryTXTIsolationAndSummary(t *testing.T) {
	a, f := newDeliveryFixture(t)
	a.showOrder(context.Background(), 42, 10, 42, 7)
	f.mu.Lock()
	if len(f.texts) != 1 || strings.Contains(f.texts[0], "PRIVATE-CARD") || !strings.Contains(f.texts[0], "TXT") {
		t.Fatal("order view not a private compact summary")
	}
	f.mu.Unlock()
	download(a, 43, 43, "file:7")
	download(a, 42, -100, "file:7")
	a.showOrder(context.Background(), -100, 10, 42, 7)
	f.mu.Lock()
	if len(f.files) != 0 || f.lookups != 2 {
		t.Fatal("cross-user/group request reached file upload or group lookup")
	}
	f.mu.Unlock()
	for _, data := range []string{"file", "file:0", "file:-1", "file:999999999999999999", "file:7:extra", "fileretry:7"} {
		download(a, 42, 42, data)
	}
	download(a, 42, 42, "file:7")
	f.mu.Lock()
	if len(f.files) != 1 || f.recipients[0] != "42" || f.names[0] != "delivery-7.txt" || !strings.Contains(string(f.files[0]), "PRIVATE-CARD<&>\nLAST-CARD") {
		t.Fatal("authorized TXT incomplete or wrong recipient/name")
	}
	f.mu.Unlock()
	if strings.Contains(f.logs.text(), "PRIVATE-CARD") || strings.Contains(f.logs.text(), "private-test-token") {
		t.Fatal("delivery secret leaked to logs")
	}
}

func TestDeliveryTXTManualDeliveredStatus(t *testing.T) {
	a, f := newDeliveryFixture(t)
	f.order.Status = "delivered" // Official manual-fulfillment path, unlike auto completed.
	download(a, 42, 42, "file:7")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.files) != 1 || orderStatus("zh-CN", "delivered") != "已交付" {
		t.Fatal("official manual delivery became inaccessible")
	}
}

func TestDeliveryTXTRejectsMismatchedOrderAndOversizedFile(t *testing.T) {
	for _, mode := range []string{"wrong order", "oversize", "unpaid", "cancelled", "refunded"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newDeliveryFixture(t)
			switch mode {
			case "wrong order":
				f.order.OrderID = 8
			case "oversize":
				f.order.FulfillmentResult = strings.Repeat("x", maxDeliveryTXT)
			case "unpaid":
				f.order.Status = "pending_payment"
			default:
				f.order.Status = mode
			}
			download(a, 42, 42, "file:7")
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.files) != 0 {
				t.Fatal("ineligible export uploaded a file")
			}
		})
	}
}

func TestDeliveryTXTConcurrentClicksAndDeliberateResend(t *testing.T) {
	a, f := newDeliveryFixture(t)
	var wg sync.WaitGroup
	for range 25 {
		wg.Go(func() { download(a, 42, 42, "file:7") })
	}
	wg.Wait()
	f.mu.Lock()
	count := len(f.files)
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("concurrent duplicate uploads", count)
	}
	r := expireDeliveryCooldown(t, a)
	download(a, 42, 42, "file:7")
	download(a, 42, 42, "fileretry:7:invalid-token")
	f.mu.Lock()
	count = len(f.files)
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("plain repeat/stale token bypassed confirmation")
	}
	download(a, 42, 42, "fileretry:7:"+r.Token)
	expireDeliveryCooldown(t, a)
	download(a, 42, 42, "fileretry:7:"+r.Token)
	f.mu.Lock()
	count = len(f.files)
	f.mu.Unlock()
	if count != 2 {
		t.Fatal("resend token replayed or explicit resend blocked", count)
	}
}

func TestDeliveryTXTRealSDKFaults(t *testing.T) {
	for _, mode := range []string{"429", "500", "403", "accepted response lost"} {
		t.Run(mode, func(t *testing.T) {
			a, f := newDeliveryFixture(t)
			f.failure, _ = strconv.Atoi(mode)
			f.drop = mode == "accepted response lost"
			download(a, 42, 42, "file:7")
			r := expireDeliveryCooldown(t, a)
			if (r.Status == "retry") != (mode == "429") {
				t.Fatal("ambiguous send became automatically retryable")
			}
			download(a, 42, 42, "file:7")
			f.mu.Lock()
			count := len(f.files)
			f.mu.Unlock()
			want := 1
			if mode == "429" {
				want = 2
			}
			if count != want {
				t.Fatal("SDK/handler repeated uncertain upload", count)
			}
			if strings.Contains(f.logs.text(), "PRIVATE-SDK-ERROR") {
				t.Fatal("SDK body logged")
			}
		})
	}
}

type failingDeliveryStore struct {
	inner  delivery.Store
	calls  int
	failAt int
}

func (s *failingDeliveryStore) Update(ctx context.Context, key string, fn func(*delivery.Receipt) error) (delivery.Receipt, error) {
	s.calls++
	if s.calls == s.failAt {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	return s.inner.Update(ctx, key, fn)
}

func TestDeliveryTXTStorageFailureNeverRetriesUpload(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(strconv.Itoa(failAt), func(t *testing.T) {
			a, f := newDeliveryFixture(t)
			inner := a.deliveries
			a.deliveries = &failingDeliveryStore{inner: inner, failAt: failAt}
			download(a, 42, 42, "file:7")
			if failAt == 2 {
				a.deliveries = inner
				expireDeliveryCooldown(t, a)
				download(a, 42, 42, "file:7")
			}
			f.mu.Lock()
			count := len(f.files)
			f.mu.Unlock()
			if count != failAt-1 {
				t.Fatal("failed claim/completion caused upload or retry", count)
			}
		})
	}
}
