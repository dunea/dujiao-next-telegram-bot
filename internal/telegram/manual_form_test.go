package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

type formFixture struct {
	mu                   sync.Mutex
	product              dujiao.ProductDetail
	categories           []dujiao.Category
	products             dujiao.ProductList
	catalogQueries       []string
	previews             []dujiao.OrderRequest
	orders               []dujiao.OrderRequest
	texts                []string
	methods              []string
	chatIDs              []string
	adminSent            chan string
	markups              []string
	walletCurrency       string
	previewFails         bool
	couponExpired        bool
	rejectCouponOnCreate bool
	couponTotal          string
	giftCodes            []string
	giftFailureCode      string
	rechargeRequests     []rechargeTestRequest
	rechargeChannels     []dujiao.PaymentChannel
	rechargeChannelsFail bool
	rechargeFailure      bool
	rechargePayment      *dujiao.WalletRechargePayment
	affiliate            affiliateTestState
	nextID               atomic.Int64
}

func newFormFixture(t *testing.T, fields []dujiao.ManualFormField) (*App, *formFixture) {
	t.Helper()
	fixture := &formFixture{couponTotal: "9.00", walletCurrency: "CNY", product: dujiao.ProductDetail{
		Product:             dujiao.Product{ID: 3, Title: "Manual product", StockStatus: "in_stock", Currency: "CNY"},
		MinPurchaseQuantity: 1, MaxPurchaseQuantity: 5,
		ManualFormSchema: dujiao.ManualFormSchema{Fields: fields},
		SKUs:             []dujiao.SKU{{ID: 31, StockStatus: "in_stock", Price: "10.00"}},
	}}
	fixture.nextID.Store(100)
	fixture.rechargeChannels = []dujiao.PaymentChannel{
		{ID: 1, Name: "免手续费", FeeRate: "0.00", FixedFee: "0.00"},
		{ID: 2, Name: "演示手续费", FeeRate: "2.50", FixedFee: "1.00"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/bot") {
			method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			var result any = true
			switch method {
			case "getMe":
				result = map[string]any{"id": 999, "is_bot": true, "first_name": "Test", "username": "test_bot"}
			case "sendMessage", "editMessageText":
				_ = r.ParseMultipartForm(1 << 20)
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
				text := r.FormValue("text")
				fixture.mu.Lock()
				fixture.texts = append(fixture.texts, text)
				fixture.methods = append(fixture.methods, method)
				fixture.chatIDs = append(fixture.chatIDs, r.FormValue("chat_id"))
				if fixture.adminSent != nil && r.FormValue("chat_id") == "900001" {
					select {
					case fixture.adminSent <- text:
					default:
					}
				}
				fixture.markups = append(fixture.markups, r.FormValue("reply_markup"))
				fixture.mu.Unlock()
				result = map[string]any{"message_id": fixture.nextID.Add(1), "date": 1, "chat": map[string]any{"id": 42, "type": "private"}, "text": text}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/channel/affiliate/") || strings.HasPrefix(r.URL.Path, "/api/v1/channel/identities/") {
			fixture.affiliate.serveHTTP(t, w, r)
			return
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/channel/catalog/categories", "/api/v1/channel/catalog/products":
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.catalogQueries = append(fixture.catalogQueries, r.URL.RequestURI())
			if r.URL.Path == "/api/v1/channel/catalog/categories" {
				data = dujiao.CategoryList{Items: fixture.categories}
			} else {
				data = fixture.products
			}
		case "/api/v1/channel/wallet/recharge":
			var request rechargeTestRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			fixture.mu.Lock()
			fixture.rechargeRequests = append(fixture.rechargeRequests, request)
			failure := fixture.rechargeFailure
			payment := dujiao.WalletRechargePayment{Amount: request.Amount, FeeAmount: "0.00", Currency: "CNY", Status: "success"}
			if fixture.rechargePayment != nil {
				payment = *fixture.rechargePayment
			}
			fixture.mu.Unlock()
			if failure {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 503, "error_code": "unavailable"})
				return
			}
			data = dujiao.WalletRechargeResult{RechargeNo: "RC42", Payment: payment}
		case "/api/v1/channel/wallet/gift-card/redeem":
			var request struct {
				ChannelUserID string `json:"channel_user_id"`
				Code          string `json:"code"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.ChannelUserID != "42" {
				t.Errorf("gift card user = %q", request.ChannelUserID)
			}
			fixture.mu.Lock()
			fixture.giftCodes = append(fixture.giftCodes, request.Code)
			failure := fixture.giftFailureCode
			fixture.mu.Unlock()
			if failure != "" {
				status := http.StatusBadRequest
				if failure == "gift_card_not_found" {
					status = http.StatusNotFound
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": status, "error_code": failure, "msg": "untrusted server text: " + request.Code})
				return
			}
			data = dujiao.GiftCardRedemption{GiftCard: dujiao.GiftCard{Amount: "50.00", Currency: "CNY", Status: "redeemed"}, Wallet: dujiao.Wallet{Balance: "150.00"}, WalletDelta: "50.00", Transaction: dujiao.WalletTransaction{Type: "gift_card_redeem", Direction: "in", Amount: "50.00", BalanceAfter: "150.00"}}
		case "/api/v1/channel/wallet":
			fixture.mu.Lock()
			data = dujiao.Wallet{Balance: "150.00", Currency: fixture.walletCurrency}
			fixture.mu.Unlock()
		case "/api/v1/channel/wallet/transactions":
			data = dujiao.WalletTransactionList{Items: []dujiao.WalletTransaction{{Type: "gift_card_redeem", Direction: "in", Amount: "50.00", BalanceAfter: "150.00"}}, Page: 1, PageSize: 5, Total: 1, TotalPages: 1}
		case "/api/v1/channel/catalog/products/3":
			fixture.mu.Lock()
			data = fixture.product
			// Encode under the lock so a concurrent schema update is safe.
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": data})
			fixture.mu.Unlock()
			return
		case "/api/v1/channel/orders/preview", "/api/v1/channel/orders":
			var request dujiao.OrderRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			fixture.mu.Lock()
			couponInvalid := request.CouponCode != "" && (request.CouponCode != "SAVE10" || fixture.couponExpired)
			if !strings.HasSuffix(r.URL.Path, "/preview") && request.CouponCode != "" && fixture.rejectCouponOnCreate {
				couponInvalid = true
				fixture.couponExpired = true
			}
			if couponInvalid {
				fixture.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": "coupon_invalid", "msg": "coupon expired"})
				return
			}
			total, discount := "10.00", "0.00"
			if request.CouponCode != "" {
				total = fixture.couponTotal
				discount = "1.00"
			}
			if strings.HasSuffix(r.URL.Path, "/preview") {
				fixture.previews = append(fixture.previews, request)
				data = dujiao.OrderPreview{Valid: !fixture.previewFails, OriginalAmount: "10.00", CouponDiscount: discount, TotalAmount: total, Currency: "CNY", Items: []dujiao.OrderPreviewItem{{ProductTitle: "Manual product", Quantity: 1, Subtotal: "10.00"}}}
			} else {
				fixture.orders = append(fixture.orders, request)
				data = dujiao.OrderCreated{OrderID: 7, OrderNo: "DJ7", TotalAmount: total, Currency: "CNY"}
			}
			fixture.mu.Unlock()
		case "/api/v1/channel/payment-channels":
			data = dujiao.PaymentChannelList{}
			if r.URL.Query().Get("context") == "recharge" {
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if fixture.rechargeChannelsFail {
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 503, "error_code": "unavailable"})
					return
				}
				data = dujiao.PaymentChannelList{Items: fixture.rechargeChannels}
			}
		default:
			t.Errorf("unexpected API request: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": data})
	}))
	t.Cleanup(server.Close)
	api, err := dujiao.NewClient(server.URL, "test-key", "test-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New("999:test", api, dujiao.BotConfig{}, "zh-CN", time.Second, server.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return app, fixture
}

func formCallback(app *App, userID int64, messageID int, data string) {
	app.handleCallback(context.Background(), &models.CallbackQuery{ID: "test", From: models.User{ID: userID}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: messageID, Chat: models.Chat{ID: 42}}}})
}

func formReply(app *App, text string) {
	draft := app.sessions.Get(42).Purchase
	app.handleUpdate(context.Background(), nil, &models.Update{Message: &models.Message{
		ID: 500, From: &models.User{ID: 42}, Chat: models.Chat{ID: 42}, Text: text, ReplyToMessage: &models.Message{ID: draft.PromptID},
	}})
}

func formOption(app *App, action string) {
	draft := app.sessions.Get(42).Purchase
	formCallback(app, 42, draft.PromptID, fmt.Sprintf("form:%s:%d:%s", draft.ID, draft.Revision, action))
}

func TestManualFormPurchaseCarriesTypedValuesAndClaimsConfirmationOnce(t *testing.T) {
	minimum, maximum := 1.0, 5.0
	app, fixture := newFormFixture(t, []dujiao.ManualFormField{
		{Key: "hostname", Type: "text", Required: true, Regex: `^[a-z]+\.example\.com$`},
		{Key: "email", Type: "email", Required: true},
		{Key: "instances", Type: "number", Required: true, Min: &minimum, Max: &maximum},
		{Key: "region", Type: "select", Required: true, Options: []string{"HK", "SG"}},
		{Key: "addons", Type: "checkbox", Required: true, Options: []string{"monitor", "backup"}},
		{Key: "note", Type: "textarea"},
	})
	formCallback(app, 42, 10, "buy:3:31:1")
	first := app.sessions.Get(42).Purchase
	if first == nil || first.PromptID == 0 {
		t.Fatal("form did not start")
	}
	formReply(app, "demo.example.com")
	formReply(app, "invalid-email")
	if got := app.sessions.Get(42).Purchase.Step; got != 1 {
		t.Fatalf("invalid email advanced step: %d", got)
	}
	// A reply to an earlier field must not be accepted for the current field.
	app.handleUpdate(context.Background(), nil, &models.Update{Message: &models.Message{From: &models.User{ID: 42}, Chat: models.Chat{ID: 42}, Text: "old@example.com", ReplyToMessage: &models.Message{ID: first.PromptID}}})
	if got := app.sessions.Get(42).Purchase.Step; got != 1 {
		t.Fatal("stale text reply advanced the form")
	}
	formReply(app, "demo@example.com")
	formReply(app, "2.5")
	formOption(app, "option:1")
	formOption(app, "done") // Required checkbox cannot be empty.
	if got := app.sessions.Get(42).Purchase.Step; got != 4 {
		t.Fatal("required checkbox accepted no selection")
	}
	formOption(app, "option:1")
	formOption(app, "option:0")
	formOption(app, "done")
	formReply(app, "/skip")
	draft := app.sessions.Get(42).Purchase
	if !draft.Ready || draft.PreviewID == 0 {
		t.Fatalf("preview not ready: %#v", draft)
	}
	confirmation := fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision)
	formCallback(app, 99, draft.PreviewID, confirmation)
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() { formCallback(app, 42, draft.PreviewID, confirmation) })
	}
	wait.Wait()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.previews) != 1 || len(fixture.orders) != 1 {
		t.Fatalf("requests: %d previews, %d orders", len(fixture.previews), len(fixture.orders))
	}
	want := map[string]any{"hostname": "demo.example.com", "email": "demo@example.com", "instances": 2.5, "region": "SG", "addons": []any{"backup", "monitor"}}
	for _, request := range []dujiao.OrderRequest{fixture.previews[0], fixture.orders[0]} {
		if !reflect.DeepEqual(request.ManualFormData["3:31"], want) {
			t.Fatalf("form data = %#v", request.ManualFormData)
		}
	}
	if app.sessions.Get(42).Purchase != nil {
		t.Fatal("submitted draft retained")
	}
}

func TestManualFormEditInvalidatesOldConfirmationAndRechecksSchema(t *testing.T) {
	app, fixture := newFormFixture(t, []dujiao.ManualFormField{{Key: "name", Type: "text", Required: true}})
	formCallback(app, 42, 10, "confirm:3:31:1") // Forged legacy confirmation must collect the form.
	formReply(app, "first")
	draft := app.sessions.Get(42).Purchase
	oldConfirm := fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision)
	formCallback(app, 42, draft.PreviewID, strings.Replace(oldConfirm, "formconfirm:", "formedit:", 1))
	formCallback(app, 42, draft.PreviewID, oldConfirm)
	formReply(app, "second")
	draft = app.sessions.Get(42).Purchase
	fixture.mu.Lock()
	fixture.product.ManualFormSchema.Fields = append(fixture.product.ManualFormSchema.Fields, dujiao.ManualFormField{Key: "new", Type: "email", Required: true})
	fixture.mu.Unlock()
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 0 {
		t.Fatal("schema change or stale confirmation created an order")
	}
}

func TestManualFormPreviousFieldAndFailedPreviewRecovery(t *testing.T) {
	app, fixture := newFormFixture(t, []dujiao.ManualFormField{{Key: "first", Type: "text", Required: true}, {Key: "second", Type: "text"}})
	formCallback(app, 42, 10, "buy:3:31:1")
	formReply(app, "original")
	formReply(app, "/back")
	if app.sessions.Get(42).Purchase.Step != 0 {
		t.Fatal("back did not return to the first field")
	}
	formReply(app, "updated")
	fixture.mu.Lock()
	fixture.previewFails = true
	fixture.mu.Unlock()
	formReply(app, "/skip")
	draft := app.sessions.Get(42).Purchase
	if draft.Ready || draft.PreviewID == 0 {
		t.Fatal("failed preview was not retained for editing")
	}
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formedit:%s:%d", draft.ID, draft.Revision))
	fixture.mu.Lock()
	fixture.previewFails = false
	fixture.mu.Unlock()
	formReply(app, "/path/to/config")
	formReply(app, "/skip")
	draft = app.sessions.Get(42).Purchase
	if !draft.Ready {
		t.Fatal("preview could not recover")
	}
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 1 {
		t.Fatalf("recovered order count = %d", len(fixture.orders))
	}
	if values := fixture.orders[0].ManualFormData["3:31"].(map[string]any); values["first"] != "/path/to/config" {
		t.Fatalf("slash-prefixed text was treated as a command: %#v", values)
	}
}

func TestManualFormCancellationNavigationAndExpiry(t *testing.T) {
	for _, mode := range []string{"cancel", "navigation", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			app, fixture := newFormFixture(t, []dujiao.ManualFormField{{Key: "name", Type: "text", Required: true}})
			formCallback(app, 42, 10, "buy:3:31:1")
			formReply(app, "demo")
			draft := app.sessions.Get(42).Purchase
			switch mode {
			case "cancel":
				app.handleUpdate(context.Background(), nil, &models.Update{Message: &models.Message{From: &models.User{ID: 42}, Chat: models.Chat{ID: 42}, Text: "/cancel"}})
			case "navigation":
				formCallback(app, 42, draft.PreviewID, "home")
			case "expiry":
				app.sessions.Update(42, func(state *session.State) { state.Purchase.ExpiresAt = time.Now().Add(-time.Second) })
			}
			formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.orders) != 0 || app.sessions.Get(42).Purchase != nil {
				t.Fatal("inactive form could be submitted")
			}
		})
	}
}
