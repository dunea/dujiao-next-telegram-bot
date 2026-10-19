package dujiao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAffiliateAPIContractsAndSignatures(t *testing.T) {
	client, err := NewClient("https://example.test", "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	const timestamp = int64(1700000000)
	client.now = func() time.Time { return time.Unix(timestamp, 0) }
	seen := map[string]bool{}
	client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		if !Verify("secret", request.Method, request.URL.Path, timestamp, body, request.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Fatal("invalid affiliate signature")
		}
		path := strings.TrimPrefix(request.URL.Path, channelPrefix)
		seen[path] = true
		var data string
		switch path {
		case "/affiliate/dashboard":
			data = `{"opened":true,"affiliate_code":"AFF42","min_withdraw_amount":10.5,"pending_commission":"1.00"}`
		case "/affiliate/open":
			var identity TelegramIdentity
			_ = json.Unmarshal(body, &identity)
			if request.Method != "POST" || identity.ChannelUserID != "42" || identity.Username != "alice" {
				t.Fatalf("open: %s", body)
			}
			data = `{"id":1,"code":"AFF42","status":"active"}`
		case "/affiliate/click":
			var click AffiliateClick
			_ = json.Unmarshal(body, &click)
			if request.Method != "POST" || click.ChannelUserID != "43" || click.VisitorKey != "telegram:43" || click.AffiliateCode != "AFF42" {
				t.Fatalf("click: %s", body)
			}
			data = `{"ok":true}`
		case "/affiliate/commissions":
			data = `{"items":[{"id":1,"order_no":"DJ1","commission_amount":"1.00","rate_percent":"10.00","status":"pending_confirm"}],"page":2,"total":6,"total_pages":2}`
		case "/affiliate/withdraws":
			data = `{"items":[{"id":2,"amount":"10.00","channel":"alipay","status":"pending_review"}],"page":1,"total":1,"total_pages":1}`
		default:
			t.Fatalf("unexpected path: %s", path)
		}
		if request.Method == http.MethodGet {
			if request.URL.Query().Get("channel_user_id") != "42" {
				t.Fatal("owner missing from affiliate query")
			}
			if path != "/affiliate/dashboard" && request.URL.Query().Get("page_size") != "5" {
				t.Fatal("missing page size")
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status_code":0,"data":` + data + `}`))}, nil
	})
	ctx := context.Background()
	dashboard, err := client.AffiliateDashboard(ctx, "42")
	if err != nil || dashboard.MinWithdrawAmount.String() != "10.5" {
		t.Fatalf("dashboard=%+v, %v", dashboard, err)
	}
	profile, err := client.OpenAffiliate(ctx, TelegramIdentity{ChannelUserID: "42", Username: "alice"})
	if err != nil || profile.Code != "AFF42" {
		t.Fatalf("open=%+v, %v", profile, err)
	}
	if err := client.TrackAffiliateClick(ctx, AffiliateClick{ChannelUserID: "43", AffiliateCode: "AFF42", VisitorKey: "telegram:43", LandingPath: "/telegram/start", Referrer: "telegram"}); err != nil {
		t.Fatal(err)
	}
	commissions, err := client.AffiliateCommissions(ctx, "42", 2)
	if err != nil || len(commissions.Items) != 1 || commissions.Items[0].CommissionAmount != "1.00" || commissions.TotalPages != 2 {
		t.Fatalf("commissions=%+v, %v", commissions, err)
	}
	withdrawals, err := client.AffiliateWithdrawals(ctx, "42", 1)
	if err != nil || len(withdrawals.Items) != 1 || withdrawals.Items[0].Status != "pending_review" {
		t.Fatalf("withdrawals=%+v, %v", withdrawals, err)
	}
	if len(seen) != 5 {
		t.Fatalf("not all affiliate endpoints covered: %v", seen)
	}
	if got := affiliatePageQuery("42", -1).Get("page"); got != "1" {
		t.Fatal("negative page not normalized")
	}
}

func TestAffiliateRejectsIncompleteWriteAcknowledgements(t *testing.T) {
	client, _ := NewClient("https://example.test", "key", "secret", time.Second)
	client.http.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status_code":0,"data":{}}`))}, nil
	})
	if _, err := client.OpenAffiliate(context.Background(), TelegramIdentity{ChannelUserID: "42"}); err == nil {
		t.Fatal("incomplete open response accepted")
	}
	if err := client.TrackAffiliateClick(context.Background(), AffiliateClick{ChannelUserID: "42"}); err == nil {
		t.Fatal("incomplete click response accepted")
	}
}

func TestAffiliatePreservesPlainHTTPErrorsFromOlderStores(t *testing.T) {
	for _, status := range []int{404, 501, 502} {
		client, _ := NewClient("https://example.test", "key", "secret", time.Second)
		client.http.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("<html>private upstream text</html>"))}, nil
		})
		_, err := client.AffiliateDashboard(context.Background(), "42")
		apiError, ok := err.(*APIError)
		if !ok || apiError.HTTPStatus != status || strings.Contains(err.Error(), "private upstream text") {
			t.Fatalf("plain HTTP %d error = %v", status, err)
		}
	}
}

func TestAffiliateWithdrawalSignsBodyAndRejectsUncertainAcknowledgement(t *testing.T) {
	client, _ := NewClient("https://example.test", "key", "secret", time.Second)
	const timestamp = int64(1700000000)
	client.now = func() time.Time { return time.Unix(timestamp, 0) }
	request := AffiliateWithdrawalRequest{ChannelUserID: "42", Amount: "20.00", Channel: "alipay", Account: "alice@example.com Alice"}
	response := AffiliateWithdrawal{ID: 3, Amount: request.Amount, Channel: request.Channel, Account: request.Account, Status: "pending_review"}
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		var got AffiliateWithdrawalRequest
		_ = json.Unmarshal(body, &got)
		if r.Method != "POST" || r.URL.Path != channelPrefix+"/affiliate/withdraws" || got != request || !Verify("secret", r.Method, r.URL.Path, timestamp, body, r.Header.Get("Dujiao-Next-Channel-Signature")) {
			t.Fatal("withdrawal request/signature mismatch")
		}
		data, _ := json.Marshal(response)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status_code":0,"data":` + string(data) + `}`))}, nil
	})
	if result, err := client.ApplyAffiliateWithdrawal(context.Background(), request); err != nil || result.ID != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, field := range []string{"id", "amount", "channel", "account", "status"} {
		original := response
		switch field {
		case "id":
			response.ID = 0
		case "amount":
			response.Amount = "30.00"
		case "channel":
			response.Channel = "usdt"
		case "account":
			response.Account = "other@example.com"
		case "status":
			response.Status = "unknown"
		}
		if _, err := client.ApplyAffiliateWithdrawal(context.Background(), request); err == nil {
			t.Fatalf("inconsistent %s accepted as success", field)
		}
		response = original
	}
}
