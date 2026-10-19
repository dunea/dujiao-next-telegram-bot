package session

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
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
	b := r.BroadcastStore().(*redisBroadcast)
	ctx := context.Background()
	switch phase {
	case "seed":
		for i, status := range []string{"sent", "posting", "retry"} {
			key := callback.ReceiptKey(85001, callback.Event{OrderID: uint(700 + i)})
			if _, err := r.CallbackReceipts().Update(ctx, key, func(s *callback.Receipt) error {
				s.Events["order-paid"] = callback.Delivery{Fingerprint: key, Token: rand.Text(), Status: status, NotBefore: time.Now().Add(time.Hour)}
				return nil
			}); err != nil {
				t.Fatal("seed callback receipt", err)
			}
		}
		state := r.Update(85001, func(s *State) {
			s.Locale, s.Flow = "en-US", "withdrawal"
			s.Withdrawal = &WithdrawalDraft{ID: "aof-draft", MessageID: 17, Step: "confirm", Account: "AOF-TEST-ACCOUNT", ExpiresAt: time.Now().Add(10 * time.Minute)}
			s.WithdrawalGuard = &WithdrawalGuard{ID: "aof-guard", Status: "unknown", ReviewMessageID: 31}
		})
		if state.Err != nil {
			t.Fatal(state.Err)
		}
		if d, err := r.Allow(context.Background(), "aof-write", ratelimit.Rule{Limit: 1, Window: time.Hour}); err != nil || !d.Allowed {
			t.Fatal("seed rate bucket", d, err)
		}
		if err := b.SetSubscribed(ctx, 85001, true); err != nil {
			t.Fatal(err)
		}
		rows, err := b.Recipients(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatal("seed consent", err)
		}
		if _, err := b.Update(ctx, func(s *broadcast.State) error {
			s.NotBefore = time.Now().Add(time.Hour)
			s.Drafts[85001] = &broadcast.Draft{ID: rand.Text(), Stage: "preview", MessageID: 81, Text: "AOF-BROADCAST-TEST", ExpiresAt: time.Now().Add(broadcast.DraftLifetime)}
			s.Job = &broadcast.Job{ID: rand.Text(), OwnerID: 85001, Worker: "previous-process", Status: "running", Text: "AOF-BROADCAST-TEST", Recipients: rows, Total: 1, Next: 1, Flight: &broadcast.Flight{Token: rand.Text(), Recipient: rows[0], Until: time.Now().Add(broadcast.Lease)}, NotBefore: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(broadcast.JobLifetime)}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	case "verify":
		var callbackKeys []string
		for i, status := range []string{"sent", "posting", "retry"} {
			key := callback.ReceiptKey(85001, callback.Event{OrderID: uint(700 + i)})
			s, err := r.CallbackReceipts().Update(ctx, key, func(*callback.Receipt) error { return nil })
			if err != nil || s.Events["order-paid"].Status != status || !s.Events["order-paid"].NotBefore.After(time.Now()) {
				t.Fatal("AOF lost callback receipt/cooldown", err)
			}
			callbackKeys = append(callbackKeys, r.prefix+":callback:v1:"+key)
		}
		callbackKeys = append(callbackKeys, r.prefix+":callback:index:v1")
		state := r.Get(85001)
		if state.Err != nil || state.Locale != "en-US" || state.Withdrawal == nil || state.Withdrawal.Account != "AOF-TEST-ACCOUNT" || state.Withdrawal.MessageID != 17 || state.WithdrawalGuard == nil || state.WithdrawalGuard.ID != "aof-guard" || state.WithdrawalGuard.ReviewMessageID != 31 {
			t.Fatalf("AOF restart lost test state: %+v", state)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if d, err := r.Allow(ctx, "aof-write", ratelimit.Rule{Limit: 1, Window: time.Hour}); err != nil || d.Allowed {
			t.Fatal("restart reset rate limit", d, err)
		}
		bs, err := b.View(ctx)
		if err != nil || bs.Drafts[85001] == nil || bs.Drafts[85001].MessageID != 81 || bs.Job == nil || bs.Job.Next != 1 || bs.Job.Text != "AOF-BROADCAST-TEST" || bs.Job.NotBefore.Before(time.Now()) || bs.NotBefore.Before(time.Now()) {
			t.Fatal("AOF lost broadcast progress", err)
		}
		if token, err := b.Consent(ctx, 85001); err != nil || token == "" || token != bs.Job.Recipients[0].Consent {
			t.Fatal("AOF lost consent", err)
		}
		e := broadcast.NewEngine(b, r)
		if err := e.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if state, _ := b.View(ctx); state.Job.Status != "paused" {
			t.Fatal("restart resumed campaign automatically")
		}
		if err := r.client.Del(ctx, append(append(r.keys(85001), r.rateKey("aof-write"), b.keys()[1], b.subkeys()[1]), callbackKeys...)...).Err(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("BOT_TEST_REDIS_PHASE must be seed or verify")
	}
}
