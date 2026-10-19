package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	moneycalc "github.com/dunea/dujiao-next-telegram-bot/internal/money"
)

type demoAffiliateProfile struct {
	Profile     dujiao.AffiliateProfile
	Clicks      int64
	Commissions []dujiao.AffiliateCommission
	Withdrawals []dujiao.AffiliateWithdrawal
}

type demoAffiliateClick struct {
	Owner, VisitorKey, LandingPath string
	At                             time.Time
}

var demoAffiliateMu sync.Mutex
var demoAffiliateProfiles = map[string]*demoAffiliateProfile{
	"9000000000": {Profile: dujiao.AffiliateProfile{ID: 1, Code: "AFFDEMO", Status: "active"}},
}
var demoAffiliateClicks []demoAffiliateClick

func demoAffiliateHandler(w http.ResponseWriter, request *http.Request, path string) {
	var identity dujiao.TelegramIdentity
	var click dujiao.AffiliateClick
	userID := request.URL.Query().Get("channel_user_id")
	if path == "/affiliate/open" || path == "/affiliate/click" {
		if request.Method != http.MethodPost {
			demoAffiliateMethodError(w, http.MethodPost)
			return
		}
		if path == "/affiliate/open" {
			if err := json.NewDecoder(request.Body).Decode(&identity); err != nil {
				demoAffiliateError(w)
				return
			}
			userID = identity.ChannelUserID
		} else {
			if err := json.NewDecoder(request.Body).Decode(&click); err != nil || click.AffiliateCode == "" || click.VisitorKey == "" {
				demoAffiliateError(w)
				return
			}
			userID = click.ChannelUserID
		}
	} else if request.Method != http.MethodGet {
		demoAffiliateMethodError(w, http.MethodGet)
		return
	}
	if id, err := strconv.ParseInt(userID, 10, 64); err != nil || id <= 0 {
		demoAffiliateError(w)
		return
	}
	demoAffiliateMu.Lock()
	defer demoAffiliateMu.Unlock()
	profile := demoAffiliateProfiles[userID]
	var data any
	switch path {
	case "/affiliate/open":
		if profile == nil {
			profile = &demoAffiliateProfile{Profile: dujiao.AffiliateProfile{ID: uint(len(demoAffiliateProfiles) + 1), Code: "DEMO" + userID, Status: "active"}}
			demoAffiliateProfiles[userID] = profile
		}
		data = profile.Profile
	case "/affiliate/click":
		owner, target := demoAffiliateByCode(click.AffiliateCode)
		if target != nil && target.Profile.Status == "active" {
			duplicate := false
			for _, prior := range demoAffiliateClicks {
				if prior.Owner == owner && prior.VisitorKey == click.VisitorKey && prior.LandingPath == click.LandingPath && time.Since(prior.At) < 10*time.Minute {
					duplicate = true
					break
				}
			}
			if !duplicate {
				demoAffiliateClicks = append(demoAffiliateClicks, demoAffiliateClick{Owner: owner, VisitorKey: click.VisitorKey, LandingPath: click.LandingPath, At: time.Now()})
				target.Clicks++
			}
		}
		data = map[string]bool{"ok": true}
	case "/affiliate/dashboard":
		dashboard := dujiao.AffiliateDashboard{PendingCommission: "0.00", AvailableCommission: "0.00", WithdrawnCommission: "0.00", MinWithdrawAmount: json.Number("10"), WithdrawChannels: []string{"alipay", "wechat", "usdt"}}
		if profile != nil {
			dashboard.Opened, dashboard.AffiliateCode, dashboard.PromotionPath, dashboard.ClickCount = true, profile.Profile.Code, "/?aff="+profile.Profile.Code, profile.Clicks
			var pending, available, withdrawn int64
			for _, row := range profile.Commissions {
				cents, _ := moneycalc.Parse(row.CommissionAmount)
				switch row.Status {
				case "pending_confirm":
					pending += cents
					dashboard.ValidOrderCount++
				case "available":
					available += cents
					dashboard.ValidOrderCount++
				case "withdrawn":
					withdrawn += cents
					dashboard.ValidOrderCount++
				}
			}
			dashboard.PendingCommission, dashboard.AvailableCommission, dashboard.WithdrawnCommission = moneycalc.Format(pending), moneycalc.Format(available), moneycalc.Format(withdrawn)
			if dashboard.ClickCount > 0 {
				dashboard.ConversionRate = float64(dashboard.ValidOrderCount) * 100 / float64(dashboard.ClickCount)
			}
		}
		data = dashboard
	case "/affiliate/commissions":
		items := []dujiao.AffiliateCommission{}
		if profile != nil {
			items = append(items, profile.Commissions...)
		}
		page, size, start, end := demoAffiliatePage(request, len(items))
		data = dujiao.AffiliateCommissionList{Items: items[start:end], Page: page, Total: int64(len(items)), TotalPages: int64(pageCount(len(items), size))}
	case "/affiliate/withdraws":
		items := []dujiao.AffiliateWithdrawal{}
		if profile != nil {
			items = append(items, profile.Withdrawals...)
		}
		page, size, start, end := demoAffiliatePage(request, len(items))
		data = dujiao.AffiliateWithdrawalList{Items: items[start:end], Page: page, Total: int64(len(items)), TotalPages: int64(pageCount(len(items), size))}
	default:
		http.NotFound(w, request)
		return
	}
	writeJSON(w, map[string]any{"status_code": 0, "msg": "success", "data": data})
}

// Caller holds demoAffiliateMu.
func demoAffiliateByCode(code string) (string, *demoAffiliateProfile) {
	for userID, profile := range demoAffiliateProfiles {
		if profile.Profile.Code == code {
			return userID, profile
		}
	}
	return "", nil
}

func demoAffiliatePage(request *http.Request, total int) (page, size, start, end int) {
	page = min(positiveQueryInt(request, "page", 1), 100000)
	size = min(positiveQueryInt(request, "page_size", 5), 100)
	start = min((page-1)*size, total)
	return page, size, start, min(start+size, total)
}

func demoAffiliateOrderCode(preview map[string]any) string {
	visitor, _ := preview["affiliate_visitor_key"].(string)
	buyer, _ := preview["channel_user_id"].(string)
	if visitor == "" || buyer == "" {
		return ""
	}
	demoAffiliateMu.Lock()
	defer demoAffiliateMu.Unlock()
	for index := len(demoAffiliateClicks) - 1; index >= 0; index-- {
		click := demoAffiliateClicks[index]
		if click.VisitorKey != visitor || time.Since(click.At) > 30*24*time.Hour {
			continue
		}
		profile := demoAffiliateProfiles[click.Owner]
		if profile == nil || profile.Profile.Status != "active" {
			continue
		}
		if click.Owner == buyer {
			return ""
		}
		return profile.Profile.Code
	}
	return ""
}

// Called only after successful demo payment, while orderMu serializes payment.
func recordDemoAffiliateCommission(order map[string]any) {
	code, _ := order["affiliate_code"].(string)
	if code == "" {
		return
	}
	demoAffiliateMu.Lock()
	defer demoAffiliateMu.Unlock()
	_, profile := demoAffiliateByCode(code)
	if profile == nil || profile.Profile.Status != "active" {
		return
	}
	orderID, _ := order["order_id"].(uint)
	for _, row := range profile.Commissions {
		if row.ID == orderID {
			return
		}
	}
	amount, _ := order["total_amount"].(string)
	fee, ok := moneycalc.Fee(amount, "10.00", "0.00")
	if !ok || fee == "0.00" {
		return
	}
	profile.Commissions = append([]dujiao.AffiliateCommission{{ID: orderID, OrderNo: fmt.Sprint(order["order_no"]), CommissionAmount: fee, RatePercent: "10.00", Status: "pending_confirm", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}, profile.Commissions...)
}

func demoAffiliateMethodError(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func demoAffiliateError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": "validation_error", "msg": "invalid affiliate request"})
}
