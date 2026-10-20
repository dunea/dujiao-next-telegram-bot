//go:build realdujiao

package realdujiao

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/config"
)

// Dedicated receipt/restart regression, not a substitute for real financial
// callbacks in TestTelegramEndToEnd. These synthetic notifications do not buy,
// pay, recharge or fulfill anything in the upstream store.
func TestTelegramCallbackRestart(t *testing.T) {
	f := newTelegramFlow(t, telegramDriver(t))
	assertTunnelRequestLifetime(t, f)
	cases := []struct {
		kind  string
		event callback.Event
	}{
		{"order-paid", callback.Event{OrderID: 9900001, TelegramUserID: "910000001"}},
		{"order-fulfilled", callback.Event{OrderID: 9900001, TelegramUserID: "910000001"}},
		{"wallet-recharge-succeeded", callback.Event{RechargeNo: "RESTART-ONLY", Amount: "1.23", Currency: "CNY", TelegramUserID: "910000001"}},
	}
	for _, c := range cases {
		if f.signed(f.config, c.kind, c.event, 200).Status != "sent" {
			t.Fatal("initial notification did not complete")
		}
	}
	namespace := (config.Config{DujiaoBaseURL: "http://upstream:8080", DujiaoChannelKey: f.config.Key}).SessionPrefix()
	read := func(event callback.Event) string {
		key := namespace + ":callback:v1:" + callback.ReceiptKey(buyerID, event)
		return f.s.docker("exec", f.s.redis, "redis-cli", "--raw", "GET", key)
	}
	snapshots := make([]string, len(cases))
	for i, c := range cases {
		snapshots[i] = read(c.event)
		var receipt callback.Receipt
		if json.Unmarshal([]byte(snapshots[i]), &receipt) != nil || !callback.ValidReceipt(receipt) || receipt.Events[c.kind].Status != "sent" {
			t.Fatal("successful callback did not leave a valid sent receipt")
		}
	}
	if f.countNotifications() != 3 {
		t.Fatal("initial callbacks did not send three notices")
	}
	for cycle := 1; cycle <= 5; cycle++ {
		f.s.docker("restart", f.bot)
		f.healthy()
		for i, c := range cases {
			if read(c.event) != snapshots[i] {
				t.Fatalf("restart %d changed completed receipt before replay", cycle)
			}
			if f.signed(f.config, c.kind, c.event, 200).Status != "duplicate_or_obsolete" {
				t.Fatalf("restart %d did not recognize completed receipt", cycle)
			}
			if read(c.event) != snapshots[i] {
				t.Fatalf("restart %d replay mutated completed receipt", cycle)
			}
		}
		if f.countNotifications() != 3 {
			t.Fatalf("restart %d replay sent a duplicate notice", cycle)
		}
		t.Logf("restart %d/5: three strict 200 duplicate acknowledgements, receipts unchanged, no extra notices", cycle)
	}
}

func assertTunnelRequestLifetime(t *testing.T, f *telegramFlow) {
	t.Helper()
	// Positive control reproduces the old bytes.Reader -> EOF transport using
	// the same BusyBox binary. It must cancel the server context deterministically.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", f.peer, "nc", "-w", "5", "127.0.0.1", "8080")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("cannot create EOF control input")
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("cannot start EOF control")
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	_, err = io.WriteString(input, "GET /test/request-context HTTP/1.1\r\nHost: peer\r\nConnection: close\r\n\r\n")
	if err != nil || input.Close() != nil {
		t.Fatal("cannot send EOF control")
	}
	response, err := http.ReadResponse(bufio.NewReader(output), nil)
	if err != nil {
		t.Fatal("EOF control did not return a response")
	}
	defer response.Body.Close()
	var old struct{ Canceled bool }
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&old) != nil || !old.Canceled {
		t.Fatal("old EOF transport did not reproduce request cancellation")
	}
	// Negative control uses the actual fixed host tunnel, not a replacement
	// fake client. Keeping stdin open must preserve the entire request lifetime.
	fixed, err := f.s.client.Get(f.peerURL + "/test/request-context")
	if err != nil {
		t.Fatal("fixed tunnel context probe failed")
	}
	defer fixed.Body.Close()
	var current struct{ Canceled bool }
	if fixed.StatusCode != 200 || json.NewDecoder(fixed.Body).Decode(&current) != nil || current.Canceled {
		t.Fatal("fixed tunnel canceled backend request prematurely")
	}
	t.Log("transport control: EOF half-close cancels backend request; fixed tunnel preserves context")
}
