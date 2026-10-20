package telegram

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/checkout"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func paymentExpired(raw string, now time.Time) bool {
	t, e := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	return e == nil && !now.Before(t)
}

func (a *App) pendingEdit(ctx context.Context, uid int64, mid int, text string, markup models.ReplyMarkup) {
	_, err := a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: uid, MessageID: mid, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		a.logger.Warn("checkout message edit failed")
	}
}
func paymentPollDeadline(now time.Time, raw string) time.Time {
	deadline := now.Add(15 * time.Minute)
	if t, e := time.Parse(time.RFC3339, strings.TrimSpace(raw)); e == nil && t.Before(deadline) {
		return t
	}
	return deadline
}
func pendingDeadline(locale, raw string, now time.Time) string {
	if paymentExpired(raw, now) {
		return tr(locale, "⏰ 订单支付时限已到，请勿再付款。商店状态可能稍后更新，请刷新核对；如已付款，请联系商家，勿重复支付。", "⏰ 訂單付款期限已到，請勿再付款。商店狀態可能稍後更新，請重新整理核對；如已付款，請聯絡商家，勿重複付款。", "⏰ Order deadline reached. Do not pay again. Refresh for the store's final status; if already paid, contact the seller.")
	}
	if t, e := time.Parse(time.RFC3339, strings.TrimSpace(raw)); e == nil {
		remaining := t.Sub(now)
		minutes := remaining / time.Minute
		if remaining%time.Minute > 0 {
			minutes++
		}
		return fmt.Sprintf(tr(locale, "⏳ 本次查看时剩余约 %d 分钟", "⏳ 本次查看時剩餘約 %d 分鐘", "⏳ About %d minutes remaining when refreshed"), minutes) + "\n" + tr(locale, "支付截止（UTC）：", "付款截止（UTC）：", "Pay before (UTC): ") + t.UTC().Format("2006-01-02 15:04:05")
	}
	return tr(locale, "请在商店规定的支付时限内完成付款；已付款请先刷新核对。", "請在商店規定的付款期限內完成付款；已付款請先重新整理核對。", "Pay within the store's time limit. If already paid, refresh before paying again.")
}

func orderListStatus(locale string, o dujiao.OrderListItem) string {
	if o.Status == "pending_payment" && paymentExpired(o.ExpiresAt, time.Now()) {
		return tr(locale, "支付时限已到", "付款期限已到", "Payment deadline reached")
	}
	return orderStatus(locale, o.Status)
}
func pendingBack(locale string, oid uint) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "核对支付 / 继续支付", "核對付款 / 繼續付款", "Check / continue payment"), CallbackData: fmt.Sprintf("resume:%d", oid)}},
		{{Text: tr(locale, "刷新订单", "重新整理訂單", "Refresh order"), CallbackData: fmt.Sprintf("order:%d", oid)}},
		{{Text: tr(locale, "‹ 我的订单", "‹ 我的訂單", "‹ My orders"), CallbackData: "orders:p:1"}},
	}}
}
func (a *App) pendingFailure(ctx context.Context, uid int64, mid int, oid uint) {
	locale := a.sessions.Get(uid).Locale
	a.logger.Warn("checkout state or upstream lookup unavailable")
	a.pendingEdit(ctx, uid, mid, tr(locale, "暂时无法核对支付或取消结果。请先刷新订单并核对支付账户；不要重复付款或提交。", "暫時無法核對付款或取消結果。請先重新整理訂單並核對付款帳戶；不要重複付款或送出。", "Payment/cancellation cannot be confirmed. Refresh the order and check your payment account before submitting again."), pendingBack(locale, oid))
}
func (a *App) pendingState(ctx context.Context, uid int64, oid uint) (checkout.State, error) {
	if a.checkout == nil {
		return checkout.State{}, checkout.ErrStorage
	}
	return a.checkout.Update(ctx, checkout.Key(uid, oid), func(*checkout.State) error { return nil })
}
func (a *App) pendingOrder(ctx context.Context, uid int64, mid int, oid uint) (dujiao.OrderDetail, bool) {
	locale := a.sessions.Get(uid).Locale
	o, e := a.api.Order(ctx, strconv.FormatInt(uid, 10), locale, oid)
	if e != nil || o.OrderID != oid {
		a.pendingFailure(ctx, uid, mid, oid)
		return o, false
	}
	if o.Status != "pending_payment" || paymentExpired(o.ExpiresAt, time.Now()) {
		a.stopPaymentPoll(ctx, uid, oid)
		a.showOrder(ctx, uid, mid, uid, oid)
		return o, false
	}
	return o, true
}
func (a *App) latestPending(ctx context.Context, uid int64, oid uint) (dujiao.PaymentResult, bool, error) {
	p, e := a.api.LatestPayment(ctx, strconv.FormatInt(uid, 10), oid)
	if e != nil {
		var apiErr *dujiao.APIError
		if errors.As(e, &apiErr) && apiErr.HTTPStatus == 404 && apiErr.ErrorCode == "payment_not_found" {
			return p, false, nil
		}
		return p, false, e
	}
	if p.OrderID != oid || p.PaymentID == 0 || p.Status != "pending" || paymentExpired(p.ExpiresAt, time.Now()) {
		return p, false, errors.New("invalid latest payment")
	}
	return p, true, nil
}

func (a *App) handlePendingCallback(ctx context.Context, q *models.CallbackQuery, parts []string) {
	m := q.Message.Message
	if m == nil || q.From.ID <= 0 || m.Chat.ID != q.From.ID || len(parts) < 2 {
		return
	}
	base := 10
	if parts[0] == "pay" && len(parts) == 5 {
		base = 36
	}
	id, e := strconv.ParseUint(parts[1], base, 64)
	if e != nil || id == 0 {
		return
	}
	uid, oid := q.From.ID, uint(id)
	switch parts[0] {
	case "resume":
		if len(parts) == 2 {
			a.showOrderPaymentMethods(ctx, uid, m.ID, uid, oid)
		}
	case "pay":
		if len(parts) == 4 {
			a.showOrderPaymentMethods(ctx, uid, m.ID, uid, oid)
			return
		} // Legacy buttons never submit.
		if len(parts) != 5 || len(parts[2]) != 26 || (parts[4] != "0" && parts[4] != "1") {
			return
		}
		ch, err := strconv.ParseUint(parts[3], 36, 64)
		if err != nil {
			return
		}
		a.createSelectedPayment(ctx, uid, m.ID, oid, uint(ch), parts[4] == "1", parts[2])
	case "payqr":
		if len(parts) == 3 {
			p, err := strconv.ParseUint(parts[2], 10, 64)
			if err == nil && p > 0 {
				a.resumePayment(ctx, uid, m.ID, oid, uint(p))
			}
		}
	case "cancelgo":
		if len(parts) == 3 && len(parts[2]) == 26 {
			a.submitCancelOrder(ctx, uid, m.ID, oid, parts[2])
		}
	case "cancelback":
		if len(parts) == 3 && len(parts[2]) == 26 {
			if a.checkout == nil {
				a.pendingFailure(ctx, uid, m.ID, oid)
				return
			}
			_, err := a.checkout.Update(ctx, checkout.Key(uid, oid), func(s *checkout.State) error {
				if s.CancelNonce != parts[2] || s.CancelMessage != m.ID {
					return checkout.ErrBusy
				}
				s.CancelNonce = ""
				return nil
			})
			if err != nil {
				a.pendingFailure(ctx, uid, m.ID, oid)
				return
			}
			a.showOrderPaymentMethods(ctx, uid, m.ID, uid, oid)
		}
	}
}
func (a *App) showOrderPaymentMethods(ctx context.Context, chatID int64, mid int, uid int64, oid uint) {
	if chatID != uid || uid <= 0 {
		return
	}
	a.resumePayment(ctx, uid, mid, oid, 0)
}
func (a *App) resumePayment(ctx context.Context, uid int64, mid int, oid, wantQR uint) {
	o, ok := a.pendingOrder(ctx, uid, mid, oid)
	if !ok {
		return
	}
	s, err := a.pendingState(ctx, uid, oid)
	if err != nil || checkout.CancelUncertain(s) || s.CancelStatus == "done" {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	p, found, err := a.latestPending(ctx, uid, oid)
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	if found {
		if wantQR != 0 && wantQR != p.PaymentID {
			a.pendingFailure(ctx, uid, mid, oid)
			return
		}
		a.presentExistingPayment(ctx, uid, mid, o, p, false, wantQR != 0)
		return
	}
	if checkout.PaymentUncertain(s) {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	channels, err := a.api.PaymentChannels(ctx, strconv.FormatInt(uid, 10), o.OrderNo)
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	a.presentPaymentMethods(ctx, uid, mid, uid, o, channels)
}
func (a *App) presentPaymentMethods(ctx context.Context, chatID int64, mid int, uid int64, o dujiao.OrderDetail, channels dujiao.PaymentChannelList) {
	if chatID != uid || uid <= 0 || a.checkout == nil {
		a.pendingFailure(ctx, uid, mid, o.OrderID)
		return
	}
	locale := a.sessions.Get(uid).Locale
	nonce := rand.Text()
	now := time.Now()
	allowed := []uint{}
	previousPayment := false
	if !channels.WalletOnlyPayment {
		for _, ch := range channels.Items {
			if ch.ID > 0 {
				allowed = append(allowed, ch.ID)
			}
		}
	}
	_, err := a.checkout.Update(ctx, checkout.Key(uid, o.OrderID), func(s *checkout.State) error {
		if checkout.PaymentUncertain(*s) || checkout.CancelUncertain(*s) || s.CancelStatus == "done" {
			return checkout.ErrBusy
		}
		previousPayment = s.PaymentID > 0
		s.Selection, s.SelectionMessage, s.SelectionUntil, s.Channels = nonce, mid, now.Add(10*time.Minute), allowed
		return nil
	})
	if err != nil {
		a.pendingFailure(ctx, uid, mid, o.OrderID)
		return
	}
	button := func(name string, ch uint, balance string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: truncate(name, 60), CallbackData: "pay:" + strconv.FormatUint(uint64(o.OrderID), 36) + ":" + nonce + ":" + strconv.FormatUint(uint64(ch), 36) + ":" + balance}
	}
	rows := [][]models.InlineKeyboardButton{{button(tr(locale, "余额支付", "餘額付款", "Pay with balance"), 0, "1")}}
	if !channels.WalletOnlyPayment {
		for _, ch := range channels.Items {
			rows = append(rows, []models.InlineKeyboardButton{button(ch.Name, ch.ID, "0")})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回订单", "‹ 返回訂單", "‹ Back to order"), CallbackData: fmt.Sprintf("order:%d", o.OrderID)}})
	text := "<b>" + tr(locale, "选择支付方式", "選擇付款方式", "Choose payment method") + "</b>\n\n" + tr(locale, "订单号：", "訂單號：", "Order: ") + html.EscapeString(truncate(o.OrderNo, 100)) + "\n" + tr(locale, "应付：", "應付：", "Total: ") + html.EscapeString(o.TotalAmount+" "+o.Currency) + "\n" + pendingDeadline(locale, o.ExpiresAt, now)
	if previousPayment {
		text += "\n\n" + tr(locale, "原支付记录已不可继续（可能已超时）。如尚未付款，可重新选择方式；如已付款，请先核对，勿重复转账。", "原付款紀錄已無法繼續（可能已逾時）。如尚未付款，可重新選擇方式；如已付款，請先核對，勿重複轉帳。", "The previous payment is no longer available (possibly expired). Choose again only if unpaid; otherwise check your payment account first.")
	}
	a.pendingEdit(ctx, uid, mid, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}
func (a *App) createSelectedPayment(ctx context.Context, uid int64, mid int, oid, ch uint, balance bool, nonce string) {
	o, ok := a.pendingOrder(ctx, uid, mid, oid)
	if !ok {
		return
	}
	s, err := a.pendingState(ctx, uid, oid)
	if err != nil || checkout.CancelUncertain(s) || s.CancelStatus == "done" {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	p, found, err := a.latestPending(ctx, uid, oid)
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	if found {
		a.presentExistingPayment(ctx, uid, mid, o, p, false, false)
		return
	}
	if checkout.PaymentUncertain(s) {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	locale := a.sessions.Get(uid).Locale
	if balance {
		wallet, e := a.api.Wallet(ctx, strconv.FormatInt(uid, 10))
		need, nok := money.Parse(o.TotalAmount)
		have, hok := money.Parse(wallet.Balance)
		if e != nil || !nok || !hok || wallet.Currency != o.Currency {
			a.pendingFailure(ctx, uid, mid, oid)
			return
		}
		if have < need {
			a.pendingEdit(ctx, uid, mid, tr(locale, "余额不足，未发起扣款。可选择其他支付方式。", "餘額不足，未發起扣款。可選擇其他付款方式。", "Insufficient balance. No charge requested. Choose another method."), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "选择支付方式", "選擇付款方式", "Choose payment method"), CallbackData: fmt.Sprintf("resume:%d", oid)}}}})
			return
		}
	}
	if !a.allowWrite(ctx, uid, uid, operations.Payment) {
		return
	}
	token := rand.Text()
	claimUntil := time.Now().Add(checkout.Lifetime)
	_, err = a.checkout.Update(ctx, checkout.Key(uid, oid), func(s *checkout.State) error {
		if s.Selection == "" || s.Selection != nonce || s.SelectionMessage != mid || !time.Now().Before(s.SelectionUntil) || checkout.PaymentUncertain(*s) || checkout.CancelUncertain(*s) || s.CancelStatus == "done" || (balance && ch != 0) || (!balance && !slices.Contains(s.Channels, ch)) {
			return checkout.ErrBusy
		}
		s.Selection, s.CancelNonce = "", ""
		s.PaymentStatus, s.PaymentToken, s.PaymentID = "posting", token, 0
		s.ExpiresAt = claimUntil
		return nil
	})
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	op := a.ops.Begin(operations.Payment, uid, oid)
	p, postErr := a.api.CreatePayment(ctx, strconv.FormatInt(uid, 10), oid, ch, balance)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	finished, saveErr := a.checkout.Update(finish, checkout.Key(uid, oid), func(s *checkout.State) error {
		if s.PaymentToken != token {
			return checkout.ErrBusy
		}
		s.PaymentStatus = "unknown"
		if postErr == nil && (p.OrderPaid || (p.OrderID == oid && p.PaymentID > 0)) {
			s.PaymentStatus, s.PaymentID = "known", p.PaymentID
		}
		var apiErr *dujiao.APIError
		if errors.As(postErr, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
			switch apiErr.ErrorCode {
			case "validation_error", "order_not_found", "order_status_invalid", "payment_method_unavailable", "wallet_only_payment_required":
				s.PaymentStatus = "rejected"
			}
		}
		return nil
	})
	outcome := operations.Unknown
	if saveErr == nil && finished.PaymentStatus == "known" {
		outcome = operations.Succeeded
	}
	if saveErr == nil && finished.PaymentStatus == "rejected" {
		outcome = operations.Rejected
	}
	op.End(outcome, oid)
	if postErr != nil || saveErr != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	if p.OrderPaid {
		a.stopPaymentPoll(ctx, uid, oid)
		a.showOrder(ctx, uid, mid, uid, oid)
		return
	}
	if p.OrderID != oid || p.PaymentID == 0 || p.Status != "pending" {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	a.presentExistingPayment(ctx, uid, mid, o, p, true, false)
}
func (a *App) presentExistingPayment(ctx context.Context, uid int64, mid int, o dujiao.OrderDetail, p dujiao.PaymentResult, newPayment, showQR bool) {
	locale := a.sessions.Get(uid).Locale
	// A positive, owner-scoped read can resolve a previously uncertain creation.
	_, err := a.checkout.Update(ctx, checkout.Key(uid, o.OrderID), func(s *checkout.State) error {
		if checkout.CancelUncertain(*s) || s.CancelStatus == "done" {
			return checkout.ErrBusy
		}
		s.Selection = ""
		s.PaymentStatus, s.PaymentID = "known", p.PaymentID
		if s.PaymentToken == "" {
			s.PaymentToken = rand.Text()
		}
		return nil
	})
	if err != nil {
		a.pendingFailure(ctx, uid, mid, o.OrderID)
		return
	}
	text := "<b>" + tr(locale, "继续支付", "繼續付款", "Continue payment") + "</b>\n\n" + html.EscapeString(truncate(o.OrderNo, 100)) + "\n" + tr(locale, "实付：", "實付：", "Pay: ") + html.EscapeString(p.Amount+" "+p.Currency)
	text += "\n" + pendingDeadline(locale, o.ExpiresAt, time.Now())
	if p.ExpiresAt != "" {
		text += "\n" + tr(locale, "支付链接截止：", "付款連結截止：", "Payment link expires: ") + html.EscapeString(truncate(p.ExpiresAt, 60))
	}
	if p.WalletAddress != "" {
		text += "\n" + tr(locale, "收款地址：", "收款地址：", "Address: ") + "<code>" + html.EscapeString(p.WalletAddress) + "</code>"
	}
	if p.ChainAmount != "" {
		text += "\n" + tr(locale, "链上金额：", "鏈上金額：", "On-chain amount: ") + "<code>" + html.EscapeString(p.ChainAmount) + "</code>"
	}
	text += "\n\n" + tr(locale, "正在使用原支付记录。已付款请刷新核对，勿重复转账。", "正在使用原付款紀錄。已付款請重新整理核對，勿重複轉帳。", "Using the existing payment. If paid, refresh; do not pay twice.")
	rows := [][]models.InlineKeyboardButton{}
	if strings.HasPrefix(p.PayURL, "https://") || strings.HasPrefix(p.PayURL, "http://") {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "前往支付", "前往付款", "Pay now"), URL: p.PayURL}})
	}
	if p.QRCode != "" || p.PayURL != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "显示二维码", "顯示 QR 碼", "Show QR code"), CallbackData: fmt.Sprintf("payqr:%d:%d", o.OrderID, p.PaymentID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "刷新状态", "重新整理狀態", "Refresh status"), CallbackData: fmt.Sprintf("resume:%d", o.OrderID)}, {Text: tr(locale, "取消订单", "取消訂單", "Cancel order"), CallbackData: fmt.Sprintf("cancelask:%d", o.OrderID)}})
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 我的订单", "‹ 我的訂單", "‹ My orders"), CallbackData: "orders:p:1"}})
	a.pendingEdit(ctx, uid, mid, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	qr := 0
	a.pollMu.Lock()
	existing := a.polls[o.OrderID]
	hasQR := existing != nil && existing.paymentID == p.PaymentID && existing.qrMessageID > 0
	a.pollMu.Unlock()
	if (newPayment || showQR) && !hasQR {
		qr = a.sendPaymentQRCode(ctx, uid, locale, p.QRCode, p.PayURL, p.InteractionMode, p.Amount, p.Currency)
	}
	exp := p.ExpiresAt
	if t, e := time.Parse(time.RFC3339, o.ExpiresAt); e == nil {
		if pt, pe := time.Parse(time.RFC3339, exp); pe != nil || t.Before(pt) {
			exp = o.ExpiresAt
		}
	}
	a.startPaymentPoll(uid, uid, o.OrderID, mid, qr, p.PaymentID, exp)
}

func (a *App) confirmCancelOrder(ctx context.Context, chatID int64, mid int, uid int64, oid uint) {
	if chatID != uid || uid <= 0 {
		return
	}
	locale := a.sessions.Get(uid).Locale
	o, err := a.api.Order(ctx, strconv.FormatInt(uid, 10), locale, oid)
	if err != nil || o.OrderID != oid {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	if o.Status != "pending_payment" {
		a.showOrder(ctx, uid, mid, uid, oid)
		return
	}
	if a.checkout == nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	nonce := rand.Text()
	_, err = a.checkout.Update(ctx, checkout.Key(uid, oid), func(s *checkout.State) error {
		if checkout.PaymentUncertain(*s) || checkout.CancelUncertain(*s) || s.CancelStatus == "done" {
			return checkout.ErrBusy
		}
		s.CancelNonce, s.CancelMessage, s.CancelUntil = nonce, mid, time.Now().Add(5*time.Minute)
		return nil
	})
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	a.stopPaymentPoll(ctx, uid, oid)
	text := tr(locale, "确定取消此订单？", "確定取消此訂單？", "Cancel this order?") + "\n\n" + html.EscapeString(truncate(o.OrderNo, 100)) + "\n" + html.EscapeString(o.TotalAmount+" "+o.Currency) + "\n\n" + tr(locale, "取消后将释放库存，原支付链接请勿再使用。如已转账，请先刷新订单或联系商家，不要取消。", "取消後將釋放庫存，原付款連結請勿再使用。如已轉帳，請先重新整理訂單或聯絡商家，不要取消。", "Cancellation releases stock. Do not use the old payment link afterwards. If you already paid, refresh or contact the seller instead of cancelling.")
	a.pendingEdit(ctx, uid, mid, text, models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "暂不取消，继续支付", "暫不取消，繼續付款", "Keep order / continue payment"), CallbackData: fmt.Sprintf("cancelback:%d:%s", oid, nonce)}},
		{{Text: tr(locale, "确认取消订单", "確認取消訂單", "Confirm cancellation"), CallbackData: fmt.Sprintf("cancelgo:%d:%s", oid, nonce)}},
	}})
}
func (a *App) cancelOrder(ctx context.Context, chatID int64, mid int, uid int64, oid uint) {
	a.confirmCancelOrder(ctx, chatID, mid, uid, oid)
} // Old confirmation requires a fresh review.
func (a *App) submitCancelOrder(ctx context.Context, uid int64, mid int, oid uint, nonce string) {
	locale := a.sessions.Get(uid).Locale
	o, err := a.api.Order(ctx, strconv.FormatInt(uid, 10), locale, oid)
	if err != nil || o.OrderID != oid {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	if o.Status != "pending_payment" {
		a.stopPaymentPoll(ctx, uid, oid)
		a.showOrder(ctx, uid, mid, uid, oid)
		return
	}
	if a.checkout == nil || !a.allowWrite(ctx, uid, uid, operations.OrderCancel) {
		return
	}
	claimUntil := time.Now().Add(checkout.Lifetime)
	_, err = a.checkout.Update(ctx, checkout.Key(uid, oid), func(s *checkout.State) error {
		if s.CancelNonce == "" || s.CancelNonce != nonce || s.CancelMessage != mid || !time.Now().Before(s.CancelUntil) || checkout.PaymentUncertain(*s) || checkout.CancelUncertain(*s) || s.CancelStatus == "done" {
			return checkout.ErrBusy
		}
		s.CancelNonce, s.Selection = "", ""
		s.CancelStatus = "posting"
		s.ExpiresAt = claimUntil
		return nil
	})
	if err != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	op := a.ops.Begin(operations.OrderCancel, uid, oid)
	_, postErr := a.api.CancelOrder(ctx, strconv.FormatInt(uid, 10), oid)
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, saveErr := a.checkout.Update(finish, checkout.Key(uid, oid), func(s *checkout.State) error {
		if s.CancelStatus != "posting" {
			return checkout.ErrBusy
		}
		s.CancelStatus = "unknown"
		if postErr == nil {
			s.CancelStatus = "done"
		}
		return nil
	})
	if postErr == nil && saveErr == nil {
		op.End(operations.Succeeded, oid)
	} else {
		op.End(operations.Unknown, oid)
	}
	if postErr != nil || saveErr != nil {
		a.pendingFailure(ctx, uid, mid, oid)
		return
	}
	a.stopPaymentPoll(ctx, uid, oid)
	a.showOrder(ctx, uid, mid, uid, oid)
}
