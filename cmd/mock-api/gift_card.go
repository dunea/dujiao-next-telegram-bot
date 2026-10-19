package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type demoGiftCard struct {
	ID          uint
	AmountCents int64
	Status      string
	Expired     bool
}

var demoGiftCardMu sync.Mutex
var demoGiftCards = map[string]demoGiftCard{
	"GIFT50":        {ID: 1, AmountCents: 5_000, Status: "active"},
	"GIFT100":       {ID: 2, AmountCents: 10_000, Status: "active"},
	"GIFT-EXPIRED":  {ID: 3, AmountCents: 1_000, Status: "active", Expired: true},
	"GIFT-DISABLED": {ID: 4, AmountCents: 1_000, Status: "disabled"},
	"GIFT-USED":     {ID: 5, AmountCents: 1_000, Status: "redeemed"},
	"GIFT-SMOKE":    {ID: 6, AmountCents: 100, Status: "active"},
}

func demoRedeemGiftCard(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		ChannelUserID string `json:"channel_user_id"`
		Code          string `json:"code"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.ChannelUserID) == "" || strings.TrimSpace(payload.Code) == "" {
		writeDemoGiftCardError(w, "gift_card_invalid", http.StatusBadRequest)
		return
	}
	result, errorCode := redeemDemoGiftCard(payload.Code)
	if errorCode != "" {
		status := http.StatusBadRequest
		if errorCode == "gift_card_not_found" {
			status = http.StatusNotFound
		}
		writeDemoGiftCardError(w, errorCode, status)
		return
	}
	writeJSON(w, map[string]any{"status_code": 0, "msg": "success", "data": result, "request_id": "mock"})
}

func redeemDemoGiftCard(code string) (dujiao.GiftCardRedemption, string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	demoGiftCardMu.Lock()
	defer demoGiftCardMu.Unlock()
	card, ok := demoGiftCards[code]
	if !ok {
		return dujiao.GiftCardRedemption{}, "gift_card_not_found"
	}
	if card.Status == "redeemed" {
		return dujiao.GiftCardRedemption{}, "gift_card_redeemed"
	}
	if card.Status == "disabled" {
		return dujiao.GiftCardRedemption{}, "gift_card_disabled"
	}
	if card.Expired {
		return dujiao.GiftCardRedemption{}, "gift_card_expired"
	}
	if card.Status != "active" || card.AmountCents <= 0 {
		return dujiao.GiftCardRedemption{}, "gift_card_invalid"
	}
	card.Status = "redeemed"
	demoGiftCards[code] = card
	balanceAfter := walletBalanceCents.Add(card.AmountCents)
	remark := "演示礼品卡兑换：" + code
	recordWalletTransaction("gift_card_redeem", "in", card.AmountCents, balanceAfter, remark)
	amount, balance := money(int(card.AmountCents)), money(int(balanceAfter))
	return dujiao.GiftCardRedemption{
		GiftCard: dujiao.GiftCard{ID: card.ID, Name: "演示礼品卡", Code: code, Amount: amount, Currency: "CNY", Status: "redeemed"},
		Wallet:   dujiao.Wallet{Balance: balance}, WalletDelta: amount,
		Transaction: dujiao.WalletTransaction{Type: "gift_card_redeem", Direction: "in", Amount: amount, BalanceAfter: balance, Remark: remark, CreatedAt: time.Now().UTC().Format(time.RFC3339)},
	}, ""
}

func writeDemoGiftCardError(w http.ResponseWriter, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": status, "msg": "gift card redemption failed", "error_code": code, "request_id": "mock"})
}
