package session

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
)

type callbackReceipts struct{ r *Redis }

func (r *Redis) CallbackReceipts() callback.Receipts { return &callbackReceipts{r: r} }
func (m *Memory) CallbackReceipts() callback.Receipts {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callbacks == nil {
		m.callbacks = callback.NewMemoryReceipts()
	}
	return m.callbacks
}

const receiptRead = `
if redis.call('GET',KEYS[1]) ~= ARGV[1] then return {-1} end
return {redis.call('GET',KEYS[2]) or ''}
`
const receiptCAS = `
if redis.call('GET',KEYS[1]) ~= ARGV[1] then return -1 end
if (redis.call('GET',KEYS[2]) or '') ~= ARGV[2] then return 0 end
local tm=redis.call('TIME')
local now=tonumber(tm[1])*1000+math.floor(tonumber(tm[2])/1000)
redis.call('ZREMRANGEBYSCORE',KEYS[3],'-inf',now)
if redis.call('ZSCORE',KEYS[3],ARGV[3]) == false and redis.call('ZCARD',KEYS[3]) >= tonumber(ARGV[6]) then return -2 end
redis.call('SET',KEYS[2],ARGV[4],'PX',ARGV[5])
redis.call('ZADD',KEYS[3],now+tonumber(ARGV[5]),ARGV[3])
redis.call('PEXPIRE',KEYS[3],ARGV[7])
return 1
`

func (c *callbackReceipts) Update(ctx context.Context, key string, fn func(*callback.Receipt) error) (callback.Receipt, error) {
	if !callback.ValidReceiptKey(key) {
		return callback.Receipt{}, callback.StorageInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	keys := []string{c.r.prefix + ":epoch", c.r.prefix + ":callback:v1:" + key, c.r.prefix + ":callback:index:v1"}
	values, err := c.r.client.Eval(ctx, receiptRead, keys, c.r.epoch).Slice()
	if err != nil || len(values) != 1 {
		return callback.Receipt{}, callback.StorageRead
	}
	body, ok := values[0].(string)
	if !ok {
		return callback.Receipt{}, callback.StorageEpoch
	}
	r := callback.Receipt{Version: 1, ExpiresAt: c.r.now().Add(callback.ReceiptLifetime), Events: make(map[string]callback.Delivery)}
	if body != "" && (json.Unmarshal([]byte(body), &r) != nil || !callback.ValidReceipt(r)) {
		return callback.Receipt{}, callback.StorageInvalid
	}
	if !c.r.now().Before(r.ExpiresAt) {
		return callback.Receipt{}, callback.StorageExpired
	}
	original := callback.CloneReceipt(r)
	if err := fn(&r); err != nil {
		return callback.Receipt{}, err
	}
	if !callback.ValidReceipt(r) {
		return callback.Receipt{}, callback.StorageInvalid
	}
	if body != "" && reflect.DeepEqual(original, r) {
		return callback.CloneReceipt(r), nil
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return callback.Receipt{}, callback.StorageInvalid
	}
	ttl := r.ExpiresAt.Sub(c.r.now()).Milliseconds()
	if ttl <= 0 || ttl > callback.ReceiptLifetime.Milliseconds() {
		return callback.Receipt{}, callback.StorageExpired
	}
	result, err := c.r.client.Eval(ctx, receiptCAS, keys, c.r.epoch, body, key, encoded, ttl, callback.ReceiptCapacity, callback.ReceiptLifetime.Milliseconds()).Int()
	if err != nil {
		return callback.Receipt{}, callback.StorageWrite
	}
	switch result {
	case 1:
		return callback.CloneReceipt(r), nil
	case 0:
		return callback.Receipt{}, callback.StorageConflict
	case -1:
		return callback.Receipt{}, callback.StorageEpoch
	case -2:
		return callback.Receipt{}, callback.StorageCapacity
	default:
		return callback.Receipt{}, callback.StorageWrite
	}
}
