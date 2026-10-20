package delivery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMemoryReceiptsExpiryCapacityAndValidation(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	m.now = func() time.Time { return now }
	ctx := context.Background()
	key := Key(42, 7)
	_, err := m.Update(ctx, key, func(r *Receipt) error {
		r.Status, r.Token, r.Fingerprint = "posting", rand.Text(), key
		r.NotBefore = now.Add(time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Update(ctx, key, func(r *Receipt) error { r.Status = "invalid"; return nil })
	if !errors.Is(err, ErrStorage) {
		t.Fatal("invalid status persisted")
	}
	r, _ := m.Update(ctx, key, func(*Receipt) error { return nil })
	if r.Status != "posting" {
		t.Fatal("failed mutation damaged guard")
	}
	for i := range Capacity {
		digest := Key(43, uint(i+1))
		m.items[digest] = Receipt{Version: 1, ExpiresAt: now.Add(Lifetime)}
	}
	if _, err = m.Update(ctx, Key(44, 1), func(*Receipt) error { return nil }); !errors.Is(err, ErrStorage) {
		t.Fatal("live guards evicted at capacity")
	}
	now = now.Add(Lifetime + time.Second)
	r, err = m.Update(ctx, key, func(*Receipt) error { return nil })
	if err != nil || r.Status != "" {
		t.Fatal("expired guard not reclaimed")
	}
	if _, err = m.Update(ctx, "invalid", func(*Receipt) error { t.Error("invalid key ran callback"); return nil }); err == nil {
		t.Fatal("invalid key accepted")
	}
	for i, status := range []string{"posting", "retry", "sent"} {
		r := Receipt{Version: 1, ExpiresAt: now.Add(Lifetime), Status: status, Token: rand.Text(), Fingerprint: key, NotBefore: now}
		if status == "sent" {
			r.MessageID = 91
		}
		if !Valid(r) {
			t.Fatal(fmt.Sprintf("invalid legitimate status %d", i))
		}
	}
}
