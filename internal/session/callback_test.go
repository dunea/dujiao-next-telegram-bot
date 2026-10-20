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

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/redis/go-redis/v9"
)

func TestRedisCallbackReceiptsAcrossClientsAndStorageFailure(t *testing.T) {
	r, server := redisFixture(t)
	other := connectRedis(t, "redis://"+server.Addr())
	ctx := context.Background()
	event := callback.Event{OrderID: 7, TelegramUserID: "42"}
	key := callback.ReceiptKey(42, event)
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			store := r.CallbackReceipts()
			if i%2 != 0 {
				store = other.CallbackReceipts()
			}
			claimed := false
			_, err := store.Update(ctx, key, func(s *callback.Receipt) error {
				if _, ok := s.Events["order-paid"]; ok {
					return callback.ErrBusy
				}
				s.Events["order-paid"] = callback.Delivery{Fingerprint: key, Token: rand.Text(), Status: "posting", NotBefore: time.Now().Add(callback.DeliveryLease)}
				claimed = true
				return nil
			})
			if err == nil && claimed {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("cross-client callback claim was not atomic", winners.Load())
	}
	restarted := connectRedis(t, "redis://"+server.Addr())
	state, err := restarted.CallbackReceipts().Update(ctx, key, func(*callback.Receipt) error { return nil })
	if err != nil || state.Events["order-paid"].Status != "posting" {
		t.Fatal("restart lost in-flight notification")
	}
	if err := r.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	state, err = restarted.CallbackReceipts().Update(ctx, key, func(s *callback.Receipt) error {
		d := s.Events["order-paid"]
		d.Status = "sent"
		s.Events["order-paid"] = d
		return nil
	})
	if err != nil || state.Events["order-paid"].Status != "sent" {
		t.Fatal("completion failed", err)
	}
	for _, name := range server.Keys() {
		if strings.Contains(name, ":callback:v1:") {
			if ttl := server.TTL(name); ttl <= 0 || ttl > callback.ReceiptLifetime {
				t.Fatal("receipt TTL missing")
			}
			value, _ := server.Get(name)
			if strings.Contains(value, "telegram_user_id") || strings.Contains(value, "order_id") {
				t.Fatal("raw callback persisted")
			}
		}
	}
	server.SetError("ERR PRIVATE_SECRET")
	if _, err := restarted.CallbackReceipts().Update(ctx, key, func(*callback.Receipt) error { t.Error("storage failure ran mutation"); return nil }); !errors.Is(err, callback.ErrStorage) {
		t.Fatal("storage failure not closed")
	}
	server.SetError("")
	server.FlushAll()
	if _, err := restarted.CallbackReceipts().Update(ctx, key, func(*callback.Receipt) error { return nil }); !errors.Is(err, callback.ErrStorage) {
		t.Fatal("epoch loss was accepted")
	}
}

func TestRedisCallbackReceiptRejectsCorruptAndUnknownFormat(t *testing.T) {
	for _, body := range []string{`{`, `{"Version":2}`, `{"Version":1,"ExpiresAt":"2099-01-01T00:00:00Z","Events":null}`} {
		t.Run(body, func(t *testing.T) {
			r, server := redisFixture(t)
			key := callback.ReceiptKey(42, callback.Event{OrderID: 7})
			server.Set(r.prefix+":callback:v1:"+key, body)
			if _, err := r.CallbackReceipts().Update(context.Background(), key, func(*callback.Receipt) error { t.Error("corrupt receipt became empty"); return nil }); err == nil {
				t.Fatal("bad receipt accepted")
			}
		})
	}
}

func TestRedisCallbackReceiptExpiredAndCapacity(t *testing.T) {
	r, server := redisFixture(t)
	ctx := context.Background()
	key := callback.ReceiptKey(42, callback.Event{OrderID: 7})
	_, err := r.CallbackReceipts().Update(ctx, key, func(s *callback.Receipt) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	server.FastForward(callback.ReceiptLifetime + time.Second)
	if _, err := r.CallbackReceipts().Update(ctx, key, func(s *callback.Receipt) error {
		if len(s.Events) != 0 {
			t.Error("expired receipt retained")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A full live registry must reject a new identity, not evict replay guards.
	index := r.prefix + ":callback:index:v1"
	for i := range callback.ReceiptCapacity {
		server.ZAdd(index, float64(time.Now().Add(time.Hour).UnixMilli()), string(rune(i+100)))
	}
	newKey := callback.ReceiptKey(43, callback.Event{OrderID: 8})
	if _, err := r.CallbackReceipts().Update(ctx, newKey, func(*callback.Receipt) error { return nil }); !errors.Is(err, callback.StorageCapacity) {
		t.Fatal("capacity did not fail closed")
	}
}

func TestCallbackCASAcknowledgementLossDoesNotAuthorizeSend(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(strconvBool(after), func(t *testing.T) {
			r, server := redisFixture(t)
			r.client.AddHook(casFailureHook{after: after, script: receiptCAS})
			key := callback.ReceiptKey(42, callback.Event{OrderID: 7})
			called := 0
			_, err := r.CallbackReceipts().Update(context.Background(), key, func(s *callback.Receipt) error {
				called++
				s.Events["order-paid"] = callback.Delivery{Fingerprint: key, Token: rand.Text(), Status: "posting", NotBefore: time.Now().Add(callback.DeliveryLease)}
				return nil
			})
			if !errors.Is(err, callback.StorageWrite) || called != 1 {
				t.Fatal("receipt CAS failure returned success/replayed closure")
			}
			other := connectRedis(t, "redis://"+server.Addr())
			receipt, err := other.CallbackReceipts().Update(context.Background(), key, func(*callback.Receipt) error { return nil })
			if err != nil || (receipt.Events["order-paid"].Status == "posting") != after {
				t.Fatal("receipt commit ambiguity not preserved")
			}
		})
	}
}

// Interfere after the read but before the one-shot CAS. Unlike a transport
// error, these receive a definite rejection from Redis; neither may replay fn.
type receiptInterferenceHook struct{ beforeCAS func() }

func (h receiptInterferenceHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h receiptInterferenceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h receiptInterferenceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "eval" && cmd.Args()[1] == receiptCAS {
			h.beforeCAS()
		}
		return next(ctx, cmd)
	}
}

func TestRedisReceiptDiagnosticCategories(t *testing.T) {
	for _, mode := range []string{"read", "epoch", "corrupt", "expired", "conflict", "epoch during CAS"} {
		t.Run(mode, func(t *testing.T) {
			r, server := redisFixture(t)
			ctx := context.Background()
			key := callback.ReceiptKey(42, callback.Event{OrderID: 7})
			_, err := r.CallbackReceipts().Update(ctx, key, func(*callback.Receipt) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			want := callback.StorageRead
			switch mode {
			case "read":
				server.SetError("ERR PRIVATE_CONNECTION_DETAILS")
			case "epoch":
				server.FlushAll()
				want = callback.StorageEpoch
			case "corrupt":
				server.Set(r.prefix+":callback:v1:"+key, "PRIVATE_CORRUPT_PAYLOAD")
				want = callback.StorageInvalid
			case "expired":
				now := time.Now().Add(callback.ReceiptLifetime + time.Hour)
				r.now = func() time.Time { return now }
				want = callback.StorageExpired
			case "conflict":
				r.client.AddHook(receiptInterferenceHook{func() { server.Set(r.prefix+":callback:v1:"+key, "concurrent mutation") }})
				want = callback.StorageConflict
			case "epoch during CAS":
				r.client.AddHook(receiptInterferenceHook{server.FlushAll})
				want = callback.StorageEpoch
			}
			called := 0
			_, err = r.CallbackReceipts().Update(ctx, key, func(s *callback.Receipt) error {
				called++
				s.Events["order-paid"] = callback.Delivery{Fingerprint: key, Token: rand.Text(), Status: "posting", NotBefore: time.Now().Add(callback.DeliveryLease)}
				return nil
			})
			if !errors.Is(err, want) || !errors.Is(err, callback.ErrStorage) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unexpected diagnostic for %s", mode)
			}
			if called > 1 || (mode != "conflict" && mode != "epoch during CAS" && called != 0) {
				t.Fatal("storage failure ran or replayed mutation unexpectedly")
			}
		})
	}
}
