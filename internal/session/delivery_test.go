package session

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
)

func claimTXT(r *delivery.Receipt, key string) error {
	if r.Status != "" {
		return delivery.ErrBlocked
	}
	r.Status, r.Token, r.Fingerprint = "posting", rand.Text(), key
	r.NotBefore = time.Now().Add(time.Minute)
	return nil
}

func TestRedisDeliveryReceiptsRestartAndIsolation(t *testing.T) {
	r, server := redisFixture(t)
	other := connectRedis(t, "redis://"+server.Addr())
	key := delivery.Key(42, 7)
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			store := r.DeliveryReceipts()
			if i%2 != 0 {
				store = other.DeliveryReceipts()
			}
			if _, err := store.Update(context.Background(), key, func(s *delivery.Receipt) error { return claimTXT(s, key) }); err == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("non-atomic TXT claim", winners.Load())
	}
	restarted := connectRedis(t, "redis://"+server.Addr())
	if err := restarted.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	s, err := restarted.DeliveryReceipts().Update(context.Background(), key, func(*delivery.Receipt) error { return nil })
	if err != nil || s.Status != "posting" {
		t.Fatal("restart/menu lost uncertain upload")
	}
	s, err = restarted.DeliveryReceipts().Update(context.Background(), key, func(s *delivery.Receipt) error { s.Status = "sent"; s.MessageID = 91; return nil })
	if err != nil || s.MessageID != 91 {
		t.Fatal("send completion not persisted")
	}
	for _, otherKey := range []string{delivery.Key(43, 7), delivery.Key(42, 8)} {
		s, err := restarted.DeliveryReceipts().Update(context.Background(), otherKey, func(*delivery.Receipt) error { return nil })
		if err != nil || s.Status != "" {
			t.Fatal("user/order guard not isolated")
		}
	}
	for _, name := range server.Keys() {
		if strings.Contains(name, ":delivery:v1:") {
			if ttl := server.TTL(name); ttl <= 0 || ttl > delivery.Lifetime {
				t.Fatal("TXT TTL invalid")
			}
			body, _ := server.Get(name)
			if strings.Contains(body, "file_id") || strings.Contains(body, "OrderNo") {
				t.Fatal("raw delivery data stored")
			}
		}
	}
	server.FlushAll()
	if _, err := r.DeliveryReceipts().Update(context.Background(), key, func(*delivery.Receipt) error { t.Error("epoch loss ran mutation"); return nil }); !errors.Is(err, delivery.ErrStorage) {
		t.Fatal("epoch loss not closed")
	}
}

func TestRedisDeliveryCorruptionCapacityAndCASLoss(t *testing.T) {
	for _, mode := range []string{"corrupt", "capacity", "ack-before", "ack-after", "conflict", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			r, server := redisFixture(t)
			key := delivery.Key(42, 7)
			switch mode {
			case "corrupt":
				server.Set(r.prefix+":delivery:v1:"+key, `{"Version":2}`)
			case "capacity":
				for i := range delivery.Capacity {
					server.ZAdd(r.prefix+":delivery:index:v1", float64(time.Now().Add(time.Hour).UnixMilli()), string(rune(i+100)))
				}
			case "ack-before", "ack-after":
				r.client.AddHook(casFailureHook{after: mode == "ack-after", script: receiptCAS})
			case "conflict":
				r.client.AddHook(receiptInterferenceHook{func() { server.Set(r.prefix+":delivery:v1:"+key, "concurrent") }})
			case "epoch":
				r.client.AddHook(receiptInterferenceHook{server.FlushAll})
			}
			calls := 0
			_, err := r.DeliveryReceipts().Update(context.Background(), key, func(s *delivery.Receipt) error { calls++; return claimTXT(s, key) })
			if !errors.Is(err, delivery.ErrStorage) || calls > 1 {
				t.Fatal("storage error authorized/replayed mutation", err, calls)
			}
			if mode == "ack-after" {
				other := connectRedis(t, "redis://"+server.Addr())
				s, err := other.DeliveryReceipts().Update(context.Background(), key, func(*delivery.Receipt) error { return nil })
				if err != nil || s.Status != "posting" {
					t.Fatal("lost CAS acknowledgement forgot uncertain claim")
				}
			}
		})
	}
}
