package session

import (
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func TestMemoryDefaultsAndUpdates(t *testing.T) {
	store := NewMemory("zh-CN")
	if got := store.Get(42).Locale; got != "zh-CN" {
		t.Fatalf("default locale = %q", got)
	}
	store.Update(42, func(state *State) {
		state.Locale = "en-US"
		state.Flow = "invite"
	})
	got := store.Get(42)
	if got.Locale != "en-US" || got.Flow != "invite" {
		t.Fatalf("unexpected state: %#v", got)
	}
	store.ResetFlow(42)
	if got := store.Get(42).Flow; got != "" {
		t.Fatalf("flow was not reset: %q", got)
	}
}

func TestOrderLookupMetadataIsolationExpiryAndGuard(t *testing.T) {
	m := NewMemory("zh-CN")
	draft := &OrderLookupInput{ID: "search", ChatID: 42, MessageID: 10, Filter: "p", Page: 2, ExpiresAt: time.Now().Add(time.Minute)}
	returned := m.Update(42, func(s *State) {
		s.Flow = "order_lookup"
		s.OrderLookup = draft
		s.WithdrawalGuard = &WithdrawalGuard{ID: "hold", Status: "unknown"}
	})
	draft.Page, returned.OrderLookup.Page = 99, 98
	if m.Get(42).OrderLookup.Page != 2 || m.Get(43).OrderLookup != nil {
		t.Fatal("shared lookup metadata")
	}
	m.Update(42, func(s *State) { s.OrderLookup.ExpiresAt = time.Now().Add(-time.Second) })
	if s := m.Get(42); s.Flow != "" || s.OrderLookup != nil || s.WithdrawalGuard == nil {
		t.Fatal("expiry retained draft/lost guard")
	}
	m.Update(42, func(s *State) { s.Flow = "order_lookup"; s.OrderLookup = draft })
	_ = m.ResetFlow(42)
	if s := m.Get(42); s.Flow != "" || s.OrderLookup != nil || s.WithdrawalGuard == nil {
		t.Fatal("reset retained draft/lost guard")
	}
}

func TestRechargeDraftIsolationExpiryAndReset(t *testing.T) {
	store := NewMemory("zh-CN")
	draft := &RechargeDraft{ID: "recharge", ChatID: 42, MessageID: 7, Channels: []dujiao.PaymentChannel{{ID: 1}}, ExpiresAt: time.Now().Add(time.Minute)}
	returned := store.Update(42, func(state *State) { state.Flow = "recharge_channels"; state.Recharge = draft })
	draft.Channels[0].ID = 99
	returned.Recharge.Channels[0].ID = 98
	read := store.Get(42)
	read.Recharge.Channels[0].ID = 97
	if got := store.Get(42).Recharge.Channels[0].ID; got != 1 {
		t.Fatalf("recharge channel shared mutable state: %d", got)
	}
	store.Update(42, func(state *State) { state.Recharge.ExpiresAt = time.Now().Add(-time.Second) })
	if state := store.Get(42); state.Recharge != nil || state.Flow != "" {
		t.Fatal("expired recharge retained")
	}
	store.Update(42, func(state *State) { state.Recharge = draft; state.Flow = "recharge_amount" })
	store.ResetFlow(42)
	if state := store.Get(42); state.Recharge != nil || state.Flow != "" {
		t.Fatal("cancelled recharge retained")
	}
}

func TestPurchaseDraftIsolationAndExpiry(t *testing.T) {
	store := NewMemory("zh-CN")
	draft := &PurchaseDraft{ID: "draft", Values: map[string]any{"options": []string{"a"}}, ExpiresAt: time.Now().Add(time.Hour)}
	returned := store.Update(42, func(state *State) { state.Flow = "manual_form"; state.Purchase = draft })
	draft.Values["options"].([]string)[0] = "outside"
	returned.Purchase.Values["options"].([]string)[0] = "returned"
	copy := store.Get(42)
	copy.Purchase.Values["options"].([]string)[0] = "read"
	if got := store.Get(42).Purchase.Values["options"].([]string)[0]; got != "a" {
		t.Fatalf("shared draft data: %q", got)
	}
	store.Update(42, func(state *State) { state.Purchase.ExpiresAt = time.Now().Add(-time.Second) })
	if state := store.Get(42); state.Purchase != nil || state.Flow != "" {
		t.Fatalf("expired draft retained: %#v", state)
	}
}

func TestGiftCardPromptIsolationAndExpiry(t *testing.T) {
	store := NewMemory("zh-CN")
	input := &GiftCardInput{ID: "prompt", ChatID: 42, PromptID: 7, ExpiresAt: time.Now().Add(time.Minute)}
	returned := store.Update(42, func(state *State) { state.Flow = "gift_card"; state.GiftCard = input })
	input.PromptID = 99
	returned.GiftCard.PromptID = 98
	read := store.Get(42)
	read.GiftCard.PromptID = 97
	if store.Get(42).GiftCard.PromptID != 7 {
		t.Fatal("gift card prompt shared mutable state")
	}
	store.Update(42, func(state *State) { state.GiftCard.ExpiresAt = time.Now().Add(-time.Second) })
	if state := store.Get(42); state.GiftCard != nil || state.Flow != "" {
		t.Fatal("expired gift card input retained")
	}
}

func TestWithdrawalDraftIsolationAndGuardSurvivesExpiryAndNavigation(t *testing.T) {
	store := NewMemory("zh-CN")
	draft := &WithdrawalDraft{ID: "draft", Account: "private", Channels: []string{"alipay"}, ExpiresAt: time.Now().Add(time.Minute)}
	guard := &WithdrawalGuard{ID: "write", Status: "unknown"}
	returned := store.Update(42, func(state *State) { state.Flow = "withdrawal"; state.Withdrawal = draft; state.WithdrawalGuard = guard })
	draft.Channels[0] = "external"
	guard.Status = "outside"
	returned.Withdrawal.Account = "changed"
	returned.WithdrawalGuard.Status = "changed"
	read := store.Get(42)
	read.Withdrawal.Channels[0] = "read"
	read.WithdrawalGuard.Status = "read"
	state := store.Get(42)
	if state.Withdrawal.Channels[0] != "alipay" || state.Withdrawal.Account != "private" || state.WithdrawalGuard.Status != "unknown" {
		t.Fatal("withdrawal state shares mutable pointers")
	}
	store.Update(42, func(state *State) { state.Withdrawal.ExpiresAt = time.Now().Add(-time.Second) })
	if state := store.Get(42); state.Withdrawal != nil || state.Flow != "" || state.WithdrawalGuard == nil {
		t.Fatal("draft expiry lost uncertainty guard or retained account")
	}
	store.Update(42, func(state *State) { state.Withdrawal = draft; state.Flow = "withdrawal" })
	store.ResetFlow(42)
	if state := store.Get(42); state.Withdrawal != nil || state.Flow != "" || state.WithdrawalGuard == nil {
		t.Fatal("navigation discarded guard or retained sensitive draft")
	}
}
