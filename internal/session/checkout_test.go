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

	"github.com/dunea/dujiao-next-telegram-bot/internal/checkout"
)

func TestRedisCheckoutClaimsAndRestart(t *testing.T) {
	r, server := redisFixture(t)
	other := connectRedis(t, "redis://"+server.Addr())
	key := checkout.Key(42, 7)
	ctx := context.Background()
	_, e := r.CheckoutStore().Update(ctx, key, func(s *checkout.State) error {
		s.Selection = rand.Text()
		s.SelectionMessage = 10
		s.SelectionUntil = time.Now().Add(time.Minute)
		s.Channels = []uint{12}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			store := r.CheckoutStore()
			if i%2 != 0 {
				store = other.CheckoutStore()
			}
			_, e := store.Update(ctx, key, func(s *checkout.State) error {
				if s.Selection == "" {
					return checkout.ErrBusy
				}
				s.Selection = ""
				s.PaymentStatus, s.PaymentToken = "posting", rand.Text()
				return nil
			})
			if e == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("checkout claim not atomic", winners.Load())
	}
	restarted := connectRedis(t, "redis://"+server.Addr())
	if e := restarted.ResetFlow(42); e != nil {
		t.Fatal(e)
	}
	s, e := restarted.CheckoutStore().Update(ctx, key, func(*checkout.State) error { return nil })
	if e != nil || !checkout.PaymentUncertain(s) {
		t.Fatal("restart/menu lost uncertain payment")
	}
	s.Channels[0] = 99
	s, _ = restarted.CheckoutStore().Update(ctx, key, func(*checkout.State) error { return nil })
	if s.Channels[0] != 12 {
		t.Fatal("state alias")
	}
	for _, name := range server.Keys() {
		if strings.Contains(name, ":checkout:v1:") {
			body, _ := server.Get(name)
			if strings.Contains(body, "pay_url") || strings.Contains(body, "channel_user_id") {
				t.Fatal("payment payload persisted")
			}
			if ttl := server.TTL(name); ttl <= 0 || ttl > checkout.Lifetime {
				t.Fatal("guard TTL invalid")
			}
		}
	}
	server.FlushAll()
	if _, e := restarted.CheckoutStore().Update(ctx, key, func(*checkout.State) error { t.Error("epoch loss ran callback"); return nil }); !errors.Is(e, checkout.ErrStorage) {
		t.Fatal("epoch loss not closed")
	}
}
func TestRedisCheckoutFailuresNeverAuthorizeWrites(t *testing.T) {
	for _, mode := range []string{"read", "corrupt", "ack-before", "ack-after", "conflict", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			r, server := redisFixture(t)
			key := checkout.Key(42, 7)
			switch mode {
			case "read":
				server.SetError("ERR PRIVATE")
			case "corrupt":
				server.Set(r.prefix+":checkout:v1:"+key, `{"Version":2}`)
			case "ack-before", "ack-after":
				r.client.AddHook(casFailureHook{after: mode == "ack-after", script: receiptCAS})
			case "conflict":
				r.client.AddHook(receiptInterferenceHook{func() { server.Set(r.prefix+":checkout:v1:"+key, "concurrent") }})
			case "capacity":
				for i := range checkout.Capacity {
					server.ZAdd(r.prefix+":checkout:index:v1", float64(time.Now().Add(time.Hour).UnixMilli()), string(rune(i+100)))
				}
			}
			calls := 0
			_, e := r.CheckoutStore().Update(context.Background(), key, func(s *checkout.State) error {
				calls++
				s.PaymentStatus, s.PaymentToken = "posting", rand.Text()
				return nil
			})
			if !errors.Is(e, checkout.ErrStorage) || calls > 1 {
				t.Fatal("failure authorized/replayed action")
			}
			if mode == "ack-after" {
				other := connectRedis(t, "redis://"+server.Addr())
				s, e := other.CheckoutStore().Update(context.Background(), key, func(*checkout.State) error { return nil })
				if e != nil || !checkout.PaymentUncertain(s) {
					t.Fatal("lost write acknowledgement lost guard")
				}
			}
		})
	}
}
