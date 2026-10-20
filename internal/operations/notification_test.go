package operations

import (
	"strings"
	"testing"
)

func TestEveryAuditCodeHasHumanReadableLabel(t *testing.T) {
	for _, action := range []Action{Order, Payment, OrderCancel, Recharge, GiftCard, Affiliate, Withdrawal, WithdrawalUnlock, CallbackPaid, CallbackFulfilled, CallbackRecharge, Session, Access, Callback, AdminTest, BroadcastDraft, BroadcastJob, BroadcastDelivery, Subscription, DeliveryTXT} {
		label := actionLabel(action)
		if label == "待核对操作" || label == string(action) || strings.ContainsAny(label, "abcdefghijklmnopqrstuvwxyz") {
			t.Fatal("untranslated action", action)
		}
	}
	for _, outcome := range []Outcome{Attempt, Succeeded, Rejected, Unknown, Blocked, Limited, Unavailable, Paused, Cancelled, Skipped, Subscribed, Unsubscribed} {
		label := outcomeLabel(outcome)
		if label == "待核对" || label == string(outcome) || strings.ContainsAny(label, "abcdefghijklmnopqrstuvwxyz") {
			t.Fatal("untranslated outcome", outcome)
		}
	}
	if strings.Contains(adminNotification(Event{Action: Action("PRIVATE-TEXT"), Outcome: Outcome("PRIVATE-TEXT")}), "PRIVATE-TEXT") {
		t.Fatal("untrusted code echoed")
	}
}

func TestAdminOrderCreationCannotBeMistakenForPayment(t *testing.T) {
	text := adminNotification(Event{id: "DIAGNOSTIC", Action: Order, Outcome: Succeeded, UserID: 42, ResourceID: 7})
	for _, want := range []string{"管理员通知", "事件：创建订单", "不代表已付款或已发货", "订单 ID：7", "用户 ID：42", "诊断编号：DIAGNOSTIC"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing distinction", want)
		}
	}
	for _, unwanted := range []string{"order.create", "succeeded", "资源 ID"} {
		if strings.Contains(text, unwanted) {
			t.Fatal("machine code shown to human", unwanted)
		}
	}
	for _, action := range []Action{CallbackPaid, CallbackFulfilled, CallbackRecharge} {
		if !strings.Contains(adminNotification(Event{Action: action, Outcome: Succeeded}), "已向买家发送通知") {
			t.Fatal("callback delivery outcome ambiguous")
		}
	}
}
