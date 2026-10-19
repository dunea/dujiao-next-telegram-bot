package session

import (
	"testing"
	"time"
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
