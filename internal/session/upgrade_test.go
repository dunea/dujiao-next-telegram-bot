package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

// The writer is the actual v0.4.0 source, invoked by scripts/test-upgrade.sh.
// This is a storage-format upgrade check, not just two current Redis clients.
func TestUpgradeFromV040(t *testing.T) {
	url := os.Getenv("BOT_UPGRADE_REDIS_URL")
	if url == "" {
		t.Skip("run make upgrade-test against the isolated Redis")
	}
	if url != "redis://127.0.0.1:16379" && url != "redis://127.0.0.1:16379/0" {
		t.Fatal("upgrade checks require the fixed integration Redis endpoint")
	}
	r, err := NewRedis(url, "dujiao-bot:test-aof", "zh-CN", testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	state := r.Get(85001)
	if state.Err != nil || state.Locale != "en-US" || state.Withdrawal == nil || state.Withdrawal.ID != "aof-draft" || state.Withdrawal.Account != "AOF-TEST-ACCOUNT" || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "unknown" || state.WithdrawalGuard.ID != "aof-guard" {
		t.Fatal("baseline withdrawal state not preserved")
	}
	ctx := context.Background()
	if decision, err := r.Allow(ctx, "aof-write", ratelimit.Rule{Limit: 1, Window: time.Hour}); err != nil || decision.Allowed {
		t.Fatal("upgrade restored exhausted write budget", err)
	}
	b := r.BroadcastStore()
	before, err := b.View(ctx)
	if err != nil || before.Drafts[85001] == nil || before.Drafts[85001].MessageID != 81 || before.Job == nil || before.Job.Next != 1 || before.Job.Total != 1 || before.NotBefore.Before(time.Now()) {
		t.Fatal("baseline broadcast state missing", err)
	}
	consent, err := b.Consent(ctx, 85001)
	if err != nil || consent == "" || consent != before.Job.Recipients[0].Consent {
		t.Fatal("upgrade lost consent", err)
	}
	// Exercise a current-format explicit opt-out alongside the old snapshot.
	if err := b.SetSubscribed(ctx, 85002, true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetSubscribed(ctx, 85002, false); err != nil {
		t.Fatal(err)
	}
	engine := broadcast.NewEngine(b, r)
	engine.Admin = func(int64) bool { return true }
	engine.Send = func(context.Context, int64, string) broadcast.Result {
		t.Fatal("upgrade auto-sent a broadcast")
		return broadcast.Result{Kind: "unknown"}
	}
	if err := engine.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := b.View(ctx)
	if err != nil || after.Job.Status != "paused" || after.Job.Next != 1 || after.NotBefore.Before(time.Now()) {
		t.Fatal("upgrade resumed or rewound broadcast", err)
	}
	second, err := NewRedis(url, "dujiao-bot:test-aof", "zh-CN", testEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if token, err := second.BroadcastStore().Consent(ctx, 85002); err != nil || token != "" {
		t.Fatal("upgrade silently resubscribed opt-out", err)
	}
	// Force the new writer to produce fresh encrypted state. The old reader then
	// verifies the still-compatible v1 records (and cleans only these test keys).
	updated := r.Update(85001, func(s *State) {
		s.Purchase = &PurchaseDraft{ID: "upgrade-purchase", Values: map[string]any{"options": []string{"a", "b"}}, ExpiresAt: time.Now().Add(time.Minute)}
	})
	if updated.Err != nil || updated.WithdrawalGuard == nil || updated.WithdrawalGuard.ID != "aof-guard" {
		t.Fatal("new writer lost financial guard", updated.Err)
	}
	if err := r.ResetFlow(85001); err != nil {
		t.Fatal(err)
	}
	if got := r.Get(85001); got.WithdrawalGuard == nil || got.WithdrawalGuard.Status != "unknown" {
		t.Fatal("upgrade navigation cleared unknown protection")
	}
	// The old verifier expects the original short-lived draft; restore it without
	// touching the independent safety metadata, as a normal current-format write.
	if got := r.Update(85001, func(s *State) { s.Flow = "withdrawal"; s.Withdrawal = state.Withdrawal }); got.Err != nil {
		t.Fatal(got.Err)
	}
}
