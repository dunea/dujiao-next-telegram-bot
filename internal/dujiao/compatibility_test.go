package dujiao_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/compatibility"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

// These are source-reviewed synthetic contract profiles, NOT recordings of
// live Dujiao instances. See docs/compatibility.md for the evidence boundary.
func TestCompatibilityVersionContracts(t *testing.T) {
	for _, version := range compatibility.Versions() {
		t.Run(version.Version, func(t *testing.T) {
			var provisions atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				stamp, _ := strconv.ParseInt(r.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
				if !dujiao.Verify("secret", r.Method, r.URL.Path, stamp, body, r.Header.Get("Dujiao-Next-Channel-Signature")) {
					t.Error("signature contract changed")
				}
				switch r.URL.Path {
				case "/api/v1/channel/identities/telegram/provision":
					provisions.Add(1)
					var identity dujiao.TelegramIdentity
					_ = json.Unmarshal(body, &identity)
					if identity.InviteCode != "JOIN" {
						t.Error("client did not send registration invite code")
					}
					if !version.ChannelInviteCode {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"status_code":500,"error_code":"internal_error"}`)
						return
					}
					fmt.Fprint(w, `{"status_code":0,"data":{"bound":true,"created":true,"user_id":42}}`)
				case "/api/v1/channel/wallet":
					fmt.Fprint(w, `{"status_code":0,"data":{"balance":"150.00","currency":"CNY","new_optional_field":123}}`)
				case "/api/v1/channel/affiliate/dashboard":
					fmt.Fprint(w, `{"status_code":0,"data":{"opened":true,"min_withdraw_amount":10,"pending_commission":"1.25","available_commission":"20.00","withdraw_channels":["alipay"]}}`)
				case "/api/v1/channel/affiliate/commissions":
					if r.URL.Query().Get("page_size") != "5" || r.URL.Query().Get("channel_user_id") != "42" {
						t.Error("wrong pagination/user binding")
					}
					fmt.Fprint(w, `{"status_code":0,"data":{"items":[{"id":7,"commission_amount":"2.00","withdraw_request_id":0}],"page":1,"total":1,"total_pages":1}}`)
				case "/api/v1/channel/orders/7":
					fmt.Fprint(w, `{"status_code":0,"data":{"order_id":7,"order_no":"SYNTHETIC-ORDER","total_amount":"10.00","status":"pending_payment"}}`)
				default:
					t.Errorf("unexpected contract route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, _ := dujiao.NewClient(server.URL, "key", "secret", time.Second)
			wallet, err := client.Wallet(context.Background(), "42")
			if err != nil || wallet.Balance != "150.00" || wallet.Currency != "CNY" {
				t.Fatal("wallet string money contract", wallet, err)
			}
			dashboard, err := client.AffiliateDashboard(context.Background(), "42")
			if err != nil || dashboard.MinWithdrawAmount.String() != "10" || dashboard.AvailableCommission != "20.00" {
				t.Fatal("dashboard decimal contract", dashboard, err)
			}
			commissions, err := client.AffiliateCommissions(context.Background(), "42", 1)
			if err != nil || len(commissions.Items) != 1 || commissions.TotalPages != 1 {
				t.Fatal("pagination contract", commissions, err)
			}
			order, err := client.Order(context.Background(), "42", "zh-CN", 7)
			if err != nil || order.OrderID != 7 {
				t.Fatal("Channel API must keep numeric order route independent of frontend API", err)
			}
			identity, err := client.ProvisionIdentity(context.Background(), dujiao.TelegramIdentity{ChannelUserID: "42", InviteCode: "JOIN"})
			if version.ChannelInviteCode {
				if err != nil || !identity.Bound {
					t.Fatal("invite-capable profile failed", err)
				}
			} else {
				var api *dujiao.APIError
				if !errors.As(err, &api) || api.HTTPStatus != 500 {
					t.Fatal("legacy invitation limitation hidden", err)
				}
			}
			if provisions.Load() != 1 {
				t.Fatal("registration was automatically retried")
			}
		})
	}
}

func TestCompatibilityUpstreamUpgradeSequence(t *testing.T) {
	var profile atomic.Int32
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		if r.URL.Path == "/api/v1/channel/telegram/config" {
			fmt.Fprintf(w, `{"status_code":0,"data":{"config":{"enabled":true},"config_version":%d}}`, 100+profile.Load())
			return
		}
		fmt.Fprint(w, `{"status_code":0,"data":{"items":[],"future_field":{"enabled":true}}}`)
	}))
	defer server.Close()
	client, _ := dujiao.NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	for i, version := range compatibility.Versions() {
		t.Run(version.Version, func(t *testing.T) {
			profile.Store(int32(i))
			report := compatibility.Probe(context.Background(), client, "")
			if !report.ReadChecksOK || report.ConfigRevision != 100+i || report.UpstreamVersion != "unknown" || report.WriteChecks != "not_run" {
				t.Fatal("profile upgrade misreported software version or readiness", report)
			}
		})
	}
	if writes.Load() != 0 {
		t.Fatal("upgrade probe caused writes")
	}
}
