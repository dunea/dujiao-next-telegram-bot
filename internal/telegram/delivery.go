package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
)

// The upstream response is bounded at 4 MiB. Never truncate an export to fit.
const maxDeliveryTXT = 4 << 20

func deliveryAllowed(order dujiao.OrderDetail) bool {
	switch order.Status {
	case "completed", "delivered", "paid", "processing", "fulfilling", "partially_delivered":
		return true
	}
	return false
}

func hasDelivery(order dujiao.OrderDetail) bool {
	if !deliveryAllowed(order) {
		return false
	}
	if formatFulfillmentPayload(order.FulfillmentResult) != "" {
		return true
	}
	for _, child := range order.Children {
		if child.Fulfillment != nil && formatFulfillmentPayload(child.Fulfillment.Payload) != "" {
			return true
		}
	}
	return false
}

// Keep strings byte-for-byte (including leading/trailing whitespace). JSON
// values are rendered deterministically; never HTML-escape or deduplicate codes.
func txtPayload(payload any) string {
	if s, ok := payload.(string); ok {
		return s
	}
	if formatFulfillmentPayload(payload) == "" {
		return ""
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func deliveryTXT(order dujiao.OrderDetail) ([]byte, error) {
	if !hasDelivery(order) {
		return nil, errors.New("delivery unavailable")
	}
	var out strings.Builder
	write := func(parts ...string) error {
		for _, part := range parts {
			if len(part) > maxDeliveryTXT-out.Len() {
				return errors.New("delivery too large")
			}
			out.WriteString(part)
		}
		return nil
	}
	if err := write("Order / 订单: ", order.OrderNo, "\n\n"); err != nil {
		return nil, err
	}
	root := txtPayload(order.FulfillmentResult)
	// The official API mirrors a single child's payload on the parent. Suppress
	// only that exact mirror, never equal codes belonging to different children.
	rootMirror := len(order.Children) == 1 && order.Children[0].Fulfillment != nil && root == txtPayload(order.Children[0].Fulfillment.Payload)
	if strings.TrimSpace(root) != "" && !rootMirror {
		if err := write("=== Delivery / 交付内容 ===\n", root, "\n\n"); err != nil {
			return nil, err
		}
	}
	if order.FulfillmentInstructions != "" {
		if err := write("=== Instructions / 使用说明 ===\n", order.FulfillmentInstructions, "\n\n"); err != nil {
			return nil, err
		}
	}
	for _, item := range order.Items {
		if item.Instructions != "" {
			if err := write("=== ", item.ProductTitle, " / ", item.SKUName, " ===\n", item.Instructions, "\n\n"); err != nil {
				return nil, err
			}
		}
	}
	for _, child := range order.Children {
		if child.Fulfillment == nil {
			continue
		}
		f := child.Fulfillment
		payload := txtPayload(f.Payload)
		if strings.TrimSpace(payload) == "" && f.Instructions == "" {
			continue
		}
		if err := write("=== Sub-order / 子订单: ", child.OrderNo, " ===\n", payload, "\n"); err != nil {
			return nil, err
		}
		if f.Instructions != "" {
			if err := write("Instructions / 使用说明:\n", f.Instructions, "\n"); err != nil {
				return nil, err
			}
		}
		if err := write("\n"); err != nil {
			return nil, err
		}
	}
	return []byte(out.String()), nil
}

func (a *App) handleDeliveryCallback(ctx context.Context, q *models.CallbackQuery, parts []string) {
	m := q.Message.Message
	if m == nil || q.From.ID <= 0 || m.Chat.ID != q.From.ID {
		return
	}
	if (parts[0] == "file" && len(parts) != 2) || (parts[0] == "fileretry" && len(parts) != 3) {
		return
	}
	id, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil || id == 0 {
		return
	}
	uid, oid := q.From.ID, uint(id)
	locale := a.sessions.Get(uid).Locale
	// Fresh owner-scoped lookup on EVERY click, even duplicates and resends.
	order, err := a.api.Order(ctx, strconv.FormatInt(uid, 10), locale, oid)
	if err != nil {
		a.deliveryLookupFailure(ctx, uid, locale)
		return
	}
	if order.OrderID != oid || !hasDelivery(order) {
		a.send(ctx, uid, tr(locale, "交付内容暂不可用，请刷新订单。", "交付內容暫不可用，請重新整理訂單。", "Delivery unavailable. Refresh the order."), nil)
		return
	}
	if a.deliveries == nil {
		a.deliveryFailure(ctx, uid, locale)
		return
	}
	content, err := deliveryTXT(order)
	if err != nil {
		a.send(ctx, uid, tr(locale, "交付文件超出大小限制，请联系商家获取完整内容；不会发送截断文件。", "交付檔案超出大小限制，請聯絡商家取得完整內容；不會發送截斷檔案。", "Delivery exceeds the file limit. Contact the seller for the complete content; no truncated file was sent."), nil)
		return
	}
	fp := sha256.Sum256(content)
	key, token, now := delivery.Key(uid, oid), rand.Text(), time.Now()
	var previous delivery.Receipt
	_, err = a.deliveries.Update(ctx, key, func(r *delivery.Receipt) error {
		previous = *r
		if r.Status != "" {
			if now.Before(r.NotBefore) {
				return delivery.ErrBlocked
			}
			if r.Status != "retry" && (parts[0] != "fileretry" || parts[2] != r.Token) {
				return delivery.ErrBlocked
			}
			// A stale resend confirmation must not authorize a later attempt.
			if parts[0] == "fileretry" && parts[2] != r.Token {
				return delivery.ErrBlocked
			}
		} else if parts[0] == "fileretry" {
			return delivery.ErrBlocked
		}
		r.Status, r.Token, r.Fingerprint = "posting", token, hex.EncodeToString(fp[:])
		r.NotBefore, r.MessageID = now.Add(2*time.Minute), 0
		r.ExpiresAt = now.Add(delivery.Lifetime)
		return nil
	})
	if errors.Is(err, delivery.ErrBlocked) {
		a.showDeliveryReceipt(ctx, uid, m.ID, locale, oid, previous)
		return
	}
	if err != nil {
		a.deliveryFailure(ctx, uid, locale)
		return
	}
	op := a.ops.Begin(operations.DeliveryTXT, uid, oid)
	// One upload attempt only. In particular never retry a timeout/5xx: the file
	// may already be in the buyer's chat. No local file or cached file_id is used.
	uploadCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	message, sendErr := a.bot.SendDocument(uploadCtx, &tgbot.SendDocumentParams{
		ChatID:                      uid,
		Document:                    &models.InputFileUpload{Filename: fmt.Sprintf("delivery-%d.txt", oid), Data: bytes.NewReader(content)},
		Caption:                     tr(locale, "订单交付文件 · 请妥善保管，勿转发给他人。", "訂單交付檔案 · 請妥善保管，勿轉傳他人。", "Order delivery · Keep this file private."),
		DisableContentTypeDetection: true,
	})
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer finishCancel()
	receipt, finishErr := a.deliveries.Update(finishCtx, key, func(r *delivery.Receipt) error {
		if r.Token != token || r.Status != "posting" {
			return delivery.ErrBlocked
		}
		if sendErr == nil && message != nil && message.ID > 0 {
			r.Status, r.MessageID, r.NotBefore = "sent", message.ID, time.Now().Add(time.Minute)
		} else {
			var limited *tgbot.TooManyRequestsError
			if errors.As(sendErr, &limited) {
				r.Status = "retry" // Telegram explicitly rejected this attempt.
				r.NotBefore = time.Now().Add(time.Duration(min(3600, max(1, limited.RetryAfter))) * time.Second)
			}
		}
		return nil
	})
	if finishErr != nil {
		op.End(operations.Unknown, oid)
		a.deliveryFailure(ctx, uid, locale)
		return
	}
	outcome := operations.Unknown
	if receipt.Status == "sent" {
		outcome = operations.Succeeded
	}
	if receipt.Status == "retry" {
		outcome = operations.Limited
	}
	op.End(outcome, oid)
	a.showDeliveryReceipt(ctx, uid, m.ID, locale, oid, receipt)
}

func (a *App) deliveryLookupFailure(ctx context.Context, uid int64, locale string) {
	// API error messages are upstream-controlled and may contain private data.
	a.logger.Warn("delivery order lookup unavailable")
	a.send(ctx, uid, tr(locale, "暂时无法读取此订单，请确认是本人的订单后稍后重试。", "暫時無法讀取此訂單，請確認是本人的訂單後稍後重試。", "Cannot read this order. Check that it is yours and try later."), nil)
}

func (a *App) deliveryFailure(ctx context.Context, uid int64, locale string) {
	// Never log SDK errors: multipart transport errors can contain secret URLs.
	a.logger.Warn("delivery TXT state unavailable; upload not retried")
	a.send(ctx, uid, tr(locale, "文件状态暂不可确认。请先查看聊天中是否已有文件，稍后刷新订单；不会自动重发。", "檔案狀態暫不可確認。請先查看聊天中是否已有檔案，稍後重新整理訂單；不會自動重發。", "File status unavailable. Check the chat before refreshing the order; no automatic resend."), nil)
}

func (a *App) showDeliveryReceipt(ctx context.Context, uid int64, mid int, locale string, oid uint, r delivery.Receipt) {
	text := tr(locale, "文件正在发送，或发送结果尚未确认。请先检查聊天，勿重复领取。", "檔案正在發送，或發送結果尚未確認。請先檢查聊天，勿重複領取。", "File sending or delivery unconfirmed. Check the chat before requesting again.")
	if r.Status == "sent" {
		text = tr(locale, "✅ TXT 已发送，请查看聊天中的文件。下载同一文件不会再次购买或扣款。", "✅ TXT 已發送，請查看聊天中的檔案。下載同一檔案不會再次購買或扣款。", "✅ TXT sent. Find the file in this chat. Downloading it does not place another order or charge you.")
	} else if r.Status == "retry" {
		text = tr(locale, "Telegram 暂时限速，文件未发送。请稍后重试。", "Telegram 暫時限速，檔案未發送。請稍後重試。", "Telegram rate limited the upload. Try again later.")
	}
	rows := [][]models.InlineKeyboardButton{}
	if !time.Now().Before(r.NotBefore) && r.Token != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "确认重新发送 TXT", "確認重新發送 TXT", "Confirm TXT resend"), CallbackData: fmt.Sprintf("fileretry:%d:%s", oid, r.Token)}})
	} else {
		text += "\n" + tr(locale, "如需重发，请稍后点击下载，再次确认。", "如需重發，請稍後點擊下載，再次確認。", "To resend, wait and tap Download again, then confirm.")
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回订单", "‹ 返回訂單", "‹ Back to order"), CallbackData: fmt.Sprintf("order:%d", oid)}})
	a.edit(ctx, uid, mid, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
