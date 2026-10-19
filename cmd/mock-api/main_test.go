package main

import (
	"net/http/httptest"
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
