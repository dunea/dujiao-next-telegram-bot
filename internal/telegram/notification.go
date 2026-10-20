package telegram

import (
	"fmt"

	"github.com/go-telegram/bot/models"
)

// Notifications need no upstream lookup or file upload. Buttons are navigation
// hints, never authorization: existing handlers fetch the current owner's order
// and use the same persistent TXT receipt regardless of the entry point.
func notificationKeyboard(locale, kind string, orderID uint) models.InlineKeyboardMarkup {
	home := models.InlineKeyboardButton{Text: tr(locale, "返回商店", "返回商店", "Store"), CallbackData: "home"}
	orders := models.InlineKeyboardButton{Text: tr(locale, "我的订单", "我的訂單", "My orders"), CallbackData: "orders:a:1"}
	if kind == "wallet-recharge-succeeded" {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: tr(locale, "查看余额", "查看餘額", "View balance"), CallbackData: "wallet"}, home,
		}}}
	}
	if orderID == 0 || (kind != "order-paid" && kind != "order-fulfilled") {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{orders, home}}}
	}
	order := models.InlineKeyboardButton{Text: tr(locale, "查看订单", "查看訂單", "View order"), CallbackData: fmt.Sprintf("order:%d", orderID)}
	if kind == "order-fulfilled" {
		return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: tr(locale, "📄 下载交付 TXT", "📄 下載交付 TXT", "📄 Download delivery TXT"), CallbackData: fmt.Sprintf("file:%d", orderID)}},
			{order, home},
		}}
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{order, home}}}
}
