package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/manualform"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

const formLifetime = 30 * time.Minute

func (a *App) startManualForm(ctx context.Context, chatID int64, messageID int, user *models.User, product dujiao.ProductDetail, skuID uint, quantity int) {
	locale := a.sessions.Get(user.ID).Locale
	if chatID != user.ID {
		a.send(ctx, chatID, tr(locale, "请在机器人私聊中填写购买资料。", "請在機器人私訊中填寫購買資料。", "Please fill in purchase details in a private chat with the bot."), nil)
		return
	}
	if err := manualform.CheckSchema(product.ManualFormSchema); err != nil {
		a.edit(ctx, chatID, messageID, tr(locale, "商店表单配置暂不受支持，请联系商家。", "商店表單設定暫不受支援，請聯絡商家。", "This form configuration is not supported. Please contact the store."), a.homeKeyboard(locale))
		return
	}
	var randomID [8]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		a.apiFailure(ctx, chatID, locale, "create form session", err)
		return
	}
	draft := &session.PurchaseDraft{
		ID: hex.EncodeToString(randomID[:]), ChatID: chatID, ProductID: product.ID, SKUID: skuID,
		Quantity: quantity, Schema: product.ManualFormSchema, Values: make(map[string]any),
		ExpiresAt: time.Now().Add(formLifetime),
	}
	a.sessions.Update(user.ID, func(state *session.State) { state.Flow = "manual_form"; state.Purchase = draft })
	a.edit(ctx, chatID, messageID, tr(locale, "请按提示逐项填写购买资料。可发送 /back 返回上一项，/cancel 取消。", "請依提示逐項填寫購買資料。可傳送 /back 返回上一項，/cancel 取消。", "Fill in each purchase detail below. Use /back for the previous field or /cancel to cancel."), nil)
	a.promptForm(ctx, user, 0)
}

func (a *App) formDraft(userID, chatID int64) *session.PurchaseDraft {
	draft := a.sessions.Get(userID).Purchase
	if draft == nil || draft.ChatID != chatID || !time.Now().Before(draft.ExpiresAt) || draft.Submitting {
		return nil
	}
	return draft
}

func (a *App) expiredForm(ctx context.Context, chatID int64, locale string) {
	a.send(ctx, chatID, tr(locale, "这份表单已失效，请重新打开商品填写。", "這份表單已失效，請重新開啟商品填寫。", "This form is no longer active. Reopen the product to fill it in."), a.homeKeyboard(locale))
}

func (a *App) promptForm(ctx context.Context, user *models.User, editID int) {
	draft := a.formDraft(user.ID, user.ID)
	if draft == nil {
		return
	}
	if draft.Step >= len(draft.Schema.Fields) {
		a.previewForm(ctx, user, draft)
		return
	}
	locale := a.sessions.Get(user.ID).Locale
	field := draft.Schema.Fields[draft.Step]
	label := field.Label
	if label == "" {
		label = field.Key
	}
	text := fmt.Sprintf("<b>%s %d/%d</b>\n\n%s", tr(locale, "购买资料", "購買資料", "Purchase details"), draft.Step+1, len(draft.Schema.Fields), html.EscapeString(truncate(label, 200)))
	if field.Required {
		text += " · " + tr(locale, "必填", "必填", "Required")
	}
	if field.Placeholder != "" {
		text += "\n" + html.EscapeString(truncate(field.Placeholder, 300))
	}
	if field.Type == "number" {
		if field.Min != nil {
			text += fmt.Sprintf("\n%s%g", tr(locale, "最小值：", "最小值：", "Minimum: "), *field.Min)
		}
		if field.Max != nil {
			text += fmt.Sprintf("\n%s%g", tr(locale, "最大值：", "最大值：", "Maximum: "), *field.Max)
		}
	}
	if field.MaxLen != nil {
		text += fmt.Sprintf("\n%s%d", tr(locale, "最多字符数：", "最多字元數：", "Maximum characters: "), *field.MaxLen)
	}
	prefix := fmt.Sprintf("form:%s:%d:", draft.ID, draft.Revision)
	var markup models.ReplyMarkup
	if field.Type == "select" || field.Type == "radio" || field.Type == "checkbox" {
		rows := make([][]models.InlineKeyboardButton, 0, len(field.Options)+3)
		chosen, _ := draft.Values[field.Key].([]string)
		for index, option := range field.Options {
			label := option
			if slices.Contains(chosen, option) {
				label = "✓ " + label
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 60), CallbackData: prefix + "option:" + strconv.Itoa(index)}})
		}
		if field.Type == "checkbox" {
			text += "\n" + tr(locale, "可选择多项，选好后点击「完成选择」。", "可選擇多項，選好後點擊「完成選擇」。", "Choose one or more options, then tap Done.")
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "完成选择", "完成選擇", "Done"), CallbackData: prefix + "done"}})
		}
		if !field.Required {
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "跳过", "略過", "Skip"), CallbackData: prefix + "skip"}})
		}
		if draft.Step > 0 {
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 上一项", "‹ 上一項", "‹ Previous"), CallbackData: prefix + "back"}})
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "取消", "取消", "Cancel"), CallbackData: prefix + "cancel"}})
		markup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	} else {
		text += "\n\n" + tr(locale, "请回复这条消息填写内容。", "請回覆這則訊息填寫內容。", "Reply to this message with your answer.")
		if !field.Required {
			text += "\n" + tr(locale, "可发送 /skip 跳过。", "可傳送 /skip 略過。", "Send /skip to skip.")
		}
		markup = models.ForceReply{ForceReply: true, Selective: true}
	}
	if editID > 0 {
		a.edit(ctx, draft.ChatID, editID, text, markup)
		return
	}
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: draft.ChatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil {
		a.logger.Error("send form prompt", "error", err)
		return
	}
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && !current.Submitting {
			current.PromptID = message.ID
		}
	})
}

func (a *App) handleFormMessage(ctx context.Context, message *models.Message) {
	draft := a.formDraft(message.From.ID, message.Chat.ID)
	locale := a.sessions.Get(message.From.ID).Locale
	if draft == nil {
		a.expiredForm(ctx, message.Chat.ID, locale)
		return
	}
	if draft.Step >= len(draft.Schema.Fields) {
		return
	}
	command, _ := parseCommand(message.Text)
	if command == "/back" {
		a.moveFormBack(ctx, message.From, draft)
		return
	}
	if command == "/skip" {
		a.acceptFormValue(ctx, message.From, draft, nil)
		return
	}
	if message.ReplyToMessage == nil || message.ReplyToMessage.ID != draft.PromptID {
		a.send(ctx, message.Chat.ID, tr(locale, "请使用「回复」填写当前表单问题，或发送 /cancel 取消。", "請使用「回覆」填寫目前的表單問題，或傳送 /cancel 取消。", "Reply to the current form question, or send /cancel to cancel."), nil)
		return
	}
	field := draft.Schema.Fields[draft.Step]
	if field.Type == "checkbox" || field.Type == "select" || field.Type == "radio" {
		a.send(ctx, message.Chat.ID, tr(locale, "请点击当前问题下方的选项。", "請點擊目前問題下方的選項。", "Tap an option under the current question."), nil)
		return
	}
	a.acceptFormValue(ctx, message.From, draft, message.Text)
}

func (a *App) acceptFormValue(ctx context.Context, user *models.User, draft *session.PurchaseDraft, input any) {
	field := draft.Schema.Fields[draft.Step]
	value, err := manualform.Value(field, input)
	if err != nil {
		locale := a.sessions.Get(user.ID).Locale
		a.send(ctx, draft.ChatID, tr(locale, "填写内容不符合要求，请检查必填、格式、长度或范围后重试。", "填寫內容不符合要求，請檢查必填、格式、長度或範圍後重試。", "Check the required value, format, length, or range and try again."), nil)
		a.promptForm(ctx, user, 0)
		return
	}
	accepted := false
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && !current.Submitting && time.Now().Before(current.ExpiresAt) {
			if value == nil {
				delete(current.Values, field.Key)
			} else {
				current.Values[field.Key] = value
			}
			current.Step++
			current.Revision++
			current.PromptID = 0
			accepted = true
		}
	})
	if accepted {
		a.promptForm(ctx, user, 0)
	}
}

func (a *App) moveFormBack(ctx context.Context, user *models.User, draft *session.PurchaseDraft) {
	changed := false
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && !current.Submitting {
			if current.Step > 0 {
				current.Step--
			}
			current.Revision++
			current.PromptID = 0
			changed = true
		}
	})
	if changed {
		a.promptForm(ctx, user, 0)
	}
}

func (a *App) handleFormCallback(ctx context.Context, query *models.CallbackQuery, parts []string) {
	message := query.Message.Message
	locale := a.sessions.Get(query.From.ID).Locale
	if len(parts) < 3 {
		return
	}
	revision, err := strconv.Atoi(parts[2])
	if err != nil {
		return
	}
	draft := a.formDraft(query.From.ID, message.Chat.ID)
	if draft == nil || draft.ID != parts[1] || draft.Revision != revision {
		a.expiredForm(ctx, message.Chat.ID, locale)
		return
	}
	if parts[0] == "formconfirm" || parts[0] == "formedit" {
		if draft.PreviewID != message.ID || (parts[0] == "formconfirm" && !draft.Ready) {
			a.expiredForm(ctx, message.Chat.ID, locale)
			return
		}
		if parts[0] == "formconfirm" {
			a.confirmForm(ctx, &query.From, draft)
			return
		}
		a.sessions.Update(query.From.ID, func(state *session.State) {
			if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == revision && !current.Submitting {
				current.Step = 0
				current.Revision++
				current.Ready = false
				current.PreviewID = 0
				current.PromptID = 0
				state.Flow = "manual_form"
			}
		})
		a.promptForm(ctx, &query.From, 0)
		return
	}
	if len(parts) < 4 || draft.Step >= len(draft.Schema.Fields) || draft.PromptID != message.ID {
		return
	}
	field := draft.Schema.Fields[draft.Step]
	switch parts[3] {
	case "cancel":
		a.sessions.Update(query.From.ID, func(state *session.State) {
			if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == revision && !current.Submitting {
				state.Purchase = nil
				state.Flow = ""
			}
		})
		a.edit(ctx, message.Chat.ID, message.ID, tr(locale, "已取消填写资料。", "已取消填寫資料。", "Form cancelled."), a.homeKeyboard(locale))
	case "back":
		a.moveFormBack(ctx, &query.From, draft)
	case "skip":
		a.acceptFormValue(ctx, &query.From, draft, nil)
	case "done":
		if field.Type == "checkbox" {
			a.acceptFormValue(ctx, &query.From, draft, draft.Values[field.Key])
		}
	case "option":
		if len(parts) != 5 {
			return
		}
		index, err := strconv.Atoi(parts[4])
		if err != nil || index < 0 || index >= len(field.Options) {
			return
		}
		if field.Type == "checkbox" {
			a.sessions.Update(query.From.ID, func(state *session.State) {
				if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == revision && !current.Submitting {
					values, _ := current.Values[field.Key].([]string)
					if selected := slices.Index(values, field.Options[index]); selected >= 0 {
						values = slices.Delete(values, selected, selected+1)
					} else {
						values = append(values, field.Options[index])
					}
					current.Values[field.Key] = values
				}
			})
			a.promptForm(ctx, &query.From, message.ID)
		} else if field.Type == "select" || field.Type == "radio" {
			a.acceptFormValue(ctx, &query.From, draft, field.Options[index])
		}
	}
}

func (a *App) previewForm(ctx context.Context, user *models.User, draft *session.PurchaseDraft) {
	locale := a.sessions.Get(user.ID).Locale
	values, err := manualform.Normalize(draft.Schema, draft.Values)
	if err != nil {
		a.expiredForm(ctx, draft.ChatID, locale)
		return
	}
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: draft.ChatID, Text: tr(locale, "正在预览订单…", "正在預覽訂單…", "Preparing order preview…")})
	if err != nil {
		a.logger.Error("send form preview", "error", err)
		return
	}
	if !a.validatePurchaseQuantity(ctx, draft.ChatID, message.ID, user.ID, locale, draft.ProductID, draft.SKUID, draft.Quantity) {
		return
	}
	request := dujiao.OrderRequest{
		TelegramIdentity: identityFromUser(user, locale),
		Items:            []dujiao.OrderItemRequest{{ProductID: draft.ProductID, SKUID: draft.SKUID, Quantity: draft.Quantity}},
		ManualFormData:   map[string]any{manualform.ItemKey(draft.ProductID, draft.SKUID): values},
	}
	callback := fmt.Sprintf("formconfirm:%s:%d", draft.ID, draft.Revision)
	ready := a.renderOrderPreview(ctx, draft.ChatID, message.ID, locale, request, callback, formSummary(locale, draft.Schema, values))
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && !current.Submitting {
			current.Ready = ready
			current.PreviewID = message.ID
			state.Flow = ""
		}
	})
	if !ready {
		a.edit(ctx, draft.ChatID, message.ID, tr(locale, "订单预览失败，资料已保留，请修改资料后重试或返回商品。", "訂單預覽失敗，資料已保留，請修改資料後重試或返回商品。", "Order preview failed. Your details are saved; edit them to retry or return to the product."), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: tr(locale, "修改资料", "修改資料", "Edit details"), CallbackData: fmt.Sprintf("formedit:%s:%d", draft.ID, draft.Revision)}},
			{{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Back to product"), CallbackData: fmt.Sprintf("product:%d", draft.ProductID)}},
		}})
	}
}

func (a *App) confirmForm(ctx context.Context, user *models.User, draft *session.PurchaseDraft) {
	claimed := false
	a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID && current.Revision == draft.Revision && current.Ready && !current.Submitting && time.Now().Before(current.ExpiresAt) {
			current.Submitting = true
			current.Ready = false
			claimed = true
		}
	})
	if !claimed {
		return
	}
	defer a.sessions.Update(user.ID, func(state *session.State) {
		if current := state.Purchase; current != nil && current.ID == draft.ID {
			state.Purchase = nil
			state.Flow = ""
		}
	})
	locale := a.sessions.Get(user.ID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(user.ID, 10), draft.ProductID)
	if err != nil {
		a.apiFailure(ctx, draft.ChatID, locale, "revalidate form product", err)
		return
	}
	if !reflect.DeepEqual(product.ManualFormSchema, draft.Schema) {
		a.edit(ctx, draft.ChatID, draft.PreviewID, tr(locale, "商店表单已更新，请返回商品重新填写。", "商店表單已更新，請返回商品重新填寫。", "The store form has changed. Reopen the product and fill it in again."), a.homeKeyboard(locale))
		return
	}
	values, err := manualform.Normalize(product.ManualFormSchema, draft.Values)
	if err != nil || !a.validatePurchaseQuantity(ctx, draft.ChatID, draft.PreviewID, user.ID, locale, draft.ProductID, draft.SKUID, draft.Quantity) {
		return
	}
	a.createOrderWithData(ctx, draft.ChatID, draft.PreviewID, user, draft.ProductID, draft.SKUID, draft.Quantity, map[string]any{manualform.ItemKey(draft.ProductID, draft.SKUID): values})
}

func formSummary(locale string, schema dujiao.ManualFormSchema, values map[string]any) string {
	text := "\n\n<b>" + tr(locale, "购买资料", "購買資料", "Purchase details") + "</b>"
	for _, field := range schema.Fields {
		value, exists := values[field.Key]
		if !exists {
			continue
		}
		label := field.Label
		if label == "" {
			label = field.Key
		}
		display := fmt.Sprint(value)
		if items, ok := value.([]string); ok {
			display = strings.Join(items, ", ")
		}
		text += "\n" + html.EscapeString(truncate(label, 50)) + ": " + html.EscapeString(truncate(display, 120))
		if len([]rune(text)) > 1500 {
			text += "\n…"
			break
		}
	}
	return text
}
