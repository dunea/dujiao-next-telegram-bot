package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func isolateGiftCardState(t *testing.T) {
	t.Helper()
	demoGiftCardMu.Lock()
	cards := maps.Clone(demoGiftCards)
	demoGiftCardMu.Unlock()
	walletTransactionMu.Lock()
	transactions := demoWalletTransactions
	walletTransactionMu.Unlock()
	balance := walletBalanceCents.Load()
	walletBalanceCents.Store(10_000)
	t.Cleanup(func() {
		demoGiftCardMu.Lock()
		demoGiftCards = cards
		demoGiftCardMu.Unlock()
		walletTransactionMu.Lock()
		demoWalletTransactions = transactions
		walletTransactionMu.Unlock()
		walletBalanceCents.Store(balance)
	})
}

func TestDemoGiftCardConcurrentRedemptionCreditsOnce(t *testing.T) {
	isolateGiftCardState(t)
	walletTransactionMu.Lock()
	originalCount := len(demoWalletTransactions)
	walletTransactionMu.Unlock()
	var success atomic.Int64
	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() {
			result, code := redeemDemoGiftCard(" gift50 ")
			if code == "" {
				success.Add(1)
				if result.WalletDelta != "50.00" || result.Wallet.Balance != "150.00" {
					t.Errorf("redemption = %#v", result)
				}
			} else if code != "gift_card_redeemed" {
				t.Errorf("unexpected error code %q", code)
			}
		})
	}
	wait.Wait()
	if success.Load() != 1 || walletBalanceCents.Load() != 15_000 {
		t.Fatal("gift card was credited more than once")
	}
	walletTransactionMu.Lock()
	defer walletTransactionMu.Unlock()
	if len(demoWalletTransactions) != originalCount+1 || demoWalletTransactions[0]["type"] != "gift_card_redeem" || demoWalletTransactions[0]["balance_after"] != "150.00" {
		t.Fatalf("gift card ledger = %#v", demoWalletTransactions)
	}
}

func TestDemoGiftCardHTTPResponses(t *testing.T) {
	isolateGiftCardState(t)
	for _, test := range []struct {
		code, errorCode string
		status          int
	}{
		{"GIFT-EXPIRED", "gift_card_expired", 400},
		{"GIFT-DISABLED", "gift_card_disabled", 400},
		{"GIFT-USED", "gift_card_redeemed", 400},
		{"UNKNOWN", "gift_card_not_found", 404},
		{"", "gift_card_invalid", 400},
	} {
		t.Run(test.code, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"channel_user_id": "42", "code": test.code})
			recorder := httptest.NewRecorder()
			dujiaoHandler(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/channel/wallet/gift-card/redeem", strings.NewReader(string(body))))
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.errorCode) {
				t.Fatalf("gift card response = %d %s", recorder.Code, recorder.Body)
			}
		})
	}
	if balance := walletBalanceCents.Load(); balance != 10_000 {
		t.Fatalf("failed redemption changed balance: %d", balance)
	}
	recorder := httptest.NewRecorder()
	dujiaoHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/channel/wallet/gift-card/redeem", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatal("GET could redeem a card")
	}
	recorder = httptest.NewRecorder()
	dujiaoHandler(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/channel/wallet/gift-card/redeem", strings.NewReader(`{"channel_user_id":"42","code":"GIFT100"}`)))
	var response struct {
		StatusCode int                       `json:"status_code"`
		Data       dujiao.GiftCardRedemption `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || response.StatusCode != 0 || response.Data.WalletDelta != "100.00" || response.Data.Wallet.Balance != "200.00" {
		t.Fatalf("redemption response = %s", recorder.Body)
	}
}
