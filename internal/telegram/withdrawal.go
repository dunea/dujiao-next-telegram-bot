package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

var errWithdrawalRules = errors.New("withdrawal rules unavailable")

func validWithdrawalText(value string, maxRunes int) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxRunes && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

func withdrawalAmount(input, minimum, available string) (string, bool) {
	cents, ok := money.Parse(strings.TrimSpace(input))
	minCents, minOK := money.Parse(minimum)
	availableCents, availableOK := money.Parse(available)
	if !ok || !minOK || !availableOK || cents <= 0 || cents < minCents || cents > availableCents {
		return "", false
	}
	return money.Format(cents), true
}

func (a *App) withdrawalRules(ctx context.Context, userID int64) (*session.WithdrawalDraft, error) {
	dashboard, err := a.api.AffiliateDashboard(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		return nil, err
	}
	if !dashboard.Opened {
		return nil, &dujiao.APIError{ErrorCode: "affiliate_not_opened"}
	}
	minimum, minOK := money.Parse(dashboard.MinWithdrawAmount.String())
	available, availableOK := money.Parse(dashboard.AvailableCommission)
	if !minOK || !availableOK || len(dashboard.WithdrawChannels) > 20 {
		return nil, errWithdrawalRules
	}
	channels := make([]string, 0, len(dashboard.WithdrawChannels))
	for _, raw := range dashboard.WithdrawChannels {
		channel := strings.TrimSpace(raw)
		if !validWithdrawalText(channel, 50) {
			return nil, errWithdrawalRules
		}
		channels = append(channels, channel)
	}
	wallet, err := a.api.Wallet(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		return nil, err
	}
	if wallet.Currency == "" {
		return nil, errWithdrawalRules
	}
	return &session.WithdrawalDraft{Minimum: money.Format(minimum), Available: money.Format(available), Currency: wallet.Currency, Channels: channels}, nil
}

func withdrawalChannelAllowed(channels []string, selected string) bool {
	if !validWithdrawalText(selected, 50) {
		return false
	}
	if len(channels) == 0 {
		return true
	} // The Channel API treats an empty list as unrestricted.
	for _, channel := range channels {
		if strings.EqualFold(channel, selected) {
			return true
		}
	}
	return false
}

func withdrawalResultKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "查看提现记录", "查看提領紀錄", "Withdrawal history"), CallbackData: "affwithdraws:1"}},
		affiliateHomeButtons(locale),
	}}
}

func (a *App) startWithdrawal(ctx context.Context, chatID, userID int64) {
	if !a.affiliatePrivateChat(ctx, chatID, userID) {
		return
	}
	state := a.sessions.Get(userID)
	if !a.sessionOK(ctx, chatID, state.Err) {
		return
	}
	if state.WithdrawalGuard != nil {
		if state.WithdrawalGuard.Status == "unknown" {
			a.ops.Record(operations.Event{Action: operations.Withdrawal, Outcome: operations.Unknown, UserID: userID})
		}
		a.send(ctx, chatID, tr(state.Locale, "上一笔提现仍在核对、提交中或结果不明，暂不能再次申请。请先查看提现记录；结果不明时请联系商家核对。", "上一筆提領仍在核對、提交中或結果不明，暫時無法再次申請。請先查看提領紀錄；結果不明時請聯絡商家核對。", "A previous withdrawal is being checked/submitted or has an unknown result. Check withdrawal history and contact the store before applying again."), withdrawalResultKeyboard(state.Locale))
		return
	}
	rules, err := a.withdrawalRules(ctx, userID)
	if err != nil {
		a.withdrawalReadFailure(ctx, chatID, userID, err)
		return
	}
	if _, ok := withdrawalAmount(rules.Available, rules.Minimum, rules.Available); !ok {
		a.send(ctx, chatID, tr(state.Locale, "当前可用佣金不足以提现。待确认佣金、审核中的提现金额和钱包余额不能用于本次申请。", "目前可用佣金不足以提領。待確認佣金、審核中的提領金額與錢包餘額不能用於本次申請。", "Available commission is insufficient. Pending commission, reserved withdrawals and wallet balance cannot fund this request.")+"\n"+tr(state.Locale, "可用佣金：", "可用佣金：", "Available: ")+affiliateMoney(rules.Available, rules.Currency)+"\n"+tr(state.Locale, "最低提现：", "最低提領：", "Minimum: ")+affiliateMoney(rules.Minimum, rules.Currency), withdrawalResultKeyboard(state.Locale))
		return
	}
	rules.ID, rules.ChatID, rules.Step, rules.ExpiresAt = rand.Text(), chatID, "amount", time.Now().Add(10*time.Minute)
	started := false
	result := a.sessions.Update(userID, func(state *session.State) {
		if state.WithdrawalGuard == nil {
			state.Flow, state.Withdrawal = "withdrawal", rules
			state.Purchase, state.GiftCard, state.Recharge = nil, nil, nil
			started = true
		}
	})
	if a.sessionOK(ctx, chatID, result.Err) && started {
		a.promptWithdrawal(ctx, userID, rules)
	}
}

func (a *App) withdrawalReadFailure(ctx context.Context, chatID, userID int64, err error) {
	locale := a.sessions.Get(userID).Locale
	if errors.Is(err, errWithdrawalRules) {
		a.send(ctx, chatID, tr(locale, "商店未返回有效的提现金额、币种或渠道配置，请联系商家。未提交提现申请。", "商店未回傳有效的提領金額、幣別或管道設定，請聯絡商家。未送出提領申請。", "The store returned incomplete withdrawal rules, currency or channels. Contact the store. No request was submitted."), withdrawalResultKeyboard(locale))
	} else {
		a.affiliateFailure(ctx, chatID, userID, err)
	}
}

func (a *App) promptWithdrawal(ctx context.Context, userID int64, draft *session.WithdrawalDraft) {
	locale := a.sessions.Get(userID).Locale
	text := "<b>" + tr(locale, "申请提现", "申請提領", "Request withdrawal") + "</b>\n\n"
	var markup models.ReplyMarkup = models.ForceReply{ForceReply: true, Selective: true}
	switch draft.Step {
	case "amount":
		text += tr(locale, "可用佣金：", "可用佣金：", "Available commission: ") + affiliateMoney(draft.Available, draft.Currency) + "\n" + tr(locale, "最低提现：", "最低提領：", "Minimum: ") + affiliateMoney(draft.Minimum, draft.Currency) + "\n\n"
		text += tr(locale, "请回复本条消息输入提现金额，大于 0、最多两位小数，不得低于门槛或超过可用佣金。发送 /back 返回推广中心，/cancel 取消。", "請回覆本則訊息輸入提領金額，大於 0、最多兩位小數，不得低於門檻或超過可用佣金。傳送 /back 返回推廣中心，/cancel 取消。", "Reply with an amount greater than zero, with up to two decimal places, between the minimum and your available commission. Send /back for the affiliate center or /cancel to cancel.")
	case "channel":
		text += tr(locale, "请选择商店允许的提现渠道：", "請選擇商店允許的提領管道：", "Choose a store-supported withdrawal channel:")
		var choices []models.InlineKeyboardButton
		for index, channel := range draft.Channels {
			choices = append(choices, models.InlineKeyboardButton{Text: channel, CallbackData: fmt.Sprintf("wd:%s:channel:%d", draft.ID, index)})
		}
		rows := compactActionRows(choices)
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 上一步", "‹ 上一步", "‹ Back"), CallbackData: "wd:" + draft.ID + ":back"}, {Text: tr(locale, "取消", "取消", "Cancel"), CallbackData: "wd:" + draft.ID + ":cancel"}})
		markup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	case "custom_channel":
		text += tr(locale, "商店未限制提现渠道名称，请按商家说明回复本条消息输入渠道（最多 50 个字符）。发送 /back 返回金额输入，/cancel 取消。", "商店未限制提領管道名稱，請依商家說明回覆本則訊息輸入管道（最多 50 個字元）。傳送 /back 返回金額輸入，/cancel 取消。", "The store does not restrict channel names. Reply with a channel following the merchant's instructions (up to 50 characters). Send /back to edit the amount or /cancel to cancel.")
	case "account":
		text += tr(locale, "提现金额：", "提領金額：", "Amount: ") + affiliateMoney(draft.Amount, draft.Currency) + "\n" + tr(locale, "渠道：", "管道：", "Channel: ") + html.EscapeString(draft.Channel) + "\n\n"
		text += tr(locale, "请回复本条消息填写收款账号或地址，并按商家要求附上收款人、网络等必要说明（单行，1–200 个字符）。不要发送密码、验证码、私钥或助记词。下一步会展示完整信息供核对。发送 /back 更换渠道，/cancel 取消。", "請回覆本則訊息填寫收款帳號或地址，並依商家要求附上收款人、網路等必要說明（單行，1–200 個字元）。不要傳送密碼、驗證碼、私鑰或助記詞。下一步會顯示完整資訊供核對。傳送 /back 更換管道，/cancel 取消。", "Reply with the receiving account/address and any recipient or network details required by the merchant (one line, 1–200 characters). Never send passwords, verification codes, private keys or seed phrases. Full details will be shown next for review. Send /back to change channel or /cancel to cancel.")
	case "confirm":
		text += tr(locale, "请核对后再确认提交：", "請核對後再確認送出：", "Review before confirming:") + "\n\n" + tr(locale, "提现金额：", "提領金額：", "Amount: ") + affiliateMoney(draft.Amount, draft.Currency) + "\n" + tr(locale, "渠道：", "管道：", "Channel: ") + html.EscapeString(draft.Channel) + "\n" + tr(locale, "收款信息：", "收款資訊：", "Receiving details: ") + "<code>" + html.EscapeString(draft.Account) + "</code>\n\n"
		text += tr(locale, "提交后将交由商店审核，不代表已打款。账号、地址和网络正确性需自行确认；本地只做通用格式校验。", "送出後將交由商店審核，不代表已匯款。帳號、地址與網路正確性需自行確認；本機僅做通用格式校驗。", "Submission requests merchant review; it does not mean funds have been paid. Verify the account, address and network. Only general format checks are performed locally.")
		markup = models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: tr(locale, "确认提交提现申请", "確認送出提領申請", "Confirm withdrawal request"), CallbackData: "wd:" + draft.ID + ":confirm"}},
			{{Text: tr(locale, "修改收款信息", "修改收款資訊", "Edit account"), CallbackData: "wd:" + draft.ID + ":back"}, {Text: tr(locale, "重新填写", "重新填寫", "Start over"), CallbackData: "wd:" + draft.ID + ":restart"}},
			{{Text: tr(locale, "取消填写", "取消填寫", "Cancel input"), CallbackData: "wd:" + draft.ID + ":cancel"}, {Text: tr(locale, "退出并查看记录", "離開並查看紀錄", "Exit to history"), CallbackData: "affwithdraws:1"}},
		}}
	default:
		return
	}
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: draft.ChatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil {
		a.logger.Warn("send withdrawal prompt failed", "user_id", userID)
		a.clearWithdrawal(userID, draft.ID, false)
		return
	}
	stored := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Withdrawal; current != nil && current.ID == draft.ID && !current.Busy {
			current.MessageID = message.ID
		}
	})
	a.sessionOK(ctx, draft.ChatID, stored.Err)
}

func (a *App) advanceWithdrawal(userID int64, draft *session.WithdrawalDraft, change func(*session.WithdrawalDraft)) *session.WithdrawalDraft {
	changed := false
	state := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Withdrawal; state.Flow == "withdrawal" && current != nil && current.ID == draft.ID && current.MessageID == draft.MessageID && !current.Busy && state.WithdrawalGuard == nil && time.Now().Before(current.ExpiresAt) {
			change(current)
			current.ID, current.MessageID = rand.Text(), 0
			changed = true
		}
	})
	if state.Err != nil || !changed {
		return nil
	}
	return state.Withdrawal
}

func (a *App) handleWithdrawalMessage(ctx context.Context, message *models.Message) {
	userID := message.From.ID
	state := a.sessions.Get(userID)
	draft := state.Withdrawal
	if draft == nil || draft.ChatID != message.Chat.ID || message.Chat.ID != userID || draft.Busy {
		return
	}
	command, _ := parseCommand(message.Text)
	if command == "/back" {
		a.withdrawalBack(ctx, userID, draft)
		return
	}
	if draft.MessageID == 0 || message.ReplyToMessage == nil || message.ReplyToMessage.ID != draft.MessageID {
		a.send(ctx, message.Chat.ID, tr(state.Locale, "请回复当前的提现填写提示。", "請回覆目前的提領填寫提示。", "Reply to the current withdrawal prompt."), nil)
		return
	}
	input := strings.TrimSpace(message.Text)
	var next *session.WithdrawalDraft
	switch draft.Step {
	case "amount":
		amount, ok := withdrawalAmount(input, draft.Minimum, draft.Available)
		if !ok {
			a.send(ctx, message.Chat.ID, tr(state.Locale, "金额无效：请使用大于 0、最多两位小数的普通数字，且不低于最低提现金额、不超过可用佣金。", "金額無效：請使用大於 0、最多兩位小數的一般數字，且不低於最低提領金額、不超過可用佣金。", "Invalid amount. Use a positive decimal with up to two places, not below the minimum or above available commission."), nil)
			return
		}
		next = a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) {
			current.Amount = amount
			current.Step = "channel"
			if len(current.Channels) == 0 {
				current.Step = "custom_channel"
			}
		})
	case "custom_channel":
		if !validWithdrawalText(input, 50) {
			a.send(ctx, message.Chat.ID, tr(state.Locale, "渠道名称应为 1–50 个可见字符的单行文字。", "管道名稱應為 1–50 個可見字元的單行文字。", "Use a single-line channel name of 1–50 visible characters."), nil)
			return
		}
		next = a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) { current.Channel = input; current.Step = "account" })
	case "account":
		if !validWithdrawalText(input, 200) {
			a.send(ctx, message.Chat.ID, tr(state.Locale, "收款信息应为 1–200 个可见字符的单行文字，不得包含换行或隐藏控制字符。", "收款資訊應為 1–200 個可見字元的單行文字，不得包含換行或隱藏控制字元。", "Receiving details must be a single line of 1–200 visible characters without hidden controls."), nil)
			return
		}
		next = a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) { current.Account = input; current.Step = "confirm" })
	default:
		a.send(ctx, message.Chat.ID, tr(state.Locale, "请使用当前消息下方按钮选择渠道或确认。", "請使用目前訊息下方按鈕選擇管道或確認。", "Use the buttons on the current message to select a channel or confirm."), nil)
	}
	if next != nil {
		a.promptWithdrawal(ctx, userID, next)
	}
}

func (a *App) withdrawalBack(ctx context.Context, userID int64, draft *session.WithdrawalDraft) {
	if draft.Step == "amount" {
		if a.cancelWithdrawalInput(userID, draft.ID) {
			a.showAffiliate(ctx, draft.ChatID, userID, false, 0)
		}
		return
	}
	next := a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) {
		switch current.Step {
		case "channel", "custom_channel":
			current.Step = "amount"
			current.Channel, current.Account = "", ""
		case "account":
			current.Step = "channel"
			if len(current.Channels) == 0 {
				current.Step = "custom_channel"
			}
			current.Account = ""
		case "confirm":
			current.Step = "account"
			current.Account = ""
		}
	})
	if next != nil {
		a.promptWithdrawal(ctx, userID, next)
	}
}

func (a *App) handleWithdrawalCallback(ctx context.Context, query *models.CallbackQuery, parts []string) {
	message, userID := query.Message.Message, query.From.ID
	if message.Chat.ID != userID {
		return
	}
	state := a.sessions.Get(userID)
	if !a.sessionOK(ctx, message.Chat.ID, state.Err) {
		return
	}
	if parts[0] == "wdack" {
		if len(parts) != 2 {
			return
		}
		guard := state.WithdrawalGuard
		if guard == nil || guard.Status != "unknown" || guard.ID != parts[1] || guard.ReviewMessageID == 0 || guard.ReviewMessageID != message.ID {
			return
		}
		if !a.allowWrite(ctx, message.Chat.ID, userID, operations.WithdrawalUnlock) {
			return
		}
		cleared := false
		result := a.sessions.Update(userID, func(state *session.State) {
			if guard := state.WithdrawalGuard; guard != nil && guard.Status == "unknown" && guard.ID == parts[1] && guard.ReviewMessageID != 0 && guard.ReviewMessageID == message.ID {
				state.WithdrawalGuard = nil
				cleared = true
			}
		})
		if a.sessionOK(ctx, message.Chat.ID, result.Err) && cleared {
			a.ops.Record(operations.Event{Action: operations.WithdrawalUnlock, Outcome: operations.Succeeded, UserID: userID})
			a.edit(ctx, message.Chat.ID, message.ID, tr(state.Locale, "已解除本地重试保护。请确认上一笔结果后再发起新的申请；本操作不会撤回或删除商店中的申请。", "已解除本機重試保護。請確認上一筆結果後再發起新申請；本操作不會撤回或刪除商店中的申請。", "Local retry protection cleared. Verify the previous result before starting a new request. This does not cancel or delete a store-side request."), withdrawalResultKeyboard(state.Locale))
		}
		return
	}
	if len(parts) < 3 {
		return
	}
	draft := state.Withdrawal
	if draft == nil || draft.ID != parts[1] || draft.ChatID != message.Chat.ID || draft.MessageID == 0 || draft.MessageID != message.ID || draft.Busy {
		a.send(ctx, message.Chat.ID, tr(state.Locale, "这组提现按钮已失效或已提交，请查看提现记录或重新打开推广中心。", "這組提領按鈕已失效或已送出，請查看提領紀錄或重新開啟推廣中心。", "These withdrawal buttons expired or were submitted. Check withdrawal history or reopen the affiliate center."), withdrawalResultKeyboard(state.Locale))
		return
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "cancel":
			if a.cancelWithdrawalInput(userID, draft.ID) {
				a.edit(ctx, message.Chat.ID, message.ID, tr(state.Locale, "已取消提现填写，未提交申请。", "已取消提領填寫，未送出申請。", "Withdrawal input cancelled. No request was submitted."), withdrawalResultKeyboard(state.Locale))
			}
		case "back":
			a.withdrawalBack(ctx, userID, draft)
		case "restart":
			if next := a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) {
				current.Step = "amount"
				current.Amount, current.Channel, current.Account = "", "", ""
			}); next != nil {
				a.promptWithdrawal(ctx, userID, next)
			}
		case "confirm":
			if draft.Step == "confirm" {
				a.submitWithdrawal(ctx, userID, draft)
			}
		}
		return
	}
	if len(parts) != 4 || parts[2] != "channel" || draft.Step != "channel" {
		return
	}
	index, err := strconv.Atoi(parts[3])
	if err != nil || index < 0 || index >= len(draft.Channels) {
		return
	}
	next := a.advanceWithdrawal(userID, draft, func(current *session.WithdrawalDraft) {
		current.Channel = current.Channels[index]
		current.Step = "account"
	})
	if next != nil {
		a.promptWithdrawal(ctx, userID, next)
	}
}

func (a *App) clearWithdrawal(userID int64, id string, clearGuard bool) error {
	stored := a.sessions.Update(userID, func(state *session.State) {
		if state.Withdrawal != nil && state.Withdrawal.ID == id {
			state.Withdrawal = nil
			if state.Flow == "withdrawal" {
				state.Flow = ""
			}
		}
		if clearGuard && state.WithdrawalGuard != nil && state.WithdrawalGuard.ID == id {
			state.WithdrawalGuard = nil
		}
	})
	if stored.Err != nil {
		a.logger.Warn("withdrawal cleanup not committed; retry protection retained", "user_id", userID)
	}
	return stored.Err
}

func (a *App) cancelWithdrawalInput(userID int64, id string) bool {
	cancelled := false
	result := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Withdrawal; current != nil && current.ID == id && !current.Busy && state.WithdrawalGuard == nil {
			state.Withdrawal = nil
			if state.Flow == "withdrawal" {
				state.Flow = ""
			}
			cancelled = true
		}
	})
	return result.Err == nil && cancelled
}

func (a *App) submitWithdrawal(ctx context.Context, userID int64, draft *session.WithdrawalDraft) {
	state := a.sessions.Get(userID)
	if !a.sessionOK(ctx, draft.ChatID, state.Err) || state.Withdrawal == nil || state.Withdrawal.ID != draft.ID || state.Withdrawal.Busy || state.WithdrawalGuard != nil {
		return
	}
	if !a.allowWrite(ctx, draft.ChatID, userID, operations.Withdrawal) {
		return
	}
	// The lease outlives the entire request deadline. Expiry only enables manual
	// reconciliation, never resubmission of this draft.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	claimed := false
	claim := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Withdrawal; state.Flow == "withdrawal" && current != nil && current.ID == draft.ID && current.MessageID == draft.MessageID && !current.Busy && state.WithdrawalGuard == nil && time.Now().Before(current.ExpiresAt) {
			current.Busy = true
			state.WithdrawalGuard = &session.WithdrawalGuard{ID: draft.ID, Status: "checking", LeaseUntil: time.Now().Add(3 * time.Minute)}
			claimed = true
		}
	})
	if !a.sessionOK(ctx, draft.ChatID, claim.Err) || !claimed {
		return
	}
	locale := claim.Locale
	rules, err := a.withdrawalRules(ctx, userID)
	if err != nil {
		a.clearWithdrawal(userID, draft.ID, true)
		a.withdrawalReadFailure(ctx, draft.ChatID, userID, err)
		return
	}
	_, amountOK := withdrawalAmount(draft.Amount, rules.Minimum, rules.Available)
	if !amountOK || !withdrawalChannelAllowed(rules.Channels, draft.Channel) || rules.Currency != draft.Currency {
		a.clearWithdrawal(userID, draft.ID, true)
		a.edit(ctx, draft.ChatID, draft.MessageID, tr(locale, "商店金额门槛、可用佣金、币种或渠道已变化，本次未提交。请刷新推广中心后重新申请。", "商店金額門檻、可用佣金、幣別或管道已變更，本次未送出。請重新整理推廣中心後重新申請。", "The store's minimum, available commission, currency or channel changed. No request was submitted. Refresh the affiliate center and start again."), withdrawalResultKeyboard(locale))
		return
	}
	posting := false
	posted := a.sessions.Update(userID, func(state *session.State) {
		if current := state.Withdrawal; state.Flow == "withdrawal" && current != nil && current.ID == draft.ID && current.Busy && time.Now().Before(current.ExpiresAt) && state.WithdrawalGuard != nil && state.WithdrawalGuard.ID == draft.ID && state.WithdrawalGuard.Status == "checking" && time.Now().Before(state.WithdrawalGuard.LeaseUntil) {
			state.Withdrawal, state.Flow = nil, "" // Erase the account from session before the write.
			state.WithdrawalGuard.Status = "posting"
			posting = true
		}
	})
	// A lost acknowledgement can mean the posting marker DID persist. Never
	// erase it here, and never issue the financial write without an acknowledgement.
	if !a.sessionOK(ctx, draft.ChatID, posted.Err) {
		return
	}
	if !posting {
		a.clearWithdrawal(userID, draft.ID, true)
		return
	}
	a.edit(ctx, draft.ChatID, draft.MessageID, tr(locale, "提现申请正在提交，请勿重复操作。", "提領申請正在送出，請勿重複操作。", "Submitting the withdrawal request. Do not repeat this action."), nil)
	op := a.ops.Begin(operations.Withdrawal, userID, 0)
	result, err := a.api.ApplyAffiliateWithdrawal(ctx, dujiao.AffiliateWithdrawalRequest{ChannelUserID: strconv.FormatInt(userID, 10), Amount: draft.Amount, Channel: draft.Channel, Account: draft.Account})
	if err != nil {
		a.logAffiliateFailure(userID, "withdrawal submission failed", err)
		text, definitive := withdrawalErrorText(locale, err)
		outcome := operations.Unknown
		if definitive {
			outcome = operations.Rejected
		}
		op.End(outcome, 0)
		if definitive {
			a.clearWithdrawal(userID, draft.ID, true)
		} else {
			stored := a.sessions.Update(userID, func(state *session.State) {
				if guard := state.WithdrawalGuard; guard != nil && guard.ID == draft.ID {
					guard.Status = "unknown"
					guard.ReviewMessageID = 0
				}
			})
			if stored.Err != nil {
				a.logger.Warn("unknown withdrawal marker not acknowledged; posting protection retained", "user_id", userID)
			}
		}
		a.edit(ctx, draft.ChatID, draft.MessageID, text, withdrawalResultKeyboard(locale))
		return
	}
	op.End(operations.Succeeded, result.ID)
	cleanupErr := a.clearWithdrawal(userID, draft.ID, true)
	text := "✅ " + tr(locale, "提现申请已记录", "提領申請已記錄", "Withdrawal request recorded") + fmt.Sprintf("\n\n#%d\n", result.ID) + affiliateMoney(result.Amount, draft.Currency) + "\n" + html.EscapeString(result.Channel) + " · <code>" + html.EscapeString(maskAffiliateAccount(draft.Account)) + "</code>\n" + tr(locale, "状态：", "狀態：", "Status: ") + affiliateStatus(locale, result.Status)
	text += "\n\n" + tr(locale, "提交申请不等于已到账，请在提现记录中查看商店审核及打款结果。", "送出申請不等於已入帳，請在提領紀錄中查看商店審核與匯款結果。", "Submission does not mean funds arrived. Check history for the merchant's review and payment result.")
	if cleanupErr != nil {
		text += "\n\n" + tr(locale, "申请已记录，但本地保护清理未获确认。请勿重新提交；稍后查看提现记录核对，必要时联系商家。", "申請已記錄，但本機保護清理未獲確認。請勿重新送出；稍後查看提領紀錄核對，必要時聯絡商家。", "The request was recorded, but clearing retry protection was not acknowledged. Do not resubmit; check history and contact the store if needed.")
	}
	a.edit(ctx, draft.ChatID, draft.MessageID, text, withdrawalResultKeyboard(locale))
}

func withdrawalErrorText(locale string, err error) (string, bool) {
	var apiError *dujiao.APIError
	if errors.As(err, &apiError) && apiError.HTTPStatus >= 400 && apiError.HTTPStatus < 500 {
		switch apiError.ErrorCode {
		case "affiliate_withdraw_insufficient":
			return tr(locale, "可用佣金不足，商店拒绝了本次申请。请刷新推广中心后重新填写。", "可用佣金不足，商店拒絕了本次申請。請重新整理推廣中心後重新填寫。", "The store rejected this request due to insufficient available commission. Refresh before applying again."), true
		case "affiliate_withdraw_amount_invalid":
			return tr(locale, "提现金额不符合商店最新门槛，申请未获接受，请刷新后重新填写。", "提領金額不符合商店最新門檻，申請未獲接受，請重新整理後重新填寫。", "The amount does not meet the store's current withdrawal rules. The request was not accepted; refresh and start again."), true
		case "affiliate_withdraw_channel_invalid", "validation_error":
			return tr(locale, "提现渠道或收款信息未通过商店校验，请确认商家要求后重新填写。", "提領管道或收款資訊未通過商店校驗，請確認商家要求後重新填寫。", "The channel or receiving details failed store validation. Check merchant requirements before applying again."), true
		case "affiliate_disabled", "affiliate_not_opened", "user_disabled", "invite_code_required", "invite_code_invalid", "user_not_found":
			return tr(locale, "商店未接受本次提现申请，推广功能或账号状态不可用，请联系商家。", "商店未接受本次提領申請，推廣功能或帳號狀態無法使用，請聯絡商家。", "The store did not accept this withdrawal because the affiliate program or account is unavailable. Contact the store."), true
		}
	}
	return tr(locale, "暂时无法确认提现结果，请勿重复提交。请查看提现记录并联系商家核对；自动重试已禁用，新申请暂被保护锁定。", "暫時無法確認提領結果，請勿重複送出。請查看提領紀錄並聯絡商家核對；自動重試已停用，新申請暫被保護鎖定。", "The withdrawal result is unknown. Do not submit again. Check history and contact the store. Automatic retries are disabled and new requests are temporarily locked."), false
}
