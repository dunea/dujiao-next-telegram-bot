package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func isolateDemoAffiliateState(t *testing.T) {
	t.Helper()
	t.Setenv("DEMO_AFFILIATE_SEED_COMMISSION", "")
	demoAffiliateMu.Lock()
	profiles, clicks := demoAffiliateProfiles, demoAffiliateClicks
	demoAffiliateProfiles = map[string]*demoAffiliateProfile{}
	demoAffiliateClicks = nil
	demoAffiliateMu.Unlock()
	orderMu.Lock()
	orders := demoOrders
	demoOrders = nil
	orderMu.Unlock()
	isolateGiftCardState(t)
	t.Cleanup(func() {
		demoAffiliateMu.Lock()
		demoAffiliateProfiles, demoAffiliateClicks = profiles, clicks
		demoAffiliateMu.Unlock()
		orderMu.Lock()
		demoOrders = orders
		orderMu.Unlock()
	})
}

func affiliateDemoRequest(t *testing.T, method, path string, payload any, result any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	recorder := httptest.NewRecorder()
	dujiaoHandler(recorder, httptest.NewRequest(method, "/api/v1/channel"+path, bytes.NewReader(body)))
	if recorder.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, recorder.Code, recorder.Body)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			t.Fatal(err)
		}
	}
}

func openDemoAffiliate(t *testing.T, userID string) dujiao.AffiliateProfile {
	t.Helper()
	var profile dujiao.AffiliateProfile
	affiliateDemoRequest(t, "POST", "/affiliate/open", dujiao.TelegramIdentity{ChannelUserID: userID}, &profile)
	return profile
}

func clickDemoAffiliate(t *testing.T, userID, code string) {
	t.Helper()
	affiliateDemoRequest(t, "POST", "/affiliate/click", dujiao.AffiliateClick{ChannelUserID: userID, AffiliateCode: code, VisitorKey: "telegram:" + userID, LandingPath: "/telegram/start"}, nil)
}

func TestDemoAffiliateClickPurchasePaymentAndOwnerIsolation(t *testing.T) {
	isolateDemoAffiliateState(t)
	profile := openDemoAffiliate(t, "42")
	clickDemoAffiliate(t, "43", profile.Code)
	clickDemoAffiliate(t, "43", profile.Code)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/channel/orders", strings.NewReader(`{"channel_user_id":"43","affiliate_visitor_key":"telegram:43","items":[{"product_id":1,"quantity":3}],"coupon_code":"SAVE10"}`))
	order := demoCreateOrder(request)
	if order == nil || order["affiliate_code"] != profile.Code {
		t.Fatalf("order lost attribution: %+v", order)
	}
	var before dujiao.AffiliateDashboard
	affiliateDemoRequest(t, "GET", "/affiliate/dashboard?channel_user_id=42", nil, &before)
	if before.ClickCount != 1 || before.ValidOrderCount != 0 || before.PendingCommission != "0.00" {
		t.Fatalf("before payment: %+v", before)
	}
	if !completeDemoOrder(order["order_id"].(uint), true) {
		t.Fatal("payment failed")
	}
	if completeDemoOrder(order["order_id"].(uint), true) {
		t.Fatal("payment repeated")
	}
	var after dujiao.AffiliateDashboard
	affiliateDemoRequest(t, "GET", "/affiliate/dashboard?channel_user_id=42", nil, &after)
	if after.ClickCount != 1 || after.ValidOrderCount != 1 || after.PendingCommission != "2.67" || after.ConversionRate != 100 {
		t.Fatalf("after payment: %+v", after)
	}
	var commissions dujiao.AffiliateCommissionList
	affiliateDemoRequest(t, "GET", "/affiliate/commissions?channel_user_id=42&page=1&page_size=5", nil, &commissions)
	if len(commissions.Items) != 1 || commissions.Items[0].CommissionAmount != "2.67" {
		t.Fatalf("commissions=%+v", commissions)
	}
	affiliateDemoRequest(t, "GET", "/affiliate/commissions?channel_user_id=43", nil, &commissions)
	if len(commissions.Items) != 0 {
		t.Fatal("buyer can see promoter commissions")
	}
	clickDemoAffiliate(t, "42", profile.Code)
	self := demoCreateOrder(httptest.NewRequest("POST", "/api/v1/channel/orders", strings.NewReader(`{"channel_user_id":"42","affiliate_visitor_key":"telegram:42","items":[{"product_id":1,"quantity":2}]}`)))
	if self == nil || self["affiliate_code"] != "" {
		t.Fatal("self-purchase attributed")
	}
}

func TestDemoAffiliateLastClickExpiryAndInvalidCode(t *testing.T) {
	isolateDemoAffiliateState(t)
	first := openDemoAffiliate(t, "42")
	second := openDemoAffiliate(t, "44")
	clickDemoAffiliate(t, "43", first.Code)
	clickDemoAffiliate(t, "43", second.Code)
	preview := map[string]any{"channel_user_id": "43", "affiliate_visitor_key": "telegram:43"}
	if demoAffiliateOrderCode(preview) != second.Code {
		t.Fatal("last click not preferred")
	}
	clickDemoAffiliate(t, "43", "INVALID")
	if demoAffiliateOrderCode(preview) != second.Code {
		t.Fatal("invalid code changed attribution")
	}
	demoAffiliateMu.Lock()
	for i := range demoAffiliateClicks {
		demoAffiliateClicks[i].At = time.Now().Add(-31 * 24 * time.Hour)
	}
	demoAffiliateMu.Unlock()
	if demoAffiliateOrderCode(preview) != "" {
		t.Fatal("expired click attributed")
	}
}

func TestDemoAffiliateConcurrentOpenAndClickAreIdempotent(t *testing.T) {
	isolateDemoAffiliateState(t)
	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() { openDemoAffiliate(t, "42") })
	}
	wait.Wait()
	for range 16 {
		wait.Go(func() { clickDemoAffiliate(t, "43", "DEMO42") })
	}
	wait.Wait()
	demoAffiliateMu.Lock()
	defer demoAffiliateMu.Unlock()
	if len(demoAffiliateProfiles) != 1 || demoAffiliateProfiles["42"].Clicks != 1 || len(demoAffiliateClicks) != 1 {
		t.Fatal("concurrent open/click duplicated data")
	}
}

func TestDemoAffiliatePaginationAndWithdrawalValidation(t *testing.T) {
	isolateDemoAffiliateState(t)
	openDemoAffiliate(t, "42")
	demoAffiliateMu.Lock()
	for id := uint(1); id <= 7; id++ {
		demoAffiliateProfiles["42"].Commissions = append(demoAffiliateProfiles["42"].Commissions, dujiao.AffiliateCommission{ID: id, CommissionAmount: "1.00"})
		demoAffiliateProfiles["42"].Withdrawals = append(demoAffiliateProfiles["42"].Withdrawals, dujiao.AffiliateWithdrawal{ID: id, Amount: "10.00", Status: "paid"})
	}
	demoAffiliateMu.Unlock()
	var commissions dujiao.AffiliateCommissionList
	affiliateDemoRequest(t, "GET", "/affiliate/commissions?channel_user_id=42&page=2&page_size=5", nil, &commissions)
	if len(commissions.Items) != 2 || commissions.Items[0].ID != 6 || commissions.TotalPages != 2 {
		t.Fatalf("page 2=%+v", commissions)
	}
	var withdrawals dujiao.AffiliateWithdrawalList
	affiliateDemoRequest(t, "GET", "/affiliate/withdraws?channel_user_id=42&page=2&page_size=5", nil, &withdrawals)
	if len(withdrawals.Items) != 2 || withdrawals.Items[1].ID != 7 {
		t.Fatalf("withdrawals=%+v", withdrawals)
	}
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/affiliate/open?channel_user_id=42", 405}, {"POST", "/affiliate/withdraws?channel_user_id=42", 400}, {"GET", "/affiliate/dashboard", 400}, {"GET", "/affiliate/dashboard?channel_user_id=-1", 400},
	} {
		recorder := httptest.NewRecorder()
		dujiaoHandler(recorder, httptest.NewRequest(test.method, "/api/v1/channel"+test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s = %d", test.method, test.path, recorder.Code)
		}
	}
}
