package telegram

import (
	"fmt"

	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

// Notices are localized system messages, never submitted input or API errors.
func inputNotice(text string, notices ...string) string {
	if len(notices) > 0 && notices[0] != "" {
		return notices[0] + "\n\n" + text
	}
	return text
}

func orderListBackButtons(locale, code string, page int) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{
		{Text: tr(locale, "‹ 返回订单列表", "‹ 返回訂單列表", "‹ Orders"), CallbackData: fmt.Sprintf("orders:%s:%d", code, page)},
		{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"},
	}
}

func cancelInputKeyboard(locale string, state session.State) models.InlineKeyboardMarkup {
	if state.WithdrawalGuard != nil || state.Withdrawal != nil {
		return withdrawalResultKeyboard(locale)
	}
	if state.GiftCard != nil || state.Recharge != nil {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: tr(locale, "‹ 返回钱包", "‹ 返回錢包", "‹ Wallet"), CallbackData: "wallet"},
			{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"},
		}}}
	}
	if state.OrderLookup != nil {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{orderListBackButtons(locale, state.OrderLookup.Filter, state.OrderLookup.Page)}}
	}
	var productID, skuID uint
	var quantity int
	if state.Purchase != nil {
		productID, skuID, quantity = state.Purchase.ProductID, state.Purchase.SKUID, state.Purchase.Quantity
	} else if state.Quantity != nil {
		productID, skuID, quantity = state.Quantity.ProductID, state.Quantity.SKUID, state.Quantity.Quantity
	}
	if productID != 0 && quantity > 0 {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Product"), CallbackData: fmt.Sprintf("qty:%d:%d:%d", productID, skuID, quantity)},
			{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"},
		}}}
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{shopHomeButtons(locale)}}
}
