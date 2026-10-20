package telegram

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/go-telegram/bot/models"
)

type supportTarget struct {
	URL       string
	Prefilled bool
}

func supportUsername(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// No network resolution or universal ?order_no= injection. Only a plain
// Telegram username supports the documented text draft. Bots/business links
// and websites keep their existing routing and use the visible/copyable text.
func orderSupportTarget(raw, draft string) supportTarget {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsFunc(raw, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) {
		return supportTarget{}
	}
	if strings.HasPrefix(raw, "@") && supportUsername(raw[1:]) {
		raw = "https://t.me/" + raw[1:]
	}
	for _, prefix := range []string{"t.me/", "telegram.me/", "telegram.dog/"} {
		if strings.HasPrefix(raw, prefix) {
			raw = "https://" + raw
			break
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return supportTarget{}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return supportTarget{}
	}
	if u.Scheme == "tg" {
		if u.Host != "resolve" || u.Path != "" || u.Fragment != "" || !supportUsername(q.Get("domain")) || len(q["domain"]) != 1 {
			return supportTarget{}
		}
		u = &url.URL{Scheme: "https", Host: "t.me", Path: "/" + q.Get("domain")}
		q.Del("domain")
		u.RawQuery = q.Encode()
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || strings.Contains(u.Host, "\\") {
		return supportTarget{}
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return supportTarget{}
		}
	}
	host := strings.ToLower(u.Hostname())
	if host != "t.me" && host != "telegram.me" && host != "telegram.dog" {
		return supportTarget{URL: u.String()}
	}
	if u.Port() != "" || u.Fragment != "" || u.RawPath != "" {
		return supportTarget{}
	}
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	// Do not turn share/proxy/login/invite/system actions into a support link.
	if len(parts) == 2 && parts[0] == "m" && validOrderNumber(parts[1]) {
		u.Scheme, u.Host = "https", "t.me"
		return supportTarget{URL: u.String()}
	}
	reserved := map[string]bool{"share": true, "proxy": true, "socks": true, "login": true, "iv": true, "joinchat": true, "addlist": true, "addstickers": true, "addemoji": true, "addtheme": true, "setlanguage": true, "confirmphone": true, "m": true, "c": true, "s": true, "contact": true, "boost": true, "giftcode": true, "invoice": true, "oauth": true}
	if len(parts) != 1 || !supportUsername(parts[0]) || reserved[strings.ToLower(parts[0])] {
		return supportTarget{}
	}
	u.Scheme, u.Host, u.Path = "https", "t.me", "/"+parts[0]
	canDraft := !strings.HasSuffix(strings.ToLower(parts[0]), "bot")
	for key := range q {
		if key != "text" && key != "profile" {
			canDraft = false
		}
	}
	if draft != "" && canDraft {
		q.Del("profile")
		q.Set("text", draft)
		u.RawQuery = q.Encode()
		return supportTarget{URL: u.String(), Prefilled: true}
	}
	return supportTarget{URL: u.String()}
}

func supportReason(locale, reason string) string {
	switch reason {
	case "p":
		return tr(locale, "支付未到账", "付款未入帳", "Payment not reflected")
	case "d":
		return tr(locale, "交付或使用问题", "交付或使用問題", "Delivery or usage issue")
	case "r":
		return tr(locale, "退款咨询", "退款諮詢", "Refund enquiry")
	default:
		return tr(locale, "订单咨询", "訂單諮詢", "Order enquiry")
	}
}

func supportMessage(locale, orderNo, reason string) string {
	if !validOrderNumber(orderNo) {
		return ""
	}
	return tr(locale, "您好，我需要订单售后帮助。\n订单号：", "您好，我需要訂單售後協助。\n訂單號：", "Hello, I need help with my order.\nOrder number: ") + orderNo + "\n" + tr(locale, "问题：", "問題：", "Issue: ") + supportReason(locale, reason)
}

func supportCallback(id uint, code string, page int, reason string) string {
	return fmt.Sprintf("support:%d:%s:%d:%s", id, code, page, reason)
}

func (a *App) orderSupportButtons(locale string, order dujiao.OrderDetail, code string, page int) []models.InlineKeyboardButton {
	buttons := []models.InlineKeyboardButton{}
	target := orderSupportTarget(a.botConfig().Basic.SupportURL, supportMessage(locale, order.OrderNo, "g"))
	if target.Prefilled {
		buttons = append(buttons, models.InlineKeyboardButton{Text: tr(locale, "💬 带订单号联系客服", "💬 帶訂單號聯絡客服", "💬 Contact support with order"), URL: target.URL})
	}
	return append(buttons, models.InlineKeyboardButton{Text: tr(locale, "🛟 售后 / 客服", "🛟 售後 / 客服", "🛟 Order support"), CallbackData: supportCallback(order.OrderID, code, page, "g")})
}

func (a *App) handleOrderSupport(ctx context.Context, query *models.CallbackQuery, parts []string) {
	m := query.Message.Message
	if m == nil || m.Chat.ID != query.From.ID || query.From.ID <= 0 || len(parts) != 5 {
		return
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 || uint64(uint(id)) != id {
		return
	}
	code, page := orderContext(parts[2], parts[3])
	reason := parts[4]
	if reason != "p" && reason != "d" && reason != "r" {
		reason = "g"
	}
	state := a.sessions.Get(query.From.ID)
	if !a.sessionOK(ctx, m.Chat.ID, state.Err) {
		return
	}
	order, err := a.api.Order(ctx, strconv.FormatInt(query.From.ID, 10), state.Locale, uint(id))
	if err != nil || order.OrderID != uint(id) {
		a.deliveryLookupFailure(ctx, m.Chat.ID, state.Locale)
		return
	}
	a.renderOrderSupport(ctx, m.Chat.ID, m.ID, state.Locale, order, code, page, reason)
}

func (a *App) renderOrderSupport(ctx context.Context, chatID int64, messageID int, locale string, order dujiao.OrderDetail, code string, page int, reason string) {
	message := supportMessage(locale, order.OrderNo, reason)
	target := orderSupportTarget(a.botConfig().Basic.SupportURL, message)
	text := "<b>" + tr(locale, "订单售后", "訂單售後", "Order support") + "</b>\n" + tr(locale, "当前状态：", "目前狀態：", "Current status: ") + orderStatus(locale, order.Status)
	text += "\n\n" + supportGuidance(locale, order.Status)
	var rows [][]models.InlineKeyboardButton
	if message != "" {
		text += "\n\n<pre>" + html.EscapeString(message) + "</pre>"
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: tr(locale, "📋 复制售后消息", "📋 複製售後訊息", "📋 Copy message"), CopyText: &models.CopyTextButton{Text: message}},
			{Text: tr(locale, "复制订单号", "複製訂單號", "Copy order no."), CopyText: &models.CopyTextButton{Text: order.OrderNo}},
		})
	} else {
		text += "\n\n" + tr(locale, "商店未返回可用的完整订单号，请刷新订单或联系商家核对。", "商店未回傳可用的完整訂單號，請重新整理訂單或聯絡商家核對。", "A usable full order number is unavailable. Refresh the order or ask the seller to check.")
	}
	if target.URL == "" {
		text += "\n\n" + tr(locale, "商店尚未配置可用的在线客服链接，请通过商家原有联系渠道提供订单号。", "商店尚未設定可用的線上客服連結，請透過商家原有聯絡管道提供訂單號。", "No usable support link is configured. Use the seller's existing contact channel and provide your order number.")
	} else {
		label := tr(locale, "💬 打开客服", "💬 開啟客服", "💬 Open support")
		if target.Prefilled {
			label = tr(locale, "💬 带订单号联系客服", "💬 帶訂單號聯絡客服", "💬 Contact support with order")
			text += "\n\n" + tr(locale, "打开客服聊天后，确认预填消息并发送；如未预填，可复制上方消息。", "開啟客服聊天後，確認預填訊息並傳送；如未預填，可複製上方訊息。", "Open the chat, review the draft and send it. If it is not prefilled, copy the message above.")
		} else {
			text += "\n\n" + tr(locale, "此客服链接不支持通用自动预填，请先复制售后消息，再打开客服粘贴。", "此客服連結不支援通用自動預填，請先複製售後訊息，再開啟客服貼上。", "This link cannot automatically prefill an order. Copy the message first, then open support and paste it.")
		}
		rows = append([][]models.InlineKeyboardButton{{{Text: label, URL: target.URL}}}, rows...)
	}
	var issues []models.InlineKeyboardButton
	for _, key := range []string{"p", "d", "r", "g"} {
		label := supportIssueLabel(locale, key)
		if key == reason {
			label = "✓ " + label
		}
		issues = append(issues, models.InlineKeyboardButton{Text: label, CallbackData: supportCallback(order.OrderID, code, page, key)})
	}
	rows = append(rows, issues[:2], issues[2:])
	text += "\n\n" + tr(locale, "仅准备咨询消息，不会自动发送、提交工单或退款。勿提供完整卡密、密码或支付密钥；截图请遮挡敏感信息。", "僅準備諮詢訊息，不會自動傳送、提交工單或退款。勿提供完整卡密、密碼或付款金鑰；截圖請遮擋敏感資訊。", "This only prepares an enquiry; it does not send messages, file tickets or issue refunds. Do not share full codes, passwords or payment keys; redact sensitive details in screenshots.")
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: tr(locale, "刷新售后信息", "重新整理售後資訊", "Refresh support"), CallbackData: supportCallback(order.OrderID, code, page, reason)},
		{Text: tr(locale, "‹ 订单详情", "‹ 訂單詳情", "‹ Order details"), CallbackData: orderViewCallback(order.OrderID, code, page)},
	})
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, messageID > 0)
}

// Short button labels; the full issue description remains in the visible draft.
func supportIssueLabel(locale, reason string) string {
	switch reason {
	case "p":
		return tr(locale, "支付问题", "付款問題", "Payment")
	case "d":
		return tr(locale, "交付 / 使用", "交付 / 使用", "Delivery / usage")
	case "r":
		return tr(locale, "退款咨询", "退款諮詢", "Refund query")
	default:
		return tr(locale, "其他咨询", "其他諮詢", "Other help")
	}
}

func supportGuidance(locale, status string) string {
	switch status {
	case "pending_payment":
		return tr(locale, "已付款但状态未更新？先刷新订单，勿重复付款或取消；仍未更新时联系商家核对。", "已付款但狀態未更新？先重新整理訂單，勿重複付款或取消；仍未更新時聯絡商家核對。", "Paid but still pending? Refresh first. Do not pay again or cancel; ask the seller to check if it remains pending.")
	case "paid", "fulfilling", "processing":
		return tr(locale, "付款后交付可能稍有延迟；人工交付需商家处理。可先刷新订单，再联系商家核对进度。", "付款後交付可能稍有延遲；人工交付需商家處理。可先重新整理訂單，再聯絡商家核對進度。", "Delivery may take time after payment; manual delivery needs seller action. Refresh first, then ask the seller about progress.")
	case "partially_delivered":
		return tr(locale, "订单尚未全部交付。TXT 仅包含当前已交付内容，请勿为催交付重复购买。", "訂單尚未全部交付。TXT 僅包含目前已交付內容，請勿為催交付重複購買。", "This order is only partly delivered. TXT contains currently available content; do not buy again to request delivery.")
	case "completed", "delivered":
		return tr(locale, "交付或使用有问题时，请描述错误提示；不要把完整卡密或密码发给他人。", "交付或使用有問題時，請描述錯誤提示；不要把完整卡密或密碼傳給他人。", "For delivery or usage issues, describe the error. Do not send anyone your full codes or passwords.")
	case "canceled", "cancelled", "refunded", "partially_refunded":
		return tr(locale, "关闭订单不等于退款。退款进度、已付款核对及售后范围由商家处理，本入口不自动退款。", "關閉訂單不等於退款。退款進度、已付款核對及售後範圍由商家處理，本入口不自動退款。", "Closing an order is not a refund. The seller handles payment checks, refund progress and after-sales eligibility; this does not issue refunds.")
	default:
		return tr(locale, "订单状态需要商家核对，可携带订单号说明问题。", "訂單狀態需要商家核對，可攜帶訂單號說明問題。", "Ask the seller to check the order status and include your order number.")
	}
}
