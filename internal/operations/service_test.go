package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func operationsFixture() (*Service, *bytes.Buffer) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	service := New(logger, logger, []int64{900001, 900002}, nil)
	service.retryDelays = []time.Duration{0, 0}
	service.sendInterval = 0
	return service, &output
}

func TestAuditCorrelationAndAllowlist(t *testing.T) {
	s, output := operationsFixture()
	op := s.Begin(Withdrawal, 42, 0)
	op.End(Unknown, 0)
	s.Record(Event{Action: Action("PRIVATE_ACCOUNT"), Outcome: Succeeded})
	s.Record(Event{Action: Order, Outcome: Outcome("SECRET_CODE")})
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || strings.Contains(output.String(), "PRIVATE") || strings.Contains(output.String(), "SECRET") {
		t.Fatal("untrusted text entered audit")
	}
	var first, last map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &last)
	if first["event_id"] == "" || first["event_id"] != last["event_id"] || first["outcome"] != "attempt" || last["outcome"] != "unknown" {
		t.Fatal("missing correlated attempt/outcome")
	}
}

func TestNotificationDedupAndDistinctResourceEvents(t *testing.T) {
	s, _ := operationsFixture()
	s.send = func(context.Context, int64, string) error { return nil }
	var wait sync.WaitGroup
	for range 32 {
		wait.Go(func() { s.Record(Event{Action: CallbackPaid, Outcome: Succeeded, UserID: 42, ResourceID: 7}) })
	}
	wait.Wait()
	if len(s.queue) != 1 {
		t.Fatal("duplicate callback summaries queued", len(s.queue))
	}
	s.Record(Event{Action: CallbackPaid, Outcome: Succeeded, UserID: 42, ResourceID: 8})
	s.Record(Event{Action: CallbackRecharge, Outcome: Succeeded, UserID: 42, Dedupe: sha256.Sum256([]byte("RECHARGE1"))})
	s.Record(Event{Action: CallbackRecharge, Outcome: Succeeded, UserID: 42, Dedupe: sha256.Sum256([]byte("RECHARGE2"))})
	if len(s.queue) != 4 {
		t.Fatal("distinct resources incorrectly merged")
	}
}

func TestNotificationRetriesNeverExposeErrorsAndContinueOtherAdmins(t *testing.T) {
	s, output := operationsFixture()
	calls := map[int64]int{}
	s.send = func(ctx context.Context, uid int64, text string) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded send")
		}
		if !strings.Contains(text, "withdrawal.apply") || !strings.Contains(text, "核对") {
			t.Fatal("missing safe summary")
		}
		calls[uid]++
		if uid == 900001 {
			return errors.New("token=SECRET account=PRIVATE")
		}
		return nil
	}
	s.Record(Event{Action: Withdrawal, Outcome: Unknown, UserID: 42})
	s.deliver(context.Background(), <-s.queue)
	if calls[900001] != 3 || calls[900002] != 1 {
		t.Fatal("retry/fanout", calls)
	}
	if strings.Contains(output.String(), "SECRET") || strings.Contains(output.String(), "PRIVATE") || !strings.Contains(output.String(), `"outcome":"failed"`) || !strings.Contains(output.String(), `"outcome":"sent"`) {
		t.Fatal("delivery error leaked or missing audit")
	}
}

func TestNotificationQueueBoundAndCancelledShutdown(t *testing.T) {
	s, output := operationsFixture()
	s.send = func(context.Context, int64, string) error { t.Fatal("send after cancellation"); return nil }
	for i := range 140 {
		s.Record(Event{Action: Order, Outcome: Succeeded, UserID: 42, ResourceID: uint(i + 1)})
	}
	if len(s.queue) != 128 || !strings.Contains(output.String(), "dropped_queue_full") {
		t.Fatal("unbounded queue or missing drop audit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	if len(s.queue) != 0 || !strings.Contains(output.String(), "interrupted") {
		t.Fatal("shutdown silently lost queued notifications")
	}
}

func TestAdminIDsRequirePrivateAllowlist(t *testing.T) {
	for _, raw := range []string{"0", "-100123", "@owner", "42,", "42 SECRET", "9223372036854775808"} {
		if _, err := ParseAdminIDs(raw); err == nil {
			t.Fatal("accepted invalid IDs", raw)
		}
	}
	ids, err := ParseAdminIDs("42, 43,42")
	if err != nil || len(ids) != 2 || ids[1] != 43 {
		t.Fatal(ids, err)
	}
	s, _ := operationsFixture()
	if !s.IsAdmin(900001) || s.IsAdmin(42) || s.IsAdmin(-900001) {
		t.Fatal("bad admin authorization")
	}
}
