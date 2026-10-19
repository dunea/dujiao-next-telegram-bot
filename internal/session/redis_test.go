package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func redisFixture(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	r := connectRedis(t, "redis://"+server.Addr())
	return r, server
}

func connectRedis(t *testing.T, url string) *Redis {
	t.Helper()
	r, err := NewRedis(url, "test:{merchant}", "zh-CN", testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestRedisEncryptedRoundTripAcrossClients(t *testing.T) {
	r, server := redisFixture(t)
	expires := time.Now().Add(10 * time.Minute)
	state := r.Update(42, func(s *State) {
		s.Locale, s.Flow = "en-US", "withdrawal"
		s.Withdrawal = &WithdrawalDraft{ID: "draft", ChatID: 42, MessageID: 73, Step: "confirm", Account: "private-account@example.com", Amount: "20.00", Channels: []string{"alipay"}, ExpiresAt: expires}
		s.WithdrawalGuard = &WithdrawalGuard{ID: "older", Status: "unknown", ReviewMessageID: 99}
		s.Purchase = &PurchaseDraft{ID: "purchase", Values: map[string]any{"checkbox": []string{"a", "b"}, "number": float64(12.5)}, ExpiresAt: expires}
		s.GiftCard = &GiftCardInput{ID: "gift", PromptID: 7, ExpiresAt: expires}
		s.Recharge = &RechargeDraft{ID: "recharge", MessageID: 8, Amount: "50.00", ExpiresAt: expires}
	})
	if state.Err != nil {
		t.Fatal(state.Err)
	}
	for _, key := range server.Keys() {
		value, err := server.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(value, "private-account") {
			t.Fatal("plaintext receiving details in Redis")
		}
	}
	_ = r.Close()
	restarted := connectRedis(t, "redis://"+server.Addr())
	got := restarted.Get(42)
	if got.Err != nil || got.Locale != "en-US" || got.Withdrawal == nil || got.Withdrawal.Account != "private-account@example.com" || got.Withdrawal.MessageID != 73 || got.WithdrawalGuard.ReviewMessageID != 99 {
		t.Fatalf("restart lost state: %+v", got)
	}
	if !reflect.DeepEqual(got.Purchase.Values["checkbox"], []string{"a", "b"}) || got.Purchase.Values["number"] != float64(12.5) || got.GiftCard.PromptID != 7 || got.Recharge.Amount != "50.00" {
		t.Fatal("UI values lost their types/data")
	}
	got.Withdrawal.Account = "mutated"
	if restarted.Get(42).Withdrawal.Account != "private-account@example.com" {
		t.Fatal("returned state aliases store")
	}
	if restarted.Get(43).Withdrawal != nil {
		t.Fatal("cross-user session leak")
	}
}

func TestRedisDraftTTLAndResetNeverExpireProtection(t *testing.T) {
	r, server := redisFixture(t)
	result := r.Update(42, func(s *State) {
		s.Locale, s.Flow = "en-US", "withdrawal"
		s.Withdrawal = &WithdrawalDraft{ID: "draft", Account: "secret", ExpiresAt: time.Now().Add(time.Minute)}
		s.WithdrawalGuard = &WithdrawalGuard{ID: "pending", Status: "unknown"}
	})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if ttl := server.TTL(r.keys(42)[2]); ttl <= 0 || ttl > time.Minute {
		t.Fatal("missing/bad draft TTL", ttl)
	}
	if server.TTL(r.keys(42)[1]) != 0 {
		t.Fatal("guard metadata must not expire")
	}
	server.FastForward(48 * time.Hour)
	got := r.Get(42)
	if got.Err != nil || got.Withdrawal != nil || got.Flow != "" || got.Locale != "en-US" || got.WithdrawalGuard == nil {
		t.Fatalf("TTL erased safety metadata: %+v", got)
	}
	if server.Exists(r.keys(42)[2]) {
		t.Fatal("expired receiving details remain in live Redis")
	}
	if err := r.ResetFlow(42); err != nil {
		t.Fatal(err)
	}
	if r.Get(42).WithdrawalGuard == nil {
		t.Fatal("reset erased protection")
	}
}

func TestRedisExpiredInFlightLeaseRequiresReview(t *testing.T) {
	for _, status := range []string{"checking", "posting"} {
		t.Run(status, func(t *testing.T) {
			r, server := redisFixture(t)
			deadline := time.Now().Add(3 * time.Minute)
			if state := r.Update(42, func(s *State) {
				s.Flow = "withdrawal"
				s.Withdrawal = &WithdrawalDraft{ID: "draft", Busy: true, Account: "secret", ExpiresAt: deadline.Add(time.Minute)}
				s.WithdrawalGuard = &WithdrawalGuard{ID: "draft", Status: status, LeaseUntil: deadline}
			}); state.Err != nil {
				t.Fatal(state.Err)
			}
			restarted := connectRedis(t, "redis://"+server.Addr())
			if restarted.Get(42).WithdrawalGuard.Status != status {
				t.Fatal("active request prematurely unlocked")
			}
			restarted.now = func() time.Time { return deadline.Add(time.Second) }
			got := restarted.Get(42)
			if got.Err != nil || got.WithdrawalGuard.Status != "unknown" || got.Withdrawal != nil || got.Flow != "" {
				t.Fatalf("unsafe lease recovery: %+v", got)
			}
			if err := restarted.ResetFlow(42); err != nil {
				t.Fatal(err)
			}
			if r.Get(42).WithdrawalGuard.Status != "unknown" {
				t.Fatal("recovery not persisted")
			}
		})
	}
}

func TestRedisCASConflictNeverReplaysCallback(t *testing.T) {
	r, server := redisFixture(t)
	other := connectRedis(t, "redis://"+server.Addr())
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan State, 1)
	var callbacks atomic.Int32
	go func() {
		result <- r.Update(42, func(s *State) { callbacks.Add(1); close(entered); <-release; s.Locale = "en-US" })
	}()
	<-entered
	if state := other.Update(42, func(s *State) { s.Locale = "zh-TW" }); state.Err != nil {
		t.Fatal(state.Err)
	}
	close(release)
	if state := <-result; !errors.Is(state.Err, ErrConflict) || callbacks.Load() != 1 {
		t.Fatalf("conflicting callback replayed/accepted: %+v", state)
	}
	if r.Get(42).Locale != "zh-TW" {
		t.Fatal("stale state overwrote winner")
	}
}

func TestRedisRejectedClaimDoesNotInvalidateWinnerRevision(t *testing.T) {
	r, server := redisFixture(t)
	if state := r.Update(42, func(s *State) {
		s.WithdrawalGuard = &WithdrawalGuard{ID: "winner", Status: "checking", LeaseUntil: time.Now().Add(time.Minute)}
	}); state.Err != nil {
		t.Fatal(state.Err)
	}
	before, _ := server.Get(r.keys(42)[1])
	claimed := false
	if state := r.Update(42, func(s *State) {
		if s.WithdrawalGuard == nil {
			claimed = true
			s.WithdrawalGuard = &WithdrawalGuard{ID: "loser", Status: "checking"}
		}
	}); state.Err != nil || claimed {
		t.Fatal("rejected claim failed/claimed", state.Err)
	}
	after, _ := server.Get(r.keys(42)[1])
	if before != after {
		t.Fatal("no-op claim invalidated the winning worker's revision")
	}
}

func TestRedisConcurrentClaimsHaveOneWinner(t *testing.T) {
	r, server := redisFixture(t)
	other := connectRedis(t, "redis://"+server.Addr())
	var winners atomic.Int32
	var wait sync.WaitGroup
	for i := range 32 {
		store := r
		if i%2 == 0 {
			store = other
		}
		wait.Go(func() {
			claimed := false
			state := store.Update(42, func(s *State) {
				if s.WithdrawalGuard == nil {
					s.WithdrawalGuard = &WithdrawalGuard{ID: "one", Status: "posting"}
					claimed = true
				}
			})
			if state.Err == nil && claimed {
				winners.Add(1)
			}
		})
	}
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatal("financial claim winners", winners.Load())
	}
}

func TestRedisCorruptionAndWrongKeyFailClosed(t *testing.T) {
	r, server := redisFixture(t)
	if state := r.Update(42, func(s *State) { s.Flow = "invite" }); state.Err != nil {
		t.Fatal(state.Err)
	}
	if _, err := NewRedis("redis://"+server.Addr(), r.prefix, "zh-CN", strings.Repeat("ab", 32)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("wrong key silently reset sessions", err)
	}
	body, _ := server.Get(r.keys(42)[2])
	meta, _ := server.Get(r.keys(42)[1])
	_ = server.Set(r.keys(43)[1], meta)
	_ = server.Set(r.keys(43)[2], body)
	if r.Get(43).Err == nil {
		t.Fatal("ciphertext copied across users was accepted")
	}
	_ = server.Set(r.keys(42)[2], "broken")
	called := false
	if state := r.Update(42, func(s *State) { called = true }); state.Err == nil || called {
		t.Fatal("corruption treated as empty session")
	}
	_ = server.Set(r.keys(42)[1], `{"Version":99,"Revision":"future"}`)
	if r.Get(42).Err == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestRedisOutageAndDatasetLossFailClosed(t *testing.T) {
	r, server := redisFixture(t)
	if state := r.Update(42, func(s *State) { s.WithdrawalGuard = &WithdrawalGuard{ID: "one", Status: "unknown"} }); state.Err != nil {
		t.Fatal(state.Err)
	}
	server.SetError("ERR connection password=do-not-expose")
	called := false
	state := r.Update(42, func(s *State) { called = true })
	if called || state.Err == nil || strings.Contains(state.Err.Error(), "do-not-expose") || r.Get(42).Err == nil || r.Health() == nil || r.ResetFlow(42) == nil {
		t.Fatal("outage bypassed protection or leaked credentials")
	}
	server.SetError("")
	if r.Get(42).WithdrawalGuard == nil {
		t.Fatal("outage lost guard")
	}
	server.FlushAll()
	if r.Get(42).Err == nil || r.Health() == nil {
		t.Fatal("dataset loss treated as new session")
	}
	if state := r.Update(42, func(s *State) { called = true }); state.Err == nil || called {
		t.Fatal("write after dataset loss")
	}
}

// Inject failure before/after Redis actually commits the CAS, not just before
// the caller's callback runs. Both are ambiguous to application code.
type casFailureHook struct {
	after  bool
	script string
}

func (h casFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}
func (h casFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h casFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		script := h.script
		if script == "" {
			script = redisCAS
		}
		if cmd.Name() == "eval" && cmd.Args()[1] == script {
			if h.after {
				if err := next(ctx, cmd); err != nil {
					return err
				}
			}
			return errors.New("injected acknowledgement failure")
		}
		return next(ctx, cmd)
	}
}

func TestRedisWriteErrorsNeverReturnSuccessfulState(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(strconvBool(after), func(t *testing.T) {
			r, server := redisFixture(t)
			r.client.AddHook(casFailureHook{after: after})
			called := 0
			state := r.Update(42, func(s *State) { called++; s.WithdrawalGuard = &WithdrawalGuard{ID: "protected", Status: "posting"} })
			if called != 1 || state.Err == nil {
				t.Fatal("failed acknowledgement was accepted/retried")
			}
			restarted := connectRedis(t, "redis://"+server.Addr())
			if got := restarted.Get(42); got.Err != nil || (got.WithdrawalGuard != nil) != after {
				t.Fatal("unexpected commit result", got.Err)
			}
		})
	}
}

func strconvBool(value bool) string {
	if value {
		return "after-commit"
	}
	return "before-commit"
}
