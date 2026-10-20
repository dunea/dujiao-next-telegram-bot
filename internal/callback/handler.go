package callback

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

const maxClockSkew = 5 * time.Minute

type Notifier interface {
	Notify(context.Context, int64, string, Event) error
}

type Event struct {
	OrderID        uint   `json:"order_id"`
	TelegramUserID string `json:"telegram_user_id"`
	RechargeNo     string `json:"recharge_no"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
}

type Handler struct {
	key      string
	secret   string
	notifier Notifier
	logger   *slog.Logger
	now      func() time.Time
	options  Options
	ingress  *ratelimit.Memory
}

type Options struct {
	Receipts Receipts
	Limiter  ratelimit.Limiter
	Policy   ratelimit.Policy
	Audit    *operations.Service
}

func New(key, secret string, notifier Notifier, logger *slog.Logger, options ...Options) *Handler {
	opts := Options{Policy: ratelimit.DefaultPolicy()}
	if len(options) > 0 {
		opts = options[0]
	}
	if !opts.Policy.HTTP.Valid() || !opts.Policy.Callback.Valid() {
		opts.Policy = ratelimit.DefaultPolicy()
	}
	if opts.Limiter == nil {
		opts.Limiter = ratelimit.NewMemory(10000)
	}
	if opts.Receipts == nil {
		opts.Receipts = NewMemoryReceipts()
	}
	if opts.Audit == nil {
		opts.Audit = operations.New(logger, logger, nil, nil)
	}
	return &Handler{key: key, secret: secret, notifier: notifier, logger: logger, now: time.Now, options: opts, ingress: ratelimit.NewMemory(10000)}
}

func (h *Handler) Register(mux *http.ServeMux) {
	for _, path := range []string{"/internal/order-paid", "/internal/order-fulfilled", "/internal/wallet-recharge-succeeded"} {
		mux.Handle("POST "+path, h)
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		h.respond(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	switch request.URL.Path {
	case "/internal/order-paid", "/internal/order-fulfilled", "/internal/wallet-recharge-succeeded":
	default:
		h.respond(w, http.StatusNotFound, "not_found")
		return
	}
	// Trust only the TCP peer. Untrusted X-Forwarded-For must not mint buckets.
	ip, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		ip = "unknown"
	}
	global := h.options.Policy.HTTP
	global.Limit = min(100000, global.Limit*2)
	if !h.gate(w, request, "http-global", global, h.ingress) || !h.gate(w, request, "http-ip:"+ip, h.options.Policy.HTTP, h.ingress) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, (64<<10)+1))
	if err != nil {
		h.respond(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if len(body) > 64<<10 {
		h.respond(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	timestamp, err := strconv.ParseInt(request.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
	at := time.Unix(timestamp, 0)
	if err != nil || at.Before(h.now().Add(-maxClockSkew)) || at.After(h.now().Add(maxClockSkew)) {
		h.auditIngress(request.Context(), operations.Blocked)
		h.respond(w, http.StatusUnauthorized, "invalid_timestamp")
		return
	}
	key := request.Header.Get("Dujiao-Next-Channel-Key")
	if !hmac.Equal([]byte(key), []byte(h.key)) || !dujiao.Verify(h.secret, request.Method, request.URL.Path, timestamp, body, request.Header.Get("Dujiao-Next-Channel-Signature")) {
		h.auditIngress(request.Context(), operations.Blocked)
		h.respond(w, http.StatusUnauthorized, "invalid_signature")
		return
	}
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		h.respond(w, http.StatusBadRequest, "invalid_json")
		return
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(event.TelegramUserID), 10, 64)
	if err != nil || userID <= 0 {
		h.respond(w, http.StatusBadRequest, "invalid_user")
		return
	}
	event.TelegramUserID = strconv.FormatInt(userID, 10)
	eventType := strings.TrimPrefix(request.URL.Path, "/internal/")
	if !validEvent(eventType, &event) {
		h.respond(w, http.StatusBadRequest, "invalid_event")
		return
	}
	if !h.gate(w, request, "callback-user:"+strconv.FormatInt(userID, 10), h.options.Policy.Callback, h.options.Limiter) {
		return
	}
	h.deliver(w, request, userID, eventType, event)
}

func (h *Handler) gate(w http.ResponseWriter, request *http.Request, key string, rule ratelimit.Rule, limiter ratelimit.Limiter) bool {
	decision, err := limiter.Allow(request.Context(), key, rule)
	if err != nil {
		h.unavailable(request.Context(), strings.TrimPrefix(request.URL.Path, "/internal/"), "limiter", err)
		w.Header().Set("Retry-After", "1")
		h.respond(w, http.StatusServiceUnavailable, "limiter_unavailable")
		return false
	}
	if !decision.Allowed {
		h.auditIngress(request.Context(), operations.Limited)
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(decision.RetryAfter.Seconds()+0.999))))
		status := http.StatusTooManyRequests
		// Reviewed Dujiao-Next workers acknowledge all 4xx without retrying.
		// A signed user's temporary quota denial must remain retryable.
		if strings.HasPrefix(key, "callback-user:") {
			status = http.StatusServiceUnavailable
		}
		h.respond(w, status, "rate_limited")
		return false
	}
	return true
}

func (h *Handler) auditIngress(ctx context.Context, outcome operations.Outcome) {
	decision, err := h.ingress.Allow(ctx, "http-audit:"+string(outcome), ratelimit.Rule{Limit: 1, Window: time.Minute})
	if err != nil || !decision.Allowed {
		return
	}
	action := operations.Callback
	if outcome == operations.Unavailable {
		action = operations.Session
	}
	h.options.Audit.Record(operations.Event{Action: action, Outcome: outcome})
}

// A disconnected request must not suppress local diagnostics. This only uses
// the bounded in-memory sampler and local logging/audit, not a network retry.
func (h *Handler) unavailable(ctx context.Context, kind, stage string, err error) {
	requestState := "active"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		requestState = "deadline_exceeded"
	} else if ctx.Err() != nil {
		requestState = "canceled"
	}
	reason := storageReason(err)
	local := context.WithoutCancel(ctx)
	h.auditIngress(local, operations.Unavailable)
	decision, sampleErr := h.ingress.Allow(local, "callback-diagnostic:"+stage+":"+reason+":"+requestState, ratelimit.Rule{Limit: 1, Window: time.Minute})
	if sampleErr == nil && decision.Allowed {
		h.logger.Warn("callback unavailable", "event", kind, "stage", stage, "reason", reason, "request_context", requestState)
	}
}

func (h *Handler) receiptUnavailable(w http.ResponseWriter, stage string) {
	w.Header().Set("Retry-After", "2")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "receipt_unavailable", "stage": stage})
}

func (h *Handler) respond(w http.ResponseWriter, status int, result string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": result})
}

func validEvent(kind string, e *Event) bool {
	if kind == "wallet-recharge-succeeded" {
		if e.OrderID != 0 || len(e.RechargeNo) == 0 || len(e.RechargeNo) > 100 {
			return false
		}
		for _, r := range e.RechargeNo {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
				return false
			}
		}
		amount, ok := money.Parse(e.Amount)
		if !ok || amount <= 0 || len(e.Currency) < 2 || len(e.Currency) > 12 {
			return false
		}
		for _, r := range e.Currency {
			if r < 'A' || r > 'Z' {
				return false
			}
		}
		e.Amount = money.Format(amount)
		return true
	}
	return e.OrderID > 0 && e.RechargeNo == "" && e.Amount == "" && e.Currency == ""
}

func (h *Handler) deliver(w http.ResponseWriter, r *http.Request, uid int64, kind string, event Event) {
	key, fp, token := ReceiptKey(uid, event), fingerprint(event), rand.Text()
	duplicate := false
	_, err := h.options.Receipts.Update(r.Context(), key, func(receipt *Receipt) error {
		if old, ok := receipt.Events[kind]; ok {
			if old.Fingerprint != fp {
				return ErrChanged
			}
			if old.Status == "sent" || old.Status == "discarded" {
				duplicate = true
				return nil
			}
		}
		if kind == "order-paid" && receipt.Events["order-fulfilled"].Status == "sent" {
			duplicate = true
			return nil
		}
		for _, d := range receipt.Events {
			if (d.Status == "posting" || d.Status == "retry") && h.now().Before(d.NotBefore) {
				return ErrBusy
			}
		}
		receipt.Events[kind] = Delivery{Fingerprint: fp, Token: token, Status: "posting", NotBefore: h.now().Add(DeliveryLease)}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrChanged) {
			h.respond(w, http.StatusConflict, "event_changed")
			return
		}
		if errors.Is(err, ErrBusy) {
			w.Header().Set("Retry-After", "2")
			h.respond(w, http.StatusServiceUnavailable, "delivery_busy")
			return
		}
		h.unavailable(r.Context(), kind, "claim", err)
		h.receiptUnavailable(w, "claim")
		return
	}
	if duplicate {
		h.respond(w, http.StatusOK, "duplicate_or_obsolete")
		return
	}
	err = h.notifier.Notify(r.Context(), uid, kind, event)
	status, delay := "sent", time.Duration(0)
	if err != nil {
		status, delay = "retry", time.Second
		var failure *NotifyError
		if errors.As(err, &failure) {
			if failure.Permanent {
				status = "discarded"
			}
			if failure.RetryAfter > 0 {
				delay = min(time.Hour, max(time.Second, failure.RetryAfter))
			}
		}
		h.logger.Warn("callback notification not delivered", "event", kind, "user_id", uid)
		h.options.Audit.Record(operations.Event{Action: operations.Callback, Outcome: operations.Unavailable, UserID: uid, ResourceID: event.OrderID})
	}
	// Complete an acknowledged send even if the upstream disconnected. Storage
	// failure leaves the posting lease intact; never pretend it was committed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	_, saveErr := h.options.Receipts.Update(ctx, key, func(receipt *Receipt) error {
		d := receipt.Events[kind]
		if d.Token != token || d.Status != "posting" {
			return ErrStorage
		}
		d.Status = status
		d.NotBefore = h.now().Add(delay)
		receipt.Events[kind] = d
		return nil
	})
	if saveErr != nil {
		h.unavailable(r.Context(), kind, "finalize", saveErr)
		h.receiptUnavailable(w, "finalize")
		return
	}
	if status == "retry" {
		w.Header().Set("Retry-After", strconv.Itoa(int(delay/time.Second)))
		var failure *NotifyError
		if errors.As(err, &failure) && failure.RetryAfter > 0 {
			h.respond(w, 503, "telegram_rate_limited")
		} else {
			h.respond(w, 503, "notify_failed")
		}
		return
	}
	h.respond(w, http.StatusOK, status)
}
