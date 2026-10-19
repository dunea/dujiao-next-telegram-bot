package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestDemoRechargeFeePoliciesCreditOnlyPrincipal(t *testing.T) {
	isolateGiftCardState(t)
	for _, tc := range []struct {
		channel      uint
		payable, fee string
	}{
		{1, "100.00", "0.00"}, {2, "103.50", "3.50"}, {3, "100.00", "3.50"},
	} {
		t.Run(fmt.Sprint(tc.channel), func(t *testing.T) {
			before := walletBalanceCents.Load()
			recorder := httptest.NewRecorder()
			body := fmt.Sprintf(`{"channel_user_id":"42","amount":"100.00","channel_id":%d}`, tc.channel)
			dujiaoHandler(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/channel/wallet/recharge", strings.NewReader(body)))
			var response struct {
				Data dujiao.WalletRechargeResult `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != 200 || response.Data.Payment.Amount != tc.payable || response.Data.Payment.FeeAmount != tc.fee || response.Data.Payment.Status != "success" {
				t.Fatalf("recharge response: %d %s", recorder.Code, recorder.Body)
			}
			if walletBalanceCents.Load() != before+10000 {
				t.Fatal("fees changed wallet credit")
			}
			walletTransactionMu.Lock()
			defer walletTransactionMu.Unlock()
			if transaction := demoWalletTransactions[0]; transaction["type"] != "recharge" || transaction["amount"] != "100.00" {
				t.Fatalf("recharge transaction = %#v", transaction)
			}
		})
	}
}

func TestDemoRechargeRejectsInvalidRequestsWithoutCredit(t *testing.T) {
	isolateGiftCardState(t)
	for _, body := range []string{
		`{`, `{"amount":"100.00","channel_id":1}`,
		`{"channel_user_id":"42","amount":"-1","channel_id":1}`,
		`{"channel_user_id":"42","amount":"0","channel_id":1}`,
		`{"channel_user_id":"42","amount":"1.234","channel_id":1}`,
		`{"channel_user_id":"42","amount":"1000000.01","channel_id":1}`,
		`{"channel_user_id":"42","amount":"100.00","channel_id":99}`,
	} {
		recorder := httptest.NewRecorder()
		dujiaoHandler(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/channel/wallet/recharge", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("invalid recharge accepted: %s", body)
		}
	}
	recorder := httptest.NewRecorder()
	dujiaoHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/channel/wallet/recharge", nil))
	if recorder.Code != http.StatusMethodNotAllowed || walletBalanceCents.Load() != 10000 {
		t.Fatal("invalid recharge changed wallet balance")
	}
}

func TestDemoRechargeChannelRules(t *testing.T) {
	recorder := httptest.NewRecorder()
	dujiaoHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/channel/payment-channels?context=recharge", nil))
	var response struct {
		Data dujiao.PaymentChannelList `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 3 || response.Data.Items[1].FeeRate != "2.50" || response.Data.Items[1].FixedFee != "1.00" {
		t.Fatalf("recharge channels = %s", recorder.Body)
	}
}
