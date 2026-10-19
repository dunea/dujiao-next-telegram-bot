package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/manualform"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func newPurchaseDraft(chatID int64, product dujiao.ProductDetail, skuID uint, quantity int) (*session.PurchaseDraft, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &session.PurchaseDraft{
		ID: hex.EncodeToString(id[:]), ChatID: chatID, ProductID: product.ID, SKUID: skuID, Quantity: quantity,
		Schema: product.ManualFormSchema, Values: make(map[string]any), ExpiresAt: time.Now().Add(formLifetime),
	}, nil
}

func draftOrderRequest(user *models.User, locale string, draft *session.PurchaseDraft, values map[string]any) dujiao.OrderRequest {
	request := dujiao.OrderRequest{
		TelegramIdentity:    identityFromUser(user, locale),
		Items:               []dujiao.OrderItemRequest{{ProductID: draft.ProductID, SKUID: draft.SKUID, Quantity: draft.Quantity}},
		CouponCode:          draft.CouponCode,
		AffiliateVisitorKey: affiliateVisitorKey(user.ID),
	}
	if len(draft.Schema.Fields) > 0 {
		request.ManualFormData = map[string]any{manualform.ItemKey(draft.ProductID, draft.SKUID): values}
	}
	return request
}

func couponButtons(locale, id, revision, code string) [][]models.InlineKeyboardButton {
	label := tr(locale, "输入优惠码", "輸入優惠碼", "Add coupon")
	if code != "" {
		label = tr(locale, "更换优惠码", "更換優惠碼", "Change coupon")
	}
	prefix := "coupon:" + id + ":" + revision + ":"
	rows := [][]models.InlineKeyboardButton{{{Text: label, CallbackData: prefix + "edit"}}}
	if code != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "移除优惠码", "移除優惠碼", "Remove coupon"), CallbackData: prefix + "remove"}})
	}
	return rows
}

func (a *App) handleCouponCallback(ctx context.Context, query *models.CallbackQuery, parts []string) {
	if len(parts) != 4 {
		return
	}
	message := query.Message.Message
	locale := a.sessions.Get(query.From.ID).Locale
	revision, err := strconv.Atoi(parts[2])
	if err != nil {
		return
	}
	draft := a.formDraft(query.From.ID, message.Chat.ID)
	if draft == nil || draft.ID != parts[1] || draft.Revision != revision || draft.PreviewID != message.ID || draft.Step < len(draft.Schema.Fields) {
		a.expiredForm(ctx, message.Chat.ID, locale)
		return
	}
	if parts[3] == "edit" && message.Chat.ID != query.From.ID {
		a.send(ctx, message.Chat.ID, tr(locale, "请在机器人私聊中输入优惠码。", "請在機器人私訊中輸入優惠碼。", "Enter coupons in a private chat with the bot."), nil)
		return
	}
	if parts[3] != "edit" && parts[3] != "remove" && parts[3] != "retry" {
		return
	}
	changed := false
	state := a.sessions.Update(query.From.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == revision && !current.Submitting {
			current.Ready = false
			current.Revision++
			current.PromptID = 0
			state.Flow = ""
			if parts[3] == "edit" {
				state.Flow = "coupon_code"
			}
			if parts[3] == "remove" {
				current.CouponCode = ""
			}
			changed = true
		}
	})
	if !changed {
		return
	}
	if parts[3] == "edit" {
		a.promptCoupon(ctx, &query.From, state.Purchase)
	} else {
		a.previewForm(ctx, &query.From, state.Purchase)
	}
}

func (a *App) promptCoupon(ctx context.Context, user *models.User, draft *session.PurchaseDraft) {
	locale := a.sessions.Get(user.ID).Locale
	text := tr(locale, "请回复这条消息输入优惠码。发送 /back 返回订单预览，/cancel 取消本次购买。", "請回覆這則訊息輸入優惠碼。傳送 /back 返回訂單預覽，/cancel 取消本次購買。", "Reply with your coupon code. Send /back to return to the preview or /cancel to cancel this purchase.")
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: draft.ChatID, Text: text, ReplyMarkup: models.ForceReply{ForceReply: true, Selective: true}})
	if err != nil {
		a.logger.Error("send coupon prompt", "error", err)
		return
	}
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && state.Flow == "coupon_code" && !current.Submitting {
			current.PromptID = message.ID
		}
	})
}

func (a *App) handleCouponMessage(ctx context.Context, message *models.Message) {
	locale := a.sessions.Get(message.From.ID).Locale
	draft := a.formDraft(message.From.ID, message.Chat.ID)
	if draft == nil {
		a.expiredForm(ctx, message.Chat.ID, locale)
		return
	}
	command, _ := parseCommand(message.Text)
	back := command == "/back"
	if !back && (message.ReplyToMessage == nil || message.ReplyToMessage.ID != draft.PromptID) {
		a.send(ctx, message.Chat.ID, tr(locale, "请回复当前的优惠码输入提示。", "請回覆目前的優惠碼輸入提示。", "Reply to the current coupon prompt."), nil)
		return
	}
	code := strings.TrimSpace(message.Text)
	if !back && (code == "" || utf8.RuneCountInString(code) > 128 || strings.ContainsAny(code, "\r\n\t")) {
		a.send(ctx, message.Chat.ID, tr(locale, "优惠码需为 1–128 个字符的单行文字。", "優惠碼需為 1–128 個字元的單行文字。", "Use a single-line coupon code of 1–128 characters."), nil)
		a.promptCoupon(ctx, message.From, draft)
		return
	}
	changed := false
	state := a.sessions.Update(message.From.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && current.PromptID == draft.PromptID && state.Flow == "coupon_code" && !current.Submitting {
			if !back {
				current.CouponCode = code
			}
			current.Revision++
			current.PromptID = 0
			current.Ready = false
			state.Flow = ""
			changed = true
		}
	})
	if changed {
		a.previewForm(ctx, message.From, state.Purchase)
	}
}

func (a *App) showCheckoutError(ctx context.Context, draft *session.PurchaseDraft, messageID int, locale string, err error) {
	text := tr(locale, "订单预览失败，购买资料已保留，可以重试或修改后再预览。", "訂單預覽失敗，購買資料已保留，可以重試或修改後再預覽。", "Order preview failed. Your details are saved; retry or edit them.")
	if isAPIError(err, "coupon_invalid") {
		text = tr(locale, "优惠码不可用，可能已过期、未达到门槛或不适用当前商品。请更换或移除优惠码后重试。购买资料已保留。", "優惠碼無法使用，可能已過期、未達門檻或不適用目前商品。請更換或移除優惠碼後重試。購買資料已保留。", "This coupon is unavailable, expired, below its minimum spend, or not applicable. Change or remove it and try again. Your purchase details are saved.")
	} else {
		a.logger.Warn("preview checkout failed", "error", err)
	}
	rows := couponButtons(locale, draft.ID, strconv.Itoa(draft.Revision), draft.CouponCode)
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "重新预览", "重新預覽", "Retry preview"), CallbackData: fmt.Sprintf("coupon:%s:%d:retry", draft.ID, draft.Revision)}})
	if len(draft.Schema.Fields) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "修改资料", "修改資料", "Edit details"), CallbackData: fmt.Sprintf("formedit:%s:%d", draft.ID, draft.Revision)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Back to product"), CallbackData: fmt.Sprintf("product:%d", draft.ProductID)}})
	a.edit(ctx, draft.ChatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) restoreCheckout(ctx context.Context, user *models.User, draft *session.PurchaseDraft) {
	changed := false
	state := a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && current.Submitting {
			current.Submitting = false
			current.Ready = false
			current.Revision++
			state.Flow = ""
			changed = true
		}
	})
	if changed {
		a.previewForm(ctx, user, state.Purchase)
	}
}
