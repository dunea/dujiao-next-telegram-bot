package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	moneycalc "github.com/dunea/dujiao-next-telegram-bot/internal/money"
)

func demoRechargeChannels() []dujiao.PaymentChannel {
	return []dujiao.PaymentChannel{
		{ID: 1, Name: "演示免手续费", ProviderType: "demo", InteractionMode: "redirect", FeeRate: "0.00", FixedFee: "0.00"},
		{ID: 2, Name: "演示用户承担手续费", ProviderType: "demo", InteractionMode: "redirect", FeeRate: "2.50", FixedFee: "1.00"},
		{ID: 3, Name: "演示商家承担手续费", ProviderType: "demo", InteractionMode: "redirect", FeeRate: "2.50", FixedFee: "1.00"},
	}
}

func demoWalletRecharge(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ChannelUserID string `json:"channel_user_id"`
		Amount        string `json:"amount"`
		ChannelID     uint   `json:"channel_id"`
	}
	err := json.NewDecoder(request.Body).Decode(&payload)
	cents, ok := moneycalc.Parse(payload.Amount)
	if err != nil || !ok || cents <= 0 || cents > 100_000_000 || strings.TrimSpace(payload.ChannelUserID) == "" || payload.ChannelID < 1 || payload.ChannelID > 3 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": "validation_error", "msg": "invalid recharge"})
		return
	}
	channel := demoRechargeChannels()[payload.ChannelID-1]
	fee, _ := moneycalc.Fee(payload.Amount, channel.FeeRate, channel.FixedFee)
	feeCents, _ := moneycalc.Parse(fee)
	payable := cents
	if payload.ChannelID == 2 {
		payable += feeCents
	}
	id := rechargeSequence.Add(1)
	balanceAfter := walletBalanceCents.Add(cents)
	recordWalletTransaction("recharge", "in", cents, balanceAfter, fmt.Sprintf("演示充值 RECHARGE%06d", id))
	result := dujiao.WalletRechargeResult{
		RechargeNo: fmt.Sprintf("RECHARGE%06d", id),
		Payment:    dujiao.WalletRechargePayment{ID: uint(id), Amount: moneycalc.Format(payable), FeeAmount: fee, Currency: "CNY", Status: "success", InteractionMode: "demo", ExpiresAt: time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)},
	}
	writeJSON(w, map[string]any{"status_code": 0, "data": result, "msg": "success", "request_id": "mock"})
}
