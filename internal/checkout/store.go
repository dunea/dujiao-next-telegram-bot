// Package checkout holds order payment/cancellation guards, never payment URLs.
package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

const Lifetime = 7 * 24 * time.Hour
const Capacity = 10000

var ErrStorage = errors.New("checkout state unavailable")
var ErrBusy = errors.New("checkout action unavailable")

type State struct {
	Version          int
	ExpiresAt        time.Time
	Selection        string
	SelectionMessage int
	SelectionUntil   time.Time
	Channels         []uint
	PaymentStatus    string // empty, posting, unknown, known, rejected
	PaymentToken     string
	PaymentID        uint
	CancelNonce      string
	CancelMessage    int
	CancelUntil      time.Time
	CancelStatus     string // empty, posting, unknown, done, rejected
}
type Store interface {
	Update(context.Context, string, func(*State) error) (State, error)
}

func Key(uid int64, oid uint) string {
	d := sha256.Sum256([]byte(fmt.Sprintf("%d:checkout:%d", uid, oid)))
	return hex.EncodeToString(d[:])
}
func ValidKey(key string) bool {
	d, e := hex.DecodeString(key)
	return e == nil && len(d) == 32 && key == hex.EncodeToString(d)
}
func Valid(s State) bool {
	if s.Version != 1 || s.ExpiresAt.IsZero() || len(s.Channels) > 50 {
		return false
	}
	if s.Selection != "" && (len(s.Selection) != 26 || s.SelectionMessage <= 0 || s.SelectionUntil.IsZero()) {
		return false
	}
	if s.CancelNonce != "" && (len(s.CancelNonce) != 26 || s.CancelMessage <= 0 || s.CancelUntil.IsZero()) {
		return false
	}
	switch s.PaymentStatus {
	case "":
		if s.PaymentToken != "" || s.PaymentID != 0 {
			return false
		}
	case "posting", "unknown", "known", "rejected":
		if len(s.PaymentToken) != 26 {
			return false
		}
	default:
		return false
	}
	switch s.CancelStatus {
	case "", "posting", "unknown", "done", "rejected":
	default:
		return false
	}
	return true
}
func Clone(s State) State { s.Channels = slices.Clone(s.Channels); return s }
func PaymentUncertain(s State) bool {
	return s.PaymentStatus == "posting" || s.PaymentStatus == "unknown"
}
func CancelUncertain(s State) bool { return s.CancelStatus == "posting" || s.CancelStatus == "unknown" }

type Memory struct {
	mu    sync.Mutex
	items map[string]State
	now   func() time.Time
}

func NewMemory() *Memory { return &Memory{items: make(map[string]State), now: time.Now} }
func (m *Memory) Update(ctx context.Context, key string, fn func(*State) error) (State, error) {
	if ctx.Err() != nil || !ValidKey(key) {
		return State{}, ErrStorage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	s, ok := m.items[key]
	if ok && !now.Before(s.ExpiresAt) {
		delete(m.items, key)
		ok = false
	}
	if !ok {
		if len(m.items) >= Capacity {
			for k, v := range m.items {
				if !now.Before(v.ExpiresAt) {
					delete(m.items, k)
				}
			}
			if len(m.items) >= Capacity {
				return State{}, ErrStorage
			}
		}
		s = State{Version: 1, ExpiresAt: now.Add(Lifetime)}
	}
	s = Clone(s)
	if err := fn(&s); err != nil {
		return State{}, err
	}
	if !Valid(s) || !now.Before(s.ExpiresAt) || s.ExpiresAt.After(now.Add(Lifetime)) {
		return State{}, ErrStorage
	}
	m.items[key] = Clone(s)
	return Clone(s), nil
}
