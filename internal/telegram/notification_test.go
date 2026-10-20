package telegram

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func notificationActions(k models.InlineKeyboardMarkup) []string {
	var actions []string
	for _, row := range k.InlineKeyboard {
		for _, b := range row {
			actions = append(actions, b.CallbackData)
		}
	}
	return actions
}

func TestNotificationKeyboardIsCompactLocalizedAndResourceBound(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, test := range []struct {
			kind string
			id   uint
			want []string
		}{
			{"order-paid", 7, []string{"order:7", "home"}},
			{"order-fulfilled", 7, []string{"file:7", "order:7", "home"}},
			{"wallet-recharge-succeeded", 7, []string{"wallet", "home"}},
			{"order-fulfilled", 0, []string{"orders:a:1", "home"}},
			{"order-paid", 0, []string{"orders:a:1", "home"}},
		} {
			k := notificationKeyboard(locale, test.kind, test.id)
			if !reflect.DeepEqual(notificationActions(k), test.want) {
				t.Fatalf("incorrect notification actions: %s %s", locale, test.kind)
			}
			for _, row := range k.InlineKeyboard {
				for _, b := range row {
					if b.Text == "" || len(b.CallbackData) > 64 || b.URL != "" {
						t.Fatal("invalid/unsafe Telegram button")
					}
				}
			}
			if test.kind == "order-fulfilled" && test.id > 0 && len(k.InlineKeyboard[0]) != 1 {
				t.Fatal("TXT is not the primary action")
			}
		}
	}
	if notificationKeyboard("zh-TW", "order-paid", 7).InlineKeyboard[0][0].Text != "查看訂單" || notificationKeyboard("en-US", "order-fulfilled", 7).InlineKeyboard[0][0].Text != "📄 Download delivery TXT" {
		t.Fatal("notification translations missing")
	}
	for _, id := range []uint{1, 1234567890, ^uint(0)} {
		for _, data := range notificationActions(notificationKeyboard("zh-CN", "order-fulfilled", id)) {
			if len(data) > 64 {
				t.Fatal("large ID exceeds callback budget")
			}
		}
	}
}

func lastNotificationKeyboard(t *testing.T, f *deliveryFixture) models.InlineKeyboardMarkup {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var k models.InlineKeyboardMarkup
	if len(f.markups) == 0 || json.Unmarshal([]byte(f.markups[len(f.markups)-1]), &k) != nil {
		t.Fatal("SDK notification markup missing")
	}
	return k
}

func clickNotification(a *App, uid, chat int64, data string) {
	a.handleCallback(context.Background(), &models.CallbackQuery{
		ID: "notification-click", From: models.User{ID: uid}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 91, Chat: models.Chat{ID: chat}}},
	})
}

func TestNotificationSendDoesNotReadStoreUploadOrChangeDraft(t *testing.T) {
	a, f := newDeliveryFixture(t)
	a.sessions.Update(42, func(s *session.State) {
		s.Flow = "purchase_quantity"
		s.Quantity = &session.QuantityInput{ID: "active-input", ChatID: 42, ProductID: 3, SKUID: 31, Quantity: 2500, ExpiresAt: time.Now().Add(time.Minute)}
	})
	before := a.sessions.Get(42)
	for _, kind := range []string{"order-paid", "order-fulfilled", "wallet-recharge-succeeded"} {
		if err := a.Notify(context.Background(), 42, kind, callback.Event{OrderID: 7, Amount: "25.00", Currency: "CNY"}); err != nil {
			t.Fatal(err)
		}
	}
	if after := a.sessions.Get(42); !reflect.DeepEqual(before, after) {
		t.Fatal("incoming notification interrupted active checkout")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookups != 0 || len(f.files) != 0 || len(f.texts) != 3 {
		t.Fatal("notification attempted upstream/financial/file operation")
	}
	for _, text := range f.texts {
		if strings.Contains(text, "PRIVATE-CARD") {
			t.Fatal("notification exposed delivery content")
		}
	}
}

func TestNotificationButtonsUseFreshOwnershipAndSharedTXTGuard(t *testing.T) {
	a, f := newDeliveryFixture(t)
	f.order.Status, f.order.FulfillmentResult = "paid", nil
	event := callback.Event{OrderID: 7, TelegramUserID: "42"}
	if err := a.Notify(context.Background(), 42, "order-paid", event); err != nil {
		t.Fatal(err)
	}
	paid := lastNotificationKeyboard(t, f)
	clickNotification(a, 42, 42, notificationActions(paid)[0])
	if actions := notificationActions(lastNotificationKeyboard(t, f)); strings.Contains(strings.Join(actions, ","), "file:") {
		t.Fatal("payment alone advertised delivery")
	}
	f.mu.Lock()
	if f.lookups != 1 || len(f.files) != 0 {
		t.Fatal("paid shortcut failed to read current order")
	}
	f.order.Status, f.order.FulfillmentResult = "completed", "PRIVATE-CARD-NOTIFICATION-END"
	f.mu.Unlock()
	if err := a.Notify(context.Background(), 42, "order-fulfilled", event); err != nil {
		t.Fatal(err)
	}
	fulfilled := lastNotificationKeyboard(t, f)
	fileAction := notificationActions(fulfilled)[0]
	clickNotification(a, 43, 43, fileAction)
	clickNotification(a, 42, -100, fileAction)
	f.mu.Lock()
	if len(f.files) != 0 || f.lookups != 2 {
		t.Fatal("forwarded/cross-user/group shortcut bypassed ownership")
	}
	f.mu.Unlock()
	clickNotification(a, 42, 42, fileAction)
	clickNotification(a, 42, 42, fileAction)
	// Enter the same order from the older payment notice, even if notifications
	// arrive out of order. Its current detail and TXT use the very same guard.
	clickNotification(a, 42, 42, notificationActions(paid)[0])
	detail := lastNotificationKeyboard(t, f)
	if notificationActions(detail)[0] != "file:7" {
		t.Fatal("old payment notice did not show latest fulfillment")
	}
	clickNotification(a, 42, 42, notificationActions(detail)[0])
	f.mu.Lock()
	if len(f.files) != 1 || !strings.Contains(string(f.files[0]), "PRIVATE-CARD-NOTIFICATION-END") {
		t.Fatal("notice/detail entry points duplicated or lost TXT")
	}
	f.order.Status = "refunded"
	f.mu.Unlock()
	clickNotification(a, 42, 42, fileAction)
	f.mu.Lock()
	if len(f.files) != 1 {
		t.Fatal("stale fulfilled button bypassed current status")
	}
	f.mu.Unlock()
	if strings.Contains(f.logs.text(), "PRIVATE-CARD") {
		t.Fatal("shortcut logged raw upstream/card data")
	}
}

func TestUnknownNotificationDoesNotSendButtons(t *testing.T) {
	a, f := newDeliveryFixture(t)
	if err := a.Notify(context.Background(), 42, "unknown", callback.Event{OrderID: 7}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) != 0 {
		t.Fatalf("unknown notification sent %d messages", len(f.texts))
	}
}
