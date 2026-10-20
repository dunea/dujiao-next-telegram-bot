package session

import (
	"testing"
	"time"
)

func TestQuantitySelectionCloneResetAndExpiryPreserveGuards(t *testing.T) {
	store := NewMemory("zh-CN")
	q := &QuantityInput{ID: "selector", ChatID: 42, ProductID: 3, SKUID: 31, Quantity: 2500, ExpiresAt: time.Now().Add(time.Minute)}
	got := store.Update(42, func(s *State) {
		s.Quantity = q
		s.Flow = "purchase_quantity"
		s.WithdrawalGuard = &WithdrawalGuard{ID: "pending", Status: "unknown"}
	})
	q.Quantity = 1
	got.Quantity.Quantity = 2
	if store.Get(42).Quantity.Quantity != 2500 {
		t.Fatal("quantity draft aliases caller")
	}
	store.Update(42, func(s *State) { s.Quantity.ExpiresAt = time.Now().Add(-time.Second) })
	if got := store.Get(42); got.Quantity != nil || got.Flow != "" || got.WithdrawalGuard == nil {
		t.Fatal("quantity expiry broke flow/financial guard")
	}
	store.Update(42, func(s *State) {
		s.Quantity = &QuantityInput{ExpiresAt: time.Now().Add(time.Minute)}
		s.Flow = "purchase_quantity"
	})
	if err := store.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	if got := store.Get(42); got.Quantity != nil || got.WithdrawalGuard == nil {
		t.Fatal("quantity navigation erased protection")
	}
}

func TestRedisQuantityOnlySelectionHasBoundedEncryptedTTL(t *testing.T) {
	r, server := redisFixture(t)
	result := r.Update(42, func(s *State) {
		s.Quantity = &QuantityInput{ID: "selector", ChatID: 42, MessageID: 10, PromptID: 11, Quantity: 2500, ExpiresAt: time.Now().Add(10 * time.Minute)}
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if ttl := server.TTL(r.keys(42)[2]); ttl <= 0 || ttl > 10*time.Minute {
		t.Fatal("quantity-only card was not persisted with bounded TTL")
	}
	fresh := connectRedis(t, "redis://"+server.Addr())
	if q := fresh.Get(42).Quantity; q == nil || q.Quantity != 2500 || q.PromptID != 11 {
		t.Fatal("quantity-only state lost across clients")
	}
	server.FastForward(11 * time.Minute)
	if q := fresh.Get(42).Quantity; q != nil {
		t.Fatal("expired quantity card survived Redis TTL")
	}
}

func TestQuantityEditExpiryDoesNotShortenExistingPurchaseLifetime(t *testing.T) {
	r, server := redisFixture(t)
	now := time.Now()
	result := r.Update(42, func(s *State) {
		s.Flow = "purchase_quantity"
		s.Purchase = &PurchaseDraft{ID: "checkout", ExpiresAt: now.Add(30 * time.Minute)}
		s.Quantity = &QuantityInput{ID: "input", PurchaseID: "checkout", ExpiresAt: now.Add(10 * time.Minute)}
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if ttl := server.TTL(r.keys(42)[2]); ttl <= 29*time.Minute || ttl > 30*time.Minute {
		t.Fatal("quantity edit discarded existing purchase details early")
	}
	r.now = func() time.Time { return now.Add(11 * time.Minute) }
	got := r.Get(42)
	if got.Err != nil || got.Quantity != nil || got.Flow != "" || got.Purchase == nil {
		t.Fatal("input expiry did not preserve original checkout")
	}
}
