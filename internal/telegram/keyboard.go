package telegram

import "github.com/go-telegram/bot/models"

// Pure layout: never truncate labels, alter actions, or invoke an operation.
// Callers keep payment/resend/cancellation confirmations in their own rows.
func compactActionRows(buttons []models.InlineKeyboardButton, fullWidthCallbacks ...string) [][]models.InlineKeyboardButton {
	var rows [][]models.InlineKeyboardButton
	var pair []models.InlineKeyboardButton
	flush := func() {
		if len(pair) > 0 {
			rows = append(rows, pair)
			pair = nil
		}
	}
	for _, button := range buttons {
		wide := len([]rune(button.Text)) > 18
		for _, callback := range fullWidthCallbacks {
			wide = wide || button.CallbackData == callback
		}
		if wide {
			flush()
			rows = append(rows, []models.InlineKeyboardButton{button})
			continue
		}
		pair = append(pair, button)
		if len(pair) == 2 {
			flush()
		}
	}
	flush()
	return rows
}

func shopHomeButtons(locale string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{
		{Text: tr(locale, "🛍 购买商品", "🛍 購買商品", "🛍 Shop"), CallbackData: "cats"},
		{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"},
	}
}

func affiliateHomeButtons(locale string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{
		{Text: tr(locale, "‹ 推广中心", "‹ 推廣中心", "‹ Affiliate"), CallbackData: "affiliate"},
		{Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"},
	}
}
