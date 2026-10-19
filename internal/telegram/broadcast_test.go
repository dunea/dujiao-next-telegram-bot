package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	tgbot "github.com/go-telegram/bot"
)

func broadcastState(t *testing.T, app *App) broadcast.State {
	t.Helper()
	s, err := app.broadcastStore.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func broadcastReply(app *App, draft *broadcast.Draft, text string) {
	app.handleUpdate(context.Background(), nil, giftCardMessage(text, draft.MessageID, 42, 42))
}
func broadcastAction(app *App, draft *broadcast.Draft, action string) {
	formCallback(app, 42, draft.MessageID, "bc:"+draft.ID+":"+action)
}
func broadcastPreview(t *testing.T, app *App) *broadcast.Draft {
	t.Helper()
	app.handleUpdate(context.Background(), nil, giftCardMessage("/broadcast", 0, 42, 42))
	draft := broadcastState(t, app).Drafts[42]
	if draft == nil || draft.Stage != "writing" || draft.MessageID == 0 {
		t.Fatal("no writing prompt", draft)
	}
	broadcastReply(app, draft, "PRIVATE-ANNOUNCEMENT <b>plain text</b>")
	draft = broadcastState(t, app).Drafts[42]
	if draft.Stage != "preview" || draft.MessageID == 0 {
		t.Fatal("no preview", draft)
	}
	return draft
}
func broadcastConfirm(t *testing.T, app *App) *broadcast.Draft {
	t.Helper()
	if err := app.broadcastStore.SetSubscribed(context.Background(), 43, true); err != nil {
		t.Fatal(err)
	}
	draft := broadcastPreview(t, app)
	broadcastAction(app, draft, "review")
	draft = broadcastState(t, app).Drafts[42]
	if draft.Stage != "confirm" || draft.MessageID == 0 || len(draft.Recipients) != 1 {
		t.Fatal("no final review", draft)
	}
	return draft
}

func TestBroadcastPreviewConfirmationAndSingleDelivery(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	var audit bytes.Buffer
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), slog.New(slog.NewJSONHandler(&audit, nil)))
	draft := broadcastConfirm(t, app)
	if s := broadcastState(t, app); s.Job != nil {
		t.Fatal("preview/review started job")
	}
	fixture.mu.Lock()
	for _, uid := range fixture.chatIDs {
		if uid != "42" {
			t.Fatal("preview sent to subscriber")
		}
	}
	fixture.mu.Unlock()
	// A second subscriber after the final review is not added to this job.
	_ = app.broadcastStore.SetSubscribed(context.Background(), 44, true)
	broadcastAction(app, draft, "send")
	job := broadcastState(t, app).Job
	if job == nil || job.Status != "queued" || job.Total != 1 || job.Recipients[0].ID != 43 {
		t.Fatal("wrong confirmed audience", job)
	}
	for range 3 {
		broadcastAction(app, draft, "send")
	}
	if got := broadcastState(t, app).Job; got.ID != job.ID || got.Next != 0 {
		t.Fatal("duplicate confirm mutated job")
	}
	if err := app.broadcastEngine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := broadcastState(t, app).Job; got.Status != "completed" || got.Sent != 1 {
		t.Fatal("job not delivered", got)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	announcements := 0
	for i, text := range fixture.texts {
		if strings.HasPrefix(text, "📣 商店公告") {
			announcements++
			if fixture.chatIDs[i] != "43" || !strings.Contains(fixture.markups[i], "bcunsubscribe") || !strings.Contains(text, "/unsubscribe") {
				t.Fatal("missing recipient/footer/unsubscribe")
			}
		}
	}
	if announcements != 1 {
		t.Fatal("duplicate delivery", announcements)
	}
	if strings.Contains(audit.String(), "PRIVATE-ANNOUNCEMENT") || !strings.Contains(audit.String(), "broadcast.delivery") {
		t.Fatal("audit content leak or missing event", audit.String())
	}
}
func TestBroadcastAuthorizationMessageBindingAndEdit(t *testing.T) {
	app, _ := newFormFixture(t, nil)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	app.handleUpdate(context.Background(), nil, giftCardMessage("/broadcast", 0, 43, 43))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/broadcast", 0, 42, -10042))
	if s := broadcastState(t, app); len(s.Drafts) != 0 {
		t.Fatal("unauthorized draft")
	}
	draft := broadcastConfirm(t, app)
	formCallback(app, 43, draft.MessageID, "bc:"+draft.ID+":send")
	formCallback(app, 42, draft.MessageID+1, "bc:"+draft.ID+":send")
	if broadcastState(t, app).Job != nil {
		t.Fatal("forged confirmation")
	}
	broadcastAction(app, draft, "edit")
	next := broadcastState(t, app).Drafts[42]
	if next.ID == draft.ID || next.Stage != "writing" || next.Text != "" || len(next.Recipients) != 0 {
		t.Fatal("edit didn't invalidate confirmation")
	}
	broadcastAction(app, draft, "send")
	if broadcastState(t, app).Job != nil {
		t.Fatal("old confirm survived edit")
	}
	broadcastReply(app, next, strings.Repeat("😀", 1501))
	if got := broadcastState(t, app).Drafts[42]; got.Stage != "writing" {
		t.Fatal("oversized text accepted")
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", 0, 42, 42))
	if len(broadcastState(t, app).Drafts) != 0 {
		t.Fatal("cancel left active draft")
	}
}
func TestBroadcastEmptyAndOversizedAudienceRefused(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			app, _ := newFormFixture(t, nil)
			app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
			app.ConfigureBroadcast(time.Second, 1)
			for i := 0; i < count; i++ {
				_ = app.broadcastStore.SetSubscribed(context.Background(), int64(43+i), true)
			}
			draft := broadcastPreview(t, app)
			broadcastAction(app, draft, "send") // no shortcut around review
			broadcastAction(app, draft, "review")
			if s := broadcastState(t, app); s.Job != nil || s.Drafts[42].Stage != "preview" {
				t.Fatal("invalid audience allowed", s)
			}
		})
	}
}

func TestBroadcastNavigationInvalidatesDraft(t *testing.T) {
	app, _ := newFormFixture(t, nil)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	draft := broadcastConfirm(t, app)
	formCallback(app, 42, 1, "home")
	broadcastAction(app, draft, "send")
	if s := broadcastState(t, app); s.Job != nil || s.Drafts[42] != nil {
		t.Fatal("shopping navigation left old confirmation valid")
	}
}

func TestBroadcastTelegramErrorClassificationAndSafeParameters(t *testing.T) {
	for _, tc := range []struct {
		code  int
		kind  string
		retry int
	}{{200, "sent", 0}, {429, "throttled", 17}, {403, "blocked", 0}, {400, "failed", 0}, {404, "failed", 0}, {500, "unknown", 0}} {
		t.Run(tc.kind+string(rune(tc.code)), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_ = r.ParseMultipartForm(1 << 20)
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
				var preview map[string]bool
				_ = json.Unmarshal([]byte(r.FormValue("link_preview_options")), &preview)
				if r.FormValue("parse_mode") != "" || r.FormValue("allow_paid_broadcast") == "true" || !preview["is_disabled"] || !strings.Contains(r.FormValue("reply_markup"), "bcunsubscribe") {
					t.Error("unsafe send parameters")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.code == 200 {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1, "chat": map[string]any{"id": 43, "type": "private"}}})
					return
				}
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": tc.code, "description": "PRIVATE-TELEGRAM-ERROR", "parameters": map[string]int{"retry_after": tc.retry}})
			}))
			defer server.Close()
			app, _ := newFormFixture(t, nil)
			b, err := tgbot.New("999:test", tgbot.WithSkipGetMe(), tgbot.WithServerURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			app.bot = b
			result := app.broadcastEngine.Send(context.Background(), 43, broadcast.Payload("<b>plain</b>"))
			if result.Kind != tc.kind || requests != 1 || (tc.retry > 0 && result.RetryAfter != time.Duration(tc.retry)*time.Second) {
				t.Fatal("wrong classification or implicit retry", result, requests)
			}
		})
	}
}
func TestBroadcastConcurrentFinalConfirmation(t *testing.T) {
	app, _ := newFormFixture(t, nil)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	draft := broadcastConfirm(t, app)
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() { defer wg.Done(); broadcastAction(app, draft, "send") }()
	}
	wg.Wait()
	s := broadcastState(t, app)
	if s.Job == nil || s.Job.ID != draft.ID || s.Job.Total != 1 || s.Job.Next != 0 || s.Drafts[42] != nil {
		t.Fatal("concurrent confirm not single-use", s)
	}
}
func TestUnsubscribeIndependentOfNormalFlowAndRateBudget(t *testing.T) {
	app, _ := newFormFixture(t, nil)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	ctx := context.Background()
	app.sessions.Update(42, func(s *session.State) {
		s.Flow = "withdrawal"
		s.WithdrawalGuard = &session.WithdrawalGuard{ID: "guard", Status: "unknown"}
	})
	app.handleUpdate(ctx, nil, giftCardMessage("/subscribe", 0, 42, 42))
	for range 100 {
		app.allow(ctx, 42, "telegram-user", app.limits.User)
	}
	app.handleUpdate(ctx, nil, giftCardMessage("/unsubscribe", 0, 42, 42))
	if token, _ := app.broadcastStore.Consent(ctx, 42); token != "" {
		t.Fatal("unsubscribe blocked by normal rate bucket")
	}
	if s := app.sessions.Get(42); s.WithdrawalGuard == nil || s.Flow != "withdrawal" {
		t.Fatal("unsubscribe changed financial state")
	}
	// Inline unsubscribe works even when normal navigation is exhausted.
	_ = app.broadcastStore.SetSubscribed(ctx, 42, true)
	formCallback(app, 42, 101, "bcunsubscribe")
	if token, _ := app.broadcastStore.Consent(ctx, 42); token != "" {
		t.Fatal("inline unsubscribe failed")
	}
}
func TestBroadcastRedisRestartRequiresOwnerResume(t *testing.T) {
	app, _, restart, _ := persistentWithdrawalFixture(t)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	draft := broadcastConfirm(t, app)
	broadcastAction(app, draft, "send")
	fresh := restart()
	fresh.ConfigureOperations([]int64{42, 44}, ratelimit.DefaultPolicy(), fresh.logger)
	if err := fresh.broadcastEngine.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := broadcastState(t, fresh).Job; j.Status != "paused" || j.Next != 0 {
		t.Fatal("restart auto-resumed", j)
	}
	fresh.broadcastJobAction(context.Background(), 44, draft.ID, "resume")
	if broadcastState(t, fresh).Job.Status != "paused" {
		t.Fatal("non-owner resumed job")
	}
	formCallback(fresh, 42, 101, "bcjob:"+draft.ID+":resume")
	if j := broadcastState(t, fresh).Job; j.Status != "queued" || j.Worker != fresh.broadcastEngine.ID {
		t.Fatal("owner could not resume", j)
	}
	formCallback(fresh, 42, 101, "bcjob:"+draft.ID+":stop")
	if broadcastState(t, fresh).Job.Status != "cancelled" {
		t.Fatal("stop failed")
	}
}

type broadcastAckFailure struct{ broadcast.Store }

func (s broadcastAckFailure) Update(ctx context.Context, fn func(*broadcast.State) error) (broadcast.State, error) {
	_, err := s.Store.Update(ctx, fn)
	if err != nil {
		return broadcast.State{}, err
	}
	return broadcast.State{}, broadcast.ErrUnavailable
}
func TestBroadcastDraftBindingLostAckFailsClosed(t *testing.T) {
	app, _ := newFormFixture(t, nil)
	app.ConfigureOperations([]int64{42}, ratelimit.DefaultPolicy(), app.logger)
	draft := broadcastConfirm(t, app)
	base := app.broadcastStore
	app.broadcastStore = broadcastAckFailure{base}
	// A committed final confirmation with a lost response is still single-use;
	// the UI must not execute a second write or claim it definitely failed.
	broadcastAction(app, draft, "send")
	app.broadcastStore = base
	broadcastAction(app, draft, "send")
	if j := broadcastState(t, app).Job; j == nil || j.ID != draft.ID || j.Next != 0 {
		t.Fatal("lost response duplicated campaign", j)
	}
	if err := app.broadcastEngine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if j := broadcastState(t, app).Job; j.Status != "prepared" || j.Sent != 0 {
		t.Fatal("lost prepare acknowledgement authorized sending", j)
	}
}
