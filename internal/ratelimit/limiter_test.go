package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryBurstRefillAndIsolation(t *testing.T) {
	m := NewMemory(10)
	now := time.Unix(1700000000, 0)
	m.now = func() time.Time { return now }
	rule := Rule{Limit: 3, Window: 6 * time.Second}
	for range 3 {
		if d, err := m.Allow(context.Background(), "user:42", rule); err != nil || !d.Allowed {
			t.Fatal(d, err)
		}
	}
	if d, err := m.Allow(context.Background(), "user:42", rule); err != nil || d.Allowed || d.RetryAfter != 2*time.Second {
		t.Fatal(d, err)
	}
	if d, _ := m.Allow(context.Background(), "user:43", rule); !d.Allowed {
		t.Fatal("cross-user denial")
	}
	now = now.Add(time.Second)
	if d, _ := m.Allow(context.Background(), "user:42", rule); d.Allowed || d.RetryAfter != time.Second {
		t.Fatal("denial extended window", d)
	}
	now = now.Add(time.Second)
	if d, _ := m.Allow(context.Background(), "user:42", rule); !d.Allowed {
		t.Fatal("token did not refill")
	}
}

func TestMemoryConcurrentRequestsAndCapacity(t *testing.T) {
	m := NewMemory(2)
	now := time.Now()
	m.now = func() time.Time { return now }
	var allowed atomic.Int32
	var wait sync.WaitGroup
	for range 100 {
		wait.Go(func() {
			d, _ := m.Allow(context.Background(), "one", Rule{Limit: 10, Window: time.Minute})
			if d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wait.Wait()
	if allowed.Load() != 10 {
		t.Fatal("concurrent burst", allowed.Load())
	}
	_, _ = m.Allow(context.Background(), "two", Rule{Limit: 1, Window: time.Minute})
	if d, _ := m.Allow(context.Background(), "three", Rule{Limit: 1, Window: time.Minute}); d.Allowed || len(m.buckets) != 2 {
		t.Fatal("capacity limit bypass")
	}
	now = now.Add(2 * time.Minute)
	if d, _ := m.Allow(context.Background(), "three", Rule{Limit: 1, Window: time.Minute}); !d.Allowed {
		t.Fatal("expired buckets not reclaimed")
	}
}

func TestInvalidRulesAndCancelledContextFailClosed(t *testing.T) {
	m := NewMemory(1)
	for _, rule := range []Rule{{}, {Limit: -1, Window: time.Minute}, {Limit: 1, Window: time.Millisecond}, {Limit: 100001, Window: time.Minute}} {
		if d, err := m.Allow(context.Background(), "one", rule); err == nil || d.Allowed {
			t.Fatal("invalid rule accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d, err := m.Allow(ctx, "one", Rule{Limit: 1, Window: time.Minute}); err == nil || d.Allowed {
		t.Fatal("cancelled limit request allowed")
	}
}
