package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

func utilityCallback(a *App, data string, keyboard models.InlineKeyboardMarkup) {
	a.handleCallback(context.Background(), &models.CallbackQuery{ID: "utility", From: models.User{ID: 42}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 10, Chat: models.Chat{ID: 42}, ReplyMarkup: &keyboard}}})
}

func TestHelpCompactShortcutsAndConfiguredIntroAreReadOnly(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		for _, target := range []string{"", "javascript:bad", "@synthetic_support", "https://support.example.invalid/help"} {
			a.UpdateConfig(dujiao.BotConfig{Basic: dujiao.BotBasicConfig{SupportURL: target}})
			a.showHelp(context.Background(), 42, locale)
			k := formKeyboard(t, f)
			assertCompactKeyboard(t, k)
			hasContact := target == "@synthetic_support" || strings.HasPrefix(target, "https://")
			if (len(k.InlineKeyboard) == 4) != hasContact || (supportURL(k) != "") != hasContact {
				t.Fatal("help contact visibility or compact row count changed")
			}
			for _, cb := range []string{"orders:a:1", "wallet", "lang", "bcsubscriptions", "cats", "home"} {
				if findCallback(k, cb) != cb {
					t.Fatal("help lost one-hop action", cb)
				}
			}
		}
		a.UpdateConfig(dujiao.BotConfig{Help: dujiao.BotHelpConfig{Intro: dujiao.LocalizedText{locale: "商家说明 <custom>&"}}})
		if a.helpText(locale) != "商家说明 &lt;custom&gt;&amp;" {
			t.Fatal("merchant help intro was rewritten or not safely escaped")
		}
		if len(f.orders) != 0 || len(f.previews) != 0 || len(f.rechargeRequests) != 0 || len(broadcastState(t, a).Drafts) != 0 || broadcastState(t, a).Job != nil {
			t.Fatal("rendering help started a business operation")
		}
	}
}

func TestLanguageChoiceRowMarksCurrentUserWithoutChangingUntilClick(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		a.sessions.Update(42, func(s *session.State) { s.Locale = locale })
		a.showLanguage(context.Background(), -100, 42, false, 0)
		k := formKeyboard(t, f)
		if len(k.InlineKeyboard) != 2 || len(k.InlineKeyboard[0]) != 3 || len(k.InlineKeyboard[1]) != 2 || findCallback(k, "help") != "help" || findCallback(k, "home") != "home" {
			t.Fatal("language row or compact navigation missing")
		}
		selected := 0
		for _, b := range k.InlineKeyboard[0] {
			if strings.HasPrefix(b.Text, "✓ ") {
				selected++
				if b.CallbackData != "setlang:"+locale {
					t.Fatal("language marks chat instead of current user")
				}
			}
		}
		if selected != 1 || a.sessions.Get(42).Locale != locale {
			t.Fatal("opening language page changed selection")
		}
		formCallback(a, 42, 10, "setlang:invalid")
		if a.sessions.Get(42).Locale != locale {
			t.Fatal("invalid language changed preference")
		}
		formCallback(a, 42, 10, "setlang:en-US")
		if a.sessions.Get(42).Locale != "en-US" || findCallback(formKeyboard(t, f), "cats") != "cats" || len(f.orders) != 0 {
			t.Fatal("explicit language choice no longer returns to home")
		}
	}
}

type subscriptionProbe struct {
	broadcast.Store
	writes                 int
	failRead, loseWriteAck bool
}

func (p *subscriptionProbe) Consent(ctx context.Context, uid int64) (string, error) {
	if p.failRead {
		return "", broadcast.ErrUnavailable
	}
	return p.Store.Consent(ctx, uid)
}
func (p *subscriptionProbe) SetSubscribed(ctx context.Context, uid int64, on bool) error {
	p.writes++
	err := p.Store.SetSubscribed(ctx, uid, on)
	if p.loseWriteAck {
		return broadcast.ErrUnavailable
	}
	return err
}

func TestSubscriptionsCompactLocalizedAndRepeatedActionsDoNotToggle(t *testing.T) {
	for _, locale := range []string{"zh-CN", "zh-TW", "en-US"} {
		a, f := newFormFixture(t, nil)
		a.sessions.Update(42, func(s *session.State) {
			s.Locale, s.Flow = locale, "withdrawal"
			s.WithdrawalGuard = &session.WithdrawalGuard{ID: "guard", Status: "unknown"}
		})
		probe := &subscriptionProbe{Store: a.broadcastStore}
		a.broadcastStore = probe
		formCallback(a, 42, 10, "bcsubscriptions")
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if len(k.InlineKeyboard) != 3 || len(k.InlineKeyboard[0]) != 1 || findCallback(k, "bcsubscribe") != "bcsubscribe" || probe.writes != 0 || f.methods[len(f.methods)-1] != "editMessageText" {
			t.Fatal("opening subscription panel changed consent or emitted an extra message")
		}
		for range 2 {
			utilityCallback(a, "bcsubscribe", k)
		}
		if token, _ := probe.Consent(context.Background(), 42); token == "" {
			t.Fatal("repeat subscribe toggled off")
		}
		k = formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if findCallback(k, "bcunsubscribe") == "" || f.methods[len(f.methods)-1] != "editMessageText" {
			t.Fatal("subscribe result lacks current state or compact navigation")
		}
		for _, cb := range []string{"help", "lang", "cats", "home"} {
			if findCallback(k, cb) != cb {
				t.Fatal("subscription result is a navigation dead end")
			}
		}
		utilityCallback(a, "bcunsubscribe", k)
		if token, _ := probe.Consent(context.Background(), 42); token != "" {
			t.Fatal("unsubscribe failed")
		}
		if s := a.sessions.Get(42); s.Flow != "withdrawal" || s.WithdrawalGuard == nil || s.Locale != locale || broadcastState(t, a).Job != nil || len(f.orders) != 0 {
			t.Fatal("subscription navigation changed financial state or started a broadcast")
		}
	}
}

func TestSubscriptionUnknownStateOnlyOffersReadOnlyRecovery(t *testing.T) {
	for _, mode := range []string{"read", "write acknowledgement"} {
		a, f := newFormFixture(t, nil)
		probe := &subscriptionProbe{Store: a.broadcastStore, failRead: mode == "read", loseWriteAck: mode != "read"}
		a.broadcastStore = probe
		if mode == "read" {
			formCallback(a, 42, 10, "bcsubscriptions")
		} else {
			utilityCallback(a, "bcsubscribe", subscriptionKeyboard("zh-CN", false))
		}
		k := formKeyboard(t, f)
		assertCompactKeyboard(t, k)
		if findCallback(k, "bcsubscribe") != "" || findCallback(k, "bcunsubscribe") != "" || findCallback(k, "bcsubscriptions") != "bcsubscriptions" || strings.Contains(f.texts[len(f.texts)-1], "/broadcast_status") || probe.writes > 1 {
			t.Fatal("uncertain subscription outcome encouraged mutation or administrator-only recovery")
		}
	}
}

func TestSubscriptionAnnouncementAndEditFailureOnlyResendPresentation(t *testing.T) {
	for _, mode := range []string{"announcement", "edit failed", "not modified", "session unavailable"} {
		a, f := newFormFixture(t, nil)
		probe := &subscriptionProbe{Store: a.broadcastStore}
		a.broadcastStore = probe
		_ = probe.Store.SetSubscribed(context.Background(), 42, true)
		keyboard := subscriptionKeyboard("zh-CN", true)
		switch mode {
		case "announcement":
			keyboard = broadcastUnsubscribeKeyboard()
		case "edit failed":
			f.editFailure = "Bad Request: message to edit not found"
		case "not modified":
			f.editFailure = "Bad Request: message is not modified"
		case "session unavailable":
			a.UpdateConfig(dujiao.BotConfig{DefaultLocale: "en-US"})
			a.sessions = unavailableSupportSessions{Store: a.sessions}
		}
		before := len(f.methods)
		utilityCallback(a, "bcunsubscribe", keyboard)
		if token, _ := probe.Consent(context.Background(), 42); token != "" || probe.writes != 1 {
			t.Fatal("presentation retry repeated mutation or failed to unsubscribe")
		}
		if mode == "not modified" {
			if len(f.methods) != before {
				t.Fatal("unchanged panel created an extra message")
			}
		} else if mode == "announcement" || mode == "edit failed" {
			if f.methods[len(f.methods)-1] != "sendMessage" {
				t.Fatal("announcement was replaced or failed edit did not recover")
			}
		}
		if mode == "session unavailable" && !strings.Contains(f.texts[len(f.texts)-1], "Announcements unsubscribed.") {
			t.Fatal("session failure did not preserve store-default locale fallback")
		}
	}
}
