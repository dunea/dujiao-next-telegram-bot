package session

import "testing"

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
