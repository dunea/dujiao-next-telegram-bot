package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

type affiliateTestState struct {
	mu             sync.Mutex
	dashboard      dujiao.AffiliateDashboard
	clicks         []dujiao.AffiliateClick
	opens          []dujiao.TelegramIdentity
	identities     []dujiao.TelegramIdentity
	queries        []string
	commissions    []dujiao.AffiliateCommission
	withdrawals    []dujiao.AffiliateWithdrawal
	failureCode    string
	failureStatus  int
	resolveMissing bool
	inviteRequired bool
	openStatus     string
}

func (state *affiliateTestState) serveHTTP(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	var data any
	state.queries = append(state.queries, r.URL.RequestURI())
	if state.failureCode != "" && strings.Contains(r.URL.Path, "/affiliate/") {
		status := state.failureStatus
		if status == 0 {
			status = 400
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": status, "error_code": state.failureCode, "msg": "untrusted <private account>"})
		return
	}
	switch r.URL.Path {
	case "/api/v1/channel/identities/telegram/resolve", "/api/v1/channel/identities/telegram/provision":
		var identity dujiao.TelegramIdentity
		if err := json.NewDecoder(r.Body).Decode(&identity); err != nil {
			t.Error(err)
		}
		state.identities = append(state.identities, identity)
		if state.resolveMissing && strings.HasSuffix(r.URL.Path, "/resolve") {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 404, "error_code": "identity_not_found"})
			return
		}
		if state.inviteRequired && strings.HasSuffix(r.URL.Path, "/provision") && identity.InviteCode != "JOIN" {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 403, "error_code": "invite_code_required"})
			return
		}
		data = dujiao.IdentityResult{Bound: true, UserID: 42}
	case "/api/v1/channel/affiliate/dashboard":
		data = state.dashboard
	case "/api/v1/channel/affiliate/open":
		var identity dujiao.TelegramIdentity
		if err := json.NewDecoder(r.Body).Decode(&identity); err != nil {
			t.Error(err)
		}
		state.opens = append(state.opens, identity)
		state.dashboard.Opened, state.dashboard.AffiliateCode = true, "AFF42"
		status := state.openStatus
		if status == "" {
			status = "active"
		}
		data = dujiao.AffiliateProfile{ID: 7, Code: "AFF42", Status: status}
	case "/api/v1/channel/affiliate/click":
		var click dujiao.AffiliateClick
		if err := json.NewDecoder(r.Body).Decode(&click); err != nil {
			t.Error(err)
		}
		state.clicks = append(state.clicks, click)
		data = map[string]bool{"ok": true}
	case "/api/v1/channel/affiliate/commissions":
		page := positiveAffiliateTestPage(r)
		start := min((page-1)*5, len(state.commissions))
		end := min(start+5, len(state.commissions))
		data = dujiao.AffiliateCommissionList{Items: state.commissions[start:end], Page: page, TotalPages: int64((len(state.commissions) + 4) / 5), Total: int64(len(state.commissions))}
	case "/api/v1/channel/affiliate/withdraws":
		page := positiveAffiliateTestPage(r)
		start := min((page-1)*5, len(state.withdrawals))
		end := min(start+5, len(state.withdrawals))
		data = dujiao.AffiliateWithdrawalList{Items: state.withdrawals[start:end], Page: page, TotalPages: int64((len(state.withdrawals) + 4) / 5), Total: int64(len(state.withdrawals))}
	default:
		t.Errorf("unexpected affiliate endpoint: %s", r.URL.Path)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 0, "data": data})
}

func positiveAffiliateTestPage(r *http.Request) int {
	page := 1
	_, _ = fmt.Sscan(r.URL.Query().Get("page"), &page)
	return max(page, 1)
}

func TestAffiliateOpenDashboardLinkAndAmounts(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.affiliate.dashboard = dujiao.AffiliateDashboard{ClickCount: 20, ValidOrderCount: 3, ConversionRate: 15, PendingCommission: "1.25", AvailableCommission: "20.00", WithdrawnCommission: "5.00", MinWithdrawAmount: json.Number("10")}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/affiliate", 0, 42, 42))
	if !strings.Contains(fixture.texts[len(fixture.texts)-1], "尚未开通推广") || len(fixture.affiliate.opens) != 0 {
		t.Fatal("viewing dashboard opened affiliate profile without confirmation")
	}
	formCallback(app, 42, 10, "affopen")
	text := fixture.texts[len(fixture.texts)-1]
	for _, want := range []string{"https://t.me/test_bot?start=aff_AFF42", "推广访问：20", "有效订单：3", "转化率：15.00%", "待确认佣金：1.25 CNY", "可用佣金：20.00 CNY", "最低提现：10.00 CNY", "申请提现请前往商店"} {
		if !strings.Contains(text, want) {
			t.Errorf("dashboard missing %q: %s", want, text)
		}
	}
	if len(fixture.affiliate.opens) != 1 || fixture.affiliate.opens[0].ChannelUserID != "42" {
		t.Fatalf("open requests = %+v", fixture.affiliate.opens)
	}
	markup := fixture.markups[len(fixture.markups)-1]
	if !strings.Contains(markup, "https://t.me/share/url?") || !strings.Contains(markup, "affcomm:1") || !strings.Contains(markup, "affwithdraws:1") {
		t.Fatal("dashboard navigation/share buttons missing")
	}
}

func TestAffiliateAttributionSurvivesInviteAndCheckoutNavigation(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			var fields []dujiao.ManualFormField
			if manual {
				fields = []dujiao.ManualFormField{{Key: "email", Type: "email", Required: true}}
			}
			app, fixture := newFormFixture(t, fields)
			fixture.affiliate.resolveMissing, fixture.affiliate.inviteRequired = true, true
			app.handleUpdate(context.Background(), nil, giftCardMessage("/start aff_REF123", 0, 42, 42))
			if app.sessions.Get(42).Flow != "invite" || len(fixture.affiliate.clicks) != 1 {
				t.Fatal("referral did not preserve registration invite flow")
			}
			for _, identity := range fixture.affiliate.identities {
				if identity.InviteCode != "" {
					t.Fatal("affiliate code was sent as an invite code")
				}
			}
			app.handleUpdate(context.Background(), nil, giftCardMessage("JOIN", 0, 42, 42))
			if app.sessions.Get(42).Flow != "" {
				t.Fatal("registration invite did not complete")
			}
			formCallback(app, 42, 10, "buy:3:31:1")
			if manual {
				formReply(app, "demo@example.com")
			}
			couponAction(app, "edit")
			formReply(app, "SAVE10")
			checkoutConfirm(app)
			click := fixture.affiliate.clicks[0]
			if click.VisitorKey != "telegram:42" || click.AffiliateCode != "REF123" || click.ChannelUserID != "42" {
				t.Fatalf("referral click = %+v", click)
			}
			if len(fixture.orders) != 1 {
				t.Fatalf("orders = %+v", fixture.orders)
			}
			requests := append([]dujiao.OrderRequest{}, fixture.previews...)
			requests = append(requests, fixture.orders...)
			for _, request := range requests {
				if request.AffiliateVisitorKey != click.VisitorKey || request.AffiliateCode != "" {
					t.Fatalf("preview/create bypassed server attribution: %+v", request)
				}
			}
		})
	}
}

func TestAffiliateReadOnlyRecordsPaginationAndAccountMask(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	for id := uint(1); id <= 7; id++ {
		fixture.affiliate.commissions = append(fixture.affiliate.commissions, dujiao.AffiliateCommission{ID: id, OrderNo: fmt.Sprintf("DJ%d<unsafe>", id), CommissionAmount: "1.00", RatePercent: "10.00", Status: "available"})
		fixture.affiliate.withdrawals = append(fixture.affiliate.withdrawals, dujiao.AffiliateWithdrawal{ID: id, Amount: "10.00", Channel: "alipay", Account: "private@example.com", Status: "pending_review"})
	}
	formCallback(app, 42, 10, "affcomm:1")
	text := fixture.texts[len(fixture.texts)-1]
	if strings.Contains(text, "<unsafe>") || !strings.Contains(text, "&lt;unsafe&gt;") || strings.Contains(text, "DJ6") || !strings.Contains(fixture.markups[len(fixture.markups)-1], "affcomm:2") {
		t.Fatal("commission page escaping or pagination failed")
	}
	formCallback(app, 42, 10, "affcomm:2")
	text = fixture.texts[len(fixture.texts)-1]
	if !strings.Contains(text, "DJ7") || strings.Contains(text, "DJ1") {
		t.Fatal("commission page 2 incorrect")
	}
	formCallback(app, 42, 10, "affwithdraws:1")
	text = fixture.texts[len(fixture.texts)-1]
	if strings.Contains(text, "private@example.com") || !strings.Contains(text, "pr****om") || !strings.Contains(text, "待审核") {
		t.Fatalf("withdrawal account was not masked: %s", text)
	}
	formCallback(app, 42, 10, "affwithdraws:2")
	if !strings.Contains(fixture.texts[len(fixture.texts)-1], "#7") {
		t.Fatal("withdrawal page 2 incorrect")
	}
	before := len(fixture.affiliate.queries)
	for _, bad := range []string{"affcomm:0", "affcomm:-1", "affcomm:100001", "affcomm:bad", "affwithdraws:1:42"} {
		formCallback(app, 42, 10, bad)
	}
	if len(fixture.affiliate.queries) != before {
		t.Fatal("invalid page reached API")
	}
	for _, query := range fixture.affiliate.queries {
		if !strings.Contains(query, "channel_user_id=42") || !strings.Contains(query, "page_size=5") {
			t.Fatalf("record query lost owner or pagination: %s", query)
		}
	}
}

func TestAffiliatePrivateChatAndMalformedStart(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	for _, callback := range []string{"affiliate", "affopen", "affcomm:1", "affwithdraws:1"} {
		formCallback(app, 43, 10, callback)
	}
	app.handleUpdate(context.Background(), nil, giftCardMessage("/affiliate", 0, 42, -100))
	app.handleUpdate(context.Background(), nil, giftCardMessage("/start aff_VALID", 0, 42, -100))
	for _, code := range []string{"", "a/b", "a?b", strings.Repeat("x", 33), "<html>"} {
		app.handleUpdate(context.Background(), nil, giftCardMessage("/start aff_"+code, 0, 42, 42))
	}
	if len(fixture.affiliate.queries) != 0 {
		t.Fatalf("private or malformed input reached API: %v", fixture.affiliate.queries)
	}
}

func TestAffiliateErrorsAndTrackingFailureDoNotBreakShopping(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		want   string
	}{
		{"affiliate_disabled", 400, "暂未开启推广"}, {"unsupported", 404, "版本暂不支持"}, {"invite_code_required", 403, "初始化"}, {"user_disabled", 401, "账号暂不可"}, {"unavailable", 503, "暂时不可用"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			app, fixture := newFormFixture(t, nil)
			fixture.affiliate.failureCode, fixture.affiliate.failureStatus = tc.code, tc.status
			formCallback(app, 42, 10, "affiliate")
			text := fixture.texts[len(fixture.texts)-1]
			if !strings.Contains(text, tc.want) || strings.Contains(text, "private account") {
				t.Fatalf("failure = %s", text)
			}
			app.handleUpdate(context.Background(), nil, giftCardMessage("/start aff_VALID", 0, 42, 42))
			if !strings.Contains(strings.Join(fixture.texts, "\n"), "本次推广访问暂时未能记录") || len(fixture.affiliate.identities) == 0 {
				t.Fatal("tracking failure blocked store start or went unreported")
			}
		})
	}
}

func TestAffiliateLinkAndAccountHelpers(t *testing.T) {
	if link := affiliateLink("test_bot", "ABC-_123"); link != "https://t.me/test_bot?start=aff_ABC-_123" {
		t.Fatal(link)
	}
	for _, pair := range [][2]string{{"bad.bot", "CODE"}, {"test_bot", "a/b"}, {"test_bot", strings.Repeat("x", 33)}, {"test_bot", ""}} {
		if affiliateLink(pair[0], pair[1]) != "" {
			t.Fatalf("unsafe referral link accepted: %v", pair)
		}
	}
	for input, want := range map[string]string{"": "****", "1234": "****", "abcde": "ab****de", "中文收款账号": "中文****账号"} {
		if got := maskAffiliateAccount(input); got != want {
			t.Errorf("mask(%q)=%q", input, got)
		}
	}
	if affiliateVisitorKey(42) == affiliateVisitorKey(43) {
		t.Fatal("visitor keys collide")
	}
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, status := range []string{"pending_confirm", "available", "withdrawn", "pending_review", "paid", "rejected", "<unknown>"} {
			if text := affiliateStatus(locale, status); text == "" || strings.Contains(text, "<unknown>") {
				t.Fatal("missing or unescaped status")
			}
		}
	}
}

func TestOrdinaryStartInviteIsNotAnAffiliateLink(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.affiliate.resolveMissing, fixture.affiliate.inviteRequired = true, true
	app.handleUpdate(context.Background(), nil, giftCardMessage("/start JOIN", 0, 42, 42))
	if len(fixture.affiliate.clicks) != 0 || len(fixture.affiliate.identities) != 2 || fixture.affiliate.identities[1].InviteCode != "JOIN" || app.sessions.Get(42).Flow != "" {
		t.Fatal("ordinary registration invite was changed by affiliate support")
	}
}
