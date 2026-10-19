package broadcast

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

type pacer struct {
	mu   sync.Mutex
	now  func() time.Time
	next time.Time
}

func (p *pacer) Allow(_ context.Context, _ string, rule ratelimit.Rule) (ratelimit.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now().Before(p.next) {
		return ratelimit.Decision{}, nil
	}
	p.next = p.now().Add(rule.Window)
	return ratelimit.Decision{Allowed: true}, nil
}
func engineFixture(t *testing.T, count int) (*Engine, *Memory, *time.Time, *[]int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	s := NewMemory()
	s.now = func() time.Time { return now }
	e := NewEngine(s, &pacer{now: s.now})
	e.Now, e.Admin = s.now, func(uid int64) bool { return uid == 42 }
	sent := []int64{}
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		sent = append(sent, uid)
		return Result{Kind: "sent"}
	}
	for i := 1; i <= count; i++ {
		if err := s.SetSubscribed(ctx, int64(i), true); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := s.Recipients(ctx)
	_, err := s.Update(ctx, func(state *State) error {
		state.Job = &Job{ID: rand.Text(), Worker: e.ID, OwnerID: 42, Text: "PRIVATE-ANNOUNCEMENT", Recipients: rows, Total: count, Status: "queued", ExpiresAt: now.Add(JobLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, s, &now, &sent
}
func tick(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func current(t *testing.T, s Store) *Job {
	t.Helper()
	state, err := s.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state.Job
}

func TestExplicitConsentVersionAndSnapshot(t *testing.T) {
	ctx := context.Background()
	s := NewMemory()
	if rows, _ := s.Recipients(ctx); len(rows) != 0 {
		t.Fatal("implicit subscription")
	}
	_ = s.SetSubscribed(ctx, 2, true)
	_ = s.SetSubscribed(ctx, 1, true)
	rows, _ := s.Recipients(ctx)
	_ = s.SetSubscribed(ctx, 1, true)
	if token, _ := s.Consent(ctx, 1); rows[0].ID != 1 || token != rows[0].Consent {
		t.Fatal("non-idempotent consent")
	}
	_ = s.SetSubscribed(ctx, 1, false)
	_ = s.SetSubscribed(ctx, 1, true)
	_ = s.RemoveIf(ctx, rows[0])
	if token, _ := s.Consent(ctx, 1); token == "" || token == rows[0].Consent {
		t.Fatal("old consent removed new subscription")
	}
}
func TestPacingSnapshotAndCompletion(t *testing.T) {
	e, s, now, sent := engineFixture(t, 4)
	ctx := context.Background()
	_ = s.SetSubscribed(ctx, 2, false)
	_ = s.SetSubscribed(ctx, 3, false)
	_ = s.SetSubscribed(ctx, 3, true) // new consent must not rejoin an old campaign
	_ = s.SetSubscribed(ctx, 5, true) // absent from confirmed snapshot
	completed := 0
	e.Audit = func(action, outcome string, _ int64, _ string) {
		if action == "broadcast.job" && outcome == "succeeded" {
			completed++
		}
	}
	tick(t, e)
	for range 20 {
		tick(t, e)
	}
	if len(*sent) != 1 {
		t.Fatal("rate limit bypassed", *sent)
	}
	for range 3 {
		*now = now.Add(time.Second)
		tick(t, e)
	}
	job := current(t, s)
	if job.Status != "completed" || job.Sent != 2 || job.Skipped != 2 || len(*sent) != 2 || (*sent)[1] != 4 || completed != 1 {
		t.Fatalf("bad completion: %+v sent=%v completed=%d", job, *sent, completed)
	}
	tick(t, e)
	if completed != 1 {
		t.Fatal("duplicate completion")
	}
}
func TestUnknownPausesAndResumeNeverReplays(t *testing.T) {
	e, s, now, sent := engineFixture(t, 2)
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		*sent = append(*sent, uid)
		return Result{Kind: "unknown"}
	}
	tick(t, e)
	*now = now.Add(2 * time.Second)
	tick(t, e)
	if j := current(t, s); j.Status != "paused" || j.Unknown != 1 || j.Next != 1 || len(*sent) != 1 {
		t.Fatal("ambiguous result replayed", j)
	}
	_, _ = s.Update(context.Background(), func(state *State) error { state.Job.Status = "queued"; return nil })
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		*sent = append(*sent, uid)
		return Result{Kind: "sent"}
	}
	tick(t, e)
	if j := current(t, s); j.Status != "completed" || j.Sent != 1 || j.Unknown != 1 || len(*sent) != 2 || (*sent)[1] != 2 {
		t.Fatal("resume replayed unknown recipient", j, *sent)
	}
}
func TestRetryAfterBoundedAndBlockedUnsubscribes(t *testing.T) {
	e, s, now, sent := engineFixture(t, 2)
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		*sent = append(*sent, uid)
		if uid == 1 {
			return Result{Kind: "throttled", RetryAfter: 30 * time.Second}
		}
		return Result{Kind: "blocked"}
	}
	for i := 0; i < 3; i++ {
		tick(t, e)
		*now = now.Add(29 * time.Second)
		tick(t, e)
		if len(*sent) != i+1 {
			t.Fatal("ignored Retry-After")
		}
		*now = now.Add(time.Second)
	}
	tick(t, e)
	if j := current(t, s); j.Status != "completed" || j.Failed != 2 || len(*sent) != 4 {
		t.Fatal("unbounded retry or wrong failure count", j, *sent)
	}
	if token, _ := s.Consent(context.Background(), 2); token != "" {
		t.Fatal("blocked user still subscribed")
	}
}

func TestFloodCooldownSurvivesReplacementCampaign(t *testing.T) {
	e, s, now, sent := engineFixture(t, 2)
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		*sent = append(*sent, uid)
		return Result{Kind: "throttled", RetryAfter: time.Minute}
	}
	tick(t, e)
	_, err := s.Update(context.Background(), func(state *State) error {
		state.Job.ID = rand.Text()
		state.Job.Status = "queued"
		state.Job.NotBefore = time.Time{}
		state.Job.Retries = 0
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Second)
	tick(t, e)
	if len(*sent) != 1 {
		t.Fatal("new campaign bypassed global flood cooldown")
	}
}

type failingStore struct {
	Store
	calls, failAt int
	commit        bool
}

func (s *failingStore) Update(ctx context.Context, fn func(*State) error) (State, error) {
	s.calls++
	if s.calls != s.failAt {
		return s.Store.Update(ctx, fn)
	}
	if s.commit {
		_, _ = s.Store.Update(ctx, fn)
	} else {
		state, _ := s.Store.View(ctx)
		_ = fn(&state)
	}
	return State{}, ErrUnavailable
}
func TestLostAcknowledgementsNeverAuthorizeReplay(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		for _, commit := range []bool{false, true} {
			t.Run(strings.Join([]string{string(rune('0' + failAt)), map[bool]string{false: "before", true: "after"}[commit]}, "-"), func(t *testing.T) {
				e, s, now, sent := engineFixture(t, 2)
				e.Store = &failingStore{Store: s, failAt: failAt, commit: commit}
				if err := e.Tick(context.Background()); !errors.Is(err, ErrUnavailable) {
					t.Fatal("missing storage error", err)
				}
				if failAt == 1 && len(*sent) != 0 {
					t.Fatal("callback execution used as commit acknowledgement")
				}
				if failAt == 1 && !commit {
					return
				} // no claim persisted, safe to try the same recipient
				*now = now.Add(Lease + time.Second)
				e.Store = s
				tick(t, e)
				for i, id := range *sent {
					if i > 0 && id == (*sent)[0] {
						t.Fatal("claimed recipient replayed")
					}
				}
				if job := current(t, s); job.Flight != nil {
					t.Fatal("expired flight not resolved")
				}
			})
		}
	}
}
func TestRestartAndRevokedAdminPause(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		e, s, _, sent := engineFixture(t, 2)
		if revoked {
			e.Admin = func(int64) bool { return false }
		} else {
			e.ID = rand.Text()
			if err := e.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		tick(t, e)
		if j := current(t, s); j.Status != "paused" || len(*sent) != 0 {
			t.Fatal("restart/revocation auto-sent", j)
		}
	}
}
func TestConcurrentTicksClaimOnlyOnce(t *testing.T) {
	e, s, _, _ := engineFixture(t, 2)
	entered, release := make(chan struct{}), make(chan struct{})
	e.Send = func(context.Context, int64, string) Result { close(entered); <-release; return Result{Kind: "sent"} }
	done := make(chan error, 1)
	go func() { done <- e.Tick(context.Background()) }()
	<-entered
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.Tick(context.Background()) }()
	}
	wg.Wait()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if j := current(t, s); j.Sent != 1 || j.Next != 1 {
		t.Fatal("duplicate claim", j)
	}
}

func TestThousandRecipientCampaignAndRegistryBound(t *testing.T) {
	e, s, now, sent := engineFixture(t, 1000)
	for range 1000 {
		tick(t, e)
		*now = now.Add(time.Second)
	}
	if j := current(t, s); j.Sent != 1000 || j.Status != "completed" || len(*sent) != 1000 {
		t.Fatal("bounded campaign incomplete", j)
	}
	seen := make(map[int64]bool)
	for _, id := range *sent {
		if seen[id] {
			t.Fatal("duplicate recipient")
		}
		seen[id] = true
	}
	for id := int64(1001); id <= RegistryLimit; id++ {
		if err := s.SetSubscribed(context.Background(), id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSubscribed(context.Background(), RegistryLimit+1, true); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded subscription registry", err)
	}
}
func TestStopDuringFlightAndExpiry(t *testing.T) {
	e, s, now, sent := engineFixture(t, 2)
	e.Send = func(_ context.Context, uid int64, _ string) Result {
		*sent = append(*sent, uid)
		_, _ = s.Update(context.Background(), func(state *State) error { state.Job.Status = "cancelled"; return nil })
		return Result{Kind: "sent"}
	}
	tick(t, e)
	*now = now.Add(time.Second)
	tick(t, e)
	if j := current(t, s); j.Status != "cancelled" || j.Sent != 1 || len(*sent) != 1 {
		t.Fatal("stop ignored", j)
	}
	*now = now.Add(JobLifetime)
	if j := current(t, s); j.Status != "expired" || j.Text != "" || len(j.Recipients) != 0 {
		t.Fatal("expired payload retained", j)
	}
}

type stopBeforeUpdate struct {
	Store
	once sync.Once
}

func (s *stopBeforeUpdate) Update(ctx context.Context, fn func(*State) error) (State, error) {
	s.once.Do(func() {
		_, _ = s.Store.Update(ctx, func(state *State) error { state.Job.Status = "cancelled"; return nil })
	})
	return s.Store.Update(ctx, fn)
}
func TestConcurrentStopCannotBeRevivedByPauseOrCompletion(t *testing.T) {
	for _, mode := range []string{"pause", "complete"} {
		t.Run(mode, func(t *testing.T) {
			e, s, _, sent := engineFixture(t, 1)
			if mode == "pause" {
				e.Admin = func(int64) bool { return false }
			} else {
				_, _ = s.Update(context.Background(), func(state *State) error { state.Job.Next = 1; return nil })
			}
			e.Store = &stopBeforeUpdate{Store: s}
			if err := e.Tick(context.Background()); !errors.Is(err, ErrConflict) {
				t.Fatal("stale state accepted", err)
			}
			if j := current(t, s); j.Status != "cancelled" || len(*sent) != 0 {
				t.Fatal("stop revived", j)
			}
		})
	}
}

func TestRunDispatchesAndStops(t *testing.T) {
	e, s, _, _ := engineFixture(t, 1)
	completed := make(chan struct{})
	e.Audit = func(action, outcome string, _ int64, _ string) {
		if action == "broadcast.job" && outcome == "succeeded" {
			close(completed)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not dispatch")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not shut down")
	}
	if j := current(t, s); j.Sent != 1 || j.Status != "completed" {
		t.Fatal("shutdown changed completed job", j)
	}
}
func TestShutdownDuringSendKeepsUnknownProtection(t *testing.T) {
	e, s, _, _ := engineFixture(t, 2)
	entered := make(chan struct{})
	e.Send = func(ctx context.Context, _ int64, _ string) Result {
		close(entered)
		<-ctx.Done()
		return Result{Kind: "unknown"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("send not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stuck")
	}
	if j := current(t, s); j.Status != "paused" || j.Unknown != 1 || j.Next != 1 || j.Flight != nil {
		t.Fatal("shutdown lost in-flight guard", j)
	}
}
func TestTextBoundsAndPayload(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("😀", 1501), "a\x00b", "a\u202eb", string([]byte{0xff})} {
		if ValidText(text) {
			t.Fatal("invalid text accepted")
		}
	}
	if !ValidText(strings.Repeat("😀", 1500)) || !ValidText("plain <b>& text\nnext") || !strings.Contains(Payload("x"), "/unsubscribe") {
		t.Fatal("valid text/mandatory footer")
	}
}
