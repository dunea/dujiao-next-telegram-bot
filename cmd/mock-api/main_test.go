package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDemoCouponPolicy(t *testing.T) {
	for _, test := range []struct {
		code            string
		quantity        int
		discount, total string
		valid           bool
	}{
		{"", 3, "0.00", "29.70", true},
		{"SAVE10", 3, "2.97", "26.73", true},
		{"LESS5", 3, "5.00", "24.70", true},
		{"LESS5", 2, "", "", false},
		{"EXPIRED", 3, "", "", false},
		{"save10", 3, "", "", false},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.code, test.quantity), func(t *testing.T) {
			payload := demoOrderInput{Items: []orderItem{{ProductID: 1, Quantity: test.quantity}}, CouponCode: test.code}
			preview := demoBuildPreview(payload)
			if preview["valid"] != test.valid {
				t.Fatalf("preview = %#v", preview)
			}
			if test.valid && (preview["coupon_discount"] != test.discount || preview["total_amount"] != test.total) {
				t.Fatalf("discounted preview = %#v", preview)
			}
			if !test.valid {
				encoded, _ := json.Marshal(payload)
				for _, path := range []string{"/api/v1/channel/orders/preview", "/api/v1/channel/orders"} {
					recorder := httptest.NewRecorder()
					dujiaoHandler(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded))))
					if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"coupon_invalid"`) {
						t.Fatalf("coupon response = %d %s", recorder.Code, recorder.Body)
					}
				}
			}
		})
	}
}

func TestDemoCouponOrderChargesDiscountedBalance(t *testing.T) {
	orderMu.Lock()
	originalOrders := demoOrders
	orderMu.Unlock()
	walletTransactionMu.Lock()
	originalTransactions := demoWalletTransactions
	walletTransactionMu.Unlock()
	originalBalance := walletBalanceCents.Load()
	t.Cleanup(func() {
		orderMu.Lock()
		demoOrders = originalOrders
		orderMu.Unlock()
		walletTransactionMu.Lock()
		demoWalletTransactions = originalTransactions
		walletTransactionMu.Unlock()
		walletBalanceCents.Store(originalBalance)
	})
	walletBalanceCents.Store(10_000)
	created := demoCreateOrder(httptest.NewRequest(http.MethodPost, "/api/v1/channel/orders", strings.NewReader(`{"items":[{"product_id":1,"quantity":2}],"coupon_code":"SAVE10"}`)))
	if created == nil || created["coupon_code"] != "SAVE10" || created["coupon_discount"] != "1.98" || created["total_amount"] != "17.82" {
		t.Fatalf("discounted order = %#v", created)
	}
	if !completeDemoOrder(created["order_id"].(uint), true) {
		t.Fatal("discounted order payment failed")
	}
	if balance := walletBalanceCents.Load(); balance != 8_218 {
		t.Fatalf("discounted balance = %d", balance)
	}
}

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
