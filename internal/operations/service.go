// Package operations records allowlisted audit metadata and delivers optional
// best-effort admin summaries independently from financial request handling.
package operations

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Action string
type Outcome string

const (
	Order             Action  = "order.create"
	Payment           Action  = "payment.create"
	OrderCancel       Action  = "order.cancel"
	Recharge          Action  = "wallet.recharge"
	GiftCard          Action  = "gift_card.redeem"
	Affiliate         Action  = "affiliate.open"
	Withdrawal        Action  = "withdrawal.apply"
	WithdrawalUnlock  Action  = "withdrawal.unlock"
	CallbackPaid      Action  = "callback.order_paid"
	CallbackFulfilled Action  = "callback.order_fulfilled"
	CallbackRecharge  Action  = "callback.recharge_succeeded"
	Session           Action  = "session.storage"
	Access            Action  = "access"
	Callback          Action  = "callback.ingress"
	AdminTest         Action  = "admin.test"
	BroadcastDraft    Action  = "broadcast.draft"
	BroadcastJob      Action  = "broadcast.job"
	BroadcastDelivery Action  = "broadcast.delivery"
	Subscription      Action  = "broadcast.subscription"
	DeliveryTXT       Action  = "delivery.txt"
	Attempt           Outcome = "attempt"
	Succeeded         Outcome = "succeeded"
	Rejected          Outcome = "rejected"
	Unknown           Outcome = "unknown"
	Blocked           Outcome = "blocked"
	Limited           Outcome = "rate_limited"
	Unavailable       Outcome = "unavailable"
	Paused            Outcome = "paused"
	Cancelled         Outcome = "cancelled"
	Skipped           Outcome = "skipped"
	Subscribed        Outcome = "subscribed"
	Unsubscribed      Outcome = "unsubscribed"
)

type Event struct {
	id         string
	Action     Action
	Outcome    Outcome
	UserID     int64
	ResourceID uint
	Dedupe     [32]byte
}

// No arbitrary text, account, code, amount, URL, username or error object is
// accepted into the event: upstream and user-controlled strings cannot leak.
func (e Event) valid() bool {
	switch e.Action {
	case Order, Payment, OrderCancel, Recharge, GiftCard, Affiliate, Withdrawal, WithdrawalUnlock, CallbackPaid, CallbackFulfilled, CallbackRecharge, Session, Access, Callback, AdminTest, BroadcastDraft, BroadcastJob, BroadcastDelivery, Subscription, DeliveryTXT:
	default:
		return false
	}
	switch e.Outcome {
	case Attempt, Succeeded, Rejected, Unknown, Blocked, Limited, Unavailable, Paused, Cancelled, Skipped, Subscribed, Unsubscribed:
	default:
		return false
	}
	return true
}

type Sender func(context.Context, int64, string) error
type delivery struct {
	event Event
}

type Service struct {
	audit, logger *slog.Logger
	admins        []int64
	send          Sender
	queue         chan delivery
	mu            sync.Mutex
	dedup         map[string]time.Time
	now           func() time.Time
	// Retry delays are bounded and interruptible; no financial action is retried.
	retryDelays  []time.Duration
	sendInterval time.Duration
}

func New(audit, logger *slog.Logger, admins []int64, sender Sender) *Service {
	return &Service{audit: audit, logger: logger, admins: append([]int64(nil), admins...), send: sender, queue: make(chan delivery, 128), dedup: make(map[string]time.Time), now: time.Now, retryDelays: []time.Duration{time.Second, 3 * time.Second}, sendInterval: time.Second}
}

func (s *Service) IsAdmin(uid int64) bool {
	if s == nil || uid <= 0 {
		return false
	}
	for _, id := range s.admins {
		if id == uid {
			return true
		}
	}
	return false
}

func (s *Service) Record(event Event) {
	if s == nil || !event.valid() {
		return
	}
	correlated := event.id != ""
	if event.id == "" {
		event.id = rand.Text()
	}
	s.audit.Info("audit", "schema", 1, "event_id", event.id, "action", event.Action, "outcome", event.Outcome, "user_id", event.UserID, "resource_id", event.ResourceID)
	if len(s.admins) == 0 || s.send == nil || !notify(event) {
		return
	}
	key := fmt.Sprintf("%s:%s:%d:%d:%x", event.Action, event.Outcome, event.UserID, event.ResourceID, event.Dedupe)
	if correlated && event.ResourceID == 0 {
		key += ":" + event.id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if expiry := s.dedup[key]; now.Before(expiry) {
		return
	}
	if len(s.dedup) >= 10000 {
		for key, expiry := range s.dedup {
			if !now.Before(expiry) {
				delete(s.dedup, key)
			}
		}
		if len(s.dedup) >= 10000 {
			s.deliveryAudit(event, "dropped_capacity", 0)
			return
		}
	}
	select {
	case s.queue <- delivery{event: event}:
		s.dedup[key] = now.Add(5 * time.Minute)
	default:
		s.deliveryAudit(event, "dropped_queue_full", 0)
	}
}

func notify(event Event) bool {
	if event.Action == BroadcastJob {
		return event.Outcome == Succeeded || event.Outcome == Unknown
	}
	if event.Action == AdminTest {
		return event.Outcome == Succeeded
	}
	if event.Action == Withdrawal || event.Action == WithdrawalUnlock || event.Action == Session {
		return event.Outcome == Succeeded || event.Outcome == Unknown || event.Outcome == Unavailable
	}
	return (event.Action == Order || event.Action == CallbackPaid || event.Action == CallbackFulfilled || event.Action == CallbackRecharge) && event.Outcome == Succeeded
}

// Campaign IDs are generated opaque base32 tokens, not announcement contents.
func (s *Service) RecordBroadcast(action Action, outcome Outcome, uid int64, id string) {
	if len(id) != 26 {
		return
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '2' || r > '7') {
			return
		}
	}
	s.Record(Event{id: id, Action: action, Outcome: outcome, UserID: uid})
}

func (s *Service) deliveryAudit(event Event, outcome string, adminID int64) {
	s.audit.Info("audit", "schema", 1, "event_id", event.id, "action", "admin.delivery", "outcome", outcome, "admin_id", adminID)
}

func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	defer func() {
		for {
			select {
			case job := <-s.queue:
				s.deliveryAudit(job.event, "interrupted_shutdown", 0)
			default:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.queue:
			s.deliver(ctx, job)
		}
	}
}

func (s *Service) deliver(ctx context.Context, job delivery) {
	text := fmt.Sprintf("商店运营通知\n事件：%s\n结果：%s\n用户 ID：%d\n资源 ID：%d\n审计 ID：%s", job.event.Action, job.event.Outcome, job.event.UserID, job.event.ResourceID, job.event.id)
	if job.event.Action == Withdrawal && job.event.Outcome != Succeeded {
		text += "\n请核对商店提现记录；勿自动重试资金操作。"
	}
	for _, adminID := range s.admins {
		delivered := false
		for attempt := 0; attempt <= len(s.retryDelays); attempt++ {
			if ctx.Err() != nil {
				s.deliveryAudit(job.event, "interrupted", adminID)
				return
			}
			if attempt > 0 {
				timer := time.NewTimer(s.retryDelays[attempt-1])
				select {
				case <-ctx.Done():
					timer.Stop()
					s.deliveryAudit(job.event, "interrupted", adminID)
					return
				case <-timer.C:
				}
			}
			requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.send(requestCtx, adminID, text)
			cancel()
			if err == nil {
				delivered = true
				break
			}
		}
		if delivered {
			s.deliveryAudit(job.event, "sent", adminID)
		} else {
			s.deliveryAudit(job.event, "failed", adminID)
			s.logger.Warn("admin notification delivery failed", "event_id", job.event.id, "admin_id", adminID)
		}
		// At most one queued summary per second per admin; delayed jobs stay bounded.
		timer := time.NewTimer(s.sendInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Operation correlates the pre-write audit with its terminal outcome.
type Operation struct {
	service *Service
	event   Event
}

func (s *Service) Begin(action Action, uid int64, resource uint) Operation {
	op := Operation{s, Event{id: rand.Text(), Action: action, Outcome: Attempt, UserID: uid, ResourceID: resource}}
	s.Record(op.event)
	return op
}
func (op Operation) End(outcome Outcome, resource uint) {
	op.event.Outcome = outcome
	if resource != 0 {
		op.event.ResourceID = resource
	}
	op.service.Record(op.event)
}

func ParseAdminIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 10 {
		return nil, fmt.Errorf("BOT_ADMIN_USER_IDS supports at most 10 private user IDs")
	}
	var ids []int64
	seen := make(map[int64]bool)
	for _, part := range parts {
		var id int64
		if _, err := fmt.Sscan(strings.TrimSpace(part), &id); err != nil || id <= 0 || fmt.Sprint(id) != strings.TrimSpace(part) {
			return nil, fmt.Errorf("BOT_ADMIN_USER_IDS must contain positive numeric Telegram user IDs")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}
