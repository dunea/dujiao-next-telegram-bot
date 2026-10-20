// Package delivery stores TXT send guards, never card contents or Telegram file IDs.
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

const Lifetime = 7 * 24 * time.Hour
const Capacity = 10000

var ErrStorage = errors.New("delivery receipt unavailable")
var ErrBlocked = errors.New("delivery confirmation required")

// Posting is intentionally NOT a reclaimable lease. An interrupted upload may
// already have reached Telegram. Only an explicit, token-bound resend can retry.
type Receipt struct {
	Version     int
	ExpiresAt   time.Time
	Status      string // empty, posting, sent, retry
	Token       string
	Fingerprint string
	NotBefore   time.Time
	MessageID   int
}

type Store interface {
	Update(context.Context, string, func(*Receipt) error) (Receipt, error)
}

func Key(uid int64, orderID uint) string {
	d := sha256.Sum256([]byte(fmt.Sprintf("%d:txt:%d", uid, orderID)))
	return hex.EncodeToString(d[:])
}

func ValidKey(key string) bool {
	b, err := hex.DecodeString(key)
	return err == nil && len(b) == 32 && key == hex.EncodeToString(b)
}

func Valid(r Receipt) bool {
	if r.Version != 1 || r.ExpiresAt.IsZero() {
		return false
	}
	if r.Status == "" {
		return r.Token == "" && r.Fingerprint == "" && r.MessageID == 0
	}
	if len(r.Token) != 26 || !ValidKey(r.Fingerprint) || r.NotBefore.IsZero() {
		return false
	}
	switch r.Status {
	case "posting", "retry":
		return r.MessageID == 0
	case "sent":
		return r.MessageID > 0
	}
	return false
}

type Memory struct {
	mu    sync.Mutex
	items map[string]Receipt
	now   func() time.Time
}

func NewMemory() *Memory { return &Memory{items: make(map[string]Receipt), now: time.Now} }

func (m *Memory) Update(ctx context.Context, key string, fn func(*Receipt) error) (Receipt, error) {
	if ctx.Err() != nil || !ValidKey(key) {
		return Receipt{}, ErrStorage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	r, ok := m.items[key]
	if ok && !now.Before(r.ExpiresAt) {
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
				return Receipt{}, ErrStorage
			}
		}
		r = Receipt{Version: 1, ExpiresAt: now.Add(Lifetime)}
	}
	if err := fn(&r); err != nil {
		return Receipt{}, err
	}
	if !Valid(r) || r.ExpiresAt.After(now.Add(Lifetime)) {
		return Receipt{}, ErrStorage
	}
	m.items[key] = r
	return r, nil
}
