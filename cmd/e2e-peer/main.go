// Command e2e-peer is an opt-in, isolated Telegram API + payment gateway fixture.
// It is not a merchant simulator: the real Dujiao-Next process owns all money,
// orders, fulfillment and gateway callback verification.
package main

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
)

const testToken = "999001:isolated-e2e"
const testUser int64 = 910000001

type message struct {
	Method   string
	ID       int
	ChatID   int64
	Text     string
	Keyboard json.RawMessage
}
type payment struct{ No, Trade, Amount string }
type fault struct {
	Method                  string
	Code, Times, RetryAfter int
	Drop                    bool
}
type peer struct {
	mu                        sync.Mutex
	updates                   []models.Update
	messages                  []message
	payments                  []payment
	sequence, updateID        int
	photos, polls, pollErrors int
	send429, send5xx          int
	fault                     fault
	secret                    string
}
type snapshot struct {
	Messages                                  []message
	Payments                                  []payment
	Photos, Polls, PollErrors, PendingUpdates int
	Send429, Send5xx                          int
}

func main() {
	if os.Getenv("E2E_PEER_ENABLED") != "1" || len(os.Getenv("E2E_GATEWAY_SECRET")) < 32 {
		fmt.Fprintln(os.Stderr, "isolated E2E opt-in and synthetic secret required")
		os.Exit(2)
	}
	p := &peer{secret: os.Getenv("E2E_GATEWAY_SECRET")}
	server := &http.Server{Addr: ":8080", Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "isolated peer stopped")
		os.Exit(1)
	}
}
func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func sign(form url.Values, secret string) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		if k != "sign" && k != "sign_type" && form.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+form.Get(k))
	}
	digest := md5.Sum([]byte(strings.Join(parts, "&") + secret))
	return hex.EncodeToString(digest[:])
}
func (p *peer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if r.URL.Path == "/healthz" {
		write(w, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/test/request-context" && r.Method == "GET" {
		// Test-only transport probe: an HTTP request with no body lets net/http
		// detect TCP half-close while the handler is still running.
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
		}
		write(w, map[string]bool{"canceled": r.Context().Err() != nil})
		return
	}
	if r.URL.Path == "/test/state" && r.Method == "GET" {
		p.mu.Lock()
		defer p.mu.Unlock()
		write(w, snapshot{Messages: append([]message{}, p.messages...), Payments: append([]payment{}, p.payments...), Photos: p.photos, Polls: p.polls, PollErrors: p.pollErrors, PendingUpdates: len(p.updates), Send429: p.send429, Send5xx: p.send5xx})
		return
	}
	if r.URL.Path == "/test/update" && r.Method == "POST" {
		var update models.Update
		if json.NewDecoder(r.Body).Decode(&update) != nil {
			http.Error(w, "invalid update", 400)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.updates) >= 1000 {
			http.Error(w, "capacity", 503)
			return
		}
		p.updateID++
		update.ID = int64(p.updateID)
		p.updates = append(p.updates, update)
		write(w, map[string]int{"update_id": p.updateID})
		return
	}
	if r.URL.Path == "/test/telegram-fault" && r.Method == "POST" {
		var f fault
		if json.NewDecoder(r.Body).Decode(&f) != nil || f.Times < 0 || f.Times > 5 {
			http.Error(w, "invalid fault", 400)
			return
		}
		p.mu.Lock()
		p.fault = f
		p.mu.Unlock()
		write(w, true)
		return
	}
	if r.URL.Path == "/test/pay" && r.Method == "POST" {
		p.pay(w, r)
		return
	}
	if r.URL.Path == "/mapi.php" && r.Method == "POST" {
		p.createPayment(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
		p.telegram(w, r)
		return
	}
	http.NotFound(w, r)
}
func (p *peer) telegram(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
	if err := r.ParseMultipartForm(9 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "invalid form", 400)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.fault
	apply := f.Times > 0 && f.Method == method
	if apply {
		p.fault.Times--
		if !f.Drop {
			if method == "sendMessage" {
				if f.Code == 429 {
					p.send429++
				}
				if f.Code >= 500 {
					p.send5xx++
				}
			}
			if method == "getUpdates" {
				p.pollErrors++
			}
			w.WriteHeader(f.Code)
			write(w, map[string]any{"ok": false, "error_code": f.Code, "description": "synthetic failure", "parameters": map[string]int{"retry_after": f.RetryAfter}})
			return
		}
	}
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 999001, "is_bot": true, "first_name": "Isolated E2E", "username": "isolated_e2e_bot"}
	case "setMyCommands", "answerCallbackQuery", "deleteMessage", "editMessageReplyMarkup":
	case "getUpdates":
		p.polls++
		offset, _ := strconv.ParseInt(r.FormValue("offset"), 10, 64)
		pending := p.updates[:0]
		for _, u := range p.updates {
			if u.ID >= offset {
				pending = append(pending, u)
			}
		}
		p.updates = pending
		result = append([]models.Update{}, p.updates...)
		// Empty polling is deliberately bounded without consuming CPU in a loop.
		if len(p.updates) == 0 {
			time.Sleep(80 * time.Millisecond)
		}
	case "sendMessage", "editMessageText", "sendPhoto":
		uid, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		if uid != testUser {
			http.Error(w, "non-synthetic recipient", 400)
			return
		}
		p.sequence++
		id := p.sequence
		if method == "editMessageText" {
			id, _ = strconv.Atoi(r.FormValue("message_id"))
		}
		text := r.FormValue("text")
		if method == "sendPhoto" {
			p.photos++
			text = r.FormValue("caption")
		}
		if len(p.messages) >= 2000 {
			http.Error(w, "capacity", 503)
			return
		}
		keyboard := json.RawMessage(r.FormValue("reply_markup"))
		if len(keyboard) == 0 {
			keyboard = json.RawMessage(`{}`)
		}
		p.messages = append(p.messages, message{Method: method, ID: id, ChatID: uid, Text: text, Keyboard: keyboard})
		result = map[string]any{"message_id": id, "date": time.Now().Unix(), "chat": map[string]any{"id": uid, "type": "private"}, "from": map[string]any{"id": 999001, "is_bot": true, "first_name": "E2E"}, "text": text}
	default:
		http.Error(w, "unsupported Telegram method", 404)
		return
	}
	if apply && f.Drop {
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		return
	}
	write(w, map[string]any{"ok": true, "result": result})
}
func (p *peer) createPayment(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil || r.Form.Get("pid") != "isolated" || subtle.ConstantTimeCompare([]byte(r.Form.Get("sign")), []byte(sign(r.Form, p.secret))) != 1 {
		http.Error(w, "invalid gateway signature", 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	trade := fmt.Sprintf("E2E%d", len(p.payments)+1)
	p.payments = append(p.payments, payment{No: r.Form.Get("out_trade_no"), Trade: trade, Amount: r.Form.Get("money")})
	write(w, map[string]any{"code": 1, "trade_no": trade, "payurl": "https://payments.example.invalid/" + trade, "qrcode": "https://payments.example.invalid/" + trade})
}
func (p *peer) pay(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Index int
		Mode  string
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid payment", 400)
		return
	}
	p.mu.Lock()
	if input.Index < 0 || input.Index >= len(p.payments) {
		p.mu.Unlock()
		http.Error(w, "unknown payment", 400)
		return
	}
	payment := p.payments[input.Index]
	p.mu.Unlock()
	form := url.Values{"pid": {"isolated"}, "out_trade_no": {payment.No}, "trade_no": {payment.Trade}, "trade_status": {"TRADE_SUCCESS"}, "money": {payment.Amount}, "type": {"alipay"}, "sign_type": {"MD5"}}
	switch input.Mode {
	case "success":
	case "bad_signature":
	case "wrong_amount":
		form.Set("money", "99999.00")
	case "wrong_merchant":
		form.Set("pid", "foreign")
	case "pending":
		form.Set("trade_status", "WAIT_BUYER_PAY")
	default:
		http.Error(w, "invalid mode", 400)
		return
	}
	form.Set("sign", sign(form, p.secret))
	if input.Mode == "bad_signature" {
		form.Set("sign", "bad")
	}
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.PostForm("http://upstream:8080/api/v1/payments/callback", form)
	if err != nil {
		http.Error(w, "isolated callback unavailable", 503)
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	write(w, map[string]any{"http_status": response.StatusCode, "result": strings.TrimSpace(string(body))})
}
