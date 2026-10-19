// Package ratelimit implements bounded GCRA buckets. Denials do not extend the
// bucket lifetime, and keys are chosen by the application, never raw user input.
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("rate limiter unavailable")

type Rule struct {
	Limit  int
	Window time.Duration
}
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}
type Limiter interface {
	Allow(context.Context, string, Rule) (Decision, error)
}

type Policy struct {
	User, Write, Withdrawal, Callback, HTTP, Global Rule
}

func DefaultPolicy() Policy {
	return Policy{User: Rule{60, time.Minute}, Write: Rule{10, time.Minute}, Withdrawal: Rule{3, 10 * time.Minute}, Callback: Rule{120, time.Minute}, HTTP: Rule{3000, time.Minute}, Global: Rule{3000, time.Minute}}
}

func (r Rule) Valid() bool {
	return r.Limit > 0 && r.Limit <= 100000 && r.Window >= time.Second && r.Window <= time.Hour
}

type Memory struct {
	mu       sync.Mutex
	buckets  map[string]time.Time
	capacity int
	now      func() time.Time
}

func NewMemory(capacity int) *Memory {
	if capacity < 1 {
		capacity = 10000
	}
	return &Memory{buckets: make(map[string]time.Time), capacity: capacity, now: time.Now}
}

func (m *Memory) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	if ctx.Err() != nil || !rule.Valid() || len(key) == 0 || len(key) > 160 {
		return Decision{}, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	tat, exists := m.buckets[key]
	if !exists && len(m.buckets) >= m.capacity {
		for key, expiry := range m.buckets {
			if !now.Before(expiry) {
				delete(m.buckets, key)
			}
		}
		if len(m.buckets) >= m.capacity {
			return Decision{RetryAfter: rule.Window}, nil
		}
	}
	interval := (rule.Window + time.Duration(rule.Limit) - 1) / time.Duration(rule.Limit)
	allowAt := tat.Add(-rule.Window + interval)
	if now.Before(allowAt) {
		return Decision{RetryAfter: allowAt.Sub(now)}, nil
	}
	if tat.Before(now) {
		tat = now
	}
	m.buckets[key] = tat.Add(interval)
	return Decision{Allowed: true}, nil
}
