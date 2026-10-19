package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ConfigureOperations is startup-only, before Start or any update is handled.
func (a *App) ConfigureOperations(adminIDs []int64, policy ratelimit.Policy, audit *slog.Logger) {
	a.limits = policy
	a.limiter = ratelimit.NewMemory(10000)
	if durable, ok := a.sessions.(ratelimit.Limiter); ok {
		a.limiter = durable
	}
	a.ops = operations.New(audit, a.logger, adminIDs, func(ctx context.Context, uid int64, text string) error {
		_, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: uid, Text: text})
		return err
	})
	a.initBroadcast()
}

func (a *App) CallbackOptions() callback.Options {
	return callback.Options{Limiter: a.limiter, Policy: a.limits, Audit: a.ops}
}

func (a *App) allow(ctx context.Context, uid int64, bucket string, rule ratelimit.Rule) bool {
	if a.limiter == nil {
		return true
	} // Unit fixtures may construct read-only App literals.
	decision, err := a.limiter.Allow(ctx, bucket+":"+strconv.FormatInt(uid, 10), rule)
	if err != nil {
		a.ops.Record(operations.Event{Action: operations.Session, Outcome: operations.Unavailable, UserID: uid})
		return false
	}
	if !decision.Allowed {
		a.ops.Record(operations.Event{Action: operations.Access, Outcome: operations.Limited, UserID: uid})
	}
	return decision.Allowed
}

func (a *App) allowIngress(ctx context.Context, chatID, uid int64, cancel bool) bool {
	if !a.allow(ctx, 0, "telegram-global", a.limits.Global) {
		return false
	}
	if cancel {
		return a.allow(ctx, uid, "telegram-cancel", ratelimit.Rule{Limit: 6, Window: time.Minute})
	}
	return a.allow(ctx, uid, "telegram-user", a.limits.User)
}

func (a *App) allowCallbackIngress(ctx context.Context, query *models.CallbackQuery) bool {
	if a.allowIngress(ctx, query.Message.Message.Chat.ID, query.From.ID, query.Data == "bcunsubscribe") {
		return true
	}
	// Never respond to floods with sendMessage (which could create another flood).
	_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: "操作过于频繁或服务暂不可用，请稍后再试。 / Too many requests or service unavailable. Try later."})
	return false
}

func (a *App) allowWrite(ctx context.Context, chatID, uid int64, action operations.Action) bool {
	if (action == operations.Withdrawal && !a.allow(ctx, uid, "withdrawal", a.limits.Withdrawal)) || !a.allow(ctx, uid, "write", a.limits.Write) {
		// Ingress is already bounded; one short failure response helps avoid
		// retrying an action whose session has not yet been consumed.
		a.send(ctx, chatID, "资金操作过于频繁或服务暂不可用，请稍后再试；提现请先核对记录。\nFinancial action rate limited or unavailable. Check history before retrying.", nil)
		return false
	}
	return true
}

func financialOutcome(err error) operations.Outcome {
	if err == nil {
		return operations.Succeeded
	}
	var apiErr *dujiao.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
		return operations.Rejected
	}
	return operations.Unknown
}

func auditCallbackAction(kind string) operations.Action {
	switch kind {
	case "order-paid":
		return operations.CallbackPaid
	case "order-fulfilled":
		return operations.CallbackFulfilled
	case "wallet-recharge-succeeded":
		return operations.CallbackRecharge
	default:
		return operations.Callback
	}
}
