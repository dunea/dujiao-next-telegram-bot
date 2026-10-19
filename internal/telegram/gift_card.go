package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

func (a *App) askGiftCard(ctx context.Context, chatID, userID int64) {
	locale := a.sessions.Get(userID).Locale
	if chatID != userID {
		a.send(ctx, chatID, tr(locale, "请在机器人私聊中兑换礼品卡。", "請在機器人私訊中兌換禮品卡。", "Redeem gift cards in a private chat with the bot."), nil)
		return
	}
	input := &session.GiftCardInput{ID: rand.Text(), ChatID: chatID, ExpiresAt: time.Now().Add(10 * time.Minute)}
	result := a.sessions.Update(userID, func(state *session.State) { state.Purchase = nil; state.Flow = "gift_card"; state.GiftCard = input })
	if !a.sessionOK(ctx, chatID, result.Err) {
		return
	}
	a.sendGiftCardPrompt(ctx, userID, input)
}

func (a *App) sendGiftCardPrompt(ctx context.Context, userID int64, input *session.GiftCardInput) {
	locale := a.sessions.Get(userID).Locale
	text := tr(locale, "请回复这条消息输入礼品卡兑换码。发送 /back 返回钱包，/cancel 取消。", "請回覆這則訊息輸入禮品卡兌換碼。傳送 /back 返回錢包，/cancel 取消。", "Reply to this message with your gift card code. Send /back to return to your wallet or /cancel to cancel.")
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: input.ChatID, Text: text, ReplyMarkup: models.ForceReply{ForceReply: true, Selective: true}})
	if err != nil {
		a.logger.Error("send gift card prompt", "error", err)
		return
	}
	stored := a.sessions.Update(userID, func(state *session.State) {
		if current := state.GiftCard; current != nil && current.ID == input.ID && !current.Submitting {
			current.PromptID = message.ID
		}
	})
	a.sessionOK(ctx, input.ChatID, stored.Err)
}

func (a *App) handleGiftCardMessage(ctx context.Context, message *models.Message) {
	userID, chatID := message.From.ID, message.Chat.ID
	state := a.sessions.Get(userID)
	input := state.GiftCard
	if input == nil || input.ChatID != chatID || chatID != userID || input.Submitting || !time.Now().Before(input.ExpiresAt) {
		return
	}
	command, _ := parseCommand(message.Text)
	if command == "/back" {
		if !a.sessionOK(ctx, chatID, a.sessions.ResetFlow(userID)) {
			return
		}
		a.showWallet(ctx, chatID, userID, false, 0)
		return
	}
	if input.PromptID == 0 || message.ReplyToMessage == nil || message.ReplyToMessage.ID != input.PromptID {
		a.send(ctx, chatID, tr(state.Locale, "请回复当前的礼品卡兑换提示。", "請回覆目前的禮品卡兌換提示。", "Reply to the current gift card prompt."), nil)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(message.Text))
	if code == "" || utf8.RuneCountInString(code) > 128 || strings.IndexFunc(code, unicode.IsControl) >= 0 {
		a.send(ctx, chatID, tr(state.Locale, "兑换码格式不正确，请输入 1–128 个字符的单行兑换码。", "兌換碼格式不正確，請輸入 1–128 個字元的單行兌換碼。", "Enter a single-line gift card code of 1–128 characters."), nil)
		a.retryGiftCard(ctx, userID, input.ID)
		return
	}
	claimed := false
	claim := a.sessions.Update(userID, func(state *session.State) {
		if current := state.GiftCard; state.Flow == "gift_card" && current != nil && current.ID == input.ID && current.PromptID == input.PromptID && !current.Submitting && time.Now().Before(current.ExpiresAt) {
			current.Submitting = true
			claimed = true
		}
	})
	if !a.sessionOK(ctx, chatID, claim.Err) || !claimed {
		return
	}
	result, err := a.api.RedeemGiftCard(ctx, strconv.FormatInt(userID, 10), code)
	if err != nil {
		text, retry := giftCardErrorText(state.Locale, err)
		// Upstream error messages may include a submitted code; log metadata only.
		var apiError *dujiao.APIError
		if errors.As(err, &apiError) {
			a.logger.Warn("gift card redemption failed", "user_id", userID, "error_code", apiError.ErrorCode, "http_status", apiError.HTTPStatus)
		} else {
			a.logger.Warn("gift card redemption result unavailable", "user_id", userID)
		}
		a.send(ctx, chatID, text, giftCardResultKeyboard(state.Locale))
		if retry {
			a.retryGiftCard(ctx, userID, input.ID)
		} else {
			a.clearGiftCard(userID, input.ID)
		}
		return
	}
	a.clearGiftCard(userID, input.ID)
	currency := result.GiftCard.Currency
	text := "✅ " + tr(state.Locale, "礼品卡兑换成功", "禮品卡兌換成功", "Gift card redeemed")
	text += "\n\n" + tr(state.Locale, "到账金额：", "入帳金額：", "Credited: ") + html.EscapeString(result.WalletDelta+" "+currency)
	text += "\n" + tr(state.Locale, "当前余额：", "目前餘額：", "Balance: ") + html.EscapeString(result.Wallet.Balance+" "+currency)
	a.send(ctx, chatID, text, giftCardResultKeyboard(state.Locale))
}

func (a *App) clearGiftCard(userID int64, id string) bool {
	cleared := false
	result := a.sessions.Update(userID, func(state *session.State) {
		if state.GiftCard != nil && state.GiftCard.ID == id {
			state.GiftCard = nil
			state.Flow = ""
			cleared = true
		}
	})
	return result.Err == nil && cleared
}

func (a *App) retryGiftCard(ctx context.Context, userID int64, id string) {
	input := &session.GiftCardInput{ID: rand.Text(), ChatID: userID, ExpiresAt: time.Now().Add(10 * time.Minute)}
	changed := false
	result := a.sessions.Update(userID, func(state *session.State) {
		if current := state.GiftCard; current != nil && current.ID == id {
			state.GiftCard = input
			state.Flow = "gift_card"
			changed = true
		}
	})
	if a.sessionOK(ctx, userID, result.Err) && changed {
		a.sendGiftCardPrompt(ctx, userID, input)
	}
}

func giftCardResultKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "查看余额", "查看餘額", "View balance"), CallbackData: "wallet"}, {Text: tr(locale, "交易流水", "交易明細", "Transactions"), CallbackData: "walletlog:1"}},
		{{Text: tr(locale, "兑换其他礼品卡", "兌換其他禮品卡", "Redeem another card"), CallbackData: "giftcard"}},
	}}
}

func giftCardErrorText(locale string, err error) (string, bool) {
	var apiError *dujiao.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode {
		case "gift_card_invalid", "gift_card_not_found", "validation_error":
			return tr(locale, "礼品卡兑换码无效或不存在，请检查后重新输入。", "禮品卡兌換碼無效或不存在，請檢查後重新輸入。", "This gift card code is invalid or does not exist. Check it and try again."), true
		case "gift_card_expired":
			return tr(locale, "这张礼品卡已过期，请联系商家。", "這張禮品卡已過期，請聯絡商家。", "This gift card has expired. Contact the store."), true
		case "gift_card_disabled":
			return tr(locale, "这张礼品卡已停用，请联系商家。", "這張禮品卡已停用，請聯絡商家。", "This gift card is disabled. Contact the store."), true
		case "gift_card_redeemed":
			return tr(locale, "这张礼品卡已兑换，请查看余额和交易流水。", "這張禮品卡已兌換，請查看餘額和交易明細。", "This gift card has already been redeemed. Check your balance and transactions."), true
		case "invite_code_required", "invite_code_invalid", "user_not_found":
			return tr(locale, "请先发送 /start 完成商店账号初始化后再兑换。", "請先傳送 /start 完成商店帳號初始化後再兌換。", "Send /start to finish setting up your store account before redeeming."), false
		case "user_disabled", "registration_disabled":
			return tr(locale, "当前账号无法兑换，请联系商家。", "目前帳號無法兌換，請聯絡商家。", "This account cannot redeem gift cards. Contact the store."), false
		}
		if apiError.HTTPStatus == http.StatusNotFound || apiError.HTTPStatus == http.StatusNotImplemented {
			return tr(locale, "商店暂不支持礼品卡兑换，请联系商家确认版本和配置。", "商店暫不支援禮品卡兌換，請聯絡商家確認版本與設定。", "Gift card redemption is unavailable. Contact the store to check its version and configuration."), false
		}
	}
	return tr(locale, "暂时无法确认兑换结果，请先查看余额和交易流水，再决定是否重试。", "暫時無法確認兌換結果，請先查看餘額和交易明細，再決定是否重試。", "The redemption result could not be confirmed. Check your balance and transactions before retrying."), false
}
