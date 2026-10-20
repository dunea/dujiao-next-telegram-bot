package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func TestInputCorrectionsAreOneFreshReplyableMessage(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, flow := range []string{"form", "coupon", "gift", "withdrawal"} {
			t.Run(locale+"/"+flow, func(t *testing.T) {
				a, f := newWithdrawalFixture(t)
				a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
				var reply func()
				switch flow {
				case "form":
					f.product.ManualFormSchema.Fields = []dujiao.ManualFormField{{Key: "email", Label: "Email", Type: "email", Required: true}}
					formCallback(a, 42, 10, "buy:3:31:1")
					reply = func() { formReply(a, "not-an-email") }
				case "coupon":
					formCallback(a, 42, 10, "buy:3:31:1")
					couponAction(a, "edit")
					reply = func() { formReply(a, strings.Repeat("X", 129)) }
				case "gift":
					formCallback(a, 42, 10, "giftcard")
					prompt := a.sessions.Get(42).GiftCard.PromptID
					reply = func() { a.handleUpdate(context.Background(), nil, giftCardMessage("bad\ncode", prompt, 42, 42)) }
				case "withdrawal":
					formCallback(a, 42, 10, "affapply")
					reply = func() { withdrawalReply(a, "-1") }
				}
				before := len(f.texts)
				f.nextID.Store(500)
				reply()
				if len(f.texts) != before+1 || f.methods[len(f.methods)-1] != "sendMessage" || !strings.Contains(f.markups[len(f.markups)-1], `"force_reply":true`) {
					t.Fatal("correction must combine error and instructions in one replyable message")
				}
				s := a.sessions.Get(42)
				promptID := 0
				switch flow {
				case "form", "coupon":
					promptID = s.Purchase.PromptID
				case "gift":
					promptID = s.GiftCard.PromptID
				case "withdrawal":
					promptID = s.Withdrawal.MessageID
				}
				if promptID <= 500 || len(f.orders)+len(f.giftCodes)+len(f.rechargeRequests)+len(f.affiliate.withdrawalRequests) != 0 {
					t.Fatal("correction updated old input or performed a business write")
				}
			})
		}
	}
}

func TestCompactManualChoicesKeepIdentityAndLongLabels(t *testing.T) {
	for _, kind := range []string{"select", "radio", "checkbox"} {
		for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
			t.Run(kind+locale, func(t *testing.T) {
				a, f := newFormFixture(t, []dujiao.ManualFormField{{Key: "option", Type: kind, Options: []string{"One", "Two", strings.Repeat("长", 25)}}})
				a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
				formCallback(a, 42, 10, "buy:3:31:1")
				k := formKeyboard(t, f)
				offset := 0
				if kind == "checkbox" {
					offset = 1
					if len(k.InlineKeyboard[0]) != 1 || !strings.HasSuffix(k.InlineKeyboard[0][0].CallbackData, ":done") {
						t.Fatal("checkbox completion must remain the primary action")
					}
				}
				if len(k.InlineKeyboard[offset]) != 2 || len(k.InlineKeyboard[offset+1]) != 1 || !strings.HasSuffix(k.InlineKeyboard[offset][1].CallbackData, ":option:1") || len(k.InlineKeyboard[len(k.InlineKeyboard)-1]) != 2 {
					t.Fatal("choices/tools not compact or option indexes changed")
				}
				if len(f.orders) != 0 {
					t.Fatal("rendering submitted purchase")
				}
			})
		}
	}
}

func TestUnavailableActionsUseOneToastWithoutChatNoise(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		for _, action := range []string{"noop", "none"} {
			a, f := newFormFixture(t, nil)
			a.sessions.Update(42, func(s *session.State) { s.Locale = locale; s.Flow = "keep-input" })
			formCallback(a, 42, 10, action)
			if len(f.callbackAnswers) != 1 || f.callbackAnswers[0] == "" || len(f.texts) != 0 || a.sessions.Get(42).Flow != "keep-input" || len(f.orders) != 0 {
				t.Fatal("unavailable action must ACK once with useful feedback and no chat/business mutation")
			}
		}
	}
}

func TestOrderSearchBottomReplyBackAndCancelContext(t *testing.T) {
	for _, action := range []string{"DJ7", "/back", "/cancel", "invalid!"} {
		t.Run(action, func(t *testing.T) {
			a, f := newPendingFixture(t)
			f.list = dujiao.OrderList{Total: 10, TotalPages: 2}
			pendingClick(a, 42, 42, 10, "orderfind:x:2")
			quantityReply(a, action, 0)
			k := f.markups[len(f.markups)-1]
			if f.methods[len(f.methods)-1] != "sendMessage" || findCallback(k, "orders:x:") == "" {
				t.Fatal("typed navigation must reply below input and preserve source filter")
			}
			if action != "invalid!" && a.sessions.Get(42).OrderLookup != nil {
				t.Fatal("finished search retained active input")
			}
			if action == "DJ7" && findCallback(k, "orders:x:") != "orders:x:2" {
				t.Fatal("found order lost source page")
			}
			if f.posts+f.cancels != 0 {
				t.Fatal("search navigation mutated orders/payments")
			}
		})
	}
}

func TestCustomRechargeCorrectionRejectsOldReplyAndBackRestoresPresets(t *testing.T) {
	a, f := newFormFixture(t, nil)
	a.askRechargeAmount(context.Background(), 42, 42, false, 0)
	rechargePick(a, "custom")
	old := a.sessions.Get(42).Recharge
	a.handleUpdate(context.Background(), nil, giftCardMessage("bad", old.MessageID, 42, 42))
	current := a.sessions.Get(42).Recharge
	a.handleUpdate(context.Background(), nil, giftCardMessage("10", old.MessageID, 42, 42))
	if a.sessions.Get(42).Recharge.ID != current.ID || a.sessions.Get(42).Flow != "recharge_amount" {
		t.Fatal("stale reply consumed the current input")
	}
	quantityReply(a, "/back", 0)
	state := a.sessions.Get(42)
	if state.Recharge == nil || state.Recharge.Custom || state.Recharge.Submitting || findCallback(formKeyboard(t, f), "rechargepick:") == "" || f.methods[len(f.methods)-1] != "sendMessage" || len(f.rechargeRequests) != 0 {
		t.Fatal("back did not restore usable presets without creating recharge")
	}
}

func TestCancelInputKeepsFinancialGuardAndContext(t *testing.T) {
	for _, flow := range []string{"gift", "recharge", "purchase", "quantity", "guard"} {
		a, f := newFormFixture(t, nil)
		want := "wallet"
		a.sessions.Update(42, func(s *session.State) {
			s.Flow = flow
			switch flow {
			case "gift":
				s.GiftCard = &session.GiftCardInput{ExpiresAt: time.Now().Add(time.Minute)}
			case "recharge":
				s.Recharge = &session.RechargeDraft{ExpiresAt: time.Now().Add(time.Minute)}
			case "purchase":
				s.Purchase = &session.PurchaseDraft{ProductID: 3, SKUID: 31, Quantity: 2500, ExpiresAt: time.Now().Add(time.Minute)}
				want = "qty:3:31:2500"
			case "quantity":
				s.Quantity = &session.QuantityInput{ProductID: 3, SKUID: 31, Quantity: 2500, ExpiresAt: time.Now().Add(time.Minute)}
				want = "qty:3:31:2500"
			case "guard":
				s.WithdrawalGuard = &session.WithdrawalGuard{ID: "unknown-result", Status: "unknown"}
				want = "affwithdraws:1"
			}
		})
		quantityReply(a, "/cancel", 0)
		state := a.sessions.Get(42)
		if state.Flow != "" || findCallback(formKeyboard(t, f), want) != want || (flow == "guard" && state.WithdrawalGuard == nil) || len(f.orders)+len(f.rechargeRequests)+len(f.giftCodes) != 0 {
			t.Fatal("cancel lost context/guard or wrote business state", flow)
		}
	}
}
