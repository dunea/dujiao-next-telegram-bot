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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
