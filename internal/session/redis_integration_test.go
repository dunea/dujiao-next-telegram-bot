package session

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in two-phase check against a disposable REAL Redis instance. Run seed,
// restart that Redis container, then verify; never point at a merchant database.
func TestRedisAOFRestart(t *testing.T) {
	url, phase := os.Getenv("BOT_TEST_REDIS_URL"), os.Getenv("BOT_TEST_REDIS_PHASE")
	if url == "" || phase == "" {
		t.Skip("set BOT_TEST_REDIS_URL and BOT_TEST_REDIS_PHASE=seed|verify")
	}
	r, err := NewRedis(url, "dujiao-bot:test-aof", "zh-CN", testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	switch phase {
	case "seed":
		state := r.Update(85001, func(s *State) {
			s.Locale, s.Flow = "en-US", "withdrawal"
			s.Withdrawal = &WithdrawalDraft{ID: "aof-draft", MessageID: 17, Step: "confirm", Account: "AOF-TEST-ACCOUNT", ExpiresAt: time.Now().Add(10 * time.Minute)}
			s.WithdrawalGuard = &WithdrawalGuard{ID: "aof-guard", Status: "unknown", ReviewMessageID: 31}
		})
		if state.Err != nil {
			t.Fatal(state.Err)
		}
	case "verify":
		state := r.Get(85001)
		if state.Err != nil || state.Locale != "en-US" || state.Withdrawal == nil || state.Withdrawal.Account != "AOF-TEST-ACCOUNT" || state.Withdrawal.MessageID != 17 || state.WithdrawalGuard == nil || state.WithdrawalGuard.ID != "aof-guard" || state.WithdrawalGuard.ReviewMessageID != 31 {
			t.Fatalf("AOF restart lost test state: %+v", state)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.client.Del(ctx, r.keys(85001)...).Err(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("BOT_TEST_REDIS_PHASE must be seed or verify")
	}
}
