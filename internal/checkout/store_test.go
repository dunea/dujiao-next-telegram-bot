package checkout

import (
	"context"
	"crypto/rand"
	"testing"
	"time"
)

func TestMemoryCheckoutGuardsSurviveSelectionExpiryAndClone(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	m.now = func() time.Time { return now }
	key := Key(42, 7)
	ctx := context.Background()
	s, e := m.Update(ctx, key, func(s *State) error {
		s.Selection = rand.Text()
		s.SelectionMessage = 10
		s.SelectionUntil = now.Add(time.Minute)
		s.Channels = []uint{12}
		s.PaymentStatus, s.PaymentToken = "unknown", rand.Text()
		s.CancelStatus = "unknown"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s.Channels[0] = 99
	now = now.Add(time.Hour)
	s, e = m.Update(ctx, key, func(*State) error { return nil })
	if e != nil || !PaymentUncertain(s) || !CancelUncertain(s) || s.Channels[0] != 12 {
		t.Fatal("UI expiry or alias lost protection")
	}
	if _, e := m.Update(ctx, key, func(s *State) error { s.PaymentStatus = "invalid"; return nil }); e == nil {
		t.Fatal("bad state accepted")
	}
	for i := range Capacity {
		m.items[Key(43, uint(i+1))] = State{Version: 1, ExpiresAt: now.Add(Lifetime)}
	}
	if _, e := m.Update(ctx, Key(44, 1), func(*State) error { return nil }); e == nil {
		t.Fatal("live guard evicted")
	}
	now = now.Add(2 * Lifetime)
	s, e = m.Update(ctx, key, func(*State) error { return nil })
	if e != nil || s.PaymentStatus != "" {
		t.Fatal("expired guard never reclaimed")
	}
}
