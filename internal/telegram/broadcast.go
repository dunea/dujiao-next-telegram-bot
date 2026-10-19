package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (a *App) initBroadcast() {
	if provider, ok := a.sessions.(interface{ BroadcastStore() broadcast.Store }); ok {
		a.broadcastStore = provider.BroadcastStore()
	}
	if a.broadcastStore == nil {
		a.broadcastStore = broadcast.NewMemory()
	}
	a.broadcastMax = 1000
	a.broadcastEngine = broadcast.NewEngine(a.broadcastStore, a.limiter)
	a.broadcastEngine.Admin = a.ops.IsAdmin
	a.broadcastEngine.Audit = func(action, outcome string, uid int64, id string) {
		mapped := operations.Outcome(outcome)
		switch outcome {
		case "sent":
			mapped = operations.Succeeded
		case "failed", "blocked":
			mapped = operations.Rejected
		case "throttled":
			mapped = operations.Limited
		}
		a.ops.RecordBroadcast(operations.Action(action), mapped, uid, id)
	}
	a.broadcastEngine.Send = func(ctx context.Context, uid int64, text string) broadcast.Result {
		_, err := a.broadcastPlain(ctx, uid, text, broadcastUnsubscribeKeyboard())
		if err == nil {
			return broadcast.Result{Kind: "sent"}
		}
		var flood *tgbot.TooManyRequestsError
		if errors.As(err, &flood) {
			if flood.RetryAfter < 0 || flood.RetryAfter > 86400 {
				return broadcast.Result{Kind: "unknown"}
			}
			return broadcast.Result{Kind: "throttled", RetryAfter: time.Duration(max(1, flood.RetryAfter)) * time.Second}
		}
		if errors.Is(err, tgbot.ErrorForbidden) {
			return broadcast.Result{Kind: "blocked"}
		}
		if errors.Is(err, tgbot.ErrorBadRequest) || errors.Is(err, tgbot.ErrorNotFound) {
			return broadcast.Result{Kind: "failed"}
		}
		return broadcast.Result{Kind: "unknown"}
	}
}
func (a *App) ConfigureBroadcast(interval time.Duration, maxRecipients int) {
	if interval >= time.Second && interval <= 10*time.Second {
		a.broadcastEngine.Interval = interval
	}
	if maxRecipients >= 1 && maxRecipients <= broadcast.RegistryLimit {
		a.broadcastMax = maxRecipients
	}
}
func broadcastUnsubscribeKeyboard() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "退订公告 / Unsubscribe", CallbackData: "bcunsubscribe"}}}}
}
func (a *App) broadcastPlain(ctx context.Context, uid int64, text string, markup models.ReplyMarkup) (*models.Message, error) {
	disabled := true
	return a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: uid, Text: text, ReplyMarkup: markup, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled}})
}
func (a *App) broadcastFailure(ctx context.Context, uid int64, err error) {
	text := "公告操作结果未获确认。请先使用 /broadcast_status 核对任务状态，不要重复提交旧按钮。"
	if errors.Is(err, broadcast.ErrConflict) {
		text = "这组公告按钮已失效或任务状态已变化。请使用 /broadcast 或 /broadcast_status 查看，勿重复确认旧消息。"
	}
	if errors.Is(err, broadcast.ErrCapacity) {
		text = "订阅人数或并行草稿超过轻量群发上限，本次没有开始群发。请联系管理员调整配置。"
	}
	a.logger.Warn("broadcast action not committed")
	_, _ = a.broadcastPlain(ctx, uid, text, nil)
}
func (a *App) broadcastAdmin(ctx context.Context, chatID, uid int64) bool {
	if chatID == uid && a.ops.IsAdmin(uid) {
		return true
	}
	a.ops.Record(operations.Event{Action: operations.Access, Outcome: operations.Blocked, UserID: uid})
	return false
}

func (a *App) handleBroadcastCommand(ctx context.Context, m *models.Message, command string) bool {
	switch command {
	case "/subscribe", "/unsubscribe":
		if m.Chat.ID == m.From.ID {
			a.changeSubscription(ctx, m.From.ID, command == "/subscribe")
		}
		return true
	case "/broadcast":
		if a.broadcastAdmin(ctx, m.Chat.ID, m.From.ID) {
			a.newBroadcast(ctx, m.From.ID)
		}
		return true
	case "/broadcast_status":
		if a.broadcastAdmin(ctx, m.Chat.ID, m.From.ID) {
			a.showBroadcastStatus(ctx, m.From.ID)
		}
		return true
	}
	return false
}
func (a *App) changeSubscription(ctx context.Context, uid int64, on bool) {
	if err := a.broadcastStore.SetSubscribed(ctx, uid, on); err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	text, outcome := "已退订商店公告；订单、支付和充值通知保持不变。已在途的一条公告可能仍会送达。\nAnnouncements unsubscribed; transaction notifications remain enabled.", operations.Unsubscribed
	if on {
		text, outcome = "已主动订阅商店公告。每条公告可退订，也可随时发送 /unsubscribe。\nAnnouncements subscribed. Send /unsubscribe any time.", operations.Subscribed
	}
	a.ops.Record(operations.Event{Action: operations.Subscription, Outcome: outcome, UserID: uid})
	_, _ = a.broadcastPlain(ctx, uid, text, nil)
}
func (a *App) newBroadcast(ctx context.Context, uid int64) {
	if !a.allow(ctx, uid, "broadcast-admin", ratelimit.Rule{Limit: 10, Window: time.Minute}) {
		return
	}
	if err := a.sessions.ResetFlow(uid); err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	draft := &broadcast.Draft{ID: rand.Text(), Stage: "writing", ExpiresAt: time.Now().Add(broadcast.DraftLifetime)}
	_, err := a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
		if broadcast.Active(s.Job) || (s.Job != nil && s.Job.Flight != nil) {
			return broadcast.ErrConflict
		}
		if s.Drafts[uid] == nil && len(s.Drafts) >= 10 {
			return broadcast.ErrCapacity
		}
		s.Drafts[uid] = draft
		return nil
	})
	if err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	a.publishBroadcastDraft(ctx, uid, draft)
}
func (a *App) publishBroadcastDraft(ctx context.Context, uid int64, draft *broadcast.Draft) {
	text := "请回复本条消息填写纯文本公告（最多 3000 个 UTF-16 单位）。只向主动订阅者发送，不支持导入买家或任意用户 ID。15 分钟内完成预览和二次确认；/cancel 取消。"
	var markup models.ReplyMarkup = models.ForceReply{ForceReply: true, Selective: true}
	if draft.Stage == "preview" {
		text = "【管理员预览：尚未开始发送】\n\n" + broadcast.Payload(draft.Text)
		markup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "下一步：核对受众", CallbackData: "bc:" + draft.ID + ":review"}},
			{{Text: "修改内容", CallbackData: "bc:" + draft.ID + ":edit"}, {Text: "取消草稿", CallbackData: "bc:" + draft.ID + ":cancel"}},
		}}
	}
	if draft.Stage == "confirm" {
		text = fmt.Sprintf("【最终确认：最多 %d 名订阅者】\n每 %s 最多发送一条，不启用付费加速。名单已固定，退订者会跳过，新订阅者不加入本次任务。结果不明不自动重发。\n\n%s", len(draft.Recipients), a.broadcastEngine.Interval, broadcast.Payload(draft.Text))
		markup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "确认开始群发", CallbackData: "bc:" + draft.ID + ":send"}},
			{{Text: "修改内容并重新预览", CallbackData: "bc:" + draft.ID + ":edit"}, {Text: "取消草稿", CallbackData: "bc:" + draft.ID + ":cancel"}},
		}}
	}
	message, err := a.broadcastPlain(ctx, uid, text, markup)
	if err != nil {
		a.logger.Warn("broadcast preview delivery failed")
		return
	}
	_, err = a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
		current := s.Drafts[uid]
		if current == nil || current.ID != draft.ID || current.Stage != draft.Stage {
			return broadcast.ErrConflict
		}
		current.MessageID = message.ID
		return nil
	})
	if err != nil {
		a.broadcastFailure(ctx, uid, err)
	}
}
func (a *App) handleBroadcastInput(ctx context.Context, m *models.Message, command string) bool {
	if m.Chat.ID != m.From.ID || !a.ops.IsAdmin(m.From.ID) {
		return false
	}
	state, err := a.broadcastStore.View(ctx)
	if err != nil {
		a.broadcastFailure(ctx, m.Chat.ID, err)
		return true
	}
	draft := state.Drafts[m.From.ID]
	if draft == nil {
		return false
	}
	if strings.HasPrefix(command, "/") {
		_, err = a.broadcastStore.Update(ctx, func(s *broadcast.State) error { delete(s.Drafts, m.From.ID); return nil })
		if err != nil {
			a.broadcastFailure(ctx, m.Chat.ID, err)
			return true
		}
		if command == "/cancel" {
			_, _ = a.broadcastPlain(ctx, m.Chat.ID, "群发草稿已取消。已确认任务请使用 /broadcast_status 查看或停止。", nil)
			return true
		}
		return false
	}
	if draft.Stage != "writing" || draft.MessageID == 0 || m.ReplyToMessage == nil || m.ReplyToMessage.ID != draft.MessageID {
		_, _ = a.broadcastPlain(ctx, m.Chat.ID, "请回复当前公告填写提示；或使用预览按钮继续，/cancel 取消草稿。", nil)
		return true
	}
	text := strings.TrimSpace(m.Text)
	if !broadcast.ValidText(text) {
		_, _ = a.broadcastPlain(ctx, m.Chat.ID, "公告不能为空，不超过 3000 个 UTF-16 单位，且不能含控制或隐藏格式字符。", nil)
		return true
	}
	next, err := a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
		current := s.Drafts[m.From.ID]
		if current == nil || current.ID != draft.ID || current.Stage != "writing" || current.MessageID != draft.MessageID {
			return broadcast.ErrConflict
		}
		current.ID, current.Stage, current.Text, current.MessageID = rand.Text(), "preview", text, 0
		return nil
	})
	if err != nil {
		a.broadcastFailure(ctx, m.Chat.ID, err)
		return true
	}
	a.ops.RecordBroadcast(operations.BroadcastDraft, operations.Succeeded, m.From.ID, next.Drafts[m.From.ID].ID)
	a.publishBroadcastDraft(ctx, m.From.ID, next.Drafts[m.From.ID])
	return true
}

func (a *App) handleBroadcastCallback(ctx context.Context, q *models.CallbackQuery) {
	m, uid := q.Message.Message, q.From.ID
	if m.Chat.ID != uid {
		return
	}
	if q.Data == "bcunsubscribe" || q.Data == "bcsubscribe" {
		a.changeSubscription(ctx, uid, q.Data == "bcsubscribe")
		return
	}
	if q.Data == "bcsubscriptions" {
		token, err := a.broadcastStore.Consent(ctx, uid)
		if err != nil {
			a.broadcastFailure(ctx, uid, err)
			return
		}
		text, label, data := "尚未订阅公告；订阅是可选的，不影响购物。", "主动订阅公告 / Subscribe", "bcsubscribe"
		if token != "" {
			text, label, data = "已订阅公告；退订仅影响公告，不影响订单通知。", "退订公告 / Unsubscribe", "bcunsubscribe"
		}
		_, _ = a.broadcastPlain(ctx, uid, text, models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: label, CallbackData: data}}}})
		return
	}
	if !a.broadcastAdmin(ctx, m.Chat.ID, uid) {
		return
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 {
		return
	}
	if parts[0] == "bcjob" {
		a.broadcastJobAction(ctx, uid, parts[1], parts[2])
		return
	}
	if parts[0] != "bc" {
		return
	}
	if !a.allow(ctx, uid, "broadcast-admin", ratelimit.Rule{Limit: 10, Window: time.Minute}) {
		return
	}
	var recipients []broadcast.Recipient
	if parts[2] == "review" {
		var err error
		recipients, err = a.broadcastStore.Recipients(ctx)
		if err != nil {
			a.broadcastFailure(ctx, uid, err)
			return
		}
		if len(recipients) == 0 {
			_, _ = a.broadcastPlain(ctx, uid, "当前没有主动订阅者，未开始群发。", nil)
			return
		}
		if len(recipients) > a.broadcastMax {
			a.broadcastFailure(ctx, uid, broadcast.ErrCapacity)
			return
		}
	}
	started, cancelled := false, false
	state, err := a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
		draft := s.Drafts[uid]
		if draft == nil || draft.ID != parts[1] || draft.MessageID == 0 || draft.MessageID != m.ID {
			return broadcast.ErrConflict
		}
		switch parts[2] {
		case "cancel":
			delete(s.Drafts, uid)
			cancelled = true
		case "edit":
			draft.ID, draft.Stage, draft.Text, draft.Recipients, draft.MessageID = rand.Text(), "writing", "", nil, 0
		case "review":
			if draft.Stage != "preview" {
				return broadcast.ErrConflict
			}
			draft.ID, draft.Stage, draft.Recipients, draft.MessageID = rand.Text(), "confirm", recipients, 0
		case "send":
			if draft.Stage != "confirm" || len(draft.Recipients) == 0 || len(draft.Recipients) > a.broadcastMax || broadcast.Active(s.Job) || (s.Job != nil && s.Job.Flight != nil) {
				return broadcast.ErrConflict
			}
			// Persist the confirmed snapshot first. A lost acknowledgement leaves
			// a prepared job which the worker cannot send automatically.
			s.Job = &broadcast.Job{ID: draft.ID, OwnerID: uid, Worker: a.broadcastEngine.ID, Text: draft.Text, Recipients: append([]broadcast.Recipient(nil), draft.Recipients...), Total: len(draft.Recipients), Status: "prepared", ExpiresAt: time.Now().Add(broadcast.JobLifetime)}
			delete(s.Drafts, uid)
			started = true
		default:
			return broadcast.ErrConflict
		}
		return nil
	})
	if err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	if cancelled {
		_, _ = a.broadcastPlain(ctx, uid, "群发草稿已取消，未开始发送。", nil)
		return
	}
	if started {
		_, err = a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
			if s.Job == nil || s.Job.ID != state.Job.ID || s.Job.Status != "prepared" || s.Job.Worker != a.broadcastEngine.ID {
				return broadcast.ErrConflict
			}
			s.Job.Status = "queued"
			return nil
		})
		if err != nil {
			a.broadcastFailure(ctx, uid, err)
			return
		}
		a.ops.RecordBroadcast(operations.BroadcastJob, operations.Attempt, uid, state.Job.ID)
		a.showBroadcastStatus(ctx, uid)
		return
	}
	a.publishBroadcastDraft(ctx, uid, state.Drafts[uid])
}

func (a *App) showBroadcastStatus(ctx context.Context, uid int64) {
	state, err := a.broadcastStore.View(ctx)
	if err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	job := state.Job
	if job == nil {
		_, _ = a.broadcastPlain(ctx, uid, "暂无群发任务。使用 /broadcast 编辑；只有主动订阅公告的用户会进入受众。", nil)
		return
	}
	text := fmt.Sprintf("群发任务 %s\n状态：%s\n受众上限 %d · 已成功 %d · 失败 %d · 跳过 %d · 结果不明 %d\n剩余未领取 %d\n结果不明的收件人不会自动重发。", job.ID, job.Status, job.Total, job.Sent, job.Failed, job.Skipped, job.Unknown, max(0, job.Total-job.Next))
	if job.Flight != nil {
		text += "\n有一条已领取/在途消息；暂停或停止后仍可能送达。"
	}
	cooldown := job.NotBefore
	if state.NotBefore.After(cooldown) {
		cooldown = state.NotBefore
	}
	if time.Now().Before(cooldown) {
		text += "\nTelegram 冷却截止：" + cooldown.UTC().Format(time.RFC3339)
	}
	rows := [][]models.InlineKeyboardButton{{{Text: "刷新群发状态", CallbackData: "bcjob:" + job.ID + ":status"}}}
	if broadcast.Active(job) {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "停止剩余群发（无法撤回在途消息）", CallbackData: "bcjob:" + job.ID + ":stop"}})
		if (job.Status == "paused" || job.Status == "prepared") && job.OwnerID == uid {
			rows = append(rows, []models.InlineKeyboardButton{{Text: "已核对，继续剩余（不重发结果不明者）", CallbackData: "bcjob:" + job.ID + ":resume"}})
		}
	}
	_, _ = a.broadcastPlain(ctx, uid, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
func (a *App) broadcastJobAction(ctx context.Context, uid int64, id, action string) {
	if action == "status" {
		a.showBroadcastStatus(ctx, uid)
		return
	}
	if !a.allow(ctx, uid, "broadcast-admin", ratelimit.Rule{Limit: 10, Window: time.Minute}) {
		return
	}
	_, err := a.broadcastStore.Update(ctx, func(s *broadcast.State) error {
		job := s.Job
		if job == nil || job.ID != id || !broadcast.Active(job) {
			return broadcast.ErrConflict
		}
		switch action {
		case "stop":
			job.Status = "cancelled"
		case "resume":
			if job.OwnerID != uid || (job.Status != "paused" && job.Status != "prepared") || job.Flight != nil {
				return broadcast.ErrConflict
			}
			job.Status, job.Worker = "queued", a.broadcastEngine.ID
		default:
			return broadcast.ErrConflict
		}
		return nil
	})
	if err != nil {
		a.broadcastFailure(ctx, uid, err)
		return
	}
	outcome := operations.Attempt
	if action == "stop" {
		outcome = operations.Cancelled
	}
	a.ops.RecordBroadcast(operations.BroadcastJob, outcome, uid, id)
	a.showBroadcastStatus(ctx, uid)
}
