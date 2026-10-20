package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

const quantityLifetime = 10 * time.Minute
const maxPurchaseQuantity = math.MaxInt32

func parsePurchaseQuantity(input string) (int, bool) {
	input = strings.TrimSpace(input)
	if len(input) == 0 || len(input) > 10 {
		return 0, false
	}
	for _, c := range input {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(input, 10, 31)
	return int(n), err == nil && n > 0
}

func quantityRange(locale string, minimum, maximum int) string {
	if maximum > 0 {
		return fmt.Sprintf("%s%d–%d", tr(locale, "可选数量：", "可選數量：", "Allowed quantity: "), minimum, maximum)
	}
	return fmt.Sprintf("%s%d", tr(locale, "起购数量：", "起購數量：", "Minimum quantity: "), minimum)
}

func quantityCard(locale string, product dujiao.ProductDetail, skuID uint, quantity int, id string) (string, models.InlineKeyboardMarkup) {
	minimum, maximum, available := purchaseQuantityBounds(product, skuID)
	name, price := product.Title, product.PriceFrom
	if sku := selectedSKU(product, skuID); sku != nil {
		if len(product.SKUs) > 1 || sku.SpecValues != "" {
			name += " · " + skuLabel(*sku)
		}
		price = sku.Price
		if sku.MemberPrice != "" {
			price = sku.MemberPrice
		}
	} else if product.MemberPriceFrom != "" {
		price = product.MemberPriceFrom
	}
	text := "<b>" + html.EscapeString(name) + "</b>"
	if description := strings.TrimSpace(product.Summary); description != "" {
		text += "\n" + html.EscapeString(truncate(description, 300))
	} else if product.Description != "" {
		text += "\n" + html.EscapeString(truncate(product.Description, 300))
	}
	var rows [][]models.InlineKeyboardButton
	if available {
		if price != "" {
			text += "\n\n" + tr(locale, "单价：", "單價：", "Unit price: ") + html.EscapeString(price+" "+product.Currency)
		}
		text += fmt.Sprintf("\n%s<b>%d</b>", tr(locale, "数量：", "數量：", "Quantity: "), quantity)
		if cents, ok := money.Parse(price); ok && quantity > 0 && cents <= math.MaxInt64/int64(quantity) {
			text += "\n" + tr(locale, "预估小计：", "預估小計：", "Estimated subtotal: ") + html.EscapeString(money.Format(cents*int64(quantity))+" "+product.Currency)
		}
		if maximum > 0 || minimum > 1 {
			text += "\n" + quantityRange(locale, minimum, maximum)
		}
		minus, plus := "none", "none"
		if quantity > minimum {
			minus = fmt.Sprintf("qty:%d:%d:%d", product.ID, skuID, quantity-1)
		}
		if quantity < maxPurchaseQuantity && (maximum == 0 || quantity < maximum) {
			plus = fmt.Sprintf("qty:%d:%d:%d", product.ID, skuID, quantity+1)
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: "−", CallbackData: minus}, {Text: tr(locale, "✎ 输入数量", "✎ 輸入數量", "✎ Enter quantity"), CallbackData: "qtyinput:" + id}, {Text: "+", CallbackData: plus}})
		var presets []models.InlineKeyboardButton
		for _, n := range []int{10, 100, 1000} {
			if n >= minimum && (maximum == 0 || n <= maximum) && n != quantity {
				presets = append(presets, models.InlineKeyboardButton{Text: fmt.Sprintf("%d%s", n, tr(locale, "件", "件", " items")), CallbackData: fmt.Sprintf("qty:%d:%d:%d", product.ID, skuID, n)})
			}
		}
		if len(presets) > 0 {
			rows = append(rows, presets)
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "下一步", "下一步", "Continue"), CallbackData: fmt.Sprintf("buy:%d:%d:%d", product.ID, skuID, quantity)}})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "已售罄", "已售罄", "Out of stock"), CallbackData: "noop"}})
	}
	back := models.InlineKeyboardButton{Text: tr(locale, "‹ 商品列表", "‹ 商品列表", "‹ Products"), CallbackData: "cat:0:1"}
	if len(product.SKUs) > 1 {
		back = models.InlineKeyboardButton{Text: tr(locale, "‹ 更换规格", "‹ 更換規格", "‹ Change option"), CallbackData: fmt.Sprintf("product:%d", product.ID)}
	}
	secondary := []models.InlineKeyboardButton{back}
	if product.Description != "" || len([]rune(product.Summary)) > 300 {
		secondary = append([]models.InlineKeyboardButton{{Text: tr(locale, "商品详情", "商品詳情", "Product details"), CallbackData: fmt.Sprintf("productinfo:%d:%d:%d", product.ID, skuID, quantity)}}, secondary...)
	}
	return text, models.InlineKeyboardMarkup{InlineKeyboard: append(rows, secondary)}
}

func (a *App) showProductInfo(ctx context.Context, chatID int64, messageID int, userID int64, productID, skuID uint, quantity int) {
	locale := a.sessions.Get(userID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(userID, 10), productID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get product details", err)
		return
	}
	description := product.Description
	if strings.TrimSpace(description) == "" {
		description = product.Summary
	}
	text := "<b>" + html.EscapeString(product.Title) + "</b>\n\n" + html.EscapeString(truncate(description, 2500))
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "‹ 返回数量选择", "‹ 返回數量選擇", "‹ Back to quantity"), CallbackData: fmt.Sprintf("qty:%d:%d:%d", productID, skuID, quantity)}}}})
}

func (a *App) publishQuantitySelector(ctx context.Context, chatID int64, messageID int, userID int64, product dujiao.ProductDetail, skuID uint, quantity int, edit bool) {
	locale := a.sessions.Get(userID).Locale
	minimum, maximum, available := purchaseQuantityBounds(product, skuID)
	quantity = clampPurchaseQuantity(quantity, minimum, maximum)
	q := &session.QuantityInput{ID: rand.Text(), ChatID: chatID, MessageID: messageID, ProductID: product.ID, SKUID: skuID, Quantity: quantity, ExpiresAt: time.Now().Add(quantityLifetime)}
	state := a.sessions.Update(userID, func(s *session.State) {
		s.Quantity, s.Flow = q, ""
		s.Purchase, s.GiftCard, s.Recharge, s.Withdrawal = nil, nil, nil, nil
	})
	if !a.sessionOK(ctx, chatID, state.Err) {
		return
	}
	text, keyboard := quantityCard(locale, product, skuID, quantity, q.ID)
	var err error
	if edit {
		_, err = a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: keyboard})
	} else {
		var m *models.Message
		m, err = a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: keyboard})
		if err == nil {
			messageID = m.ID
		}
	}
	stored := a.sessions.Update(userID, func(s *session.State) {
		if s.Quantity != nil && s.Quantity.ID == q.ID {
			if err != nil || !available {
				s.Quantity = nil
			} else {
				s.Quantity.MessageID = messageID
			}
		}
	})
	a.sessionOK(ctx, chatID, stored.Err)
	if err != nil {
		a.logger.Warn("quantity selection could not be displayed")
	}
}

func (a *App) handleQuantityCallback(ctx context.Context, query *models.CallbackQuery, parts []string) {
	message, userID := query.Message.Message, query.From.ID
	if message.Chat.ID != userID {
		return
	}
	state := a.sessions.Get(userID)
	if parts[0] == "qtyedit" {
		if len(parts) != 3 {
			return
		}
		revision, err := strconv.Atoi(parts[2])
		if err != nil {
			return
		}
		draft := state.Purchase
		if draft == nil || draft.ID != parts[1] || draft.Revision != revision || draft.PreviewID != message.ID || draft.ChatID != userID || !draft.Ready || draft.Submitting {
			return
		}
		q := &session.QuantityInput{ID: rand.Text(), ChatID: userID, MessageID: message.ID, ProductID: draft.ProductID, SKUID: draft.SKUID, Quantity: draft.Quantity, PurchaseID: draft.ID, PurchaseRevision: revision + 1, ExpiresAt: minTime(draft.ExpiresAt, time.Now().Add(quantityLifetime))}
		claimed := false
		stored := a.sessions.Update(userID, func(s *session.State) {
			if d := s.Purchase; d != nil && d.ID == draft.ID && d.Revision == revision && d.Ready && !d.Submitting {
				d.Ready = false
				d.Revision++
				s.Quantity = q
				s.Flow = "purchase_quantity"
				claimed = true
			}
		})
		if a.sessionOK(ctx, userID, stored.Err) && claimed {
			a.promptQuantity(ctx, userID, q)
		}
		return
	}
	q := state.Quantity
	if len(parts) != 2 || q == nil || q.ID != parts[1] || q.ChatID != userID {
		return
	}
	if parts[0] == "qtyback" {
		if state.Flow == "purchase_quantity" && q.PromptID == message.ID {
			a.finishQuantityInput(ctx, &query.From, q, q.Quantity, true)
		}
		return
	}
	if state.Flow != "" || q.MessageID != message.ID || q.PromptID != 0 {
		return
	}
	claimed := false
	stored := a.sessions.Update(userID, func(s *session.State) {
		if s.Quantity != nil && s.Quantity.ID == q.ID && s.Flow == "" && s.Quantity.PromptID == 0 {
			s.Flow = "purchase_quantity"
			claimed = true
		}
	})
	if a.sessionOK(ctx, userID, stored.Err) && claimed {
		a.promptQuantity(ctx, userID, q)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (a *App) promptQuantity(ctx context.Context, userID int64, q *session.QuantityInput) {
	locale := a.sessions.Get(userID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(userID, 10), q.ProductID)
	if err != nil {
		a.apiFailure(ctx, userID, locale, "get quantity input limits", err)
		a.restoreQuantityPrompt(ctx, userID, q)
		return
	}
	minimum, maximum, _ := purchaseQuantityBounds(product, q.SKUID)
	text := tr(locale, "请输入购买数量，例如 2500。", "請輸入購買數量，例如 2500。", "Enter a quantity, e.g. 2500.") + "\n" + quantityRange(locale, minimum, maximum)
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "qtyback:" + q.ID}}}}
	message, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: userID, Text: text, ReplyMarkup: keyboard})
	if err != nil {
		a.logger.Warn("quantity prompt could not be displayed")
		a.restoreQuantityPrompt(ctx, userID, q)
		return
	}
	stored := a.sessions.Update(userID, func(s *session.State) {
		if s.Quantity != nil && s.Quantity.ID == q.ID && s.Flow == "purchase_quantity" {
			s.Quantity.PromptID = message.ID
		}
	})
	if !a.sessionOK(ctx, userID, stored.Err) {
		return
	}
	// Old card buttons should not compete with the active input prompt.
	_, _ = a.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{ChatID: userID, MessageID: q.MessageID})
}

func (a *App) restoreQuantityPrompt(ctx context.Context, userID int64, q *session.QuantityInput) {
	changed := false
	stored := a.sessions.Update(userID, func(s *session.State) {
		if s.Quantity != nil && s.Quantity.ID == q.ID && s.Quantity.PromptID == 0 && s.Flow == "purchase_quantity" {
			s.Flow = ""
			if q.PurchaseID != "" {
				s.Quantity = nil
			}
			changed = true
		}
	})
	if a.sessionOK(ctx, userID, stored.Err) && changed && q.PurchaseID != "" && stored.Purchase != nil {
		a.previewForm(ctx, &models.User{ID: userID}, stored.Purchase)
	}
}

func (a *App) quantityInputError(ctx context.Context, q *session.QuantityInput, locale, text string) {
	a.edit(ctx, q.ChatID, q.PromptID, text, models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "qtyback:" + q.ID}}}})
}

func (a *App) handleQuantityMessage(ctx context.Context, message *models.Message) {
	state := a.sessions.Get(message.From.ID)
	q := state.Quantity
	if message.Chat.ID != message.From.ID || q == nil || q.ChatID != message.Chat.ID || q.PromptID == 0 {
		return
	}
	command, _ := parseCommand(message.Text)
	if command == "/back" {
		a.finishQuantityInput(ctx, message.From, q, q.Quantity, true)
		return
	}
	if message.ID <= q.PromptID || (message.ReplyToMessage != nil && message.ReplyToMessage.ID != q.PromptID) {
		return
	}
	n, ok := parsePurchaseQuantity(message.Text)
	if !ok {
		a.quantityInputError(ctx, q, state.Locale, tr(state.Locale, "请输入正整数，例如 2500；不要输入小数或逗号。", "請輸入正整數，例如 2500；不要輸入小數或逗號。", "Enter a whole number such as 2500, without decimals or commas."))
		return
	}
	a.finishQuantityInput(ctx, message.From, q, n, false)
}

func (a *App) finishQuantityInput(ctx context.Context, user *models.User, q *session.QuantityInput, n int, back bool) {
	locale := a.sessions.Get(user.ID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(user.ID, 10), q.ProductID)
	if err != nil {
		a.apiFailure(ctx, user.ID, locale, "validate typed quantity", err)
		return
	}
	minimum, maximum, available := purchaseQuantityBounds(product, q.SKUID)
	if !back && (!available || n < minimum || (maximum > 0 && n > maximum) || n > maxPurchaseQuantity) {
		text := tr(locale, "当前数量不可购买，请重新输入。", "目前數量無法購買，請重新輸入。", "That quantity is unavailable. Enter another quantity.") + "\n" + quantityRange(locale, minimum, maximum)
		if !available {
			text = tr(locale, "当前库存不足，请返回重新选择。", "目前庫存不足，請返回重新選擇。", "This option is no longer available. Go back and choose again.")
		}
		a.quantityInputError(ctx, q, locale, text)
		return
	}
	claimed := false
	stored := a.sessions.Update(user.ID, func(s *session.State) {
		if current := s.Quantity; current == nil || current.ID != q.ID || current.PromptID != q.PromptID || s.Flow != "purchase_quantity" || !time.Now().Before(current.ExpiresAt) {
			return
		}
		if q.PurchaseID != "" {
			d := s.Purchase
			if d == nil || d.ID != q.PurchaseID || d.Revision != q.PurchaseRevision || d.Submitting {
				return
			}
			d.Quantity = n
			d.Ready = false
			d.Revision++
			d.PreviewID = q.PromptID
		}
		s.Quantity = nil
		s.Flow = ""
		claimed = true
	})
	if !a.sessionOK(ctx, user.ID, stored.Err) || !claimed {
		return
	}
	if q.PurchaseID != "" {
		a.previewForm(ctx, user, stored.Purchase)
	} else {
		a.publishQuantitySelector(ctx, user.ID, q.PromptID, user.ID, product, q.SKUID, n, true)
	}
}
