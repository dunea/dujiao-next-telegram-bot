package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func giftCardMessage(text string, promptID int, userID, chatID int64) *models.Update {
	return &models.Update{Message: &models.Message{ID: 500, From: &models.User{ID: userID}, Chat: models.Chat{ID: chatID}, Text: text, ReplyToMessage: &models.Message{ID: promptID}}}
}

func TestGiftCardRedemptionClaimsInputOnceAndShowsWallet(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	formCallback(app, 42, 10, "giftcard")
	input := app.sessions.Get(42).GiftCard
	if input == nil || input.PromptID == 0 {
		t.Fatal("gift card prompt not created")
	}
	message := giftCardMessage(" gift50 ", input.PromptID, 42, 42)
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() { app.handleUpdate(context.Background(), nil, message) })
	}
	wait.Wait()
	formCallback(app, 42, 10, "walletlog:1")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.giftCodes) != 1 || fixture.giftCodes[0] != "GIFT50" {
		t.Fatalf("gift card requests = %#v", fixture.giftCodes)
	}
	text := strings.Join(fixture.texts, "\n")
	if !strings.Contains(text, "到账金额：50.00 CNY") || !strings.Contains(text, "当前余额：150.00 CNY") {
		t.Fatalf("missing redemption result: %s", text)
	}
	if !strings.Contains(text, "礼品卡兑换</b>\n+50.00") {
		t.Fatal("gift card credit not shown in wallet transactions")
	}
	if app.sessions.Get(42).GiftCard != nil {
		t.Fatal("completed gift card input retained")
	}
}

func TestGiftCardKnownErrorsRetryWithoutEchoingCode(t *testing.T) {
	for _, errorCode := range []string{"gift_card_not_found", "gift_card_invalid", "gift_card_expired", "gift_card_disabled", "gift_card_redeemed"} {
		t.Run(errorCode, func(t *testing.T) {
			app, fixture := newFormFixture(t, nil)
			var logs bytes.Buffer
			app.logger = slog.New(slog.NewTextHandler(&logs, nil))
			fixture.giftFailureCode = errorCode
			formCallback(app, 42, 10, "giftcard")
			input := app.sessions.Get(42).GiftCard
			before := len(fixture.texts)
			app.handleUpdate(context.Background(), nil, giftCardMessage("SECRET-FAIL-CODE", input.PromptID, 42, 42))
			current := app.sessions.Get(42).GiftCard
			if current == nil || current.ID == input.ID || current.PromptID == input.PromptID {
				t.Fatal("known card error did not allow fresh input")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.texts) != before+1 || !strings.Contains(fixture.markups[len(fixture.markups)-1], `"force_reply":true`) {
				t.Fatal("known rejection must combine error and new reply prompt")
			}
			if strings.Contains(strings.Join(fixture.texts, "\n"), "SECRET-FAIL-CODE") || strings.Contains(logs.String(), "SECRET-FAIL-CODE") {
				t.Fatal("gift card code leaked in outgoing message or logs")
			}
			if len(fixture.giftCodes) != 1 {
				t.Fatal("redemption was automatically retried")
			}
		})
	}
}

func TestGiftCardUnknownResultDoesNotRetry(t *testing.T) {
	app, fixture := newFormFixture(t, nil)
	fixture.giftFailureCode = "gift_card_redeem_failed"
	formCallback(app, 42, 10, "giftcard")
	input := app.sessions.Get(42).GiftCard
	app.handleUpdate(context.Background(), nil, giftCardMessage("SECRET-CODE", input.PromptID, 42, 42))
	if app.sessions.Get(42).GiftCard != nil {
		t.Fatal("ambiguous redemption kept accepting inputs")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.giftCodes) != 1 || !strings.Contains(strings.Join(fixture.texts, "\n"), "暂时无法确认兑换结果") {
		t.Fatal("uncertain redemption result not handled")
	}
}

func TestGiftCardStaleInputExpiryNavigationAndPrivateChat(t *testing.T) {
	for _, mode := range []string{"stale", "expiry", "cancel", "wallet", "back", "other-user", "group"} {
		t.Run(mode, func(t *testing.T) {
			app, fixture := newFormFixture(t, nil)
			formCallback(app, 42, 10, "giftcard")
			input := app.sessions.Get(42).GiftCard
			switch mode {
			case "stale":
				formCallback(app, 42, 10, "giftcard")
			case "expiry":
				app.sessions.Update(42, func(state *session.State) { state.GiftCard.ExpiresAt = time.Now().Add(-time.Second) })
			case "cancel":
				app.handleUpdate(context.Background(), nil, giftCardMessage("/cancel", input.PromptID, 42, 42))
			case "wallet":
				app.handleUpdate(context.Background(), nil, giftCardMessage("/wallet", input.PromptID, 42, 42))
			case "back":
				app.handleUpdate(context.Background(), nil, giftCardMessage("/back", input.PromptID, 42, 42))
			case "other-user":
				app.handleUpdate(context.Background(), nil, giftCardMessage("GIFT50", input.PromptID, 99, 42))
			case "group":
				app.askGiftCard(context.Background(), -100, 99)
				app.handleUpdate(context.Background(), nil, giftCardMessage("GIFT50", input.PromptID, 42, -100))
			}
			if mode != "other-user" && mode != "group" {
				app.handleUpdate(context.Background(), nil, giftCardMessage("GIFT50", input.PromptID, 42, 42))
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.giftCodes) != 0 {
				t.Fatalf("inactive or foreign input redeemed a card: %#v", fixture.giftCodes)
			}
		})
	}
}
