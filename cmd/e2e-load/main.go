// Command e2e-load is a bounded, test-only sustained load generator. Its targets
// are fixed internal aliases: it cannot accept a merchant URL or Telegram token.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
)

const rate = 100
const workers = 16
const slots = 32

type metrics struct {
	Requests, Health, Accepted, Limited, Retryable, Unexpected, Dropped int64
	HealthP99MS, CallbackP99MS                                          int64
	ElapsedMS                                                           int64
	hist                                                                [201]int64
	callbackHist                                                        [201]int64
}
type outcome struct {
	health   bool
	category string
	latency  time.Duration
}

func (m *metrics) add(o outcome) {
	m.Requests++
	if o.health {
		m.Health++
		m.hist[min(200, int(o.latency/(25*time.Millisecond)))]++
	} else {
		m.callbackHist[min(200, int(o.latency/(25*time.Millisecond)))]++
	}
	switch o.category {
	case "ok":
		m.Accepted++
	case "limited":
		m.Limited++
	case "retry":
		m.Retryable++
	default:
		m.Unexpected++
	}
}
func (m metrics) p99() int64 {
	return percentile(m.hist, m.Health)
}
func percentile(hist [201]int64, count int64) int64 {
	var total int64
	for i, n := range hist {
		total += n
		if total*100 >= count*99 && count > 0 {
			return int64(i+1) * 25
		}
	}
	return 0
}
func (m metrics) valid(duration time.Duration) bool {
	return m.Unexpected == 0 && m.Dropped == 0 && m.Retryable <= 16 && m.Health > 0 && m.Limited > 0 && m.Accepted-m.Health >= slots && m.p99() <= 1500 && percentile(m.callbackHist, m.Requests-m.Health) <= 2500 && m.Requests >= int64(duration.Seconds()*rate*0.9)
}

type report struct {
	Kind                          string
	Metrics                       metrics
	Delivered, Fault429, Fault5xx int
}
type runner struct {
	client                 *http.Client
	bot, peer, key, secret string
}

func main() {
	if os.Getenv("E2E_LOAD_ENABLED") != "1" || os.Getenv("DUJIAO_CHANNEL_KEY") == "" || os.Getenv("DUJIAO_CHANNEL_SECRET") == "" {
		fmt.Fprintln(os.Stderr, "isolated load opt-in and synthetic credentials required")
		os.Exit(2)
	}
	duration, err := time.ParseDuration(os.Getenv("DUJIAO_SOAK_DURATION"))
	if err != nil || duration < time.Minute || duration > 30*time.Minute {
		fmt.Fprintln(os.Stderr, "duration must be between 1m and 30m")
		os.Exit(2)
	}
	transport := &http.Transport{MaxIdleConns: workers * 2, MaxIdleConnsPerHost: workers * 2, IdleConnTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	r := &runner{client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, bot: "http://bot:8444", peer: "http://peer:8080", key: os.Getenv("DUJIAO_CHANNEL_KEY"), secret: os.Getenv("DUJIAO_CHANNEL_SECRET")}
	ctx, cancel := context.WithTimeout(context.Background(), duration+2*time.Minute)
	defer cancel()
	if err := r.run(ctx, duration, json.NewEncoder(os.Stdout)); err != nil {
		fmt.Fprintln(os.Stderr, "isolated sustained acceptance failed")
		os.Exit(1)
	}
}

func classify(status int, retry string, body []byte, health bool) string {
	var envelope struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "unexpected"
	}
	if health {
		if status == 200 && envelope.Status == "ok" {
			return "ok"
		}
		return "unexpected"
	}
	if status == 200 {
		if envelope.Status == "sent" || envelope.Status == "duplicate_or_obsolete" {
			return "ok"
		}
		return "unexpected"
	}
	delay, err := strconv.Atoi(retry)
	if (status != 429 && status != 503) || err != nil || delay < 1 || delay > 3600 {
		return "unexpected"
	}
	if envelope.Status == "rate_limited" {
		return "limited"
	}
	if status == 503 && (envelope.Status == "delivery_busy" || envelope.Status == "notify_failed" || envelope.Status == "telegram_rate_limited") {
		return "retry"
	}
	// Explicit storage failures, generic 5xx, authentication and payload errors
	// are not expected denials. Only explicit busy/delivery failures have a
	// small retry budget and must resolve during the per-resource drain.
	return "unexpected"
}
func eventFor(id int) (string, callback.Event) {
	e := callback.Event{TelegramUserID: "910000001"}
	switch id % 3 {
	case 0:
		e.OrderID = uint(9000000 + id)
		return "order-paid", e
	case 1:
		e.OrderID = uint(9000000 + id)
		return "order-fulfilled", e
	default:
		e.RechargeNo = fmt.Sprintf("SOAK-%d", id)
		e.Amount = money.Format(int64(id + 1))
		e.Currency = "CNY"
		return "wallet-recharge-succeeded", e
	}
}
func (r *runner) request(ctx context.Context, health bool, id int) outcome {
	start := time.Now()
	path := "/healthz"
	method := "GET"
	var body []byte
	if !health {
		kind, e := eventFor(id)
		path = "/internal/" + kind
		body, _ = json.Marshal(e)
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, r.bot+path, bytes.NewReader(body))
	if err != nil {
		return outcome{health, "unexpected", time.Since(start)}
	}
	if !health {
		timestamp := time.Now().Unix()
		req.Header.Set("Dujiao-Next-Channel-Key", r.key)
		req.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
		req.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign(r.secret, method, path, timestamp, body))
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return outcome{health, "unexpected", time.Since(start)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	category := "unexpected"
	if err == nil && len(data) <= 4096 {
		category = classify(resp.StatusCode, resp.Header.Get("Retry-After"), data, health)
	}
	return outcome{health, category, time.Since(start)}
}
func (r *runner) fault(ctx context.Context, code int) error {
	body, _ := json.Marshal(map[string]any{"Method": "sendMessage", "Code": code, "Times": 1, "RetryAfter": 1})
	req, _ := http.NewRequestWithContext(ctx, "POST", r.peer+"/test/telegram-fault", bytes.NewReader(body))
	resp, err := r.client.Do(req)
	if err != nil {
		return errors.New("peer control failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return errors.New("peer control rejected")
	}
	return nil
}
func (r *runner) run(ctx context.Context, duration time.Duration, output *json.Encoder) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type job struct {
		health bool
		id     int
	}
	jobs := make(chan job, workers)
	results := make(chan outcome, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				o := r.request(ctx, j.health, j.id)
				select {
				case results <- o:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	done := make(chan struct{})
	var dropped int64
	var injectionErr error
	go func() {
		defer close(jobs)
		defer close(done)
		start := time.Now()
		ticker := time.NewTicker(time.Second / rate)
		defer ticker.Stop()
		phase, seq, cb := 0, 0, 0
		for {
			select {
			case <-ctx.Done():
				injectionErr = ctx.Err()
				return
			case now := <-ticker.C:
				elapsed := now.Sub(start)
				if elapsed >= duration {
					return
				}
				next := min(2, int(elapsed/(duration/3)))
				if next > phase {
					if err := r.fault(ctx, map[int]int{1: 429, 2: 500}[next]); err != nil {
						injectionErr = err
						return
					}
					phase = next
				}
				j := job{health: seq%5 != 0, id: phase*slots + cb%slots}
				seq++
				if !j.health {
					cb++
				}
				select {
				case jobs <- j:
				default:
					dropped++
				}
			}
		}
	}()
	go func() { <-done; wg.Wait(); close(results) }()
	start := time.Now()
	windowStart := start
	windowTick := time.NewTicker(10 * time.Second)
	defer windowTick.Stop()
	var total, window metrics
	for results != nil {
		select {
		case o, ok := <-results:
			if !ok {
				results = nil
				break
			}
			total.add(o)
			window.add(o)
		case <-windowTick.C:
			window.ElapsedMS = time.Since(windowStart).Milliseconds()
			window.HealthP99MS = window.p99()
			window.CallbackP99MS = percentile(window.callbackHist, window.Requests-window.Health)
			if err := output.Encode(report{Kind: "window", Metrics: window}); err != nil {
				return err
			}
			window = metrics{}
			windowStart = time.Now()
		}
	}
	total.Dropped = dropped
	total.ElapsedMS = time.Since(start).Milliseconds()
	total.HealthP99MS = total.p99()
	total.CallbackP99MS = percentile(total.callbackHist, total.Requests-total.Health)
	if window.Requests > 0 {
		window.ElapsedMS = time.Since(windowStart).Milliseconds()
		window.HealthP99MS = window.p99()
		window.CallbackP99MS = percentile(window.callbackHist, window.Requests-window.Health)
		if err := output.Encode(report{Kind: "window", Metrics: window}); err != nil {
			return err
		}
	}
	if injectionErr != nil || !total.valid(duration) {
		_ = output.Encode(report{Kind: "failed", Metrics: total})
		return errors.New("load thresholds failed")
	}
	// Drain every synthetic event, including ones denied while under pressure.
	// These are notification-only callbacks, never payment/recharge API retries.
	for id := 0; id <= 3*slots; id++ {
		for {
			o := r.request(ctx, false, id)
			if o.category == "ok" {
				break
			}
			if o.category != "limited" && o.category != "retry" {
				return errors.New("drain failed")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	resp, err := r.client.Get(r.peer + "/test/state")
	if err != nil {
		return errors.New("peer verification unavailable")
	}
	defer resp.Body.Close()
	var state struct {
		Messages         []struct{ Text string }
		Send429, Send5xx int
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state) != nil {
		return errors.New("peer verification invalid")
	}
	texts := map[string]bool{}
	for _, m := range state.Messages {
		if strings.Contains(m.Text, "订单支付成功") || strings.Contains(m.Text, "订单已完成交付") || strings.Contains(m.Text, "余额充值成功") {
			if texts[m.Text] {
				return errors.New("duplicate acknowledged notification")
			}
			texts[m.Text] = true
		}
	}
	if len(texts) != 3*slots+1 || state.Send429 != 1 || state.Send5xx != 1 {
		return errors.New("delivery or fault evidence missing")
	}
	return output.Encode(report{Kind: "summary", Metrics: total, Delivered: len(texts), Fault429: state.Send429, Fault5xx: state.Send5xx})
}
