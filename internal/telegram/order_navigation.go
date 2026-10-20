package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
	"github.com/go-telegram/bot/models"
)

// The upstream accepts ONE exact status, not comma-separated groups. Keep
// delivered (manual) separate from completed (automatic) and preserve old p/c.
var orderFilters = []struct{ code, status string }{
	{"a", ""}, {"p", "pending_payment"}, {"s", "paid"},
	{"f", "fulfilling"}, {"d", "partially_delivered"}, {"v", "delivered"},
	{"c", "completed"}, {"x", "canceled"}, {"r", "refunded"},
	{"h", "partially_refunded"}, {"g", "processing"},
}

func orderFilterStatus(code string) string {
	for _, filter := range orderFilters {
		if filter.code == code {
			return filter.status
		}
	}
	return ""
}
func orderFilterCode(status string) string {
	for _, filter := range orderFilters {
		if filter.status == status {
			return filter.code
		}
	}
	return "a"
}
func orderFilterLabel(locale, code string) string {
	if status := orderFilterStatus(code); status != "" {
		return orderStatus(locale, status)
	}
	return tr(locale, "全部", "全部", "All")
}
func orderContext(code, rawPage string) (string, int) {
	code = orderFilterCode(orderFilterStatus(code))
	page, err := strconv.Atoi(rawPage)
	if err != nil || page < 1 || page > 100000 {
		page = 1
	}
	return code, page
}
func orderViewCallback(id uint, code string, page int) string {
	return fmt.Sprintf("order:%d:%s:%d", id, code, page)
}

func (a *App) showOrderFilters(ctx context.Context, chatID int64, messageID int, locale, code string, page int) {
	var choices []models.InlineKeyboardButton
	for _, filter := range orderFilters {
		label := orderFilterLabel(locale, filter.code)
		if filter.code == code {
			label = "✓ " + label
		}
		choices = append(choices, models.InlineKeyboardButton{Text: label, CallbackData: "orders:" + filter.code + ":1"})
	}
	rows := compactActionRows(choices)
	rows = append(rows, orderListBackButtons(locale, code, page))
	a.edit(ctx, chatID, messageID, tr(locale, "选择订单状态（按商店原始状态筛选）：", "選擇訂單狀態（按商店原始狀態篩選）：", "Choose an order status (exact store status):"), models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func validOrderNumber(number string) bool {
	if len(number) == 0 || len(number) > 100 {
		return false
	}
	for _, c := range number {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (a *App) askOrderLookup(ctx context.Context, chatID int64, messageID int, userID int64, code string, page int) {
	if chatID != userID || userID <= 0 || messageID <= 0 {
		return
	}
	state := a.sessions.Update(userID, func(s *session.State) {
		s.Flow = "order_lookup"
		s.OrderLookup = &session.OrderLookupInput{ID: rand.Text(), ChatID: chatID, MessageID: messageID, Filter: code, Page: page, ExpiresAt: time.Now().Add(5 * time.Minute)}
	})
	if !a.sessionOK(ctx, chatID, state.Err) {
		return
	}
	a.edit(ctx, chatID, messageID, tr(state.Locale,
		"🔎 请发送完整订单号（不是商品编号）。\n仅查找你自己的订单，不受当前状态筛选限制。五分钟内有效；/cancel 退出。\n也可随时发送：<code>/orders 订单号</code>",
		"🔎 請傳送完整訂單號（不是商品編號）。\n僅查找你自己的訂單，不受目前狀態篩選限制。五分鐘內有效；/cancel 退出。\n也可隨時傳送：<code>/orders 訂單號</code>",
		"🔎 Send the full order number (not a product ID).\nOnly your own orders are searched, regardless of the current filter. Valid for five minutes; /cancel exits.\nOr send: <code>/orders ORDER_NUMBER</code>"), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(state.Locale, "‹ 返回订单列表", "‹ 返回訂單列表", "‹ Back to orders"), CallbackData: fmt.Sprintf("orders:%s:%d", code, page)}}}})
}

func (a *App) handleOrderLookupMessage(ctx context.Context, message *models.Message, state session.State) {
	draft := state.OrderLookup
	if draft == nil || message.Chat.ID != message.From.ID || draft.ChatID != message.Chat.ID || !time.Now().Before(draft.ExpiresAt) {
		return
	}
	if message.ReplyToMessage != nil && message.ReplyToMessage.ID != draft.MessageID {
		a.send(ctx, message.Chat.ID, tr(state.Locale, "请回复当前查找提示，或直接发送 /orders 订单号。", "請回覆目前查找提示，或直接傳送 /orders 訂單號。", "Reply to the current search prompt, or send /orders ORDER_NUMBER."), nil)
		return
	}
	if command, _ := parseCommand(message.Text); command == "/back" {
		accepted := false
		stored := a.sessions.Update(message.From.ID, func(s *session.State) {
			if s.Flow == "order_lookup" && s.OrderLookup != nil && s.OrderLookup.ID == draft.ID {
				s.Flow, s.OrderLookup, accepted = "", nil, true
			}
		})
		if a.sessionOK(ctx, message.Chat.ID, stored.Err) && accepted {
			a.clearInputButtons(ctx, message.Chat.ID, draft.MessageID)
			a.showOrders(ctx, message.Chat.ID, message.From.ID, orderFilterStatus(draft.Filter), draft.Page, false, 0)
		}
		return
	}
	// Text input always gets a new result below the user's message.
	a.findOrder(ctx, message.Chat.ID, 0, message.From.ID, message.Text, draft.Filter, draft.Page, draft)
}

func (a *App) findOrder(ctx context.Context, chatID int64, messageID int, userID int64, raw, code string, page int, expected *session.OrderLookupInput) {
	if chatID != userID || userID <= 0 {
		return
	}
	locale := a.sessions.Get(userID).Locale
	back := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{orderListBackButtons(locale, code, page)}}
	number := strings.TrimSpace(raw)
	if !validOrderNumber(number) {
		a.send(ctx, chatID, tr(locale, "请输入完整订单号：仅支持字母、数字、横线和下划线，最多 100 个字符。", "請輸入完整訂單號：僅支援字母、數字、橫線和底線，最多 100 個字元。", "Enter a full order number: letters, digits, hyphens and underscores only, up to 100 characters."), back)
		return
	}
	order, err := a.api.OrderByOrderNo(ctx, strconv.FormatInt(userID, 10), locale, number)
	if err != nil {
		a.orderReadFailure(ctx, chatID, locale, err, back)
		return
	}
	if order.OrderID == 0 || order.OrderNo != number {
		a.orderReadFailure(ctx, chatID, locale, nil, back)
		return
	}
	if expected != nil {
		accepted := false
		state := a.sessions.Update(userID, func(s *session.State) {
			if s.Flow == "order_lookup" && s.OrderLookup != nil && s.OrderLookup.ID == expected.ID && time.Now().Before(s.OrderLookup.ExpiresAt) {
				s.OrderLookup, s.Flow, accepted = nil, "", true
			}
		})
		if !a.sessionOK(ctx, chatID, state.Err) || !accepted {
			return
		}
		a.clearInputButtons(ctx, chatID, expected.MessageID)
	}
	a.renderOrder(ctx, chatID, messageID, locale, order, "", code, page)
}

// Neither upstream errors nor user input belongs in logs. Missing and unowned
// orders are indistinguishable; a store outage is not reported as "no orders".
func (a *App) orderReadFailure(ctx context.Context, chatID int64, locale string, err error, keyboards ...models.InlineKeyboardMarkup) {
	var markup models.ReplyMarkup
	if len(keyboards) > 0 {
		markup = keyboards[0]
	}
	var api *dujiao.APIError
	if errors.As(err, &api) && api.HTTPStatus == 404 && api.ErrorCode == "order_not_found" {
		a.send(ctx, chatID, tr(locale, "未找到你名下的这个订单，请核对完整订单号后重试。", "未找到你名下的這個訂單，請核對完整訂單號後重試。", "No matching order in your account. Check the full order number and try again."), markup)
		return
	}
	a.logger.Warn("order read unavailable")
	a.send(ctx, chatID, tr(locale, "订单查询暂时不可用，请稍后重试。", "訂單查詢暫時無法使用，請稍後再試。", "Order lookup is temporarily unavailable. Please try again later."), markup)
}

func orderPaymentStatus(locale, status string) string {
	switch status {
	case "pending_payment":
		return tr(locale, "待支付", "待付款", "Awaiting payment")
	case "paid", "processing", "fulfilling", "partially_delivered", "delivered", "completed":
		return tr(locale, "已支付", "已付款", "Paid")
	case "refunded", "partially_refunded":
		return orderStatus(locale, status)
	case "canceled", "cancelled":
		return tr(locale, "订单已关闭；已付款请联系商家核对", "訂單已關閉；已付款請聯絡商家核對", "Closed; contact the seller if already paid")
	default:
		return tr(locale, "待核对", "待核對", "Unconfirmed")
	}
}

// List responses do not contain fulfillment details. Never fetch N extra
// orders per page or infer downloadable codes from payment/status alone.
func listDeliveryStatus(locale, status string) string {
	switch status {
	case "pending_payment":
		return tr(locale, "付款后安排", "付款後安排", "After payment")
	case "paid":
		return tr(locale, "等待交付", "等待交付", "Awaiting delivery")
	case "processing", "fulfilling":
		return tr(locale, "交付处理中", "交付處理中", "Delivery in progress")
	case "partially_delivered":
		return tr(locale, "部分交付", "部分交付", "Partially delivered")
	case "completed", "delivered":
		return tr(locale, "商店标记已交付", "商店標記已交付", "Store marks delivered")
	case "canceled", "cancelled":
		return tr(locale, "订单已关闭", "訂單已關閉", "Order closed")
	case "refunded", "partially_refunded":
		return tr(locale, "涉及退款，请核对详情", "涉及退款，請核對詳情", "Refund involved; check details")
	default:
		return tr(locale, "查看详情核对", "查看詳情核對", "Check details")
	}
}

func detailDeliveryStatus(locale string, order dujiao.OrderDetail) string {
	if !deliveryAllowed(order) {
		return listDeliveryStatus(locale, order.Status)
	}
	delivered := 0
	for _, child := range order.Children {
		if child.Status == "completed" || child.Status == "delivered" || (child.Fulfillment != nil && child.Fulfillment.Status == "delivered") {
			delivered++
		}
	}
	if len(order.Children) > 0 && delivered < len(order.Children) && (delivered > 0 || order.Status == "partially_delivered") {
		return fmt.Sprintf(tr(locale, "部分交付（%d/%d 个子订单已交付）", "部分交付（%d/%d 個子訂單已交付）", "Partial delivery (%d/%d sub-orders delivered)"), delivered, len(order.Children))
	}
	if order.Status == "partially_delivered" {
		return listDeliveryStatus(locale, order.Status)
	}
	if order.Status == "completed" || order.Status == "delivered" || order.FulfillmentStatus == "delivered" || (len(order.Children) > 0 && delivered == len(order.Children)) {
		return tr(locale, "已交付", "已交付", "Delivered")
	}
	if hasDelivery(order) {
		return tr(locale, "已有交付内容，整体进度待核对", "已有交付內容，整體進度待核對", "Content available; overall progress unconfirmed")
	}
	if order.FulfillmentType == "manual" {
		return tr(locale, "等待商家人工交付", "等待商家人工交付", "Awaiting manual delivery by seller")
	}
	return listDeliveryStatus(locale, order.Status)
}
