package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func TestOrderNavigationFiltersContextAndBoundedList(t *testing.T) {
	a, f := newPendingFixture(t)
	f.list = dujiao.OrderList{Total: 7, TotalPages: 2, Items: []dujiao.OrderListItem{{OrderID: 7, OrderNo: "DJ7", ProductTitle: "商品<&>" + strings.Repeat("长", 1000), Status: "paid", TotalAmount: "10.00", Currency: "CNY"}}}
	for _, filter := range orderFilters {
		pendingClick(a, 42, 42, 10, "orders:"+filter.code+":2")
		if f.listCalls[len(f.listCalls)-1] != filter.status+":2" {
			t.Fatal("filter not exact upstream status")
		}
		markup := f.markups[len(f.markups)-1]
		if got := findCallback(markup, "order:"); got != "order:7:"+filter.code+":2" {
			t.Fatal("list lost filter/page", got)
		}
		text := f.texts[len(f.texts)-1]
		if len([]rune(text)) > 1500 || !strings.Contains(text, "2/2") || !strings.Contains(text, "交付：等待交付") || !strings.Contains(text, "&lt;&amp;&gt;") {
			t.Fatal("list unbounded/unclear")
		}
		pendingClick(a, 42, 42, 10, "order:7:"+filter.code+":2")
		encoded, _ := json.Marshal(f.markups[len(f.markups)-1])
		if !strings.Contains(string(encoded), "orders:"+filter.code+":2") || !strings.Contains(string(encoded), "order:7:"+filter.code+":2") {
			t.Fatal("detail lost navigation")
		}
	}
	reads := len(f.listCalls)
	a.showOrders(context.Background(), -100, 42, "", 1, false, 0)
	if len(f.listCalls) != reads || f.posts+f.cancels != 0 {
		t.Fatal("group leak or navigation writes")
	}
}

func findCallback(markup models.InlineKeyboardMarkup, prefix string) string {
	for _, row := range markup.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, prefix) {
				return b.CallbackData
			}
		}
	}
	return ""
}

func TestOrderLookupCommandPromptAndIsolation(t *testing.T) {
	a, f := newPendingFixture(t)
	quantityReply(a, "/orders DJ7", 0)
	if f.lookupCalls != 1 || !strings.Contains(f.texts[len(f.texts)-1], "<code>DJ7</code>") {
		t.Fatal("direct lookup unavailable")
	}
	pendingClick(a, 42, 42, 10, "orderfind:p:2")
	draft := a.sessions.Get(42).OrderLookup
	if draft == nil {
		t.Fatal("prompt missing")
	}
	quantityReply(a, "DJ7", 99) // Old/other prompt cannot consume this input.
	quantityReply(a, "../DJ7", 10)
	if f.lookupCalls != 1 {
		t.Fatal("invalid/stale input reached upstream")
	}
	quantityReply(a, " DJ7 ", 10)
	if f.lookupCalls != 2 || a.sessions.Get(42).OrderLookup != nil || !strings.Contains(f.texts[len(f.texts)-1], "订单详情") {
		t.Fatal("prompt lookup did not finish")
	}
	encoded, _ := json.Marshal(f.markups[len(f.markups)-1])
	if !strings.Contains(string(encoded), "orders:p:2") {
		t.Fatal("lookup lost source list")
	}
	// The same known number from another account gives the same generic
	// absence as a nonexistent number; never reveal the actual order.
	m := quantityMessage("/orders DJ7", 0)
	m.Chat.ID, m.From.ID = 43, 43
	a.handleUpdate(context.Background(), nil, &models.Update{Message: m})
	unowned := f.texts[len(f.texts)-1]
	quantityReply(a, "/orders NOT-FOUND", 0)
	if unowned != f.texts[len(f.texts)-1] || strings.Contains(unowned, "DJ7") {
		t.Fatal("existence leaked")
	}
	before := len(f.texts)
	m.Chat.ID = -100
	a.handleUpdate(context.Background(), nil, &models.Update{Message: m})
	if len(f.texts) != before {
		t.Fatal("lookup in group leaked")
	}
	if strings.Contains(f.logs.text(), "DJ7") || f.posts+f.cancels != 0 {
		t.Fatal("lookup logged input or wrote business state")
	}
}

func TestOrderLookupRetryNavigationExpiryAndMismatch(t *testing.T) {
	a, f := newPendingFixture(t)
	pendingClick(a, 42, 42, 10, "orderfind:a:1")
	f.lookupFailure = 503
	quantityReply(a, "DJ7", 0)
	if a.sessions.Get(42).OrderLookup == nil || !strings.Contains(f.texts[len(f.texts)-1], "暂时不可用") {
		t.Fatal("outage mistaken for empty orders")
	}
	f.lookupFailure, f.lookupMismatch = 0, true
	quantityReply(a, "DJ7", 0)
	if strings.Contains(f.texts[len(f.texts)-1], "OTHER-PRIVATE") {
		t.Fatal("mismatching response rendered")
	}
	if strings.Contains(f.logs.text(), "PRIVATE-PAYMENT-ERROR") {
		t.Fatal("raw upstream error logged")
	}
	a.sessions.Update(42, func(s *session.State) {
		s.OrderLookup.ExpiresAt = time.Now().Add(-time.Second)
		s.WithdrawalGuard = &session.WithdrawalGuard{ID: "old", Status: "unknown"}
	})
	reads := f.lookupCalls
	quantityReply(a, "DJ7", 0)
	if f.lookupCalls != reads {
		t.Fatal("expired lookup used")
	}
	for _, navigate := range []string{"/orders", "/cancel", "/help"} {
		pendingClick(a, 42, 42, 10, "orderfind:a:1")
		quantityReply(a, navigate, 0)
		state := a.sessions.Get(42)
		if state.OrderLookup != nil || state.WithdrawalGuard == nil {
			t.Fatal("navigation kept prompt or lost financial guard")
		}
	}
}

func TestOrderStatusSemanticsAndCallbackLimits(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, filter := range orderFilters {
			if orderFilterCode(filter.status) != filter.code || orderFilterStatus(filter.code) != filter.status {
				t.Fatal("filter roundtrip")
			}
			if filter.status != "" && orderStatus(locale, filter.status) == filter.status {
				t.Fatal("untranslated official status", filter.status)
			}
			if len(orderViewCallback(^uint(0), filter.code, 100000)) > 64 {
				t.Fatal("callback limit")
			}
		}
	}
	for _, raw := range []string{"", "../7", "7/8", "DJ?x=1", "<x>", "DJ 7", "DJ\n7", strings.Repeat("7", 101), "１２３"} {
		if validOrderNumber(raw) {
			t.Fatal("unsafe number accepted")
		}
	}
	for _, raw := range []string{"DJ7", "DJ-7_01", "1234567890"} {
		if !validOrderNumber(raw) {
			t.Fatal("valid number rejected")
		}
	}
	for _, raw := range []string{"0", "-1", "100001", "99999999999999999999999", "x"} {
		code, page := orderContext("invalid", raw)
		if code != "a" || page != 1 {
			t.Fatal("bad context accepted")
		}
	}
	cases := []struct {
		order dujiao.OrderDetail
		want  string
	}{
		{dujiao.OrderDetail{Status: "pending_payment"}, "付款后安排"},
		{dujiao.OrderDetail{Status: "paid", FulfillmentType: "manual"}, "等待商家人工交付"},
		{dujiao.OrderDetail{Status: "fulfilling"}, "交付处理中"},
		{dujiao.OrderDetail{Status: "partially_delivered", Children: []dujiao.OrderChild{{Status: "delivered"}, {Status: "paid"}}}, "部分交付（1/2"},
		{dujiao.OrderDetail{Status: "completed"}, "已交付"},
		{dujiao.OrderDetail{Status: "paid", FulfillmentResult: "PRIVATE-CODE"}, "整体进度待核对"},
		{dujiao.OrderDetail{Status: "partially_refunded"}, "涉及退款"},
		{dujiao.OrderDetail{Status: "refunded", FulfillmentResult: "PRIVATE-CODE"}, "涉及退款"},
		{dujiao.OrderDetail{Status: "unknown-new-state"}, "查看详情核对"},
	}
	for i, tc := range cases {
		if got := detailDeliveryStatus("zh-CN", tc.order); !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE") {
			t.Fatal(fmt.Sprint(i), got)
		}
	}
}

func TestOrderListShrinkingPageAndEmptyFilter(t *testing.T) {
	a, f := newPendingFixture(t)
	f.list = dujiao.OrderList{Total: 1, TotalPages: 1}
	a.showOrders(context.Background(), 42, 42, "paid", 5, true, 10)
	if len(f.listCalls) != 2 || f.listCalls[1] != "paid:1" {
		t.Fatal("shrinking list not clamped")
	}
	f.list = dujiao.OrderList{}
	a.showOrders(context.Background(), 42, 42, "delivered", 9, true, 10)
	if !strings.Contains(f.texts[len(f.texts)-1], "1/1") || !strings.Contains(f.texts[len(f.texts)-1], "当前筛选下没有订单") {
		t.Fatal("empty page unclear")
	}
}

func TestOrderLookupConcurrentReplyAndStaleDraft(t *testing.T) {
	a, f := newPendingFixture(t)
	pendingClick(a, 42, 42, 10, "orderfind:p:2")
	draft := a.sessions.Get(42).OrderLookup
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); a.findOrder(context.Background(), 42, 10, 42, "DJ7", "p", 2, draft) }()
	}
	workers.Wait()
	views := 0
	for _, text := range f.texts {
		if strings.Contains(text, "<b>订单详情</b>") {
			views++
		}
	}
	if views != 1 {
		t.Fatal("same prompt consumed more than once")
	}
	a.sessions.Update(42, func(s *session.State) {
		s.Flow = "manual_form"
		s.Purchase = &session.PurchaseDraft{ID: "new", ExpiresAt: time.Now().Add(time.Hour)}
	})
	before := len(f.texts)
	a.findOrder(context.Background(), 42, 10, 42, "DJ7", "p", 2, draft)
	state := a.sessions.Get(42)
	if state.Purchase == nil || state.Purchase.ID != "new" || len(f.texts) != before {
		t.Fatal("stale lookup disrupted a newer flow")
	}
}

func TestOrderLookupStorageFailureDoesNotCompletePrompt(t *testing.T) {
	for _, committed := range []bool{false, true} {
		a, f := newPendingFixture(t)
		pendingClick(a, 42, 42, 10, "orderfind:a:1")
		draft := a.sessions.Get(42).OrderLookup
		a.sessions = &failingSessionStore{Store: a.sessions, failAt: 1, commit: committed}
		a.findOrder(context.Background(), 42, 10, 42, "DJ7", "a", 1, draft)
		for _, text := range f.texts {
			if strings.Contains(text, "<b>订单详情</b>") {
				t.Fatal("uncertain session commit completed search")
			}
		}
		if f.posts+f.cancels != 0 {
			t.Fatal("lookup executed writes")
		}
	}
}
