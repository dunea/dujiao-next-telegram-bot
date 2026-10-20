package telegram

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

func couponAction(app *App, action string) {
	draft := app.sessions.Get(42).Purchase
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("coupon:%s:%d:%s", draft.ID, draft.Revision, action))
}

func checkoutConfirm(app *App) {
	draft := app.sessions.Get(42).Purchase
	formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
}

func TestCouponReplyCreatesFreshPreviewBelowInput(t *testing.T) {
	app, f := newFormFixture(t, nil)
	formCallback(app, 42, 10, "buy:3:31:1")
	old := app.sessions.Get(42).Purchase
	couponAction(app, "edit")
	f.nextID.Store(500)
	formReply(app, "SAVE10")
	draft := app.sessions.Get(42).Purchase
	if draft == nil || draft.PreviewID <= 500 || draft.PreviewID == old.PreviewID || !draft.Ready || len(f.orders) != 0 {
		t.Fatal("coupon reply updated an old preview or created an order")
	}
}

func TestCouponOrdinaryPurchaseAndStaleConfirmation(t *testing.T) {
	app, fixture := newFormFixture(t, []dujiao.ManualFormField{})
	formCallback(app, 42, 10, "buy:3:31:1")
	original := app.sessions.Get(42).Purchase
	if original == nil || !original.Ready {
		t.Fatal("ordinary checkout is not ready")
	}
	couponAction(app, "edit")
	formReply(app, " SAVE10 ")
	draft := app.sessions.Get(42).Purchase
	if !draft.Ready || draft.CouponCode != "SAVE10" || draft.PreviewTotal != "9.00" {
		t.Fatalf("coupon draft = %#v", draft)
	}
	formCallback(app, 42, original.PreviewID, fmt.Sprintf("formconfirm:%s:%d", original.ID, original.Revision))
	fixture.mu.Lock()
	ordersBefore := len(fixture.orders)
	texts := strings.Join(fixture.texts, "\n")
	fixture.mu.Unlock()
	if ordersBefore != 0 {
		t.Fatal("old undiscounted confirmation created an order")
	}
	if !strings.Contains(texts, "优惠券优惠：−1.00 CNY") || !strings.Contains(texts, "9.00 CNY") {
		t.Fatal("discount and total are not shown")
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			formCallback(app, 42, draft.PreviewID, fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision))
		})
	}
	wait.Wait()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 1 || fixture.orders[0].CouponCode != "SAVE10" || len(fixture.orders[0].ManualFormData) != 0 {
		t.Fatalf("orders = %#v", fixture.orders)
	}
}

func TestInvalidCouponPreservesManualDataAndCanBeRemoved(t *testing.T) {
	app, fixture := newFormFixture(t, []dujiao.ManualFormField{{Key: "email", Type: "email", Required: true}})
	formCallback(app, 42, 10, "buy:3:31:1")
	formReply(app, "demo@example.com")
	couponAction(app, "edit")
	formReply(app, "BAD")
	draft := app.sessions.Get(42).Purchase
	if draft.Ready || draft.Values["email"] != "demo@example.com" {
		t.Fatal("invalid coupon lost the form or enabled confirmation")
	}
	couponAction(app, "remove")
	draft = app.sessions.Get(42).Purchase
	if !draft.Ready || draft.CouponCode != "" || draft.PreviewTotal != "10.00" {
		t.Fatal("coupon could not be removed")
	}
	couponAction(app, "edit")
	formReply(app, "SAVE10")
	checkoutConfirm(app)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 1 || fixture.orders[0].CouponCode != "SAVE10" {
		t.Fatalf("orders = %#v", fixture.orders)
	}
	values := fixture.orders[0].ManualFormData["3:31"].(map[string]any)
	if values["email"] != "demo@example.com" {
		t.Fatal("coupon retry lost manual form data")
	}
}

func TestCouponExpiryAtPreviewAndCreateKeepsCheckout(t *testing.T) {
	for _, mode := range []string{"preview", "create"} {
		t.Run(mode, func(t *testing.T) {
			app, fixture := newFormFixture(t, []dujiao.ManualFormField{{Key: "name", Type: "text", Required: true}})
			formCallback(app, 42, 10, "buy:3:31:1")
			formReply(app, "demo")
			couponAction(app, "edit")
			formReply(app, "SAVE10")
			fixture.mu.Lock()
			if mode == "preview" {
				fixture.couponExpired = true
			} else {
				fixture.rejectCouponOnCreate = true
			}
			fixture.mu.Unlock()
			checkoutConfirm(app)
			draft := app.sessions.Get(42).Purchase
			if draft == nil || draft.Ready || draft.Submitting || draft.Values["name"] != "demo" {
				t.Fatalf("expired coupon draft = %#v", draft)
			}
			couponAction(app, "remove")
			checkoutConfirm(app)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.orders) != 1 || fixture.orders[0].CouponCode != "" {
				t.Fatalf("fallback order = %#v", fixture.orders)
			}
		})
	}
}

func TestCouponPriceChangeRequiresNewConfirmation(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	formCallback(app, 42, 10, "buy:3:31:1")
	couponAction(app, "edit")
	formReply(app, "SAVE10")
	old := app.sessions.Get(42).Purchase
	fixture.mu.Lock()
	fixture.couponTotal = "8.00"
	fixture.mu.Unlock()
	checkoutConfirm(app)
	draft := app.sessions.Get(42).Purchase
	if !draft.Ready || draft.PreviewTotal != "8.00" || draft.Revision == old.Revision {
		t.Fatalf("new preview = %#v", draft)
	}
	fixture.mu.Lock()
	count := len(fixture.orders)
	fixture.mu.Unlock()
	if count != 0 {
		t.Fatal("changed amount was accepted without confirmation")
	}
	checkoutConfirm(app)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 1 {
		t.Fatal("new total could not be confirmed")
	}
}

func TestCouponPromptBackCancelAndCrossUserIsolation(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	formCallback(app, 42, 10, "buy:3:31:1")
	couponAction(app, "edit")
	formReply(app, "SAVE10")
	couponAction(app, "edit")
	draft := app.sessions.Get(42).Purchase
	formCallback(app, 99, draft.PreviewID, fmt.Sprintf("coupon:%s:%d:remove", draft.ID, draft.Revision))
	if app.sessions.Get(42).Purchase.CouponCode != "SAVE10" {
		t.Fatal("another user changed the coupon")
	}
	formReply(app, "/back")
	if current := app.sessions.Get(42).Purchase; !current.Ready || current.CouponCode != "SAVE10" {
		t.Fatal("back lost the applied coupon")
	}
	couponAction(app, "edit")
	formReply(app, "/cancel")
	if app.sessions.Get(42).Purchase != nil {
		t.Fatal("cancel retained checkout")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.orders) != 0 {
		t.Fatal("prompt navigation created an order")
	}
}
