package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	moneycalc "github.com/dunea/dujiao-next-telegram-bot/internal/money"
)

var demoWithdrawalSequence atomic.Uint64
var demoSplitCommissionSequence atomic.Uint64

func demoSeedAffiliateCommission(profile *demoAffiliateProfile) {
	cents, ok := moneycalc.Parse(os.Getenv("DEMO_AFFILIATE_SEED_COMMISSION"))
	if !ok || cents <= 0 {
		return
	}
	profile.Commissions = append(profile.Commissions, dujiao.AffiliateCommission{ID: 1000000000 + profile.Profile.ID, OrderNo: "DEMO-SEED", CommissionAmount: moneycalc.Format(cents), RatePercent: "0.00", Status: "available", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
}

func demoApplyWithdrawal(w http.ResponseWriter, request *http.Request) {
	var input dujiao.AffiliateWithdrawalRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeDemoWithdrawalError(w, "validation_error")
		return
	}
	userID, err := strconv.ParseInt(input.ChannelUserID, 10, 64)
	if err != nil || userID <= 0 {
		writeDemoWithdrawalError(w, "validation_error")
		return
	}
	cents, ok := moneycalc.Parse(input.Amount)
	if !ok || cents < 1000 {
		writeDemoWithdrawalError(w, "affiliate_withdraw_amount_invalid")
		return
	}
	input.Channel, input.Account = strings.TrimSpace(input.Channel), strings.TrimSpace(input.Account)
	if !demoWithdrawalText(input.Account, 200) {
		writeDemoWithdrawalError(w, "validation_error")
		return
	}
	if input.Channel != "alipay" && input.Channel != "wechat" && input.Channel != "usdt" {
		writeDemoWithdrawalError(w, "affiliate_withdraw_channel_invalid")
		return
	}
	demoAffiliateMu.Lock()
	defer demoAffiliateMu.Unlock()
	profile := demoAffiliateProfiles[input.ChannelUserID]
	if profile == nil || profile.Profile.Status != "active" {
		writeDemoWithdrawalError(w, "affiliate_not_opened")
		return
	}
	var available int64
	for _, row := range profile.Commissions {
		if row.Status == "available" && row.WithdrawalRequestID == 0 {
			value, _ := moneycalc.Parse(row.CommissionAmount)
			available += value
		}
	}
	if cents > available {
		writeDemoWithdrawalError(w, "affiliate_withdraw_insufficient")
		return
	}
	id := uint(demoWithdrawalSequence.Add(1))
	remaining := cents
	for index := range profile.Commissions {
		row := &profile.Commissions[index]
		if remaining == 0 {
			break
		}
		if row.Status != "available" || row.WithdrawalRequestID != 0 {
			continue
		}
		value, _ := moneycalc.Parse(row.CommissionAmount)
		if value <= 0 {
			continue
		}
		bound := min(value, remaining)
		row.CommissionAmount, row.WithdrawalRequestID = moneycalc.Format(bound), id
		remaining -= bound
		if value > bound {
			rest := *row
			rest.ID = 2000000000 + uint(demoSplitCommissionSequence.Add(1))
			rest.WithdrawalRequestID, rest.CommissionAmount = 0, moneycalc.Format(value-bound)
			profile.Commissions = append(profile.Commissions, rest)
		}
	}
	result := dujiao.AffiliateWithdrawal{ID: id, Amount: moneycalc.Format(cents), Channel: input.Channel, Account: input.Account, Status: "pending_review", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	profile.Withdrawals = append([]dujiao.AffiliateWithdrawal{result}, profile.Withdrawals...)
	writeJSON(w, map[string]any{"status_code": 0, "data": result, "msg": "success"})
}

func demoWithdrawalText(value string, maxRunes int) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxRunes && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

func writeDemoWithdrawalError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": code, "msg": "withdrawal rejected"})
}
