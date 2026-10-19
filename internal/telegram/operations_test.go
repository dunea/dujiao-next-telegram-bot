package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

// A synchronized sink also permits checking logs while the admin worker runs.
type auditBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *auditBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *auditBuffer) text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func TestWithdrawalAuditAndAdminSummaryContainNoReceivingDetails(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	var audit auditBuffer
	app.ConfigureOperations([]int64{900001}, ratelimit.DefaultPolicy(), slog.New(slog.NewJSONHandler(&audit, nil)))
	fixture.adminSent = make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.ops.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	readyWithdrawal(app, "PRIVATE_RECEIVING_ACCOUNT_123")
	withdrawalAction(app, "confirm")
	select {
	case text := <-fixture.adminSent:
		if !strings.Contains(text, "withdrawal.apply") || !strings.Contains(text, "succeeded") || strings.Contains(text, "PRIVATE") {
			t.Fatal("unsafe/incomplete admin summary", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no admin summary")
	}
	var attemptID, terminalID string
	for _, line := range strings.Split(strings.TrimSpace(audit.text()), "\n") {
		var event struct {
			EventID string `json:"event_id"`
			Action  string `json:"action"`
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Action == "withdrawal.apply" {
			if event.Outcome == "attempt" {
				attemptID = event.EventID
			}
			if event.Outcome == "succeeded" {
				terminalID = event.EventID
			}
		}
	}
	if attemptID == "" || attemptID != terminalID || strings.Contains(audit.text(), "PRIVATE") || len(fixture.affiliate.withdrawalRequests) != 1 {
		t.Fatal("missing/unsafe audit or repeated request")
	}
}

func TestWriteRateLimitPreservesWithdrawalDraftAndCancelStillWorks(t *testing.T) {
	app, fixture := newWithdrawalFixture(t)
	readyWithdrawal(app, "SAFE_TEST_ACCOUNT")
	policy := ratelimit.DefaultPolicy()
	policy.Write.Limit = 1
	policy.User.Limit = 1
	var audit auditBuffer
	app.ConfigureOperations(nil, policy, slog.New(slog.NewJSONHandler(&audit, nil)))
	if !app.allowWrite(context.Background(), 42, 42, operations.Payment) {
		t.Fatal("first write blocked")
	}
	draft := app.sessions.Get(42).Withdrawal
	app.submitWithdrawal(context.Background(), 42, draft)
	if state := app.sessions.Get(42); state.Withdrawal == nil || state.Withdrawal.Busy || state.WithdrawalGuard != nil || len(fixture.affiliate.withdrawalRequests) != 0 {
		t.Fatal("rate denial consumed/submitted draft")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/whoami", 0, 42, 42))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/help", 0, 42, 42))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
	if app.sessions.Get(42).Withdrawal != nil || !strings.Contains(audit.text(), "rate_limited") {
		t.Fatal("cancel trapped behind exhausted user bucket")
	}
}

func TestSharedWriteLimitStopsRechargeAfterGiftCard(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	policy := ratelimit.DefaultPolicy()
	policy.Write.Limit = 1
	app.ConfigureOperations(nil, policy, app.logger)
	formCallback(app, 42, 10, "giftcard")
	input := app.sessions.Get(42).GiftCard
	app.handleUpdate(context.Background(), nil, giftCardMessage("GIFT50", input.PromptID, 42, 42))
	app.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(app, "100.00")
	rechargePay(app, 1)
	if len(fixture.giftCodes) != 1 || len(fixture.rechargeRequests) != 0 || app.sessions.Get(42).Recharge == nil {
		t.Fatal("separate actions bypassed shared write budget")
	}
}

func TestAdminCommandRequiresConfiguredPrivateSender(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	var audit auditBuffer
	app.ConfigureOperations([]int64{900001}, ratelimit.DefaultPolicy(), slog.New(slog.NewJSONHandler(&audit, nil)))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/admin", 0, 42, 42))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/admin", 0, 900001, -100))
	if len(fixture.texts) != 0 || strings.Contains(audit.text(), "admin.test") {
		t.Fatal("unauthorized/group admin command accepted")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/admin", 0, 900001, 900001))
	if !strings.Contains(audit.text(), "admin.test") || len(fixture.texts) != 1 {
		t.Fatal("allowlisted admin command not recorded")
	}
}

func TestRedisLimitFailureCannotSubmitOrClearWithdrawalGuard(t *testing.T) {
	app, fixture, _, server := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "PRIVATE_RATE_REDIS_ACCOUNT")
	draft := app.sessions.Get(42).Withdrawal
	server.SetError("ERR private=secret")
	app.submitWithdrawal(context.Background(), 42, draft)
	server.SetError("")
	if len(fixture.affiliate.withdrawalRequests) != 0 || app.sessions.Get(42).Withdrawal == nil {
		t.Fatal("Redis limiter failure authorized financial action")
	}
}
