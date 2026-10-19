package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
)

type redisBroadcast struct{ r *Redis }

func (r *Redis) BroadcastStore() broadcast.Store { return &redisBroadcast{r: r} }
func (m *Memory) BroadcastStore() broadcast.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.broadcast == nil {
		m.broadcast = broadcast.NewMemory()
	}
	return m.broadcast
}

const broadcastRead = `return {redis.call('GET', KEYS[1]) or '',redis.call('GET', KEYS[2]) or ''}`
const broadcastCAS = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return -1 end
if (redis.call('GET', KEYS[2]) or '') ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[2], ARGV[3], 'PX', ARGV[4])
return 1
`
const subscribeLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return -1 end
if ARGV[3] == 'off' then redis.call('HDEL', KEYS[2], ARGV[2]); return 1 end
if ARGV[3] == 'remove_if' then
 if redis.call('HGET', KEYS[2], ARGV[2]) == ARGV[4] then redis.call('HDEL', KEYS[2], ARGV[2]) end
 return 1
end
if redis.call('HEXISTS', KEYS[2], ARGV[2]) == 1 then return 1 end
if redis.call('HLEN', KEYS[2]) >= tonumber(ARGV[5]) then return -2 end
redis.call('HSET', KEYS[2], ARGV[2], ARGV[4]); return 1
`
const recipientsLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return {-1} end
if redis.call('HLEN', KEYS[2]) > tonumber(ARGV[2]) then return {-2} end
return redis.call('HGETALL', KEYS[2])
`
const consentLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return {-1} end
return {redis.call('HGET', KEYS[2], ARGV[2]) or ''}
`

func (b *redisBroadcast) keys() []string {
	return []string{b.r.prefix + ":epoch", b.r.prefix + ":broadcast:v1"}
}
func (b *redisBroadcast) subkeys() []string {
	return []string{b.r.prefix + ":epoch", b.r.prefix + ":broadcast:subscribers:v1"}
}
func (b *redisBroadcast) read(ctx context.Context) (broadcast.State, string, error) {
	values, err := b.r.client.Eval(ctx, broadcastRead, b.keys()).Slice()
	if err != nil || len(values) != 2 || values[0] != b.r.epoch {
		return broadcast.State{}, "", broadcast.ErrUnavailable
	}
	body, ok := values[1].(string)
	if !ok {
		return broadcast.State{}, "", broadcast.ErrUnavailable
	}
	state := broadcast.NewState()
	if body == "" {
		return state, "", nil
	}
	nonceSize := b.r.aead.NonceSize()
	if len(body) < nonceSize {
		return broadcast.State{}, "", broadcast.ErrUnavailable
	}
	plain, err := b.r.aead.Open(nil, []byte(body[:nonceSize]), []byte(body[nonceSize:]), []byte(b.keys()[1]))
	if err != nil || json.Unmarshal(plain, &state) != nil || state.Version != 1 || len(state.Drafts) > 10 {
		return broadcast.State{}, "", broadcast.ErrUnavailable
	}
	if state.Job != nil && (len(state.Job.Recipients) > broadcast.RegistryLimit || state.Job.Next < 0 || (state.Job.Status != "expired" && state.Job.Next > len(state.Job.Recipients))) {
		return broadcast.State{}, "", broadcast.ErrUnavailable
	}
	return broadcast.Normalize(state, b.r.now()), body, nil
}
func (b *redisBroadcast) View(ctx context.Context) (broadcast.State, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, _, err := b.read(ctx)
	return state, err
}
func (b *redisBroadcast) Update(ctx context.Context, fn func(*broadcast.State) error) (broadcast.State, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, previous, err := b.read(ctx)
	if err != nil {
		return broadcast.State{}, err
	}
	original := broadcast.Clone(state)
	if err := fn(&state); err != nil {
		return broadcast.State{}, err
	}
	if reflect.DeepEqual(original, state) {
		return broadcast.Clone(state), nil
	}
	if len(state.Drafts) > 10 || (state.Job != nil && len(state.Job.Recipients) > broadcast.RegistryLimit) {
		return broadcast.State{}, broadcast.ErrCapacity
	}
	plain, err := json.Marshal(state)
	if err != nil {
		return broadcast.State{}, broadcast.ErrUnavailable
	}
	nonce := make([]byte, b.r.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return broadcast.State{}, broadcast.ErrUnavailable
	}
	body := b.r.aead.Seal(nonce, nonce, plain, []byte(b.keys()[1]))
	result, err := b.r.client.Eval(ctx, broadcastCAS, b.keys(), b.r.epoch, previous, body, (48 * time.Hour).Milliseconds()).Int()
	if err != nil || result < 0 {
		return broadcast.State{}, broadcast.ErrUnavailable
	}
	if result != 1 {
		return broadcast.State{}, broadcast.ErrConflict
	}
	return broadcast.Clone(state), nil
}
func (b *redisBroadcast) change(ctx context.Context, uid int64, mode, token string) error {
	if uid <= 0 {
		return broadcast.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := b.r.client.Eval(ctx, subscribeLua, b.subkeys(), b.r.epoch, strconv.FormatInt(uid, 10), mode, token, broadcast.RegistryLimit).Int()
	if err != nil || result == -1 {
		return broadcast.ErrUnavailable
	}
	if result == -2 {
		return broadcast.ErrCapacity
	}
	return nil
}
func (b *redisBroadcast) SetSubscribed(ctx context.Context, uid int64, on bool) error {
	mode := "off"
	if on {
		mode = "on"
	}
	return b.change(ctx, uid, mode, rand.Text())
}
func (b *redisBroadcast) RemoveIf(ctx context.Context, r broadcast.Recipient) error {
	return b.change(ctx, r.ID, "remove_if", r.Consent)
}
func (b *redisBroadcast) Consent(ctx context.Context, uid int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	values, err := b.r.client.Eval(ctx, consentLua, b.subkeys(), b.r.epoch, strconv.FormatInt(uid, 10)).Slice()
	if err != nil || len(values) != 1 {
		return "", broadcast.ErrUnavailable
	}
	token, ok := values[0].(string)
	if !ok || (token != "" && len(token) != 26) {
		return "", broadcast.ErrUnavailable
	}
	return token, nil
}
func (b *redisBroadcast) Recipients(ctx context.Context) ([]broadcast.Recipient, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	values, err := b.r.client.Eval(ctx, recipientsLua, b.subkeys(), b.r.epoch, broadcast.RegistryLimit).Slice()
	if err != nil || len(values)%2 != 0 {
		return nil, broadcast.ErrUnavailable
	}
	rows := make([]broadcast.Recipient, 0, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, broadcast.ErrUnavailable
		}
		uid, err := strconv.ParseInt(key, 10, 64)
		token, ok := values[i+1].(string)
		if err != nil || uid <= 0 || !ok || len(token) != 26 {
			return nil, broadcast.ErrUnavailable
		}
		rows = append(rows, broadcast.Recipient{ID: uid, Consent: token})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
