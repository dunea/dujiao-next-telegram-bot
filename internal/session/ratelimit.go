package session

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

// Redis time avoids sharing process clocks. GCRA permits a bounded burst, then
// replenishes evenly; TTL bounds idle keys without evicting active protection.
const rateLimitLua = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return -1 end
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000000 + tonumber(clock[2])
local window = tonumber(ARGV[2])*1000
local interval = math.ceil(window/tonumber(ARGV[3]))
local tat = tonumber(redis.call('GET', KEYS[2])) or now
local allowAt = tat - window + interval
if now < allowAt then return math.ceil((allowAt-now)/1000) end
local next = math.max(tat,now)+interval
redis.call('SET', KEYS[2], string.format('%.0f',next), 'PX', math.max(1,math.ceil((next-now)/1000)))
return 0
`

func (r *Redis) Allow(ctx context.Context, key string, rule ratelimit.Rule) (ratelimit.Decision, error) {
	if !rule.Valid() || len(key) == 0 || len(key) > 160 {
		return ratelimit.Decision{}, ratelimit.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	keys := []string{r.prefix + ":epoch", r.rateKey(key)}
	wait, err := r.client.Eval(ctx, rateLimitLua, keys, r.epoch, rule.Window.Milliseconds(), rule.Limit).Int64()
	if err != nil || wait < 0 {
		return ratelimit.Decision{}, ratelimit.ErrUnavailable
	}
	return ratelimit.Decision{Allowed: wait == 0, RetryAfter: time.Duration(wait) * time.Millisecond}, nil
}

func (r *Redis) rateKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s:rate:v1:%x", r.prefix, digest[:16])
}
