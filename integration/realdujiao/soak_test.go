//go:build realdujiao

package realdujiao

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type loadReport struct {
	Kind    string
	Metrics struct {
		Requests, Health, Accepted, Limited, Retryable, Unexpected, Dropped int64
		HealthP99MS, CallbackP99MS, ElapsedMS                               int64
	}
	Delivered, Fault429, Fault5xx int
}
type processMetrics struct{ RSS, Threads int64 }

func TestTelegramSustainedLoad(t *testing.T) {
	if os.Getenv("DUJIAO_SOAK_TEST") != "1" {
		t.Skip("explicit isolated sustained load opt-in required")
	}
	durationText := os.Getenv("DUJIAO_SOAK_DURATION")
	if durationText == "" {
		durationText = "5m"
	}
	duration, err := time.ParseDuration(durationText)
	if err != nil || duration < time.Minute || duration > 30*time.Minute {
		t.Fatal("DUJIAO_SOAK_DURATION must be between 1m and 30m")
	}
	f := newTelegramFlow(t, telegramDriver(t))
	s := f.s
	f.command("/start")
	f.waitText("欢迎")
	c := s.channel(f.config, false)
	f.assertWallet(c, "0.00", 0)
	initial, redisInitial := f.resources()
	peak := initial
	redisPeak := redisInitial
	samples := 1
	ctx, cancel := context.WithTimeout(context.Background(), duration+2*time.Minute)
	defer cancel()
	// Generate load inside the internal network, not through one docker-exec
	// tunnel per request. The fixture binary cannot accept arbitrary target URLs.
	cmd := exec.CommandContext(ctx, "docker", "exec", "-e", "E2E_LOAD_ENABLED=1", "-e", "DUJIAO_SOAK_DURATION="+durationText, "-e", "DUJIAO_CHANNEL_KEY="+f.config.Key, "-e", "DUJIAO_CHANNEL_SECRET="+f.config.Secret, f.peer, "/usr/local/bin/e2e-load")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("cannot create isolated load stream")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start isolated load process")
	}
	reports := make(chan loadReport, 16)
	finished := make(chan error, 1)
	go func() {
		defer close(reports)
		scanner := bufio.NewScanner(stdout)
		var parseErr error
		for scanner.Scan() {
			var r loadReport
			if json.Unmarshal(scanner.Bytes(), &r) != nil {
				parseErr = context.Canceled
				continue
			}
			select {
			case reports <- r:
			case <-ctx.Done():
			}
		}
		if scanner.Err() != nil {
			parseErr = scanner.Err()
		}
		err := cmd.Wait()
		if err == nil {
			err = parseErr
		}
		finished <- err
	}()
	defer func() { cancel(); _ = cmd.Process.Kill() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var summary *loadReport
	for reports != nil {
		select {
		case report, ok := <-reports:
			if !ok {
				reports = nil
				break
			}
			if report.Kind != "window" && report.Kind != "summary" && report.Kind != "failed" {
				t.Fatal("unexpected load report type")
			}
			t.Logf("load %s: requests=%d health=%d accepted=%d limited=%d retryable=%d unexpected=%d dropped=%d health_p99_ms=%d callback_p99_ms=%d", report.Kind, report.Metrics.Requests, report.Metrics.Health, report.Metrics.Accepted, report.Metrics.Limited, report.Metrics.Retryable, report.Metrics.Unexpected, report.Metrics.Dropped, report.Metrics.HealthP99MS, report.Metrics.CallbackP99MS)
			if report.Kind == "summary" {
				copy := report
				summary = &copy
			}
		case <-ticker.C:
			current, redisCurrent := f.resources()
			samples++
			peak.RSS = max(peak.RSS, current.RSS)
			peak.Threads = max(peak.Threads, current.Threads)
			redisPeak = max(redisPeak, redisCurrent)
			if !resourceBudget(initial, current, redisInitial, redisCurrent) {
				t.Fatal("sustained resource budget exceeded")
			}
		case <-ctx.Done():
			t.Fatal("isolated sustained load deadline exceeded")
		}
	}
	err = <-finished
	_ = os.WriteFile(filepath.Join(s.dir, "load-stderr.log"), stderr.Bytes(), 0600)
	if err != nil || summary == nil {
		t.Fatal("isolated load failed; diagnostics retained privately")
	}
	last, redisLast := f.resources()
	samples++
	peak.RSS = max(peak.RSS, last.RSS)
	peak.Threads = max(peak.Threads, last.Threads)
	redisPeak = max(redisPeak, redisLast)
	if !resourceBudget(initial, last, redisInitial, redisLast) {
		t.Fatal("post-load resource budget exceeded")
	}
	if summary.Metrics.Unexpected != 0 || summary.Metrics.Dropped != 0 || summary.Metrics.Limited == 0 || summary.Delivered != 97 || summary.Fault429 != 1 || summary.Fault5xx != 1 || samples < 3 {
		t.Fatal("sustained acceptance evidence incomplete")
	}
	f.healthy()
	f.assertWallet(c, "0.00", 0)
	orders, err := c.Orders(context.Background(), strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	orders = must(t, "load financial isolation", orders, err)
	if orders.Total != 0 {
		t.Fatal("notification load unexpectedly created orders")
	}
	f.command("/wallet")
	f.waitText("0.00 CNY")
	t.Logf("PASS: duration=%s rate=100/s workers=16 delivered=%d faults429=%d faults5xx=%d samples=%d bot_peak_rss_bytes=%d bot_rss_growth_bytes=%d bot_peak_threads=%d redis_peak_growth_bytes=%d; wallet/ledger/orders unchanged", durationText, summary.Delivered, summary.Fault429, summary.Fault5xx, samples, peak.RSS, peak.RSS-initial.RSS, peak.Threads, redisPeak-redisInitial)
}

func resourceBudget(initial, current processMetrics, redisInitial, redisCurrent int64) bool {
	return current.RSS <= 128<<20 && current.RSS-initial.RSS <= 64<<20 && current.Threads <= 128 && redisCurrent-redisInitial <= 16<<20
}

func parseProcessMetrics(body string) (processMetrics, bool) {
	var p processMetrics
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		switch fields[0] {
		case "VmRSS:":
			if len(fields) == 3 && fields[2] == "kB" {
				p.RSS = n * 1024
			}
		case "Threads:":
			p.Threads = n
		}
	}
	return p, p.RSS > 0 && p.Threads > 0
}
func (f *telegramFlow) resources() (processMetrics, int64) {
	body := f.s.docker("exec", f.bot, "cat", "/proc/1/status")
	p, ok := parseProcessMetrics(body)
	if !ok {
		f.s.t.Fatal("missing Bot process resource metrics")
	}
	info := f.s.docker("exec", f.s.redis, "redis-cli", "INFO", "memory")
	for _, line := range strings.Split(info, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "used_memory:"); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err == nil && n > 0 {
				return p, n
			}
		}
	}
	f.s.t.Fatal("missing Redis resource metrics")
	return p, 0
}

func TestParseProcessMetrics(t *testing.T) {
	p, ok := parseProcessMetrics("Name:\tdujiao-bot\nVmRSS:\t20480 kB\nThreads:\t12\n")
	if !ok || p.RSS != 20<<20 || p.Threads != 12 {
		t.Fatal("incorrect process resource metrics")
	}
	for _, body := range []string{"", "VmRSS: NaN kB\nThreads: 12", "VmRSS: 20 MB\nThreads: 12", "VmRSS: 20 kB"} {
		if _, ok := parseProcessMetrics(body); ok {
			t.Fatal("invalid resource metrics accepted")
		}
	}
	initial := processMetrics{RSS: 20 << 20, Threads: 10}
	for _, p := range []processMetrics{{RSS: 129 << 20, Threads: 10}, {RSS: 85 << 20, Threads: 10}, {RSS: 21 << 20, Threads: 129}} {
		if resourceBudget(initial, p, 1, 1) {
			t.Fatal("invalid resource budget accepted")
		}
	}
	if resourceBudget(initial, initial, 1, 17<<20) || !resourceBudget(initial, initial, 1, 1) {
		t.Fatal("Redis resource budget incorrect")
	}
}
