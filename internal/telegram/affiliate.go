package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

// The server owns last-click attribution and its expiry. Do not retain a raw
// affiliate code locally or send one on every order, which would bypass expiry.
func affiliateVisitorKey(userID int64) string {
	return "telegram:" + strconv.FormatInt(userID, 10)
}

func affiliateLink(username, code string) string {
	if !telegramLinkToken(username, 32) || len(username) < 5 || !telegramLinkToken(code, 32) {
		return ""
	}
	return "https://t.me/" + username + "?start=aff_" + code
}

func telegramLinkToken(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (a *App) affiliatePrivateChat(ctx context.Context, chatID, userID int64) bool {
	if chatID == userID {
		return true
	}
	locale := a.sessions.Get(userID).Locale
	a.send(ctx, chatID, tr(locale, "请在机器人私聊中使用推广中心。", "請在機器人私訊中使用推廣中心。", "Use the affiliate center in a private chat with the bot."), nil)
	return false
}

func (a *App) acceptAffiliateStart(ctx context.Context, message *models.Message, code string) bool {
	userID := message.From.ID
	if !a.affiliatePrivateChat(ctx, message.Chat.ID, userID) {
		return false
	}
	locale := a.sessions.Get(userID).Locale
	if !telegramLinkToken(code, 32) {
		a.send(ctx, message.Chat.ID, tr(locale, "推广链接格式无效，请向分享者索取新链接，或发送 /start 打开商店。", "推廣連結格式無效，請向分享者索取新連結，或傳送 /start 開啟商店。", "This referral link is invalid. Ask the sender for a new link or send /start to open the store."), nil)
		return false
	}
	err := a.api.TrackAffiliateClick(ctx, dujiao.AffiliateClick{
		ChannelUserID: strconv.FormatInt(userID, 10), AffiliateCode: code,
		VisitorKey: affiliateVisitorKey(userID), LandingPath: "/telegram/start", Referrer: "telegram",
	})
	if err != nil {
		a.logAffiliateFailure(userID, "track affiliate click", err)
		a.send(ctx, message.Chat.ID, tr(locale, "本次推广访问暂时未能记录，不影响浏览和购物。如需保留本次推广归因，请稍后重新打开链接。", "本次推廣造訪暫時無法記錄，不影響瀏覽與購物。如需保留本次推廣歸因，請稍後重新開啟連結。", "This referral visit could not be recorded. Shopping is still available; reopen the link later if you want this referral attributed."), nil)
	}
	return true
}

func (a *App) showAffiliate(ctx context.Context, chatID, userID int64, edit bool, messageID int) {
	if !a.affiliatePrivateChat(ctx, chatID, userID) {
		return
	}
	locale := a.sessions.Get(userID).Locale
	dashboard, err := a.api.AffiliateDashboard(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		a.affiliateFailure(ctx, chatID, userID, err)
		return
	}
	text := "<b>" + tr(locale, "推广中心", "推廣中心", "Affiliate center") + "</b>\n\n"
	rows := [][]models.InlineKeyboardButton{}
	if !dashboard.Opened {
		text += tr(locale, "尚未开通推广。开通后可获取专属链接，佣金规则及结算以商店设置为准。", "尚未開通推廣。開通後可取得專屬連結，佣金規則及結算以商店設定為準。", "Your affiliate profile is not open. Open it to get a referral link. Commission and settlement rules are set by the store.")
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "开通推广", "開通推廣", "Open affiliate profile"), CallbackData: "affopen"}})
	} else {
		currency, ok := a.affiliateCurrency(ctx, chatID, userID)
		if !ok {
			return
		}
		text += tr(locale, "推广码：", "推廣碼：", "Referral code: ") + "<code>" + html.EscapeString(truncate(dashboard.AffiliateCode, 32)) + "</code>\n"
		me, meErr := a.bot.GetMe(ctx)
		link := ""
		if meErr == nil {
			link = affiliateLink(me.Username, dashboard.AffiliateCode)
		}
		if link != "" {
			text += tr(locale, "专属 Bot 链接：", "專屬 Bot 連結：", "Your Bot referral link: ") + "\n<code>" + html.EscapeString(link) + "</code>\n"
			shareURL := "https://t.me/share/url?" + url.Values{"url": {link}}.Encode()
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "分享推广链接", "分享推廣連結", "Share referral link"), URL: shareURL}})
		} else {
			text += tr(locale, "暂时无法生成 Bot 链接，请稍后刷新。", "暫時無法產生 Bot 連結，請稍後重新整理。", "The Bot link is unavailable. Refresh later.") + "\n"
		}
		text += fmt.Sprintf("\n%s%d\n%s%d\n%s%.2f%%", tr(locale, "推广访问：", "推廣造訪：", "Visits: "), dashboard.ClickCount, tr(locale, "有效订单：", "有效訂單：", "Valid orders: "), dashboard.ValidOrderCount, tr(locale, "转化率：", "轉換率：", "Conversion: "), dashboard.ConversionRate)
		text += "\n" + tr(locale, "待确认佣金：", "待確認佣金：", "Pending commission: ") + affiliateMoney(dashboard.PendingCommission, currency)
		text += "\n" + tr(locale, "可用佣金：", "可用佣金：", "Available commission: ") + affiliateMoney(dashboard.AvailableCommission, currency)
		text += "\n" + tr(locale, "已提现佣金：", "已提領佣金：", "Withdrawn commission: ") + affiliateMoney(dashboard.WithdrawnCommission, currency)
		text += "\n" + tr(locale, "最低提现：", "最低提領：", "Minimum withdrawal: ") + affiliateMoney(dashboard.MinWithdrawAmount.String(), currency)
		text += "\n\n" + tr(locale, "数据及归因以商店为准；自购、退款和结算规则由商店校验。提现申请需商店审核，不代表已打款。", "資料與歸因以商店為準；自購、退款與結算規則由商店校驗。提領申請需商店審核，不代表已匯款。", "Attribution and figures come from the store, which validates self-purchases, refunds and settlement. Withdrawal requests require merchant review and do not mean funds have been paid.")
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "佣金明细", "佣金明細", "Commissions"), CallbackData: "affcomm:1"}, {Text: tr(locale, "提现记录", "提領紀錄", "Withdrawals"), CallbackData: "affwithdraws:1"}})
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "申请提现", "申請提領", "Request withdrawal"), CallbackData: "affapply"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "刷新", "重新整理", "Refresh"), CallbackData: "affiliate"}, {Text: tr(locale, "‹ 返回首页", "‹ 返回首頁", "‹ Home"), CallbackData: "home"}})
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) openAffiliate(ctx context.Context, message *models.Message, user *models.User) {
	if !a.affiliatePrivateChat(ctx, message.Chat.ID, user.ID) {
		return
	}
	locale := a.sessions.Get(user.ID).Locale
	profile, err := a.api.OpenAffiliate(ctx, identityFromUser(user, locale))
	if err != nil {
		a.affiliateFailure(ctx, message.Chat.ID, user.ID, err)
		return
	}
	if profile.Status != "active" {
		a.send(ctx, message.Chat.ID, tr(locale, "推广账号已停用，请联系商家。", "推廣帳號已停用，請聯絡商家。", "Your affiliate profile is disabled. Contact the store."), a.backKeyboard(locale))
		return
	}
	a.showAffiliate(ctx, message.Chat.ID, user.ID, true, message.ID)
}

func (a *App) affiliateCurrency(ctx context.Context, chatID, userID int64) (string, bool) {
	wallet, err := a.api.Wallet(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		a.affiliateFailure(ctx, chatID, userID, err)
		return "", false
	}
	if wallet.Currency == "" {
		locale := a.sessions.Get(userID).Locale
		a.send(ctx, chatID, tr(locale, "商店未返回币种，暂时无法准确展示佣金金额。", "商店未回傳幣別，暫時無法準確顯示佣金金額。", "The store did not return a currency. Commission amounts cannot be displayed reliably."), nil)
		return "", false
	}
	return wallet.Currency, true
}

func affiliateMoney(value, currency string) string {
	cents, ok := money.Parse(value)
	if !ok {
		return "—"
	}
	return html.EscapeString(money.Format(cents) + " " + currency)
}

func (a *App) showAffiliateRecords(ctx context.Context, chatID int64, messageID int, userID int64, kind string, page int) {
	if !a.affiliatePrivateChat(ctx, chatID, userID) {
		return
	}
	locale := a.sessions.Get(userID).Locale
	currency, ok := a.affiliateCurrency(ctx, chatID, userID)
	if !ok {
		return
	}
	var text string
	var pages int64
	if kind == "affcomm" {
		result, err := a.api.AffiliateCommissions(ctx, strconv.FormatInt(userID, 10), page)
		if err != nil {
			a.affiliateFailure(ctx, chatID, userID, err)
			return
		}
		pages = result.TotalPages
		text = "<b>" + tr(locale, "佣金明细", "佣金明細", "Commissions") + "</b>"
		for _, row := range result.Items[:min(len(result.Items), 5)] {
			status := affiliateStatus(locale, row.Status)
			if row.Status == "available" && row.WithdrawalRequestID > 0 {
				status = tr(locale, "提现审核中（已冻结）", "提領審核中（已凍結）", "Reserved for withdrawal review")
			}
			text += "\n\n<code>" + html.EscapeString(truncate(row.OrderNo, 32)) + "</code>\n" + affiliateMoney(row.CommissionAmount, currency) + " · " + status
			text += "\n" + tr(locale, "佣金比例：", "佣金比例：", "Rate: ") + html.EscapeString(truncate(row.RatePercent, 12)) + "%\n" + html.EscapeString(truncate(row.CreatedAt, 25))
		}
		if len(result.Items) == 0 {
			text += "\n\n" + tr(locale, "当前页没有佣金记录。", "目前頁面沒有佣金紀錄。", "No commissions on this page.")
		}
	} else {
		result, err := a.api.AffiliateWithdrawals(ctx, strconv.FormatInt(userID, 10), page)
		if err != nil {
			a.affiliateFailure(ctx, chatID, userID, err)
			return
		}
		pages = result.TotalPages
		text = "<b>" + tr(locale, "提现记录", "提領紀錄", "Withdrawals") + "</b>"
		for _, row := range result.Items[:min(len(result.Items), 5)] {
			text += fmt.Sprintf("\n\n#%d · %s · %s", row.ID, affiliateMoney(row.Amount, currency), affiliateStatus(locale, row.Status))
			text += "\n" + html.EscapeString(truncate(row.Channel, 24)) + " · <code>" + html.EscapeString(maskAffiliateAccount(row.Account)) + "</code>\n" + html.EscapeString(truncate(row.CreatedAt, 25))
		}
		if len(result.Items) == 0 {
			text += "\n\n" + tr(locale, "当前页没有提现记录。", "目前頁面沒有提領紀錄。", "No withdrawals on this page.")
		}
	}
	rows := [][]models.InlineKeyboardButton{}
	var pager []models.InlineKeyboardButton
	if page > 1 {
		pager = append(pager, models.InlineKeyboardButton{Text: "‹", CallbackData: fmt.Sprintf("%s:%d", kind, page-1)})
	}
	if int64(page) < pages {
		pager = append(pager, models.InlineKeyboardButton{Text: "›", CallbackData: fmt.Sprintf("%s:%d", kind, page+1)})
	}
	if len(pager) > 0 {
		rows = append(rows, pager)
	}
	if kind == "affwithdraws" {
		state := a.sessions.Get(userID)
		if guard := state.WithdrawalGuard; guard != nil && guard.Status == "unknown" {
			text += "\n\n" + tr(locale, "⚠️ 上一笔申请结果不明。记录可能尚未更新，请联系商家核对；仅在确认可以重新申请后，才解除本地重试保护。解除保护不会撤回商店中的申请。", "⚠️ 上一筆申請結果不明。紀錄可能尚未更新，請聯絡商家核對；僅在確認可以重新申請後，才解除本機重試保護。解除保護不會撤回商店中的申請。", "⚠️ A previous request has an unknown result. History may be delayed. Contact the store and clear local retry protection only after verifying that you can apply again. This does not cancel a store-side request.")
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "已核对，解除重试保护", "已核對，解除重試保護", "Reviewed; clear retry protection"), CallbackData: "wdack:" + guard.ID}})
			a.sessions.Update(userID, func(state *session.State) {
				if current := state.WithdrawalGuard; current != nil && current.ID == guard.ID && current.Status == "unknown" {
					current.ReviewMessageID = messageID
				}
			})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回推广中心", "‹ 返回推廣中心", "‹ Affiliate center"), CallbackData: "affiliate"}})
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func maskAffiliateAccount(account string) string {
	runes := []rune(strings.TrimSpace(account))
	if len(runes) <= 4 {
		return "****"
	}
	return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
}

func affiliateStatus(locale, status string) string {
	switch status {
	case "pending_confirm":
		return tr(locale, "待确认", "待確認", "Pending confirmation")
	case "available":
		return tr(locale, "可用", "可用", "Available")
	case "withdrawn":
		return tr(locale, "已提现", "已提領", "Withdrawn")
	case "pending_review":
		return tr(locale, "待审核", "待審核", "Pending review")
	case "paid":
		return tr(locale, "已打款", "已匯款", "Paid")
	case "rejected":
		return tr(locale, "已拒绝", "已拒絕", "Rejected")
	default:
		return tr(locale, "未知状态", "未知狀態", "Unknown status")
	}
}

func (a *App) affiliateFailure(ctx context.Context, chatID, userID int64, err error) {
	a.logAffiliateFailure(userID, "affiliate request failed", err)
	locale := a.sessions.Get(userID).Locale
	text := tr(locale, "推广服务暂时不可用，请稍后刷新。", "推廣服務暫時無法使用，請稍後重新整理。", "The affiliate service is unavailable. Refresh later.")
	var apiError *dujiao.APIError
	if errors.As(err, &apiError) {
		switch {
		case apiError.ErrorCode == "affiliate_not_opened":
			text = tr(locale, "请先在推广中心开通推广，再申请提现。", "請先在推廣中心開通推廣，再申請提領。", "Open your affiliate profile before requesting a withdrawal.")
		case apiError.ErrorCode == "affiliate_disabled":
			text = tr(locale, "商店暂未开启推广功能，请联系商家。", "商店暫未開啟推廣功能，請聯絡商家。", "The store has not enabled its affiliate program. Contact the store.")
		case apiError.ErrorCode == "user_disabled":
			text = tr(locale, "账号暂不可使用推广功能，请联系商家。", "帳號暫時無法使用推廣功能，請聯絡商家。", "This account cannot use the affiliate program. Contact the store.")
		case apiError.ErrorCode == "invite_code_required" || apiError.ErrorCode == "invite_code_invalid" || apiError.ErrorCode == "user_not_found":
			text = tr(locale, "请先发送 /start 完成商店账号初始化；推广链接不能替代注册邀请码。", "請先傳送 /start 完成商店帳號初始化；推廣連結不能取代註冊邀請碼。", "Send /start to set up your store account first. Referral links do not replace registration invite codes.")
		case apiError.HTTPStatus == http.StatusNotFound || apiError.HTTPStatus == http.StatusNotImplemented:
			text = tr(locale, "当前商店版本暂不支持推广接口，请联系商家确认版本。", "目前商店版本暫不支援推廣介面，請聯絡商家確認版本。", "This store version does not support affiliate endpoints. Contact the store.")
		}
	}
	a.send(ctx, chatID, text, a.backKeyboard(locale))
}

func (a *App) logAffiliateFailure(userID int64, operation string, err error) {
	var apiError *dujiao.APIError
	if errors.As(err, &apiError) {
		a.logger.Warn(operation, "user_id", userID, "error_code", apiError.ErrorCode, "http_status", apiError.HTTPStatus)
	} else {
		a.logger.Warn(operation, "user_id", userID)
	}
}
