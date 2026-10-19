package session

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
)

func TestBroadcastRedisEncryptedRestartAndConsent(t *testing.T) {
	r, server := redisFixture(t)
	ctx := context.Background()
	b := r.BroadcastStore().(*redisBroadcast)
	if rows, err := b.Recipients(ctx); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if err := b.SetSubscribed(ctx, 42, true); err != nil {
		t.Fatal(err)
	}
	rows, _ := b.Recipients(ctx)
	_, err := b.Update(ctx, func(s *broadcast.State) error {
		s.Drafts[42] = &broadcast.Draft{ID: rand.Text(), Stage: "confirm", MessageID: 77, Text: "PRIVATE-BROADCAST-CONTENT", Recipients: rows, ExpiresAt: time.Now().Add(broadcast.DraftLifetime)}
		s.Job = &broadcast.Job{ID: rand.Text(), OwnerID: 42, Worker: "old-worker", Status: "paused", Text: "PRIVATE-BROADCAST-CONTENT", Recipients: rows, Total: 1, Next: 1, Unknown: 1, ExpiresAt: time.Now().Add(broadcast.JobLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := server.Get(b.keys()[1])
	if strings.Contains(body, "PRIVATE-BROADCAST") || server.TTL(b.keys()[1]) != 48*time.Hour || server.TTL(b.subkeys()[1]) != 0 {
		t.Fatal("payload not encrypted or wrong retention")
	}
	restarted := connectRedis(t, "redis://"+server.Addr()).BroadcastStore()
	s, err := restarted.View(ctx)
	if err != nil || s.Drafts[42].Text != "PRIVATE-BROADCAST-CONTENT" || s.Drafts[42].MessageID != 77 || s.Job.Next != 1 || s.Job.Unknown != 1 {
		t.Fatal("restart lost state", s, err)
	}
	token, err := restarted.Consent(ctx, 42)
	if err != nil || token != rows[0].Consent {
		t.Fatal("consent not durable", err)
	}
	_ = restarted.SetSubscribed(ctx, 42, false)
	_ = restarted.SetSubscribed(ctx, 42, true)
	_ = restarted.RemoveIf(ctx, rows[0])
	if token, _ := restarted.Consent(ctx, 42); token == "" || token == rows[0].Consent {
		t.Fatal("new consent removed by old snapshot")
	}
	s.Drafts[42].Text = "mutated"
	if again, _ := restarted.View(ctx); again.Drafts[42].Text != "PRIVATE-BROADCAST-CONTENT" {
		t.Fatal("alias")
	}
}
func TestBroadcastRedisCASNeverReplaysClosure(t *testing.T) {
	r, server := redisFixture(t)
	b := r.BroadcastStore()
	other := connectRedis(t, "redis://"+server.Addr()).BroadcastStore()
	ctx := context.Background()
	calls := 0
	_, err := b.Update(ctx, func(s *broadcast.State) error {
		calls++
		_, inner := other.Update(ctx, func(n *broadcast.State) error {
			n.Drafts[43] = &broadcast.Draft{ID: rand.Text(), ExpiresAt: time.Now().Add(time.Minute)}
			return nil
		})
		if inner != nil {
			t.Fatal(inner)
		}
		s.Drafts[42] = &broadcast.Draft{ID: rand.Text(), ExpiresAt: time.Now().Add(time.Minute)}
		return nil
	})
	if !errors.Is(err, broadcast.ErrConflict) || calls != 1 {
		t.Fatal("CAS replayed", err, calls)
	}
	state, _ := b.View(ctx)
	if state.Drafts[42] != nil || state.Drafts[43] == nil {
		t.Fatal("failed CAS committed")
	}
}
func TestBroadcastRedisExpiryAndFinancialIsolation(t *testing.T) {
	r, _ := redisFixture(t)
	b := r.BroadcastStore()
	ctx := context.Background()
	now := time.Now()
	r.now = func() time.Time { return now }
	if state := r.Update(42, func(s *State) {
		s.Locale = "en-US"
		s.WithdrawalGuard = &WithdrawalGuard{ID: "guard", Status: "unknown"}
	}); state.Err != nil {
		t.Fatal(state.Err)
	}
	_ = b.SetSubscribed(ctx, 42, true)
	_, err := b.Update(ctx, func(s *broadcast.State) error {
		s.Drafts[42] = &broadcast.Draft{ID: rand.Text(), Text: "secret", ExpiresAt: now.Add(time.Minute)}
		s.Job = &broadcast.Job{ID: rand.Text(), Status: "running", Next: 1, Total: 1, Recipients: []broadcast.Recipient{{ID: 42}}, Flight: &broadcast.Flight{Until: now.Add(time.Minute)}, ExpiresAt: now.Add(time.Hour)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	state, err := b.View(ctx)
	if err != nil || state.Drafts[42] != nil || state.Job.Status != "paused" || state.Job.Unknown != 1 || state.Job.Flight != nil {
		t.Fatal("expiry failed", state, err)
	}
	for range 3 {
		state, _ = b.View(ctx)
		if state.Job.Unknown != 1 {
			t.Fatal("unknown counted repeatedly")
		}
	}
	_ = b.SetSubscribed(ctx, 42, false)
	if state := r.Get(42); state.Err != nil || state.Locale != "en-US" || state.WithdrawalGuard == nil {
		t.Fatal("subscription changed financial metadata", state)
	}
}
func TestBroadcastRedisFailsClosed(t *testing.T) {
	for _, mode := range []string{"ciphertext", "epoch", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			r, server := redisFixture(t)
			b := r.BroadcastStore().(*redisBroadcast)
			ctx := context.Background()
			_ = b.SetSubscribed(ctx, 42, true)
			switch mode {
			case "ciphertext":
				_ = server.Set(b.keys()[1], "broken")
			case "epoch":
				server.Del(b.keys()[0])
			case "capacity":
				_, err := b.Update(ctx, func(s *broadcast.State) error {
					s.Job = &broadcast.Job{Recipients: make([]broadcast.Recipient, broadcast.RegistryLimit+1)}
					return nil
				})
				if !errors.Is(err, broadcast.ErrCapacity) {
					t.Fatal(err)
				}
				return
			}
			called := false
			_, err := b.Update(ctx, func(*broadcast.State) error { called = true; return nil })
			if !errors.Is(err, broadcast.ErrUnavailable) || called {
				t.Fatal("corruption became empty store", err)
			}
			if mode == "epoch" {
				if _, err := b.Consent(ctx, 42); !errors.Is(err, broadcast.ErrUnavailable) {
					t.Fatal("lost epoch allows consent read")
				}
				if err := b.SetSubscribed(ctx, 43, true); !errors.Is(err, broadcast.ErrUnavailable) {
					t.Fatal("lost epoch allows opt-in")
				}
			}
		})
	}
}
