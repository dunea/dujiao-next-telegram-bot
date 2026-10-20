package callback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const ReceiptLifetime = 7 * 24 * time.Hour
const DeliveryLease = 2 * time.Minute
const ReceiptCapacity = 10000

var ErrStorage = errors.New("callback receipt storage unavailable")
var ErrBusy = errors.New("callback delivery already in progress")
var ErrChanged = errors.New("callback payload changed")

// StorageFailure preserves only a fixed diagnostic category, never a Redis
// error, key, payload or credential. It remains compatible with ErrStorage.
type StorageFailure string

const (
	StorageRead     StorageFailure = "read_failed"
	StorageEpoch    StorageFailure = "epoch_mismatch"
	StorageInvalid  StorageFailure = "invalid_receipt"
	StorageExpired  StorageFailure = "expired_receipt"
	StorageWrite    StorageFailure = "write_failed"
	StorageConflict StorageFailure = "cas_conflict"
	StorageCapacity StorageFailure = "capacity_exhausted"
)

func (StorageFailure) Error() string { return ErrStorage.Error() }
func (StorageFailure) Unwrap() error { return ErrStorage }

func storageReason(err error) string {
	var failure StorageFailure
	if errors.As(err, &failure) {
		switch failure {
		case StorageRead, StorageEpoch, StorageInvalid, StorageExpired, StorageWrite, StorageConflict, StorageCapacity:
			return string(failure)
		}
	}
	return "unknown"
}

// Only hashes and delivery metadata are persisted, never amounts, receiving
// details or notification text. Keep this separate from UI/financial guards.
type Receipt struct {
	Version   int
	ExpiresAt time.Time
	Events    map[string]Delivery
}
type Delivery struct {
	Fingerprint string
	Token       string
	Status      string // posting, retry, sent, discarded
	NotBefore   time.Time
}
type Receipts interface {
	Update(context.Context, string, func(*Receipt) error) (Receipt, error)
}

func ReceiptKey(uid int64, event Event) string {
	resource := fmt.Sprintf("order:%d", event.OrderID)
	if event.RechargeNo != "" {
		resource = "recharge:" + event.RechargeNo
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", uid, resource)))
	return hex.EncodeToString(digest[:])
}
func fingerprint(event Event) string {
	data, _ := json.Marshal(event)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func ValidReceiptKey(key string) bool {
	data, err := hex.DecodeString(key)
	return err == nil && len(data) == 32 && key == hex.EncodeToString(data)
}
func ValidReceipt(r Receipt) bool {
	if r.Version != 1 || r.ExpiresAt.IsZero() || r.Events == nil || len(r.Events) > 3 {
		return false
	}
	for kind, d := range r.Events {
		if kind != "order-paid" && kind != "order-fulfilled" && kind != "wallet-recharge-succeeded" {
			return false
		}
		if !ValidReceiptKey(d.Fingerprint) || len(d.Token) != 26 {
			return false
		}
		switch d.Status {
		case "posting", "retry", "sent", "discarded":
		default:
			return false
		}
	}
	return true
}
func CloneReceipt(r Receipt) Receipt {
	copy := r
	copy.Events = make(map[string]Delivery, len(r.Events))
	for k, v := range r.Events {
		copy.Events[k] = v
	}
	return copy
}

type MemoryReceipts struct {
	mu    sync.Mutex
	items map[string]Receipt
	now   func() time.Time
}

func NewMemoryReceipts() *MemoryReceipts {
	return &MemoryReceipts{items: make(map[string]Receipt), now: time.Now}
}
func (m *MemoryReceipts) Update(ctx context.Context, key string, fn func(*Receipt) error) (Receipt, error) {
	if ctx.Err() != nil || !ValidReceiptKey(key) {
		return Receipt{}, ErrStorage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	r, exists := m.items[key]
	if exists && !now.Before(r.ExpiresAt) {
		delete(m.items, key)
		exists = false
	}
	if !exists {
		if len(m.items) >= ReceiptCapacity {
			for k, v := range m.items {
				if !now.Before(v.ExpiresAt) {
					delete(m.items, k)
				}
			}
			if len(m.items) >= ReceiptCapacity {
				return Receipt{}, ErrStorage
			}
		}
		r = Receipt{Version: 1, ExpiresAt: now.Add(ReceiptLifetime), Events: make(map[string]Delivery)}
	}
	r = CloneReceipt(r)
	if err := fn(&r); err != nil {
		return Receipt{}, err
	}
	if !ValidReceipt(r) {
		return Receipt{}, ErrStorage
	}
	m.items[key] = CloneReceipt(r)
	return CloneReceipt(r), nil
}

// NotifyError contains no SDK/body text. 429 may request a bounded delay;
// permanent blocked-chat responses can be acknowledged without futile retries.
type NotifyError struct {
	RetryAfter time.Duration
	Permanent  bool
}

func (*NotifyError) Error() string { return "callback notification failed" }
