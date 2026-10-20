package session

import (
	"context"
	"encoding/json"
	"github.com/dunea/dujiao-next-telegram-bot/internal/checkout"
	"reflect"
	"time"
)

type checkoutStore struct{ r *Redis }

func (r *Redis) CheckoutStore() checkout.Store { return &checkoutStore{r: r} }
func (m *Memory) CheckoutStore() checkout.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checkout == nil {
		m.checkout = checkout.NewMemory()
	}
	return m.checkout
}
func (s *checkoutStore) Update(ctx context.Context, key string, fn func(*checkout.State) error) (checkout.State, error) {
	if !checkout.ValidKey(key) {
		return checkout.State{}, checkout.ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r := s.r
	keys := []string{r.prefix + ":epoch", r.prefix + ":checkout:v1:" + key, r.prefix + ":checkout:index:v1"}
	values, err := r.client.Eval(ctx, receiptRead, keys, r.epoch).Slice()
	if err != nil || len(values) != 1 {
		return checkout.State{}, checkout.ErrStorage
	}
	body, ok := values[0].(string)
	if !ok {
		return checkout.State{}, checkout.ErrStorage
	}
	state := checkout.State{Version: 1, ExpiresAt: r.now().Add(checkout.Lifetime)}
	if body != "" && (json.Unmarshal([]byte(body), &state) != nil || !checkout.Valid(state)) {
		return checkout.State{}, checkout.ErrStorage
	}
	if !r.now().Before(state.ExpiresAt) {
		return checkout.State{}, checkout.ErrStorage
	}
	original := checkout.Clone(state)
	if err := fn(&state); err != nil {
		return checkout.State{}, err
	}
	if !checkout.Valid(state) {
		return checkout.State{}, checkout.ErrStorage
	}
	if body != "" && reflect.DeepEqual(original, state) {
		return checkout.Clone(state), nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return checkout.State{}, checkout.ErrStorage
	}
	ttl := state.ExpiresAt.Sub(r.now()).Milliseconds()
	if ttl <= 0 || ttl > checkout.Lifetime.Milliseconds() {
		return checkout.State{}, checkout.ErrStorage
	}
	result, err := r.client.Eval(ctx, receiptCAS, keys, r.epoch, body, key, encoded, ttl, checkout.Capacity, checkout.Lifetime.Milliseconds()).Int()
	if err != nil || result != 1 {
		return checkout.State{}, checkout.ErrStorage
	}
	return checkout.Clone(state), nil
}
