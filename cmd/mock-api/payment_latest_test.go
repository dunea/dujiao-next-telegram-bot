package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDemoLatestPaymentMissingIsExplicitAndOwnerScoped(t *testing.T) {
	orderMu.Lock()
	before := demoOrders
	demoOrders = []map[string]any{{"order_id": uint(7), "order_no": "DEMO7", "status": "pending_payment", "channel_user_id": "42", "total_amount": "10.00"}}
	orderMu.Unlock()
	t.Cleanup(func() { orderMu.Lock(); demoOrders = before; orderMu.Unlock() })
	for _, tc := range []struct{ uid, code string }{{"42", "payment_not_found"}, {"43", "order_not_found"}, {"", "order_not_found"}} {
		w := httptest.NewRecorder()
		dujiaoHandler(w, httptest.NewRequest("GET", "/api/v1/channel/payments/latest?order_id=7&channel_user_id="+tc.uid, nil))
		var body struct {
			Code string `json:"error_code"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusNotFound || body.Code != tc.code {
			t.Fatal(fmt.Sprint(tc.uid), "latest payment must return explicit absence, never successful null")
		}
	}
}

func TestDemoLatestPaymentRetainsOriginalRecordAndHidesClosedOrders(t *testing.T) {
	orderMu.Lock()
	before, beforePayments := demoOrders, demoPayments
	demoOrders = []map[string]any{{"order_id": uint(7), "status": "pending_payment", "channel_user_id": "42"}}
	demoPayments = map[uint]map[string]any{7: {"payment_id": uint(9), "order_id": uint(7), "status": "pending", "amount": "10.00", "currency": "CNY", "qr_code": "SYNTHETIC-QR", "expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}}
	orderMu.Unlock()
	t.Cleanup(func() { orderMu.Lock(); demoOrders, demoPayments = before, beforePayments; orderMu.Unlock() })
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		dujiaoHandler(w, httptest.NewRequest("GET", "/api/v1/channel/payments/latest?order_id=7&channel_user_id=42", nil))
		var result struct {
			Data struct {
				ID uint   `json:"payment_id"`
				QR string `json:"qr_code"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != 200 || result.Data.ID != 9 || result.Data.QR != "SYNTHETIC-QR" {
			t.Fatal("original pending record not reusable")
		}
	}
	orderMu.Lock()
	demoPayments[7]["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	orderMu.Unlock()
	w := httptest.NewRecorder()
	dujiaoHandler(w, httptest.NewRequest("GET", "/api/v1/channel/payments/latest?order_id=7&channel_user_id=42", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "SYNTHETIC-QR") {
		t.Fatal("expired payment offered as reusable")
	}
	orderMu.Lock()
	demoPayments[7]["expires_at"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	orderMu.Unlock()
	for _, status := range []string{"completed", "cancelled"} {
		orderMu.Lock()
		demoOrders[0]["status"] = status
		orderMu.Unlock()
		w := httptest.NewRecorder()
		dujiaoHandler(w, httptest.NewRequest("GET", "/api/v1/channel/payments/latest?order_id=7&channel_user_id=42", nil))
		if w.Code != 404 || strings.Contains(w.Body.String(), "SYNTHETIC-QR") {
			t.Fatal("closed order offered stale payment")
		}
	}
}

func TestMockTelegramMediaReturnsMessageObject(t *testing.T) {
	for _, method := range []string{"sendPhoto", "sendDocument"} {
		w := httptest.NewRecorder()
		telegramHandler(w, httptest.NewRequest("POST", "/bot999:synthetic/"+method, nil))
		var result struct {
			Result struct {
				ID int `json:"message_id"`
			} `json:"result"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Result.ID <= 0 {
			t.Fatal("mock media response is not a Telegram Message")
		}
	}
}
