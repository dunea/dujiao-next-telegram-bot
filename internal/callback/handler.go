package callback

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
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
}

func New(key, secret string, notifier Notifier, logger *slog.Logger) *Handler {
	return &Handler{key: key, secret: secret, notifier: notifier, logger: logger, now: time.Now}
}

func (h *Handler) Register(mux *http.ServeMux) {
	for _, path := range []string{"/internal/order-paid", "/internal/order-fulfilled", "/internal/wallet-recharge-succeeded"} {
		mux.Handle("POST "+path, h)
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 64<<10))
	if err != nil {
		h.respond(w, http.StatusBadRequest, "invalid_body")
		return
	}
	timestamp, err := strconv.ParseInt(request.Header.Get("Dujiao-Next-Channel-Timestamp"), 10, 64)
	if err != nil || absDuration(h.now().Sub(time.Unix(timestamp, 0))) > maxClockSkew {
		h.respond(w, http.StatusUnauthorized, "invalid_timestamp")
		return
	}
	key := request.Header.Get("Dujiao-Next-Channel-Key")
	if !hmac.Equal([]byte(key), []byte(h.key)) || !dujiao.Verify(h.secret, request.Method, request.URL.Path, timestamp, body, request.Header.Get("Dujiao-Next-Channel-Signature")) {
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
	eventType := strings.TrimPrefix(request.URL.Path, "/internal/")
	if err := h.notifier.Notify(request.Context(), userID, eventType, event); err != nil {
		h.logger.Error("send callback notification", "event", eventType, "user_id", userID, "error", err)
		h.respond(w, http.StatusInternalServerError, "notify_failed")
		return
	}
	h.respond(w, http.StatusOK, "ok")
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
