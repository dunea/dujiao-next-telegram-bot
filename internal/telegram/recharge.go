package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"html"
	"strconv"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

var rechargePresets = [...]string{"10.00", "50.00", "100.00", "200.00", "500.00"}

const rechargeChannelPageSize = 5

func (a *App) askRechargeAmount(ctx context.Context, chatID, userID int64, edit bool, messageID int) {
	locale := a.sessions.Get(userID).Locale
	if chatID != userID {
		a.send(ctx, chatID, tr(locale, "请在机器人私聊中充值。", "請在機器人私訊中儲值。", "Recharge in a private chat with the bot."), nil)
		return
	}
	wallet, err := a.api.Wallet(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get recharge currency", err)
		return
	}
	if wallet.Currency == "" {
		a.send(ctx, chatID, tr(locale, "商店未返回钱包币种，暂时无法充值。", "商店未回傳錢包幣別，暫時無法儲值。", "The store did not return the wallet currency. Recharge is unavailable."), nil)
		return
	}
	draft := &session.RechargeDraft{ID: rand.Text(), ChatID: chatID, Currency: wallet.Currency, ExpiresAt: time.Now().Add(10 * time.Minute)}
	result := a.sessions.Update(userID, func(state *session.State) {
		state.Flow, state.Recharge = "recharge_amount", draft
		state.Purchase, state.GiftCard = nil, nil
	})
	if !a.sessionOK(ctx, chatID, result.Err) {
		return
	}
	rows := [][]models.InlineKeyboardButton{{}, {}}
	for i, amount := range rechargePresets {
		row := 0
		if i >= 3 {
			row = 1
		}
		rows[row] = append(rows[row], models.InlineKeyboardButton{Text: amount + " " + wallet.Currency, CallbackData: "rechargepick:" + draft.ID + ":" + amount})
	}
	rows[1] = append(rows[1], models.InlineKeyboardButton{Text: tr(locale, "自定义金额", "自訂金額", "Custom amount"), CallbackData: "rechargepick:" + draft.ID + ":custom"})
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回钱包", "‹ 返回錢包", "‹ Back to wallet"), CallbackData: "wallet"}})
	text := "<b>" + tr(locale, "钱包充值", "錢包儲值", "Wallet recharge") + "</b>\n\n"
	text += tr(locale, "钱包币种：", "錢包幣別：", "Wallet currency: ") + html.EscapeString(wallet.Currency) + "\n"
	text += tr(locale, "请选择充值金额，也可直接输入金额（大于 0，最多两位小数，上限 1,000,000）。\n发送 /cancel 取消，下一步选择渠道时展示费用规则。", "請選擇儲值金額，也可直接輸入金額（大於 0，最多兩位小數，上限 1,000,000）。\n傳送 /cancel 取消，下一步選擇管道時顯示費用規則。", "Choose an amount or type a custom amount (greater than 0, up to 2 decimal places, maximum 1,000,000).\nSend /cancel to cancel. Channel fee rules are shown next.")
	a.publishRecharge(ctx, userID, draft, messageID, edit, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) publishRecharge(ctx context.Context, userID int64, draft *session.RechargeDraft, messageID int, edit bool, text string, markup models.ReplyMarkup) {
	var err error
	if edit {
		_, err = a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: draft.ChatID, MessageID: messageID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	} else {
		var message *models.Message
		message, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: draft.ChatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
		if err == nil {
			messageID = message.ID
		}
	}
	if err != nil {
		a.logger.Warn("show recharge selection", "user_id", userID, "error", err)
	}
	stored := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Recharge; current != nil && current.ID == draft.ID {
			if err != nil {
				state.Recharge, state.Flow = nil, ""
			} else {
				current.MessageID = messageID
			}
		}
	})
	a.sessionOK(ctx, draft.ChatID, stored.Err)
}

func (a *App) claimRecharge(userID int64, id string, messageID int, flow string) *session.RechargeDraft {
	claimed := false
	state := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Recharge; state.Flow == flow && current != nil && current.ID == id && current.ChatID == userID && current.MessageID != 0 && current.MessageID == messageID && !current.Submitting && time.Now().Before(current.ExpiresAt) {
			current.Submitting = true
			claimed = true
		}
	})
	if state.Err != nil || !claimed {
		return nil
	}
	return state.Recharge
}

func (a *App) handleRechargeCallback(ctx context.Context, query *models.CallbackQuery, parts []string) {
	message, userID := query.Message.Message, query.From.ID
	if message.Chat.ID != userID || len(parts) != 3 {
		return
	}
	state := a.sessions.Get(userID)
	draft := state.Recharge
	if draft == nil || draft.ID != parts[1] || draft.MessageID != message.ID || draft.Submitting {
		a.send(ctx, message.Chat.ID, tr(state.Locale, "这组充值按钮已失效或已提交。请查看钱包，或发送 /recharge 重新选择。", "這組儲值按鈕已失效或已提交。請查看錢包，或傳送 /recharge 重新選擇。", "These recharge buttons have expired or were already submitted. Check your wallet or send /recharge to start again."), nil)
		return
	}
	if parts[0] == "rechargepick" && state.Flow == "recharge_amount" {
		if parts[2] == "custom" {
			if a.claimRecharge(userID, draft.ID, message.ID, state.Flow) == nil {
				return
			}
			next := *draft
			next.ID, next.Custom, next.MessageID, next.Submitting = rand.Text(), true, 0, false
			result := a.sessions.Update(userID, func(state *session.State) {
				if state.Recharge != nil && state.Recharge.ID == draft.ID {
					state.Recharge = &next
				}
			})
			if !a.sessionOK(ctx, message.Chat.ID, result.Err) {
				return
			}
			a.promptCustomRecharge(ctx, userID, &next, "")
			a.clearInputButtons(ctx, draft.ChatID, draft.MessageID)
			return
		}
		for _, amount := range rechargePresets {
			if amount == parts[2] {
				a.selectRechargeAmount(ctx, userID, draft, amount, true)
				return
			}
		}
	}
	if parts[0] != "rechargepay" || state.Flow != "recharge_channels" {
		if parts[0] == "rechargepage" && state.Flow == "recharge_channels" {
			page, err := strconv.Atoi(parts[2])
			if err != nil || page < 0 || page >= (len(draft.Channels)+rechargeChannelPageSize-1)/rechargeChannelPageSize || page == draft.Page || a.claimRecharge(userID, draft.ID, message.ID, state.Flow) == nil {
				return
			}
			next := *draft
			next.ID, next.Page, next.Submitting = rand.Text(), page, false
			changed := false
			result := a.sessions.Update(userID, func(state *session.State) {
				if state.Recharge != nil && state.Recharge.ID == draft.ID {
					state.Recharge = &next
					changed = true
				}
			})
			if a.sessionOK(ctx, message.Chat.ID, result.Err) && changed {
				a.showRechargeChannels(ctx, userID, &next, true)
			}
		}
		return
	}
	channelID, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || channelID == 0 {
		return
	}
	for _, channel := range draft.Channels {
		if uint64(channel.ID) == channelID {
			if !a.allowWrite(ctx, message.Chat.ID, userID, operations.Recharge) {
				return
			}
			if a.claimRecharge(userID, draft.ID, message.ID, state.Flow) == nil {
				return
			}
			// Consume before the write. An uncertain network result must not retry it.
			consumed := a.sessions.Update(userID, func(state *session.State) {
				if state.Recharge != nil && state.Recharge.ID == draft.ID {
					state.Recharge, state.Flow = nil, ""
				}
			})
			if !a.sessionOK(ctx, message.Chat.ID, consumed.Err) {
				return
			}
			a.createWalletRecharge(ctx, message.Chat.ID, message.ID, userID, draft.Amount, draft.Currency, channel.ID)
			return
		}
	}
}

func (a *App) promptCustomRecharge(ctx context.Context, userID int64, draft *session.RechargeDraft, notice string) {
	locale := a.sessions.Get(userID).Locale
	text := tr(locale, "请回复这条消息输入充值金额（大于 0，最多两位小数，上限 1,000,000）。发送 /back 返回金额选择，/cancel 取消。\n币种：", "請回覆這則訊息輸入儲值金額（大於 0，最多兩位小數，上限 1,000,000）。傳送 /back 返回金額選擇，/cancel 取消。\n幣別：", "Reply with an amount greater than 0, up to 2 decimal places, maximum 1,000,000. Send /back to choose an amount or /cancel to cancel.\nCurrency: ") + html.EscapeString(draft.Currency)
	a.publishRecharge(ctx, userID, draft, 0, false, inputNotice(text, notice), models.ForceReply{ForceReply: true, Selective: true})
}

func (a *App) handleRechargeAmount(ctx context.Context, message *models.Message) {
	state := a.sessions.Get(message.From.ID)
	draft := state.Recharge
	if draft == nil || draft.Submitting || draft.ChatID != message.Chat.ID || message.Chat.ID != message.From.ID {
		return
	}
	if command, _ := parseCommand(message.Text); command == "/back" {
		if a.claimRecharge(message.From.ID, draft.ID, draft.MessageID, "recharge_amount") != nil {
			a.askRechargeAmount(ctx, message.Chat.ID, message.From.ID, false, 0)
			stored := a.sessions.Update(message.From.ID, func(s *session.State) {
				if s.Recharge != nil && s.Recharge.ID == draft.ID {
					s.Recharge.Submitting = false // Read failure must not lock input.
				}
			})
			a.sessionOK(ctx, message.Chat.ID, stored.Err)
			a.clearInputButtons(ctx, draft.ChatID, draft.MessageID)
		}
		return
	}
	if draft.Custom && (message.ReplyToMessage == nil || message.ReplyToMessage.ID != draft.MessageID) {
		a.send(ctx, message.Chat.ID, tr(state.Locale, "请回复当前的充值金额提示。", "請回覆目前的儲值金額提示。", "Reply to the current recharge amount prompt."), nil)
		return
	}
	amount, ok := normalizeMoneyInput(message.Text)
	if !ok {
		notice := tr(state.Locale, "金额格式不正确，请输入大于 0、最多两位小数且不超过 1,000,000 的金额。", "金額格式不正確，請輸入大於 0、最多兩位小數且不超過 1,000,000 的金額。", "Enter an amount greater than 0, with up to 2 decimal places and no more than 1,000,000.")
		if draft.Custom {
			if a.claimRecharge(message.From.ID, draft.ID, draft.MessageID, "recharge_amount") == nil {
				return
			}
			next := *draft
			next.ID, next.MessageID, next.Submitting = rand.Text(), 0, false
			changed := false
			stored := a.sessions.Update(message.From.ID, func(s *session.State) {
				if s.Flow == "recharge_amount" && s.Recharge != nil && s.Recharge.ID == draft.ID && s.Recharge.Submitting {
					s.Recharge, changed = &next, true
				}
			})
			if a.sessionOK(ctx, message.Chat.ID, stored.Err) && changed {
				a.promptCustomRecharge(ctx, message.From.ID, &next, notice)
			}
		} else {
			a.send(ctx, message.Chat.ID, notice, cancelInputKeyboard(state.Locale, state))
		}
		return
	}
	a.selectRechargeAmount(ctx, message.From.ID, draft, amount, false)
}

func (a *App) selectRechargeAmount(ctx context.Context, userID int64, draft *session.RechargeDraft, amount string, edit bool) {
	if a.claimRecharge(userID, draft.ID, draft.MessageID, "recharge_amount") == nil {
		return
	}
	locale := a.sessions.Get(userID).Locale
	channels, err := a.api.RechargeChannels(ctx)
	if err != nil || len(channels.Items) == 0 {
		stored := a.sessions.Update(userID, func(state *session.State) {
			if state.Recharge != nil && state.Recharge.ID == draft.ID {
				state.Recharge.Submitting = false
			}
		})
		if !a.sessionOK(ctx, draft.ChatID, stored.Err) {
			return
		}
		if err != nil {
			a.apiFailure(ctx, draft.ChatID, locale, "list recharge channels", err)
		} else {
			a.send(ctx, draft.ChatID, tr(locale, "商店暂未配置钱包充值渠道。", "商店暫未設定錢包儲值管道。", "No wallet recharge channel is configured."), a.backKeyboard(locale))
		}
		return
	}
	next := *draft
	next.ID, next.Amount, next.Channels, next.Submitting = rand.Text(), amount, channels.Items, false
	changed := false
	result := a.sessions.Update(userID, func(state *session.State) {
		if state.Recharge != nil && state.Recharge.ID == draft.ID && time.Now().Before(state.Recharge.ExpiresAt) {
			state.Recharge, state.Flow = &next, "recharge_channels"
			changed = true
		}
	})
	if !a.sessionOK(ctx, draft.ChatID, result.Err) || !changed {
		return
	}
	a.showRechargeChannels(ctx, userID, &next, edit)
	if !edit {
		a.clearInputButtons(ctx, draft.ChatID, draft.MessageID)
	}
}

func (a *App) showRechargeChannels(ctx context.Context, userID int64, draft *session.RechargeDraft, edit bool) {
	locale := a.sessions.Get(userID).Locale
	text := tr(locale, "充值到账金额：", "儲值入帳金額：", "Wallet credit: ") + "<b>" + html.EscapeString(draft.Amount+" "+draft.Currency) + "</b>\n\n"
	text += tr(locale, "请选择支付渠道（点击后创建充值单）：", "請選擇付款管道（點擊後建立儲值單）：", "Choose a channel to create the recharge:")
	var choices []models.InlineKeyboardButton
	start, end := draft.Page*rechargeChannelPageSize, min((draft.Page+1)*rechargeChannelPageSize, len(draft.Channels))
	for _, channel := range draft.Channels[start:end] {
		name := truncate(channel.Name, 40)
		text += "\n\n<b>" + html.EscapeString(name) + "</b>\n" + rechargeFeeRule(locale, draft.Amount, draft.Currency, channel)
		choices = append(choices, models.InlineKeyboardButton{Text: name, CallbackData: fmt.Sprintf("rechargepay:%s:%d", draft.ID, channel.ID)})
	}
	rows := compactActionRows(choices)
	var pager []models.InlineKeyboardButton
	if draft.Page > 0 {
		pager = append(pager, models.InlineKeyboardButton{Text: "‹", CallbackData: fmt.Sprintf("rechargepage:%s:%d", draft.ID, draft.Page-1)})
	}
	if end < len(draft.Channels) {
		pager = append(pager, models.InlineKeyboardButton{Text: "›", CallbackData: fmt.Sprintf("rechargepage:%s:%d", draft.ID, draft.Page+1)})
	}
	if len(pager) > 0 {
		rows = append(rows, pager)
	}
	text += "\n\n" + tr(locale, "以上是渠道手续费参考，并非最终应付报价。接口未提供手续费由谁承担的配置；是否加收、最终币种与实付金额以商店创建的充值单为准。", "以上為管道手續費參考，並非最終應付報價。介面未提供手續費由誰承擔的設定；是否加收、最終幣別與實付金額以商店建立的儲值單為準。", "Channel fees are indicative, not a final quote. The API does not expose who pays the fee. Any surcharge, final currency and payable amount are determined by the store's recharge response.")
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "修改金额", "修改金額", "Change amount"), CallbackData: "recharge"}, {Text: tr(locale, "‹ 返回钱包", "‹ 返回錢包", "‹ Back to wallet"), CallbackData: "wallet"}})
	a.publishRecharge(ctx, userID, draft, draft.MessageID, edit, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func rechargeFeeRule(locale, amount, currency string, channel dujiao.PaymentChannel) string {
	fee, ok := money.Fee(amount, channel.FeeRate, channel.FixedFee)
	if !ok {
		return tr(locale, "渠道未返回有效费用规则，请以充值单为准。", "管道未回傳有效費用規則，請以儲值單為準。", "Fee rules are unavailable. See the final recharge.")
	}
	return tr(locale, "费率：", "費率：", "Rate: ") + html.EscapeString(channel.FeeRate) + "% + " + html.EscapeString(channel.FixedFee+" "+currency) + "\n" + tr(locale, "渠道手续费参考：", "管道手續費參考：", "Indicative channel fee: ") + html.EscapeString(fee+" "+currency)
}

func rechargeReceipt(locale, amount, currency string, result dujiao.WalletRechargeResult) string {
	text := tr(locale, "充值单号：", "儲值單號：", "Recharge: ") + "<code>" + html.EscapeString(result.RechargeNo) + "</code>"
	text += "\n" + tr(locale, "充值到账金额：", "儲值入帳金額：", "Wallet credit: ") + html.EscapeString(amount+" "+currency)
	fee := result.Payment.FeeAmount
	if fee == "" {
		fee = tr(locale, "商店未返回", "商店未回傳", "Not returned by the store")
	} else {
		// Dujiao-Next computes FeeAmount before any provider currency conversion.
		fee += " " + currency
	}
	text += "\n" + tr(locale, "渠道手续费：", "管道手續費：", "Channel fee: ") + html.EscapeString(fee)
	baseCents, baseOK := money.Parse(amount)
	payCents, payOK := money.Parse(result.Payment.Amount)
	feeCents, feeOK := money.Parse(result.Payment.FeeAmount)
	if currency == result.Payment.Currency && baseOK && payOK && feeOK && feeCents > 0 && baseCents == payCents {
		text += tr(locale, "（商家承担，未向您加收）", "（商家承擔，未向您加收）", " (merchant absorbed; no surcharge)")
	}
	label := tr(locale, "应付金额：", "應付金額：", "Amount payable: ")
	if result.Payment.Status == "success" || result.Payment.Status == "succeeded" || result.Payment.Status == "paid" {
		label = tr(locale, "实付金额：", "實付金額：", "Amount paid: ")
	}
	text += "\n<b>" + label + html.EscapeString(result.Payment.Amount+" "+result.Payment.Currency) + "</b>"
	return text
}

func rechargeResultKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "查看余额", "查看餘額", "View balance"), CallbackData: "wallet"}, {Text: tr(locale, "交易流水", "交易明細", "Transactions"), CallbackData: "walletlog:1"}},
		{
			{Text: tr(locale, "🛍 购买商品", "🛍 購買商品", "🛍 Shop"), CallbackData: "cats"},
			{Text: tr(locale, "📦 我的订单", "📦 我的訂單", "📦 Orders"), CallbackData: "orders:a:1"},
		},
	}}
}
