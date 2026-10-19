package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDemoOrderListFiltersAndPaginates(t *testing.T) {
	orderMu.Lock()
	original := demoOrders
	demoOrders = []map[string]any{
		{"order_id": uint(3), "status": "pending_payment"},
		{"order_id": uint(2), "status": "completed"},
		{"order_id": uint(1), "status": "pending_payment"},
	}
	orderMu.Unlock()
	t.Cleanup(func() {
		orderMu.Lock()
		demoOrders = original
		orderMu.Unlock()
	})

	request := httptest.NewRequest("GET", "/api/v1/channel/orders?status=pending_payment&page=2&page_size=1", nil)
	result := demoOrderList(request)
	items := result["items"].([]map[string]any)
	if len(items) != 1 || items[0]["order_id"] != uint(1) {
		t.Fatalf("items = %#v", items)
	}
	if result["total"] != 2 || result["total_pages"] != 2 {
		t.Fatalf("pagination = %#v", result)
	}
}

func TestDemoManualOrderRejectsMissingDataAndRetainsSubmission(t *testing.T) {
	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/channel/orders", strings.NewReader(`{"items":[{"product_id":3,"quantity":1}]}`))
	recorder := httptest.NewRecorder()
	dujiaoHandler(recorder, invalid)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing form data status = %d", recorder.Code)
	}

	orderMu.Lock()
	originalOrders := demoOrders
	orderMu.Unlock()
	t.Cleanup(func() { orderMu.Lock(); demoOrders = originalOrders; orderMu.Unlock() })
	valid := httptest.NewRequest(http.MethodPost, "/api/v1/channel/orders", strings.NewReader(`{"items":[{"product_id":3,"quantity":1}],"manual_form_data":{"3:0":{"hostname":"demo.example.com","email":"demo@example.com","region":"香港","addons":["备份"]}}}`))
	created := demoCreateOrder(valid)
	if created == nil || created["fulfillment_type"] != "manual" {
		t.Fatalf("manual order = %#v", created)
	}
	items := created["items"].([]map[string]any)
	submission := items[0]["manual_form_submission"].(map[string]any)
	if submission["hostname"] != "demo.example.com" {
		t.Fatalf("submission = %#v", submission)
	}
	if !completeDemoOrder(created["order_id"].(uint), false) {
		t.Fatal("manual order payment failed")
	}
	detail := demoOrderDetail(created["order_id"].(uint))
	if detail["status"] != "processing" || detail["fulfillment_result"] != nil {
		t.Fatalf("manual payment did not enter processing: %#v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil || !strings.Contains(string(encoded), "demo.example.com") {
		t.Fatalf("stored details lost: %s, %v", encoded, err)
	}
}

func TestCompleteDemoOrderRecordsBalanceTransactionOnce(t *testing.T) {
	orderMu.Lock()
	originalOrders := demoOrders
	demoOrders = []map[string]any{{
		"order_id": uint(7), "order_no": "DEMO000007", "status": "pending_payment",
		"total_amount": "9.90", "wallet_paid_amount": "0.00", "online_paid_amount": "0.00",
	}}
	orderMu.Unlock()
	walletTransactionMu.Lock()
	originalTransactions := demoWalletTransactions
	demoWalletTransactions = nil
	walletTransactionMu.Unlock()
	originalBalance := walletBalanceCents.Load()
	walletBalanceCents.Store(10_000)
	t.Cleanup(func() {
		orderMu.Lock()
		demoOrders = originalOrders
		orderMu.Unlock()
		walletTransactionMu.Lock()
		demoWalletTransactions = originalTransactions
		walletTransactionMu.Unlock()
		walletBalanceCents.Store(originalBalance)
	})

	completeDemoOrder(7, true)
	completeDemoOrder(7, true)
	if got := walletBalanceCents.Load(); got != 9_010 {
		t.Fatalf("balance = %d", got)
	}
	walletTransactionMu.Lock()
	defer walletTransactionMu.Unlock()
	if len(demoWalletTransactions) != 1 || demoWalletTransactions[0]["type"] != "order_pay" {
		t.Fatalf("transactions = %#v", demoWalletTransactions)
	}
}

func TestDemoOrderPreviewValidatesQuantity(t *testing.T) {
	invalidRequest := httptest.NewRequest("POST", "/api/v1/channel/orders/preview", strings.NewReader(`{"items":[{"product_id":1,"quantity":1}]}`))
	invalid := demoOrderPreview(invalidRequest)
	if invalid["valid"] != false || len(invalid["validation_errors"].([]string)) != 1 {
		t.Fatalf("invalid preview = %#v", invalid)
	}

	validRequest := httptest.NewRequest("POST", "/api/v1/channel/orders/preview", strings.NewReader(`{"items":[{"product_id":1,"quantity":3}]}`))
	valid := demoOrderPreview(validRequest)
	if valid["valid"] != true || valid["total_amount"] != "29.70" {
		t.Fatalf("valid preview = %#v", valid)
	}
}

func TestDemoBalancePaymentRejectsInsufficientFunds(t *testing.T) {
	orderMu.Lock()
	originalOrders := demoOrders
	demoOrders = []map[string]any{{
		"order_id": uint(8), "order_no": "DEMO000008", "status": "pending_payment", "total_amount": "200.00",
	}}
	orderMu.Unlock()
	originalBalance := walletBalanceCents.Load()
	walletBalanceCents.Store(10_000)
	t.Cleanup(func() {
		orderMu.Lock()
		demoOrders = originalOrders
		orderMu.Unlock()
		walletBalanceCents.Store(originalBalance)
	})

	request := httptest.NewRequest("POST", "/api/v1/channel/payments", strings.NewReader(`{"order_id":8,"use_balance":true}`))
	result, errorCode := demoCreatePayment(request)
	if result != nil || errorCode != "insufficient_wallet_balance" {
		t.Fatalf("payment = %#v, %q", result, errorCode)
	}
	if got := walletBalanceCents.Load(); got != 10_000 {
		t.Fatalf("balance = %d", got)
	}
}
