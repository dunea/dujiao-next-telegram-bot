package callback

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
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
	Limiter ratelimit.Limiter
	Policy  ratelimit.Policy
	Audit   *operations.Service
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
	if err != nil || absDuration(h.now().Sub(time.Unix(timestamp, 0))) > maxClockSkew {
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
	if !h.gate(w, request, "callback-user:"+strconv.FormatInt(userID, 10), h.options.Policy.Callback, h.options.Limiter) {
		return
	}
	eventType := strings.TrimPrefix(request.URL.Path, "/internal/")
	if err := h.notifier.Notify(request.Context(), userID, eventType, event); err != nil {
		h.logger.Error("send callback notification failed", "event", eventType, "user_id", userID)
		h.options.Audit.Record(operations.Event{Action: operations.Callback, Outcome: operations.Unavailable, UserID: userID, ResourceID: event.OrderID})
		h.respond(w, http.StatusInternalServerError, "notify_failed")
		return
	}
	h.respond(w, http.StatusOK, "ok")
}

func (h *Handler) gate(w http.ResponseWriter, request *http.Request, key string, rule ratelimit.Rule, limiter ratelimit.Limiter) bool {
	decision, err := limiter.Allow(request.Context(), key, rule)
	if err != nil {
		h.auditIngress(request.Context(), operations.Unavailable)
		w.Header().Set("Retry-After", "1")
		h.respond(w, http.StatusServiceUnavailable, "limiter_unavailable")
		return false
	}
	if !decision.Allowed {
		h.auditIngress(request.Context(), operations.Limited)
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(decision.RetryAfter.Seconds()+0.999))))
		h.respond(w, http.StatusTooManyRequests, "rate_limited")
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

func (h *Handler) respond(w http.ResponseWriter, status int, result string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": result})
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
