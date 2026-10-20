package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrUnavailable = errors.New("session storage unavailable")
	ErrConflict    = errors.New("session changed concurrently; action not committed")
	ErrCorrupt     = errors.New("session data or encryption key invalid")
)

// Redis stores encrypted, expiring drafts separately from permanent safety metadata.
// Updates use one CAS attempt: a callback is NEVER replayed after a conflict/error.
type Redis struct {
	client                       *redis.Client
	aead                         cipher.AEAD
	prefix, epoch, defaultLocale string
	now                          func() time.Time
}

type redisMeta struct {
	Version  int
	Revision string
	Locale   string
	Guard    *WithdrawalGuard
}

const redisRead = `
return {redis.call('GET', KEYS[1]) or '', redis.call('GET', KEYS[2]) or '', redis.call('GET', KEYS[3]) or ''}
`
const redisCAS = `
if (redis.call('GET', KEYS[1]) or '') ~= ARGV[1] then return -1 end
if (redis.call('GET', KEYS[2]) or '') ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[2], ARGV[3])
if ARGV[4] == '' then redis.call('DEL', KEYS[3])
else redis.call('SET', KEYS[3], ARGV[4], 'PX', ARGV[5]) end
return 1
`

func NewRedis(url, prefix, defaultLocale, encryptionKey string) (*Redis, error) {
	key, err := hex.DecodeString(encryptionKey)
	if err != nil || len(key) != 32 {
		return nil, ErrCorrupt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCorrupt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrCorrupt
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("invalid BOT_REDIS_URL")
	}
	// In particular, never transparently retry a write with a lost acknowledgement.
	options.MaxRetries = -1
	options.DialTimeout, options.ReadTimeout, options.WriteTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	options.ContextTimeoutEnabled = true
	r := &Redis{client: redis.NewClient(options), aead: aead, prefix: prefix, defaultLocale: defaultLocale, now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keyHash := sha256.Sum256(key)
	epoch := hex.EncodeToString(keyHash[:]) + ":" + rand.Text()
	if err := r.client.SetNX(ctx, prefix+":epoch", epoch, 0).Err(); err != nil {
		_ = r.Close()
		return nil, ErrUnavailable
	}
	r.epoch, err = r.client.Get(ctx, prefix+":epoch").Result()
	if err != nil {
		_ = r.Close()
		return nil, ErrUnavailable
	}
	if len(r.epoch) < 65 || r.epoch[:65] != epoch[:65] {
		_ = r.Close()
		return nil, ErrCorrupt
	}
	return r, nil
}

func (r *Redis) keys(uid int64) []string {
	base := r.prefix + ":user:" + strconv.FormatInt(uid, 10)
	return []string{r.prefix + ":epoch", base + ":meta", base + ":draft"}
}

func (r *Redis) failure(err error) State { return State{Locale: r.defaultLocale, Err: err} }

func (r *Redis) read(ctx context.Context, uid int64) (State, string, error) {
	values, err := r.client.Eval(ctx, redisRead, r.keys(uid)).Slice()
	if err != nil {
		return State{}, "", ErrUnavailable
	}
	if len(values) != 3 || values[0] != r.epoch {
		return State{}, "", ErrUnavailable
	}
	metaJSON, ok := values[1].(string)
	if !ok {
		return State{}, "", ErrCorrupt
	}
	body, ok := values[2].(string)
	if !ok {
		return State{}, "", ErrCorrupt
	}
	state := State{Locale: r.defaultLocale}
	if metaJSON == "" {
		if body != "" {
			return State{}, "", ErrCorrupt
		}
		return state, "", nil
	}
	var meta redisMeta
	if json.Unmarshal([]byte(metaJSON), &meta) != nil || meta.Version != 1 || meta.Revision == "" {
		return State{}, "", ErrCorrupt
	}
	if body != "" {
		if len(body) < r.aead.NonceSize() {
			return State{}, "", ErrCorrupt
		}
		plain, err := r.aead.Open(nil, []byte(body[:r.aead.NonceSize()]), []byte(body[r.aead.NonceSize():]), []byte(r.keys(uid)[2]))
		if err != nil || json.Unmarshal(plain, &state) != nil {
			return State{}, "", ErrCorrupt
		}
		// JSON represents checkbox slices as []any; restore the typed UI values.
		if state.Purchase != nil {
			for key, value := range state.Purchase.Values {
				if values, ok := value.([]any); ok {
					strings := make([]string, len(values))
					for i, item := range values {
						var valid bool
						strings[i], valid = item.(string)
						if !valid {
							return State{}, "", ErrCorrupt
						}
					}
					state.Purchase.Values[key] = strings
				}
			}
		}
	}
	state.Locale, state.WithdrawalGuard = meta.Locale, meta.Guard
	return normalizeState(state, r.now()), metaJSON, nil
}

func (r *Redis) Get(uid int64) State {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, _, err := r.read(ctx, uid)
	if err != nil {
		return r.failure(err)
	}
	return state
}

func (r *Redis) Update(uid int64, update func(*State)) State {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, previous, err := r.read(ctx, uid)
	if err != nil {
		return r.failure(err)
	}
	original := cloneState(state)
	update(&state)
	// A rejected claim must not bump the revision: otherwise a burst of stale
	// confirmations can continually invalidate the winning worker's posting CAS.
	if reflect.DeepEqual(original, state) {
		var old redisMeta
		if previous == "" || (json.Unmarshal([]byte(previous), &old) == nil && reflect.DeepEqual(old.Guard, state.WithdrawalGuard)) {
			return cloneState(state)
		}
	}
	meta, err := json.Marshal(redisMeta{Version: 1, Revision: rand.Text(), Locale: state.Locale, Guard: state.WithdrawalGuard})
	if err != nil {
		return r.failure(ErrCorrupt)
	}
	draft := state
	draft.Locale, draft.WithdrawalGuard = "", nil
	var body []byte
	ttl := 24 * time.Hour
	if draft.Flow != "" || draft.Purchase != nil || draft.GiftCard != nil || draft.Recharge != nil || draft.Quantity != nil || draft.Withdrawal != nil {
		for _, expiry := range draftExpiries(draft) {
			if remaining := expiry.Sub(r.now()); remaining < ttl {
				ttl = remaining
			}
		}
		if ttl < time.Millisecond {
			return r.failure(ErrConflict)
		}
		plain, err := json.Marshal(draft)
		if err != nil {
			return r.failure(ErrCorrupt)
		}
		nonce := make([]byte, r.aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return r.failure(ErrUnavailable)
		}
		body = r.aead.Seal(nonce, nonce, plain, []byte(r.keys(uid)[2]))
	}
	result, err := r.client.Eval(ctx, redisCAS, r.keys(uid), r.epoch, previous, meta, body, ttl.Milliseconds()).Int()
	if err != nil || result == -1 {
		return r.failure(ErrUnavailable)
	}
	if result != 1 {
		return r.failure(ErrConflict)
	}
	return cloneState(state)
}

func draftExpiries(state State) []time.Time {
	var times []time.Time
	// A short quantity-edit prompt must not discard its existing, longer-lived
	// purchase form/coupon. normalizeState expires the linked prompt separately.
	if state.Quantity != nil && (state.Purchase == nil || state.Quantity.PurchaseID != state.Purchase.ID) {
		times = append(times, state.Quantity.ExpiresAt)
	}
	if state.Purchase != nil {
		times = append(times, state.Purchase.ExpiresAt)
	}
	if state.Recharge != nil {
		times = append(times, state.Recharge.ExpiresAt)
	}
	if state.GiftCard != nil {
		times = append(times, state.GiftCard.ExpiresAt)
	}
	if state.Withdrawal != nil {
		times = append(times, state.Withdrawal.ExpiresAt)
	}
	return times
}

func (r *Redis) ResetFlow(uid int64) error { return r.Update(uid, resetFlow).Err }
func (r *Redis) Close() error              { return r.client.Close() }
func (r *Redis) Health() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	epoch, err := r.client.Get(ctx, r.prefix+":epoch").Result()
	if err != nil || epoch != r.epoch {
		return ErrUnavailable
	}
	return nil
}
