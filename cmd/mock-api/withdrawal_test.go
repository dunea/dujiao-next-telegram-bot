package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestDemoWithdrawalReservesOnlyRequestedAmountAndPreservesWallet(t *testing.T) {
	isolateDemoAffiliateState(t)
	t.Setenv("DEMO_AFFILIATE_SEED_COMMISSION", "100.00")
	openDemoAffiliate(t, "42")
	before := walletBalanceCents.Load()
	for _, amount := range []string{"30.00", "20.00"} {
		var row dujiao.AffiliateWithdrawal
		affiliateDemoRequest(t, "POST", "/affiliate/withdraws", dujiao.AffiliateWithdrawalRequest{ChannelUserID: "42", Amount: amount, Channel: "alipay", Account: "alice@example.com Alice"}, &row)
		if row.ID == 0 || row.Status != "pending_review" || row.Amount != amount {
			t.Fatalf("withdrawal=%+v", row)
		}
	}
	var dashboard dujiao.AffiliateDashboard
	affiliateDemoRequest(t, "GET", "/affiliate/dashboard?channel_user_id=42", nil, &dashboard)
	if dashboard.AvailableCommission != "50.00" || dashboard.WithdrawnCommission != "0.00" || dashboard.ValidOrderCount != 0 || walletBalanceCents.Load() != before {
		t.Fatalf("dashboard=%+v", dashboard)
	}
	var rows dujiao.AffiliateCommissionList
	affiliateDemoRequest(t, "GET", "/affiliate/commissions?channel_user_id=42", nil, &rows)
	if len(rows.Items) != 3 {
		t.Fatalf("partial commission not split: %+v", rows)
	}
	reserved, unreserved := 0, 0
	for _, row := range rows.Items {
		if row.WithdrawalRequestID != 0 {
			reserved++
		} else {
			unreserved++
		}
	}
	if reserved != 2 || unreserved != 1 {
		t.Fatal("partial withdrawal froze excess commission")
	}
	var history dujiao.AffiliateWithdrawalList
	affiliateDemoRequest(t, "GET", "/affiliate/withdraws?channel_user_id=42", nil, &history)
	if len(history.Items) != 2 {
		t.Fatal("withdrawal history missing")
	}
	affiliateDemoRequest(t, "GET", "/affiliate/withdraws?channel_user_id=43", nil, &history)
	if len(history.Items) != 0 {
		t.Fatal("withdrawals crossed account boundary")
	}
}

func TestDemoWithdrawalConcurrentOverspendIsRejected(t *testing.T) {
	isolateDemoAffiliateState(t)
	t.Setenv("DEMO_AFFILIATE_SEED_COMMISSION", "100.00")
	openDemoAffiliate(t, "42")
	var successes atomic.Int64
	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() {
			recorder := httptest.NewRecorder()
			dujiaoHandler(recorder, httptest.NewRequest("POST", "/api/v1/channel/affiliate/withdraws", strings.NewReader(`{"channel_user_id":"42","amount":"100.00","channel":"usdt","account":"TRC20 DemoAddress"}`)))
			if recorder.Code == 200 {
				successes.Add(1)
			} else if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "affiliate_withdraw_insufficient") {
				t.Errorf("unexpected rejection: %d %s", recorder.Code, recorder.Body)
			}
		})
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent withdrawals succeeded %d times", successes.Load())
	}
	var dashboard dujiao.AffiliateDashboard
	affiliateDemoRequest(t, "GET", "/affiliate/dashboard?channel_user_id=42", nil, &dashboard)
	if dashboard.AvailableCommission != "0.00" {
		t.Fatal("reserved commission stayed spendable")
	}
}

func TestDemoWithdrawalRejectsInvalidOrUnavailableFundsWithoutMutation(t *testing.T) {
	isolateDemoAffiliateState(t)
	t.Setenv("DEMO_AFFILIATE_SEED_COMMISSION", "100.00")
	openDemoAffiliate(t, "42")
	for _, tc := range []struct{ amount, channel, account, code string }{
		{"9.99", "alipay", "alice", "affiliate_withdraw_amount_invalid"}, {"0", "alipay", "alice", "affiliate_withdraw_amount_invalid"}, {"-1", "alipay", "alice", "affiliate_withdraw_amount_invalid"}, {"1.234", "alipay", "alice", "affiliate_withdraw_amount_invalid"}, {"100.01", "alipay", "alice", "affiliate_withdraw_insufficient"}, {"20.00", "unknown", "alice", "affiliate_withdraw_channel_invalid"}, {"20.00", "alipay", "", "validation_error"}, {"20.00", "alipay", "a\nb", "validation_error"},
	} {
		body, _ := json.Marshal(dujiao.AffiliateWithdrawalRequest{ChannelUserID: "42", Amount: tc.amount, Channel: tc.channel, Account: tc.account})
		recorder := httptest.NewRecorder()
		dujiaoHandler(recorder, httptest.NewRequest("POST", "/api/v1/channel/affiliate/withdraws", strings.NewReader(string(body))))
		if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), tc.code) {
			t.Fatalf("%+v: %d %s", tc, recorder.Code, recorder.Body)
		}
	}
	var dashboard dujiao.AffiliateDashboard
	affiliateDemoRequest(t, "GET", "/affiliate/dashboard?channel_user_id=42", nil, &dashboard)
	if dashboard.AvailableCommission != "100.00" {
		t.Fatal("rejected application changed available commission")
	}
	for _, userID := range []string{"43", "-1", ""} {
		recorder := httptest.NewRecorder()
		body := fmt.Sprintf(`{"channel_user_id":%q,"amount":"20.00","channel":"alipay","account":"alice"}`, userID)
		dujiaoHandler(recorder, httptest.NewRequest("POST", "/api/v1/channel/affiliate/withdraws", strings.NewReader(body)))
		if recorder.Code != 400 {
			t.Fatal("unopened/invalid identity could withdraw")
		}
	}
}
