package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

const redisTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func persistentWithdrawalFixture(t *testing.T) (*App, *formFixture, func() *App, *miniredis.Miniredis) {
	t.Helper()
	app, fixture := newWithdrawalFixture(t)
	server := miniredis.RunT(t)
	prefix := "telegram-test:{" + rand.Text() + "}"
	connect := func() session.Store {
		r, err := session.NewRedis("redis://"+server.Addr(), prefix, "zh-CN", redisTestKey)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		return r
	}
	app.sessions = connect()
	app.ConfigureOperations(nil, ratelimit.DefaultPolicy(), app.logger)
	restart := func() *App {
		fresh := &App{bot: app.bot, api: app.api, sessions: connect(), logger: app.logger, config: app.botConfig(), runCtx: context.Background(), polls: make(map[uint]*activePaymentPoll)}
		fresh.ConfigureOperations(nil, ratelimit.DefaultPolicy(), fresh.logger)
		return fresh
	}
	return app, fixture, restart, server
}

func TestRedisWithdrawalDraftResumesAndOldConfirmStaysConsumed(t *testing.T) {
	app, fixture, restart, _ := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "PRIVATE-RESTART-ACCOUNT")
	draft := app.sessions.Get(42).Withdrawal
	_ = app.sessions.Close()
	app = restart()
	if got := app.sessions.Get(42).Withdrawal; got == nil || got.ID != draft.ID || got.Account != draft.Account {
		t.Fatal("restart lost review draft")
	}
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	_ = app.sessions.Close()
	app = restart()
	for range 5 {
		formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	}
	if len(fixture.affiliate.withdrawalRequests) != 1 {
		t.Fatal("old confirmation submitted twice across restarts")
	}
}

func TestRedisUnknownWithdrawalSurvivesRestartAndRequiresReview(t *testing.T) {
	app, fixture, restart, _ := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "PRIVATE-UNKNOWN-ACCOUNT")
	draft := app.sessions.Get(42).Withdrawal
	fixture.affiliate.withdrawalFailureCode, fixture.affiliate.withdrawalFailureStatus = "unavailable", 503
	withdrawalAction(app, "confirm")
	_ = app.sessions.Close()
	app = restart()
	state := app.sessions.Get(42)
	if state.Err != nil || state.Withdrawal != nil || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "unknown" {
		t.Fatal("unknown-result guard lost on restart")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	formCallback(app, 42, 20, "affapply")
	formCallback(app, 42, 0, "wdack:"+draft.ID)
	if app.sessions.Get(42).WithdrawalGuard == nil || len(fixture.affiliate.withdrawalRequests) != 1 {
		t.Fatal("restart/navigation bypassed protection")
	}
	formCallback(app, 42, 200, "affwithdraws:1")
	// Even the reviewed history message ID persists across another restart.
	app = restart()
	formCallback(app, 42, 201, "wdack:"+draft.ID)
	if app.sessions.Get(42).WithdrawalGuard == nil {
		t.Fatal("stale acknowledgement unlocked")
	}
	formCallback(app, 42, 200, "wdack:"+draft.ID)
	if app.sessions.Get(42).WithdrawalGuard != nil || len(fixture.affiliate.withdrawalRequests) != 1 {
		t.Fatal("recovery retried financial request")
	}
}

func TestRedisStalePostingLeaseNeverResendsWithdrawal(t *testing.T) {
	for _, status := range []string{"checking", "posting"} {
		t.Run(status, func(t *testing.T) {
			app, fixture, restart, _ := persistentWithdrawalFixture(t)
			readyWithdrawal(app, "PRIVATE-CRASH-ACCOUNT")
			draft := app.sessions.Get(42).Withdrawal
			state := app.sessions.Update(42, func(s *session.State) {
				s.Withdrawal.Busy = true
				s.WithdrawalGuard = &session.WithdrawalGuard{ID: draft.ID, Status: status, LeaseUntil: time.Now().Add(-time.Second)}
			})
			if state.Err != nil {
				t.Fatal(state.Err)
			}
			app = restart()
			formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
			formCallback(app, 42, 20, "affapply")
			state = app.sessions.Get(42)
			if state.Withdrawal != nil || state.WithdrawalGuard == nil || state.WithdrawalGuard.Status != "unknown" || len(fixture.affiliate.withdrawalRequests) != 0 {
				t.Fatal("crashed operation auto-retried or unlocked")
			}
		})
	}
}

func TestRedisTwoClientsConcurrentConfirmSendOneRequest(t *testing.T) {
	app, fixture, restart, _ := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "CONCURRENT-ACCOUNT")
	draft := app.sessions.Get(42).Withdrawal
	other := restart()
	var wait sync.WaitGroup
	for i := range 24 {
		worker := app
		if i%2 == 0 {
			worker = other
		}
		wait.Go(func() { formCallback(worker, 42, draft.MessageID, "wd:"+draft.ID+":confirm") })
	}
	wait.Wait()
	if len(fixture.affiliate.withdrawalRequests) != 1 {
		t.Fatal("concurrent confirmations did not produce one request", len(fixture.affiliate.withdrawalRequests))
	}
}

// Simulate failed storage acknowledgements after callbacks run, optionally after
// the write actually committed. Application-local flags alone must not authorize POST.
type failingSessionStore struct {
	session.Store
	failAt, calls int
	commit        bool
}

func (s *failingSessionStore) Update(uid int64, update func(*session.State)) session.State {
	s.calls++
	if s.calls != s.failAt {
		return s.Store.Update(uid, update)
	}
	var state session.State
	if s.commit {
		state = s.Store.Update(uid, update)
	} else {
		state = s.Store.Get(uid)
		update(&state)
	}
	state.Err = session.ErrUnavailable
	return state
}

func TestWithdrawalStorageWriteFailureBeforePOSTNeverSubmits(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("write-%d/committed-%t", failAt, committed), func(t *testing.T) {
				app, fixture, restart, _ := persistentWithdrawalFixture(t)
				readyWithdrawal(app, "WRITE-FAILURE-ACCOUNT")
				draft := app.sessions.Get(42).Withdrawal
				app.sessions = &failingSessionStore{Store: app.sessions, failAt: failAt, commit: committed}
				app.submitWithdrawal(context.Background(), 42, draft)
				if len(fixture.affiliate.withdrawalRequests) != 0 {
					t.Fatal("failed session write authorized POST")
				}
				if failAt == 2 || committed {
					app = restart()
					formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
					formCallback(app, 42, 20, "affapply")
					if len(fixture.affiliate.withdrawalRequests) != 0 || app.sessions.Get(42).WithdrawalGuard == nil {
						t.Fatal("ambiguous commit guard lost after restart")
					}
				}
			})
		}
	}
}

func TestWithdrawalCompletedButCleanupFailedStaysProtected(t *testing.T) {
	app, fixture, restart, _ := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "SUCCESS-LOST-CLEANUP")
	draft := app.sessions.Get(42).Withdrawal
	app.sessions = &failingSessionStore{Store: app.sessions, failAt: 3}
	app.submitWithdrawal(context.Background(), 42, draft)
	app = restart()
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	formCallback(app, 42, 20, "affapply")
	if len(fixture.affiliate.withdrawalRequests) != 1 || app.sessions.Get(42).WithdrawalGuard == nil {
		t.Fatal("failed cleanup permitted duplicate request")
	}
}

func TestRedisOutageStopsIncomingWritesAndHealth(t *testing.T) {
	app, fixture, restart, server := persistentWithdrawalFixture(t)
	readyWithdrawal(app, "OUTAGE-ACCOUNT")
	draft := app.sessions.Get(42).Withdrawal
	server.SetError("ERR simulated Redis outage")
	formCallback(app, 42, draft.MessageID, "wd:"+draft.ID+":confirm")
	app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
	if app.SessionHealth() == nil || len(fixture.affiliate.withdrawalRequests) != 0 {
		t.Fatal("Redis outage did not stop operation")
	}
	server.SetError("")
	app = restart()
	if app.sessions.Get(42).Withdrawal == nil {
		t.Fatal("outage discarded draft")
	}
}

func TestGiftCardFailedClaimDoesNotRedeem(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	formCallback(app, 42, 10, "giftcard")
	input := app.sessions.Get(42).GiftCard
	app.sessions = &failingSessionStore{Store: app.sessions, failAt: 1}
	app.handleUpdate(context.Background(), nil, giftCardMessage("GIFT50", input.PromptID, 42, 42))
	if len(fixture.giftCodes) != 0 {
		t.Fatal("failed claim authorized redemption")
	}
}

func TestCheckoutFailedClaimDoesNotCreateOrder(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	formCallback(app, 42, 10, "buy:3:31:1")
	draft := app.sessions.Get(42).Purchase
	app.sessions = &failingSessionStore{Store: app.sessions, failAt: 1}
	app.confirmForm(context.Background(), &models.User{ID: 42}, draft)
	if len(fixture.orders) != 0 {
		t.Fatal("failed claim authorized order")
	}
}

func TestRechargeFailedSessionWriteDoesNotCreatePayment(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			app, fixture := newFormFixture(t, nil)
			app.askRechargeAmount(context.Background(), 42, 42, false, 0)
			rechargePick(app, "100.00")
			app.sessions = &failingSessionStore{Store: app.sessions, failAt: failAt}
			rechargePay(app, 1)
			if len(fixture.rechargeRequests) != 0 {
				t.Fatal("failed session write authorized recharge")
			}
		})
	}
}
