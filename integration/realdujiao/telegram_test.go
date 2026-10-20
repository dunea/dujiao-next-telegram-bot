//go:build realdujiao

package realdujiao

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/config"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/setup"
)

const buyerID int64 = 910000001

type tgMessage struct {
	Filename   string
	FileSHA256 string
	FileBytes  int
	Method     string
	ID         int
	ChatID     int64
	Text       string
	Keyboard   json.RawMessage
}
type peerState struct {
	Messages                                  []tgMessage
	Payments                                  []struct{ No, Trade, Amount string }
	Photos, Polls, PollErrors, PendingUpdates int
	Send429, Send5xx                          int
}
type telegramFlow struct {
	s               *stack
	peer, bot       string
	peerURL, botURL string
	cursor          int
	inputID         int
	config          fixture
	paymentChannel  uint
	gatewaySecret   string
}

func newTelegramFlow(t *testing.T, driver string) *telegramFlow {
	t.Helper()
	s := newStack(t, driver)
	s.start(2, "telegram")
	f := s.seed()
	flow := &telegramFlow{s: s, peer: s.id + "-peer", bot: s.id + "-bot"}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	image := s.id + "-e2e"
	s.images = append(s.images, image)
	s.docker("build", "--file", filepath.Join(root, "Dockerfile.e2e"), "--label", ownerLabel+"="+s.id, "--tag", image, root)
	secret := randomHex(32)
	s.containers = append(s.containers, flow.peer)
	s.docker("run", "-d", "--name", flow.peer, "--label", ownerLabel+"="+s.id, "--network", s.id, "--network-alias", "peer", "-e", "E2E_PEER_ENABLED=1", "-e", "E2E_GATEWAY_SECRET="+secret, image)
	peerTunnel := s.tunnel(func() string { return flow.peer }, "8080")
	t.Cleanup(peerTunnel.Close)
	flow.peerURL = peerTunnel.URL
	s.wait(func() bool {
		r, err := s.client.Get(flow.peerURL + "/healthz")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	// Real configuration APIs; the gateway and Bot targets are internal aliases.
	var channels []struct {
		ID  uint   `json:"id"`
		Key string `json:"channel_key"`
	}
	s.admin("GET", "/channel-clients", s.login(), nil, &channels)
	if len(channels) != 1 || channels[0].Key != f.Key {
		t.Fatal("unexpected channel fixture")
	}
	s.admin("PUT", fmt.Sprintf("/channel-clients/%d", channels[0].ID), s.login(), map[string]any{"callback_url": "http://bot:8444"}, nil)
	// Fictional local administrator + synthetic store only; exercise the normal
	// setup flow rather than disabling the upstream payment compliance gate.
	s.admin("POST", "/compliance/acknowledge", s.login(), map[string]string{"segment1": "我已阅读并理解上述合规声明提醒", "segment2": "知悉相关法律风险", "segment3": "并确认自行承担部署运营和收费行为产生的法律责任"}, nil)
	s.admin("PUT", "/settings", s.login(), map[string]any{"key": "payment_config", "value": map[string]any{"customer_fee_enabled": true}}, nil)
	var channel struct {
		ID uint `json:"id"`
	}
	s.admin("POST", "/payment-channels", s.login(), map[string]any{"name": "Isolated gateway", "provider_type": "epay", "channel_type": "alipay", "interaction_mode": "qr", "fee_rate": "2.50", "fixed_fee": "1.00", "is_active": true, "config_json": map[string]any{"gateway_url": "http://peer:8080", "epay_version": "v1", "merchant_id": "isolated", "merchant_key": secret, "notify_url": "http://upstream:8080/api/v1/payments/callback", "return_url": "https://payments.example.invalid/return"}}, &channel)
	s.containers = append(s.containers, flow.bot)
	sessionKey := randomHex(32)
	s.docker("run", "-d", "--name", flow.bot, "--label", ownerLabel+"="+s.id, "--network", s.id, "--network-alias", "bot", "--entrypoint", "/usr/local/bin/dujiao-bot",
		"-e", "DUJIAO_BASE_URL=http://upstream:8080", "-e", "DUJIAO_CHANNEL_KEY="+f.Key, "-e", "DUJIAO_CHANNEL_SECRET="+f.Secret,
		"-e", "TELEGRAM_BOT_TOKEN=999001:isolated-e2e", "-e", "TELEGRAM_API_URL=http://peer:8080", "-e", "BOT_POLL_TIMEOUT=1s", "-e", "BOT_HTTP_LISTEN=:8444",
		"-e", "BOT_REDIS_URL=redis://redis:6379/0", "-e", "BOT_SESSION_ENCRYPTION_KEY="+sessionKey, "-e", "BOT_AUDIT_FILE=/tmp/audit.jsonl", image)
	botTunnel := s.tunnel(func() string { return flow.bot }, "8444")
	t.Cleanup(botTunnel.Close)
	flow.botURL = botTunnel.URL
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := json.Marshal(flow.state())
			_ = os.WriteFile(filepath.Join(s.dir, "peer-state.json"), data, 0600)
		}
	})
	flow.healthy()
	// Check the final image's offline entry point using actual injected settings,
	// without printing its JSON or any synthetic secrets into test/CI output.
	diagnostic := s.docker("exec", flow.bot, "/usr/local/bin/dujiao-bot", "setup", "check")
	var checked setup.Report
	if json.Unmarshal([]byte(diagnostic), &checked) != nil || !checked.ConfigurationOK || checked.NetworkChecks != "not_run" || checked.Source != "process" {
		t.Fatal("offline setup entry point failed in real Bot image")
	}
	expected := config.Config{DujiaoBaseURL: "http://upstream:8080", DujiaoChannelKey: f.Key}
	if checked.SessionNamespace != expected.SessionPrefix() {
		t.Fatal("setup/runtime namespace mismatch")
	}
	for _, private := range []string{f.Key, f.Secret, sessionKey, secret, "999001:isolated-e2e"} {
		if strings.Contains(diagnostic, private) {
			t.Fatal("setup diagnostic leaked a credential")
		}
	}
	t.Log("offline setup check passed in real Bot image; values omitted, namespace stable")
	flow.config, flow.paymentChannel, flow.gatewaySecret = f, channel.ID, secret
	return flow
}

func TestTelegramEndToEnd(t *testing.T) {
	flow := newTelegramFlow(t, telegramDriver(t))
	s, f, secret := flow.s, flow.config, flow.gatewaySecret
	flow.command("/start")
	flow.waitText("欢迎")
	flow.command("/wallet")
	flow.waitText("0.00 CNY")
	flow.command("/recharge")
	menu := flow.waitButton("rechargepick:")
	flow.click(menu, button(menu, "rechargepick:", ":100.00"))
	menu = flow.waitButton("rechargepay:")
	flow.click(menu, button(menu, "rechargepay:", fmt.Sprint(flow.paymentChannel)))
	flow.waitText("103.50 CNY")
	s.wait(func() bool { state := flow.state(); return len(state.Payments) == 1 && state.Photos > 0 })
	state := flow.state()
	amount, valid := money.Parse(state.Payments[0].Amount)
	if len(state.Payments) != 1 || !valid || amount != 10350 || state.Photos == 0 {
		t.Fatal("real recharge did not create fee-inclusive gateway payment and QR photo")
	}
	c := s.channel(f, false)
	ctx := context.Background()
	flow.assertWallet(c, "0.00", 0)
	for _, mode := range []string{"bad_signature", "wrong_amount", "wrong_merchant", "pending"} {
		flow.pay(0, mode)
		flow.assertWallet(c, "0.00", 0)
	}
	// The real worker must retry a failed Telegram send; returning 429 would
	// silently lose it because this upstream intentionally does not retry 4xx.
	flow.control("/test/telegram-fault", map[string]any{"Method": "sendMessage", "Code": 429, "Times": 1, "RetryAfter": 1}, nil)
	flow.pay(0, "success")
	flow.assertWallet(c, "100.00", 1)
	rechargeNotice := flow.waitText("余额充值成功")
	if button(rechargeNotice, "wallet", "") != "wallet" {
		t.Fatal("recharge notice missing balance shortcut")
	}
	flow.click(rechargeNotice, "wallet")
	flow.waitText("100.00 CNY")
	flow.pay(0, "success")
	flow.assertWallet(c, "100.00", 1)
	flow.command("/products")
	menu = flow.waitButton("cat:")
	flow.click(menu, button(menu, "cat:", ""))
	menu = flow.waitButton("product:")
	flow.click(menu, button(menu, "product:", ""))
	menu = flow.waitButton("buy:")
	flow.click(menu, button(menu, "buy:", ""))
	menu = flow.waitButton("formconfirm:")
	flow.click(menu, button(menu, "formconfirm:", ""))
	menu = flow.waitButton("pay:")
	// Select the online gateway (not wallet-only), entirely via returned buttons.
	flow.click(menu, button(menu, "pay:", fmt.Sprintf(":%d:0", flow.paymentChannel)))
	flow.waitText("支付")
	s.wait(func() bool { return len(flow.state().Payments) == 2 })
	orders, err := c.Orders(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", "", 1, 20)
	orders = must(t, "E2E orders", orders, err)
	if orders.Total != 1 || len(orders.Items) != 1 {
		t.Fatal("Telegram checkout did not create exactly one order")
	}
	orderID := orders.Items[0].OrderID
	for _, mode := range []string{"bad_signature", "wrong_amount", "wrong_merchant", "pending"} {
		flow.pay(1, mode)
		detail, err := c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
		detail = must(t, "unpaid order", detail, err)
		if detail.Status != "pending_payment" {
			t.Fatal("invalid provider callback paid order")
		}
	}
	flow.pay(1, "success")
	paidNotice := flow.waitText("订单支付成功")
	if button(paidNotice, "order:", "") != fmt.Sprintf("order:%d", orderID) || button(paidNotice, "file:", "") != "" {
		t.Fatal("paid notice did not offer the exact order or advertised premature TXT")
	}
	flow.click(paidNotice, button(paidNotice, "order:", ""))
	paidView := flow.waitText("订单详情")
	if button(paidView, "file:", "") != "" {
		t.Fatal("manual order exposed delivery before fulfillment")
	}
	flow.pay(1, "success")
	detail, err := c.Order(ctx, strconv.FormatInt(buyerID, 10), "zh-CN", orderID)
	detail = must(t, "paid order", detail, err)
	if detail.PaidAmount != "7.25" || detail.OnlinePaidAmount != "7.25" {
		t.Fatal("online payment amount mismatch")
	}
	flow.assertWallet(c, "100.00", 1)
	// Complete real manual fulfillment; parent order has child item ownership.
	fulfillID := orderID
	if len(detail.Children) > 0 {
		fulfillID = detail.Children[0].OrderID
	}
	s.admin("POST", "/fulfillments", s.login(), map[string]any{"order_id": fulfillID, "payload": "SYNTHETIC-DELIVERY-ONLY"}, nil)
	fulfilled := flow.waitText("订单已完成交付")
	_, idText, ok := strings.Cut(fulfilled.Text, "订单 ID：")
	fulfilledID, parseErr := strconv.ParseUint(strings.TrimSpace(idText), 10, 64)
	if !ok || parseErr != nil || fulfilledID == 0 {
		t.Fatal("fulfilled notification did not identify real order")
	}
	if button(fulfilled, "file:", "") != fmt.Sprintf("file:%d", orderID) || button(fulfilled, "order:", "") != fmt.Sprintf("order:%d", orderID) || strings.Contains(fulfilled.Text, "SYNTHETIC-DELIVERY-ONLY") {
		t.Fatal("fulfilled notice missing exact private shortcuts or exposed cards")
	}
	flow.click(fulfilled, button(fulfilled, "file:", ""))
	flow.waitMessage(func(m tgMessage) bool { return m.Method == "sendDocument" && m.FileBytes > 0 })
	flow.waitText("TXT 已发送")
	flow.command("/orders")
	menu = flow.waitButton("order:")
	flow.click(menu, button(menu, "order:", ""))
	menu = flow.waitButton("file:")
	flow.click(menu, button(menu, "file:", ""))
	flow.waitText("TXT 已发送")
	// Notifications may be retried/reordered, never replaying a financial API.
	event := callback.Event{OrderID: uint(fulfilledID), TelegramUserID: strconv.FormatInt(buyerID, 10)}
	flow.signed(f, "order-paid", event, 200)
	flow.signed(f, "order-fulfilled", event, 200)
	before := flow.countNotifications()
	if before != 3 {
		t.Fatal("expected exactly one acknowledged recharge, paid and fulfilled notification")
	}
	s.docker("restart", flow.bot)
	flow.healthy()
	flow.signed(f, "order-fulfilled", event, 200)
	if flow.countNotifications() != before {
		t.Fatal("Bot restart forgot completed receipt")
	}
	flow.click(fulfilled, button(fulfilled, "file:", ""))
	flow.waitText("TXT 已发送")
	files := 0
	for _, m := range flow.state().Messages {
		if m.Method == "sendDocument" {
			files++
		}
	}
	if files != 1 {
		t.Fatal("notice/order/restart entry points duplicated manual TXT")
	}
	// Only stop this test's Redis; no money mutation is attempted during outage.
	s.docker("stop", "--time", "15", s.redis)
	flow.signed(f, "order-paid", callback.Event{OrderID: orderID + 100, TelegramUserID: event.TelegramUserID}, 503)
	s.docker("start", s.redis)
	flow.healthy()
	flow.assertWallet(c, "100.00", 1)
	flow.control("/test/telegram-fault", map[string]any{"Method": "getUpdates", "Code": 503, "Times": 1}, nil)
	flow.command("/wallet")
	flow.waitText("100.00 CNY")
	s.wait(func() bool { return flow.state().PollErrors == 1 && flow.state().PendingUpdates == 0 })
	if flow.countNotifications() != before {
		t.Fatal("replayed callback or Redis outage duplicated notices")
	}
	audit := s.docker("exec", flow.bot, "cat", "/tmp/audit.jsonl")
	for _, private := range []string{f.Key, f.Secret, secret, "isolated-e2e", "SYNTHETIC-DELIVERY-ONLY", "payments.example.invalid"} {
		if strings.Contains(audit, private) {
			t.Fatal("private content leaked into Bot audit")
		}
	}
	for _, action := range []string{"wallet.recharge", "payment.create", "callback.order_paid", "callback.order_fulfilled", "callback.recharge_succeeded"} {
		if !strings.Contains(audit, action) {
			t.Fatalf("missing audit action %s", action)
		}
	}
	t.Log("PASS: real Bot polling/buttons -> real Dujiao-Next -> signed provider callbacks -> real queue -> Bot receipts -> isolated Telegram API; recharge/order/fulfillment, faults and restart verified")
}

func telegramDriver(t *testing.T) string {
	t.Helper()
	if os.Getenv("DUJIAO_E2E_TEST") != "1" {
		t.Skip("explicit isolated Telegram E2E opt-in required")
	}
	driver := os.Getenv("DUJIAO_REAL_DB")
	if driver != "sqlite" && driver != "postgres" {
		t.Fatal("DUJIAO_REAL_DB must be sqlite or postgres")
	}
	return driver
}

func (f *telegramFlow) control(path string, input, target any) {
	f.s.t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	method := "POST"
	if input == nil {
		method = "GET"
	}
	r, err := http.NewRequest(method, f.peerURL+path, bytes.NewReader(body))
	if err != nil {
		f.s.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := f.s.client.Do(r)
	if err != nil {
		f.s.t.Fatal("peer transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		f.s.t.Fatalf("peer %s returned HTTP %d", path, resp.StatusCode)
	}
	if target != nil && json.NewDecoder(resp.Body).Decode(target) != nil {
		f.s.t.Fatal("invalid peer result")
	}
}
func (f *telegramFlow) state() peerState {
	var result peerState
	f.control("/test/state", nil, &result)
	return result
}
func (f *telegramFlow) command(text string, replyTo ...tgMessage) {
	// Incoming and outgoing messages share one Telegram private-chat sequence.
	for _, m := range f.state().Messages {
		f.inputID = max(f.inputID, m.ID)
	}
	f.inputID++
	message := map[string]any{"message_id": f.inputID, "date": time.Now().Unix(), "chat": map[string]any{"id": buyerID, "type": "private"}, "from": map[string]any{"id": buyerID, "is_bot": false, "first_name": "Synthetic", "language_code": "zh-hans"}, "text": text}
	if len(replyTo) > 0 {
		message["reply_to_message"] = map[string]any{"message_id": replyTo[0].ID, "chat": map[string]any{"id": buyerID, "type": "private"}}
	}
	f.control("/test/update", map[string]any{"message": message}, nil)
}
func (f *telegramFlow) click(m tgMessage, data string) {
	if data == "" {
		f.s.t.Fatal("expected real button missing")
	}
	f.inputID++
	message := map[string]any{"message_id": m.ID, "date": time.Now().Unix(), "chat": map[string]any{"id": buyerID, "type": "private"}, "text": m.Text}
	if len(m.Keyboard) > 0 {
		message["reply_markup"] = m.Keyboard
	}
	f.control("/test/update", map[string]any{"callback_query": map[string]any{"id": fmt.Sprint(f.inputID), "from": map[string]any{"id": buyerID, "is_bot": false, "first_name": "Synthetic"}, "data": data, "chat_instance": "isolated", "message": message}}, nil)
}
func button(m tgMessage, prefix, suffix string) string {
	var keyboard struct {
		Rows [][]struct {
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal(m.Keyboard, &keyboard)
	for _, row := range keyboard.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Data, prefix) && strings.HasSuffix(b.Data, suffix) {
				return b.Data
			}
		}
	}
	return ""
}
func (f *telegramFlow) waitMessage(match func(tgMessage) bool) tgMessage {
	f.s.t.Helper()
	var found tgMessage
	f.s.wait(func() bool {
		state := f.state()
		for i := f.cursor; i < len(state.Messages); i++ {
			if match(state.Messages[i]) {
				f.cursor = i + 1
				found = state.Messages[i]
				return true
			}
		}
		return false
	})
	return found
}
func (f *telegramFlow) waitText(text string) tgMessage {
	f.s.t.Logf("await Telegram text: %s", text)
	return f.waitMessage(func(m tgMessage) bool { return strings.Contains(m.Text, text) })
}
func (f *telegramFlow) waitButton(prefix string) tgMessage {
	f.s.t.Logf("await Telegram button: %s", prefix)
	return f.waitMessage(func(m tgMessage) bool { return button(m, prefix, "") != "" })
}
func (f *telegramFlow) pay(index int, mode string) {
	f.control("/test/pay", map[string]any{"Index": index, "Mode": mode}, nil)
}
func (f *telegramFlow) healthy() {
	f.s.wait(func() bool {
		if f.s.docker("inspect", "--format", "{{.State.Running}}", f.bot) != "true" {
			f.s.t.Fatal("isolated Bot exited; inspect private test logs")
		}
		r, err := f.s.client.Get(f.botURL + "/healthz")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
}
func (f *telegramFlow) assertWallet(c *dujiao.Client, balance string, count int64) {
	f.s.t.Helper()
	wallet, err := c.Wallet(context.Background(), strconv.FormatInt(buyerID, 10))
	wallet = must(f.s.t, "E2E wallet", wallet, err)
	tx, err := c.WalletTransactions(context.Background(), strconv.FormatInt(buyerID, 10), 1, 20)
	tx = must(f.s.t, "E2E ledger", tx, err)
	if wallet.Balance != balance || tx.Total != count {
		f.s.t.Fatal("callback changed wallet incorrectly or credited twice")
	}
}
func (f *telegramFlow) countNotifications() int {
	count := 0
	for _, m := range f.state().Messages {
		if strings.Contains(m.Text, "余额充值成功") || strings.Contains(m.Text, "订单支付成功") || strings.Contains(m.Text, "订单已完成交付") {
			count++
		}
	}
	return count
}

type callbackResult struct{ Status, Stage string }

func (f *telegramFlow) signed(config fixture, kind string, event callback.Event, want int) callbackResult {
	f.s.t.Helper()
	body, _ := json.Marshal(event)
	path := "/internal/" + kind
	timestamp := time.Now().Unix()
	req, _ := http.NewRequest("POST", f.botURL+path, bytes.NewReader(body))
	req.Header.Set("Dujiao-Next-Channel-Key", config.Key)
	req.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("Dujiao-Next-Channel-Signature", dujiao.Sign(config.Secret, "POST", path, timestamp, body))
	resp, err := f.s.client.Do(req)
	if err != nil {
		f.s.t.Fatal("callback transport failure")
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var result callbackResult
	parseErr := json.Unmarshal(responseBody, &result)
	if resp.StatusCode != want || parseErr != nil {
		_ = os.WriteFile(filepath.Join(f.s.dir, "callback-error.json"), responseBody, 0600)
		reason := "unknown_response"
		for _, known := range []string{"delivery_busy", "delivery_unavailable", "receipt_unavailable", "limiter_unavailable", "rate_limited", "notify_failed", "telegram_rate_limited"} {
			if result.Status == known {
				reason = known
			}
		}
		stage := "unknown"
		if result.Stage == "claim" || result.Stage == "finalize" {
			stage = result.Stage
		}
		f.s.t.Fatalf("callback %s: got %d, expected %d, reason=%s stage=%s", kind, resp.StatusCode, want, reason, stage)
	}
	return result
}
