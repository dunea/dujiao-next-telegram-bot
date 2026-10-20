package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
)

type deliveryReceipts struct{ r *Redis }

func (r *Redis) DeliveryReceipts() delivery.Store { return &deliveryReceipts{r: r} }
func (m *Memory) DeliveryReceipts() delivery.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deliveries == nil {
		m.deliveries = delivery.NewMemory()
	}
	return m.deliveries
}

// Separate namespace and schema: no changes to callback or financial receipts.
func (s *deliveryReceipts) Update(ctx context.Context, key string, fn func(*delivery.Receipt) error) (delivery.Receipt, error) {
	if !delivery.ValidKey(key) {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r := s.r
	keys := []string{r.prefix + ":epoch", r.prefix + ":delivery:v1:" + key, r.prefix + ":delivery:index:v1"}
	values, err := r.client.Eval(ctx, receiptRead, keys, r.epoch).Slice()
	if err != nil || len(values) != 1 {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	body, ok := values[0].(string)
	if !ok {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	state := delivery.Receipt{Version: 1, ExpiresAt: r.now().Add(delivery.Lifetime)}
	if body != "" && (json.Unmarshal([]byte(body), &state) != nil || !delivery.Valid(state)) {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	if !r.now().Before(state.ExpiresAt) {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	original := state
	if err := fn(&state); err != nil {
		return delivery.Receipt{}, err
	}
	if !delivery.Valid(state) {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	if body != "" && state == original {
		return state, nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	ttl := state.ExpiresAt.Sub(r.now()).Milliseconds()
	if ttl <= 0 || ttl > delivery.Lifetime.Milliseconds() {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	result, err := r.client.Eval(ctx, receiptCAS, keys, r.epoch, body, key, encoded, ttl, delivery.Capacity, delivery.Lifetime.Milliseconds()).Int()
	if err != nil || result != 1 {
		return delivery.Receipt{}, delivery.ErrStorage
	}
	return state, nil
}
