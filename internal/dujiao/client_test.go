package dujiao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientSignsAndDecodesRequest(t *testing.T) {
	const timestamp = int64(1700000000)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/channel/catalog/categories" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("locale") != "zh-CN" {
			t.Errorf("locale = %q", request.URL.Query().Get("locale"))
		}
		if request.Header.Get("Dujiao-Next-Channel-Key") != "key" {
			t.Error("missing channel key")
		}
		if !Verify("secret", http.MethodGet, request.URL.Path, timestamp, nil, request.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Error("invalid request signature")
		}
		var body strings.Builder
		_ = json.NewEncoder(&body).Encode(map[string]any{
			"status_code": 0,
			"msg":         "success",
			"data": map[string]any{"items": []map[string]any{{
				"id": 3, "name": "Software", "product_count": 2,
			}}},
		})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body.String()))}, nil
	})

	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(timestamp, 0) }
	client.http.Transport = transport
	categories, err := client.Categories(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || categories[0].ID != 3 || categories[0].ProductCount != 2 {
		t.Fatalf("categories = %#v", categories)
	}
}

func TestLatestPaymentUsesOwnerScopedReadOnlyEndpoint(t *testing.T) {
	c, e := NewClient("https://example.test", "key", "secret", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/channel/payments/latest" || r.URL.Query().Get("channel_user_id") != "42" || r.URL.Query().Get("order_id") != "7" {
			t.Fatal("latest payment not owner-scoped read")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status_code":0,"data":{"payment_id":8,"order_id":7,"status":"pending","pay_url":"https://payments.example.invalid/original"}}`))}, nil
	})
	p, e := c.LatestPayment(context.Background(), "42", 7)
	if e != nil || p.PaymentID != 8 || p.OrderID != 7 {
		t.Fatal("latest payment decode failed", e)
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := `{"status_code":400,"msg":"bad invite","error_code":"invite_code_invalid","request_id":"req-1"}`
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	_, err = client.ProvisionIdentity(context.Background(), TelegramIdentity{ChannelUserID: strconv.FormatInt(42, 10)})
	apiError, ok := err.(*APIError)
	if !ok || apiError.ErrorCode != "invite_code_invalid" || apiError.RequestID != "req-1" {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientCreatesWalletRecharge(t *testing.T) {
	const timestamp = int64(1700000000)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q", request.Method)
		}
		if request.URL.Path != "/api/v1/channel/wallet/recharge" {
			t.Errorf("path = %q", request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			ChannelUserID string `json:"channel_user_id"`
			Amount        string `json:"amount"`
			ChannelID     uint   `json:"channel_id"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ChannelUserID != "42" || payload.Amount != "50.00" || payload.ChannelID != 7 {
			t.Errorf("payload = %#v", payload)
		}
		if !Verify("secret", http.MethodPost, request.URL.Path, timestamp, body, request.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Error("invalid request signature")
		}
		response := `{"status_code":0,"msg":"success","data":{"recharge_no":"WR001","payment":{"id":9,"amount":"50.00","currency":"CNY","status":"pending","pay_url":"https://pay.example.test/WR001"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})

	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(timestamp, 0) }
	client.http.Transport = transport
	result, err := client.CreateWalletRecharge(context.Background(), "42", "50.00", 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.RechargeNo != "WR001" || result.Payment.ID != 9 || result.Payment.Status != "pending" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientRedeemsGiftCard(t *testing.T) {
	const timestamp = int64(1700000000)
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(timestamp, 0) }
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/channel/wallet/gift-card/redeem" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["channel_user_id"] != "42" || payload["code"] != "GIFT50" {
			t.Fatalf("payload = %#v", payload)
		}
		if !Verify("secret", request.Method, request.URL.Path, timestamp, body, request.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Fatal("invalid redemption signature")
		}
		response := `{"status_code":0,"data":{"gift_card":{"id":7,"amount":"50.00","currency":"CNY","status":"redeemed"},"wallet":{"balance":"150.00"},"wallet_delta":"50.00","transaction":{"type":"gift_card_redeem","direction":"in","amount":"50.00","balance_after":"150.00"}}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	result, err := client.RedeemGiftCard(context.Background(), "42", "GIFT50")
	if err != nil || result.WalletDelta != "50.00" || result.Wallet.Balance != "150.00" || result.Transaction.Type != "gift_card_redeem" {
		t.Fatalf("redemption = %#v, %v", result, err)
	}
	client.http.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status_code":0,"data":{}}`))}, nil
	})
	if _, err := client.RedeemGiftCard(context.Background(), "42", "GIFT50"); err == nil {
		t.Fatal("incomplete response was accepted as a redemption")
	}
}

func TestClientGetsOrderDetail(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/channel/orders/23" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.URL.Query().Get("channel_user_id") != "42" || request.URL.Query().Get("locale") != "zh-CN" {
			t.Errorf("query = %q", request.URL.RawQuery)
		}
		response := `{"status_code":0,"msg":"success","data":{"order_id":23,"order_no":"DJ23","status":"completed","currency":"CNY","total_amount":"9.90","fulfillment_status":"delivered","fulfillment_result":{"card":"DEMO-CARD"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport
	order, err := client.Order(context.Background(), "42", "zh-CN", 23)
	if err != nil {
		t.Fatal(err)
	}
	if order.OrderNo != "DJ23" || order.FulfillmentStatus != "delivered" {
		t.Fatalf("order = %#v", order)
	}
}

func TestClientFiltersOrders(t *testing.T) {
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if request.URL.Path != "/api/v1/channel/orders" || query.Get("status") != "pending_payment" {
			t.Errorf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if query.Get("page") != "2" || query.Get("page_size") != "5" {
			t.Errorf("pagination = %q", request.URL.RawQuery)
		}
		response := `{"status_code":0,"msg":"success","data":{"items":[],"page":2,"page_size":5,"total":6,"total_pages":2}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	orders, err := client.Orders(context.Background(), "42", "zh-CN", "pending_payment", 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if orders.Page != 2 || orders.TotalPages != 2 {
		t.Fatalf("orders = %#v", orders)
	}
}

func TestClientListsWalletTransactions(t *testing.T) {
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if request.URL.Path != "/api/v1/channel/wallet/transactions" || query.Get("channel_user_id") != "42" {
			t.Errorf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if query.Get("page") != "1" || query.Get("page_size") != "5" {
			t.Errorf("pagination = %q", request.URL.RawQuery)
		}
		response := `{"status_code":0,"msg":"success","data":{"items":[{"type":"recharge","direction":"in","amount":"50.00","balance_after":"150.00","remark":"test","created_at":"2026-10-19T00:00:00Z"}],"page":1,"page_size":5,"total":1,"total_pages":1}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	transactions, err := client.WalletTransactions(context.Background(), "42", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions.Items) != 1 || transactions.Items[0].BalanceAfter != "150.00" {
		t.Fatalf("transactions = %#v", transactions)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
