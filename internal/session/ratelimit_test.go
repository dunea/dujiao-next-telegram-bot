package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

func TestRedisRateLimitSurvivesClientRestartAndRefills(t *testing.T) {
	r, server := redisFixture(t)
	now := time.Unix(1700000000, 0)
	server.SetTime(now)
	rule := ratelimit.Rule{Limit: 2, Window: time.Minute}
	for range 2 {
		if d, err := r.Allow(context.Background(), "write:42", rule); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
	}
	restarted := connectRedis(t, "redis://"+server.Addr())
	if d, err := restarted.Allow(context.Background(), "write:42", rule); err != nil || d.Allowed || d.RetryAfter != 30*time.Second {
		t.Fatal("restart reset bucket", d, err)
	}
	if d, _ := restarted.Allow(context.Background(), "write:43", rule); !d.Allowed {
		t.Fatal("cross-user rate leak")
	}
	server.SetTime(now.Add(30 * time.Second))
	if d, err := restarted.Allow(context.Background(), "write:42", rule); err != nil || !d.Allowed {
		t.Fatal("token did not refill", d, err)
	}
	server.FastForward(2 * time.Minute)
	if d, err := restarted.Allow(context.Background(), "write:42", rule); err != nil || !d.Allowed {
		t.Fatal("TTL did not reclaim bucket", d, err)
	}
}

func TestRedisRateLimitAtomicAcrossClients(t *testing.T) {
	r, server := redisFixture(t)
	server.SetTime(time.Now())
	other := connectRedis(t, "redis://"+server.Addr())
	var allowed atomic.Int32
	var wait sync.WaitGroup
	for i := range 50 {
		store := r
		if i%2 == 0 {
			store = other
		}
		wait.Go(func() {
			d, err := store.Allow(context.Background(), "withdrawal:42", ratelimit.Rule{Limit: 3, Window: 10 * time.Minute})
			if err != nil {
				t.Error(err)
			}
			if d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wait.Wait()
	if allowed.Load() != 3 {
		t.Fatal("distributed limit winners", allowed.Load())
	}
}

func TestRedisRateLimitOutageAndDatasetLossNeverFallback(t *testing.T) {
	r, server := redisFixture(t)
	rule := ratelimit.Rule{Limit: 2, Window: time.Minute}
	server.SetError("ERR secret=private")
	if d, err := r.Allow(context.Background(), "write:42", rule); err == nil || d.Allowed {
		t.Fatal("outage allowed operation")
	}
	server.SetError("")
	server.FlushAll()
	if d, err := r.Allow(context.Background(), "write:42", rule); err == nil || d.Allowed {
		t.Fatal("data loss allowed operation")
	}
}
