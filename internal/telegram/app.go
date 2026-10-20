package telegram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/callback"
	"github.com/dunea/dujiao-next-telegram-bot/internal/delivery"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/money"
	"github.com/dunea/dujiao-next-telegram-bot/internal/operations"
	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
	"github.com/dunea/dujiao-next-telegram-bot/internal/session"
)

const pageSize = 8

type App struct {
	bot             *tgbot.Bot
	api             *dujiao.Client
	sessions        session.Store
	logger          *slog.Logger
	runCtx          context.Context
	ops             *operations.Service
	limiter         ratelimit.Limiter
	limits          ratelimit.Policy
	broadcastStore  broadcast.Store
	broadcastEngine *broadcast.Engine
	broadcastMax    int
	deliveries      delivery.Store

	configMu sync.RWMutex
	config   dujiao.BotConfig
	runCtxMu sync.RWMutex
	pollMu   sync.Mutex
	polls    map[uint]*activePaymentPoll
}

type activePaymentPoll struct {
	cancel      context.CancelFunc
	qrMessageID int
}

func New(token string, api *dujiao.Client, config dujiao.BotConfig, defaultLocale string, pollTimeout time.Duration, serverURL string, logger *slog.Logger, stores ...session.Store) (*App, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Telegram bot token is empty")
	}
	app := &App{
		api:      api,
		sessions: session.NewMemory(defaultLocale),
		logger:   logger,
		config:   config,
		runCtx:   context.Background(),
		polls:    make(map[uint]*activePaymentPoll),
	}
	if len(stores) > 0 && stores[0] != nil {
		app.sessions = stores[0]
	}
	options := []tgbot.Option{
		tgbot.WithWorkers(4),
		tgbot.WithHTTPClient(pollTimeout, &http.Client{Timeout: pollTimeout + 10*time.Second}),
		tgbot.WithDefaultHandler(app.handleUpdate),
		tgbot.WithErrorsHandler(func(err error) { logger.Error("telegram polling error; check connectivity and token configuration") }),
	}
	if serverURL != "" {
		options = append(options, tgbot.WithServerURL(strings.TrimRight(serverURL, "/")))
	}
	bot, err := tgbot.New(token, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize Telegram bot: %w", err)
	}
	app.bot = bot
	if provider, ok := app.sessions.(interface{ DeliveryReceipts() delivery.Store }); ok {
		app.deliveries = provider.DeliveryReceipts()
	}
	app.ConfigureOperations(nil, ratelimit.DefaultPolicy(), logger)
	return app, nil
}

func (a *App) Start(ctx context.Context) {
	a.runCtxMu.Lock()
	a.runCtx = ctx
	a.runCtxMu.Unlock()
	notificationCtx, stopNotifications := context.WithCancel(ctx)
	notificationsDone := make(chan struct{})
	go func() { defer close(notificationsDone); a.ops.Run(notificationCtx) }()
	defer func() { stopNotifications(); <-notificationsDone }()
	broadcastDone := make(chan struct{})
	go func() { defer close(broadcastDone); a.broadcastEngine.Run(notificationCtx) }()
	defer func() { stopNotifications(); <-broadcastDone }()
	if _, err := a.bot.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "start", Description: "打开商店 / Open store"},
		{Command: "products", Description: "浏览商品 / Browse products"},
		{Command: "orders", Description: "我的订单 / My orders"},
		{Command: "wallet", Description: "账户余额 / Wallet"},
		{Command: "recharge", Description: "钱包充值 / Recharge"},
		{Command: "redeem", Description: "礼品卡兑换 / Redeem gift card"},
		{Command: "affiliate", Description: "推广中心 / Affiliate"},
		{Command: "language", Description: "切换语言 / Language"},
		{Command: "help", Description: "帮助 / Help"},
		{Command: "whoami", Description: "查看 Telegram 用户 ID / My ID"},
		{Command: "subscribe", Description: "订阅商店公告 / Subscribe"},
		{Command: "unsubscribe", Description: "退订公告（保留订单通知）/ Unsubscribe"},
		{Command: "cancel", Description: "取消当前操作 / Cancel"},
	}}); err != nil {
		a.logger.Warn("set Telegram commands", "error", err)
	}
	a.bot.Start(ctx)
}

func (a *App) UpdateConfig(config dujiao.BotConfig) {
	a.configMu.Lock()
	a.config = config
	a.configMu.Unlock()
}

func (a *App) Notify(ctx context.Context, userID int64, eventType string, event callback.Event) error {
	state := a.sessions.Get(userID)
	if state.Err != nil {
		return state.Err
	}
	locale := state.Locale
	var text string
	switch eventType {
	case "order-paid":
		text = "✅ " + tr(locale, "订单支付成功，正在处理。", "訂單付款成功，正在處理。", "Your order has been paid and is being processed.")
	case "order-fulfilled":
		text = "🎉 " + tr(locale, "订单已完成交付，请前往商店订单详情查看交付内容。", "訂單已完成交付，請前往商店訂單詳情查看交付內容。", "Your order has been fulfilled. Open the store order details to view delivery.")
	case "wallet-recharge-succeeded":
		text = "✅ " + tr(locale, "余额充值成功：", "餘額儲值成功：", "Wallet recharge succeeded: ") + html.EscapeString(event.Amount+" "+event.Currency)
	default:
		return nil
	}
	if event.OrderID > 0 {
		text += fmt.Sprintf("\n%s%d", tr(locale, "订单 ID：", "訂單 ID：", "Order ID: "), event.OrderID)
	}
	_, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: userID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: a.homeKeyboard(locale)})
	if err == nil {
		a.ops.Record(operations.Event{Action: auditCallbackAction(eventType), Outcome: operations.Succeeded, UserID: userID, ResourceID: event.OrderID, Dedupe: sha256.Sum256([]byte(event.RechargeNo))})
	}
	if err != nil {
		var limited *tgbot.TooManyRequestsError
		if errors.As(err, &limited) {
			return &callback.NotifyError{RetryAfter: time.Duration(min(3600, max(1, limited.RetryAfter))) * time.Second}
		}
		if errors.Is(err, tgbot.ErrorForbidden) {
			return &callback.NotifyError{Permanent: true}
		}
	}
	return err
}

func (a *App) handleUpdate(ctx context.Context, _ *tgbot.Bot, update *models.Update) {
	if update.CallbackQuery != nil {
		a.handleCallback(ctx, update.CallbackQuery)
		return
	}
	if update.Message == nil || update.Message.From == nil || update.Message.Text == "" {
		return
	}
	message := update.Message
	command, argument := parseCommand(message.Text)
	if !a.allowIngress(ctx, message.Chat.ID, message.From.ID, command == "/cancel" || command == "/unsubscribe") {
		return
	}
	if a.handleBroadcastCommand(ctx, message, command) {
		return
	}
	if command == "/whoami" {
		if message.Chat.ID == message.From.ID {
			a.send(ctx, message.Chat.ID, fmt.Sprintf("Telegram 用户 ID：<code>%d</code>", message.From.ID), nil)
		}
		return
	}
	if command == "/admin" {
		if message.Chat.ID != message.From.ID || !a.ops.IsAdmin(message.From.ID) {
			a.ops.Record(operations.Event{Action: operations.Access, Outcome: operations.Blocked, UserID: message.From.ID})
			return
		}
		a.ops.Record(operations.Event{Action: operations.AdminTest, Outcome: operations.Succeeded, UserID: message.From.ID})
		a.send(ctx, message.Chat.ID, "管理员测试已记录（重复事件可能合并）；发送结果请查看审计日志。\n轻量群发：/broadcast\n群发进度与停止：/broadcast_status", nil)
		return
	}
	state := a.sessions.Get(message.From.ID)
	if !a.sessionOK(ctx, message.Chat.ID, state.Err) {
		return
	}
	if a.handleBroadcastInput(ctx, message, command) {
		return
	}
	navigating := false
	switch command {
	case "/start", "/products", "/orders", "/wallet", "/recharge", "/redeem", "/affiliate", "/language", "/help", "/cancel":
		navigating = true
	}
	if state.Flow == "manual_form" && !navigating {
		a.handleFormMessage(ctx, message)
		return
	}
	if state.Flow == "coupon_code" && !navigating {
		a.handleCouponMessage(ctx, message)
		return
	}
	if state.Flow == "purchase_quantity" && !navigating {
		a.handleQuantityMessage(ctx, message)
		return
	}
	if state.Flow == "gift_card" && !navigating {
		a.handleGiftCardMessage(ctx, message)
		return
	}
	if state.Flow == "withdrawal" && !navigating {
		a.handleWithdrawalMessage(ctx, message)
		return
	}
	if navigating && (state.Purchase != nil || state.GiftCard != nil || state.Recharge != nil || state.Quantity != nil || state.Withdrawal != nil) {
		if !a.sessionOK(ctx, message.Chat.ID, a.sessions.ResetFlow(message.From.ID)) {
			return
		}
	}
	if state.Flow == "invite" && !strings.HasPrefix(message.Text, "/") {
		a.finishInvite(ctx, message, strings.TrimSpace(message.Text))
		return
	}
	if state.Flow == "recharge_amount" && !strings.HasPrefix(message.Text, "/") {
		a.handleRechargeAmount(ctx, message)
		return
	}

	switch command {
	case "/start":
		a.start(ctx, message, argument)
	case "/products":
		a.showCategories(ctx, message.Chat.ID, message.From, false, 0)
	case "/orders":
		a.showOrders(ctx, message.Chat.ID, message.From.ID, "", 1, false, 0)
	case "/wallet":
		a.showWallet(ctx, message.Chat.ID, message.From.ID, false, 0)
	case "/recharge":
		a.askRechargeAmount(ctx, message.Chat.ID, message.From.ID, false, 0)
	case "/redeem":
		a.askGiftCard(ctx, message.Chat.ID, message.From.ID)
	case "/affiliate":
		a.showAffiliate(ctx, message.Chat.ID, message.From.ID, false, 0)
	case "/language":
		a.showLanguage(ctx, message.Chat.ID, false, 0)
	case "/help":
		a.showHelp(ctx, message.Chat.ID, state.Locale)
	case "/cancel":
		if !a.sessionOK(ctx, message.Chat.ID, a.sessions.ResetFlow(message.From.ID)) {
			return
		}
		if state.WithdrawalGuard != nil {
			a.send(ctx, message.Chat.ID, tr(state.Locale, "已取消当前填写；已发出的提现申请无法通过 /cancel 撤回，请核对提现记录。", "已取消目前填寫；已送出的提領申請無法透過 /cancel 撤回，請核對提領紀錄。", "Current input cancelled. An already submitted withdrawal cannot be recalled with /cancel; check withdrawal history."), nil)
			return
		}
		a.send(ctx, message.Chat.ID, tr(state.Locale, "已取消当前操作。", "已取消目前操作。", "Current operation cancelled."), nil)
	default:
		a.send(ctx, message.Chat.ID, tr(state.Locale, "请使用下方菜单选择功能。", "請使用下方選單選擇功能。", "Please choose an action below."), a.homeKeyboard(state.Locale))
	}
}

func (a *App) start(ctx context.Context, message *models.Message, inviteCode string) {
	user := message.From
	if strings.HasPrefix(inviteCode, "aff_") {
		if !a.acceptAffiliateStart(ctx, message, strings.TrimPrefix(inviteCode, "aff_")) {
			return
		}
		inviteCode = "" // Referral links are not registration invite codes.
	}
	locale := normalizeLocale(user.LanguageCode)
	state := a.sessions.Get(user.ID)
	if state.Locale != "" {
		locale = state.Locale
	}
	identity := identityFromUser(user, locale)
	result, err := a.api.ResolveIdentity(ctx, identity)
	if err == nil && result.Bound {
		a.welcome(ctx, message.Chat.ID, user.ID, locale)
		return
	}
	if err != nil && !isAPIError(err, "identity_not_found") {
		a.handleIdentityError(ctx, message.Chat.ID, user.ID, locale, err)
		return
	}
	identity.InviteCode = inviteCode
	if _, err = a.api.ProvisionIdentity(ctx, identity); err != nil {
		a.handleIdentityError(ctx, message.Chat.ID, user.ID, locale, err)
		return
	}
	a.welcome(ctx, message.Chat.ID, user.ID, locale)
}

func (a *App) finishInvite(ctx context.Context, message *models.Message, code string) {
	locale := a.sessions.Get(message.From.ID).Locale
	identity := identityFromUser(message.From, locale)
	identity.InviteCode = code
	if _, err := a.api.ProvisionIdentity(ctx, identity); err != nil {
		a.handleIdentityError(ctx, message.Chat.ID, message.From.ID, locale, err)
		return
	}
	if !a.sessionOK(ctx, message.Chat.ID, a.sessions.ResetFlow(message.From.ID)) {
		return
	}
	a.welcome(ctx, message.Chat.ID, message.From.ID, locale)
}

func (a *App) handleIdentityError(ctx context.Context, chatID, userID int64, locale string, err error) {
	if isAPIError(err, "invite_code_required") || isAPIError(err, "invite_code_invalid") {
		result := a.sessions.Update(userID, func(state *session.State) { state.Flow = "invite" })
		if !a.sessionOK(ctx, chatID, result.Err) {
			return
		}
		message := tr(locale, "请输入有效的邀请码，或发送 /cancel 取消。", "請輸入有效的邀請碼，或傳送 /cancel 取消。", "Please enter a valid invite code, or send /cancel.")
		a.send(ctx, chatID, message, nil)
		return
	}
	a.logger.Error("resolve Telegram identity", "user_id", userID, "error", err)
	a.send(ctx, chatID, tr(locale, "暂时无法连接商店，请稍后再试。", "暫時無法連線商店，請稍後再試。", "The store is temporarily unavailable. Please try again later."), nil)
}

func (a *App) welcome(ctx context.Context, chatID, userID int64, locale string) {
	result := a.sessions.Update(userID, func(state *session.State) { state.Locale = locale; state.Flow = "" })
	if !a.sessionOK(ctx, chatID, result.Err) {
		return
	}
	config := a.botConfig()
	message := localized(config.Welcome.Message, locale)
	if message == "" {
		name := config.Basic.DisplayName
		if name == "" {
			name = "Dujiao Next"
		}
		message = tr(locale, "欢迎来到 "+name+"！", "歡迎來到 "+name+"！", "Welcome to "+name+"!")
	} else {
		message = html.EscapeString(message)
	}
	a.send(ctx, chatID, message, a.homeKeyboard(locale))
}

func (a *App) handleCallback(ctx context.Context, query *models.CallbackQuery) {
	message := query.Message.Message
	if message == nil {
		return
	}
	// Reject flooding before session reads and ACK with no extra chat message.
	if !a.allowCallbackIngress(ctx, query) {
		return
	}
	_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
	if strings.HasPrefix(query.Data, "bc:") || query.Data == "bcunsubscribe" || query.Data == "bcsubscribe" || query.Data == "bcsubscriptions" || strings.HasPrefix(query.Data, "bcjob:") {
		a.handleBroadcastCallback(ctx, query)
		return
	}
	if message.Chat.ID == query.From.ID && a.ops.IsAdmin(query.From.ID) && query.Data != "none" && query.Data != "noop" {
		// Switching back to shopping invalidates announcement drafts, so an
		// old final-confirmation message cannot outlive that navigation.
		if _, err := a.broadcastStore.Update(ctx, func(s *broadcast.State) error { delete(s.Drafts, query.From.ID); return nil }); err != nil {
			a.broadcastFailure(ctx, query.From.ID, err)
			return
		}
	}
	state := a.sessions.Get(query.From.ID)
	if !a.sessionOK(ctx, message.Chat.ID, state.Err) {
		return
	}
	locale := state.Locale
	parts := strings.Split(query.Data, ":")
	if parts[0] == "file" || parts[0] == "fileretry" {
		a.handleDeliveryCallback(ctx, query, parts)
		return
	}
	if parts[0] == "qtyinput" || parts[0] == "qtyback" || parts[0] == "qtyedit" {
		a.handleQuantityCallback(ctx, query, parts)
		return
	}
	if parts[0] == "wd" || parts[0] == "wdack" {
		a.handleWithdrawalCallback(ctx, query, parts)
		return
	}
	if parts[0] == "rechargepick" || parts[0] == "rechargepay" || parts[0] == "rechargepage" {
		a.handleRechargeCallback(ctx, query, parts)
		return
	}
	if parts[0] == "coupon" {
		a.handleCouponCallback(ctx, query, parts)
		return
	}
	if parts[0] == "form" || parts[0] == "formconfirm" || parts[0] == "formedit" {
		a.handleFormCallback(ctx, query, parts)
		return
	}
	if parts[0] != "none" && parts[0] != "noop" {
		if !a.sessionOK(ctx, message.Chat.ID, a.sessions.ResetFlow(query.From.ID)) {
			return
		}
	}
	switch parts[0] {
	case "home":
		a.edit(ctx, message.Chat.ID, message.ID, tr(locale, "请选择功能：", "請選擇功能：", "Choose an action:"), a.homeKeyboard(locale))
	case "cats":
		a.showCategories(ctx, message.Chat.ID, &query.From, true, message.ID)
	case "cat":
		if len(parts) == 3 {
			categoryID, _ := strconv.ParseUint(parts[1], 10, 64)
			page, _ := strconv.Atoi(parts[2])
			a.showProducts(ctx, message.Chat.ID, query.From.ID, uint(categoryID), page, true, message.ID)
		}
	case "product":
		if len(parts) == 2 {
			productID, _ := strconv.ParseUint(parts[1], 10, 64)
			a.showProduct(ctx, message.Chat.ID, query.From.ID, uint(productID), true, message.ID)
		}
	case "qty":
		if len(parts) == 4 {
			productID, _ := strconv.ParseUint(parts[1], 10, 64)
			skuID, _ := strconv.ParseUint(parts[2], 10, 64)
			quantity, _ := strconv.Atoi(parts[3])
			a.showQuantitySelector(ctx, message.Chat.ID, message.ID, query.From.ID, uint(productID), uint(skuID), quantity)
		}
	case "productinfo":
		if len(parts) == 4 {
			productID, _ := strconv.ParseUint(parts[1], 10, 64)
			skuID, _ := strconv.ParseUint(parts[2], 10, 64)
			quantity, _ := strconv.Atoi(parts[3])
			a.showProductInfo(ctx, message.Chat.ID, message.ID, query.From.ID, uint(productID), uint(skuID), quantity)
		}
	case "buy":
		if len(parts) == 4 {
			productID, _ := strconv.ParseUint(parts[1], 10, 64)
			skuID, _ := strconv.ParseUint(parts[2], 10, 64)
			quantity, _ := strconv.Atoi(parts[3])
			a.previewOrder(ctx, message.Chat.ID, message.ID, &query.From, uint(productID), uint(skuID), quantity)
		}
	case "confirm":
		if len(parts) == 4 {
			productID, _ := strconv.ParseUint(parts[1], 10, 64)
			skuID, _ := strconv.ParseUint(parts[2], 10, 64)
			quantity, _ := strconv.Atoi(parts[3])
			a.createOrder(ctx, message.Chat.ID, message.ID, &query.From, uint(productID), uint(skuID), quantity)
		}
	case "pay":
		if len(parts) == 4 {
			orderID, _ := strconv.ParseUint(parts[1], 10, 64)
			channelID, _ := strconv.ParseUint(parts[2], 10, 64)
			useBalance := parts[3] == "1"
			a.createPayment(ctx, message.Chat.ID, message.ID, query.From.ID, uint(orderID), uint(channelID), useBalance)
		}
	case "orders":
		status := ""
		page := 1
		if len(parts) == 2 {
			page, _ = strconv.Atoi(parts[1])
		} else if len(parts) == 3 {
			status = orderFilterStatus(parts[1])
			page, _ = strconv.Atoi(parts[2])
		}
		a.showOrders(ctx, message.Chat.ID, query.From.ID, status, page, true, message.ID)
	case "order":
		if len(parts) == 2 {
			orderID, _ := strconv.ParseUint(parts[1], 10, 64)
			a.showOrder(ctx, message.Chat.ID, message.ID, query.From.ID, uint(orderID))
		}
	case "orderpay":
		if len(parts) == 2 {
			orderID, _ := strconv.ParseUint(parts[1], 10, 64)
			a.showOrderPaymentMethods(ctx, message.Chat.ID, message.ID, query.From.ID, uint(orderID))
		}
	case "cancelask":
		if len(parts) == 2 {
			orderID, _ := strconv.ParseUint(parts[1], 10, 64)
			a.confirmCancelOrder(ctx, message.Chat.ID, message.ID, query.From.ID, uint(orderID))
		}
	case "cancelorder":
		if len(parts) == 2 {
			orderID, _ := strconv.ParseUint(parts[1], 10, 64)
			a.cancelOrder(ctx, message.Chat.ID, message.ID, query.From.ID, uint(orderID))
		}
	case "wallet":
		a.showWallet(ctx, message.Chat.ID, query.From.ID, true, message.ID)
	case "walletlog":
		page := 1
		if len(parts) == 2 {
			page, _ = strconv.Atoi(parts[1])
		}
		a.showWalletTransactions(ctx, message.Chat.ID, query.From.ID, page, true, message.ID)
	case "recharge":
		a.askRechargeAmount(ctx, message.Chat.ID, query.From.ID, true, message.ID)
	case "giftcard":
		a.askGiftCard(ctx, message.Chat.ID, query.From.ID)
	case "affiliate":
		a.showAffiliate(ctx, message.Chat.ID, query.From.ID, true, message.ID)
	case "affopen":
		a.openAffiliate(ctx, message, &query.From)
	case "affapply":
		a.startWithdrawal(ctx, message.Chat.ID, query.From.ID)
	case "affcomm", "affwithdraws":
		if len(parts) == 2 {
			page, err := strconv.Atoi(parts[1])
			if err == nil && page >= 1 && page <= 100000 {
				a.showAffiliateRecords(ctx, message.Chat.ID, message.ID, query.From.ID, parts[0], page)
			}
		}
	case "lang":
		a.showLanguage(ctx, message.Chat.ID, true, message.ID)
	case "setlang":
		if len(parts) == 2 && validLocale(parts[1]) {
			locale = parts[1]
			result := a.sessions.Update(query.From.ID, func(state *session.State) { state.Locale = locale })
			if !a.sessionOK(ctx, message.Chat.ID, result.Err) {
				return
			}
			a.edit(ctx, message.Chat.ID, message.ID, tr(locale, "语言已切换。", "語言已切換。", "Language updated."), a.homeKeyboard(locale))
		}
	case "help":
		a.showHelpEdit(ctx, message.Chat.ID, message.ID, locale)
	case "none":
		return
	case "noop":
		_, _ = a.bot.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            tr(locale, "该商品暂不支持机器人下单。", "此商品暫不支援機器人下單。", "This product cannot be ordered in the bot yet."),
			ShowAlert:       true,
		})
	}
}

func (a *App) showCategories(ctx context.Context, chatID int64, user *models.User, edit bool, messageID int) {
	locale := a.sessions.Get(user.ID).Locale
	if _, err := a.api.ResolveIdentity(ctx, identityFromUser(user, locale)); err != nil {
		a.send(ctx, chatID, tr(locale, "请先发送 /start 绑定商店账号。", "請先傳送 /start 綁定商店帳號。", "Send /start first to connect your store account."), nil)
		return
	}
	categories, err := a.api.Categories(ctx, locale)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "list categories", err)
		return
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(categories)+2)
	for _, category := range categories {
		label := category.Name
		if category.ProductCount > 0 {
			label += fmt.Sprintf(" (%d)", category.ProductCount)
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("cat:%d:1", category.ID)}})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: tr(locale, "全部商品", "全部商品", "All products"), CallbackData: "cat:0:1"}},
		[]models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "home"}},
	)
	text := tr(locale, "请选择商品分类：", "請選擇商品分類：", "Choose a product category:")
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) showProducts(ctx context.Context, chatID, userID int64, categoryID uint, page int, edit bool, messageID int) {
	if page < 1 {
		page = 1
	}
	locale := a.sessions.Get(userID).Locale
	products, err := a.api.Products(ctx, locale, strconv.FormatInt(userID, 10), categoryID, page, pageSize)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "list products", err)
		return
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(products.Items)+2)
	for _, product := range products.Items {
		label := product.Title
		if product.PriceFrom != "" {
			label += " · " + product.PriceFrom + " " + product.Currency
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 60), CallbackData: fmt.Sprintf("product:%d", product.ID)}})
	}
	var pager []models.InlineKeyboardButton
	if page > 1 {
		pager = append(pager, models.InlineKeyboardButton{Text: "‹", CallbackData: fmt.Sprintf("cat:%d:%d", categoryID, page-1)})
	}
	if int64(page) < products.TotalPage {
		pager = append(pager, models.InlineKeyboardButton{Text: "›", CallbackData: fmt.Sprintf("cat:%d:%d", categoryID, page+1)})
	}
	if len(pager) > 0 {
		rows = append(rows, pager)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 分类", "‹ 分類", "‹ Categories"), CallbackData: "cats"}})
	text := tr(locale, "商品列表", "商品列表", "Products")
	if len(products.Items) == 0 {
		text = tr(locale, "这个分类暂时没有商品。", "這個分類暫時沒有商品。", "There are no products in this category yet.")
	}
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) showProduct(ctx context.Context, chatID, userID int64, productID uint, edit bool, messageID int) {
	locale := a.sessions.Get(userID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(userID, 10), productID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get product", err)
		return
	}
	if len(product.SKUs) <= 1 {
		var skuID uint
		if len(product.SKUs) == 1 {
			skuID = product.SKUs[0].ID
		}
		minimum, _ := purchaseQuantityLimits(product)
		a.publishQuantitySelector(ctx, chatID, messageID, userID, product, skuID, minimum, edit)
		return
	}
	price := product.PriceFrom
	if product.MemberPriceFrom != "" {
		price = product.MemberPriceFrom
	}
	text := "<b>" + html.EscapeString(product.Title) + "</b>"
	if price != "" {
		text += "\n\n" + tr(locale, "价格：", "價格：", "Price: ") + html.EscapeString(price+" "+product.Currency)
	}
	if product.StockDisplay != "" {
		text += "\n" + tr(locale, "库存：", "庫存：", "Stock: ") + html.EscapeString(product.StockDisplay)
	}
	minimum, maximum := purchaseQuantityLimits(product)
	if maximum > 0 {
		text += fmt.Sprintf("\n%s%d–%d", tr(locale, "购买数量：", "購買數量：", "Quantity: "), minimum, maximum)
	} else if minimum > 1 {
		text += fmt.Sprintf("\n%s%d", tr(locale, "起购数量：", "起購數量：", "Minimum quantity: "), minimum)
	}
	description := strings.TrimSpace(product.Summary)
	if description == "" {
		description = strings.TrimSpace(product.Description)
	}
	if description != "" {
		text += "\n\n" + html.EscapeString(truncate(description, 2500))
	}
	quantity := minimum
	rows := make([][]models.InlineKeyboardButton, 0, len(product.SKUs)+2)
	if product.StockStatus == "out_of_stock" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "已售罄", "已售罄", "Out of stock"), CallbackData: "noop"}})
	} else if len(product.SKUs) == 0 {
		_, maximum, available := purchaseQuantityBounds(product, 0)
		if !available {
			rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "库存不足", "庫存不足", "Insufficient stock"), CallbackData: "noop"}})
		} else {
			label := tr(locale, "选择数量", "選擇數量", "Choose quantity")
			if maximum == quantity {
				label = tr(locale, "立即购买", "立即購買", "Buy now")
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("qty:%d:0:%d", product.ID, quantity)}})
		}
	} else {
		for _, sku := range product.SKUs {
			label := sku.SpecValues
			if label == "" {
				label = sku.SKUCode
			}
			price := sku.Price
			if sku.MemberPrice != "" {
				price = sku.MemberPrice
			}
			if price != "" {
				label += " · " + price + " " + product.Currency
			}
			callback := "noop"
			if _, _, available := purchaseQuantityBounds(product, sku.ID); available {
				callback = fmt.Sprintf("qty:%d:%d:%d", product.ID, sku.ID, quantity)
			} else {
				label += " · " + tr(locale, "库存不足", "庫存不足", "Unavailable")
			}
			rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 60), CallbackData: callback}})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 商品列表", "‹ 商品列表", "‹ Products"), CallbackData: "cat:0:1"}})
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) showQuantitySelector(ctx context.Context, chatID int64, messageID int, userID int64, productID, skuID uint, quantity int) {
	locale := a.sessions.Get(userID).Locale
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(userID, 10), productID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get product quantity options", err)
		return
	}
	a.publishQuantitySelector(ctx, chatID, messageID, userID, product, skuID, quantity, true)
}

func (a *App) validatePurchaseQuantity(ctx context.Context, chatID int64, messageID int, userID int64, locale string, productID, skuID uint, quantity int) bool {
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(userID, 10), productID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "validate product quantity", err)
		return false
	}
	minimum, maximum, available := purchaseQuantityBounds(product, skuID)
	if available && quantity >= minimum && (maximum == 0 || quantity <= maximum) {
		return true
	}
	message := tr(locale, "所选数量已超过当前购买限制或库存，请重新选择。", "所選數量已超過目前購買限制或庫存，請重新選擇。", "The selected quantity exceeds the current purchase limit or stock. Choose again.")
	if !available {
		message = tr(locale, "该商品或规格当前不可购买，请重新选择。", "該商品或規格目前無法購買，請重新選擇。", "This product or option is currently unavailable. Choose again.")
	} else if maximum > 0 {
		message += fmt.Sprintf("\n%s%d–%d", tr(locale, "可选范围：", "可選範圍：", "Allowed range: "), minimum, maximum)
	} else {
		message += fmt.Sprintf("\n%s%d+", tr(locale, "可选范围：", "可選範圍：", "Allowed range: "), minimum)
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	if available {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "重新选择数量", "重新選擇數量", "Choose quantity"), CallbackData: fmt.Sprintf("qty:%d:%d:%d", productID, skuID, minimum)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Back to product"), CallbackData: fmt.Sprintf("product:%d", productID)}})
	a.edit(ctx, chatID, messageID, message, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	return false
}

func (a *App) previewOrder(ctx context.Context, chatID int64, messageID int, user *models.User, productID, skuID uint, quantity int) {
	locale := a.sessions.Get(user.ID).Locale
	if !a.validatePurchaseQuantity(ctx, chatID, messageID, user.ID, locale, productID, skuID, quantity) {
		return
	}
	product, err := a.api.Product(ctx, locale, strconv.FormatInt(user.ID, 10), productID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get purchase form", err)
		return
	}
	if len(product.ManualFormSchema.Fields) > 0 {
		a.startManualForm(ctx, chatID, messageID, user, product, skuID, quantity)
		return
	}
	draft, err := newPurchaseDraft(chatID, product, skuID, quantity)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "create checkout", err)
		return
	}
	draft.PreviewID = messageID
	result := a.sessions.Update(user.ID, func(state *session.State) { state.Flow = ""; state.Purchase = draft })
	if !a.sessionOK(ctx, chatID, result.Err) {
		return
	}
	a.previewForm(ctx, user, draft)
}

func (a *App) renderOrderPreview(ctx context.Context, chatID int64, messageID int, locale string, request dujiao.OrderRequest, confirmCallback, formSummary string) (dujiao.OrderPreview, error) {
	productID := request.Items[0].ProductID
	preview, err := a.api.PreviewOrder(ctx, request)
	if err != nil {
		return preview, err
	}
	if !preview.Valid {
		return preview, errors.New("order preview validation failed")
	}
	title := tr(locale, "确认订单", "確認訂單", "Confirm order")
	text := "<b>" + title + "</b>"
	for _, item := range preview.Items {
		text += "\n\n" + html.EscapeString(item.ProductTitle)
		if item.SKUName != "" {
			text += " · " + html.EscapeString(item.SKUName)
		}
		text += fmt.Sprintf("\n× %d　%s %s", item.Quantity, html.EscapeString(item.Subtotal), html.EscapeString(preview.Currency))
	}
	original, originalOK := money.Parse(preview.OriginalAmount)
	total, totalOK := money.Parse(preview.TotalAmount)
	if preview.OriginalAmount != "" && (!originalOK || !totalOK || original != total) {
		text += "\n\n" + tr(locale, "原价：", "原價：", "Original amount: ") + html.EscapeString(preview.OriginalAmount+" "+preview.Currency)
	}
	for _, discount := range []struct{ label, amount string }{
		{tr(locale, "优惠券优惠：", "優惠券折扣：", "Coupon discount: "), preview.CouponDiscount},
		{tr(locale, "活动优惠：", "活動折扣：", "Promotion discount: "), preview.PromotionDiscount},
		{tr(locale, "批量优惠：", "批量折扣：", "Wholesale discount: "), preview.WholesaleDiscount},
	} {
		if amount, err := strconv.ParseFloat(discount.amount, 64); err == nil && amount > 0 {
			text += "\n" + discount.label + "−" + html.EscapeString(discount.amount+" "+preview.Currency)
		}
	}
	if request.CouponCode != "" {
		text += "\n" + tr(locale, "优惠码：", "優惠碼：", "Coupon: ") + "<code>" + html.EscapeString(request.CouponCode) + "</code>"
	}
	text += "\n<b>" + tr(locale, "应付：", "應付：", "Total: ") + html.EscapeString(preview.TotalAmount+" "+preview.Currency) + "</b>"
	text += formSummary
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "确认下单", "確認下單", "Place order"), CallbackData: confirmCallback}},
		{{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Back to product"), CallbackData: fmt.Sprintf("product:%d", productID)}},
	}}
	if formSummary != "" {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard[:1],
			[]models.InlineKeyboardButton{{Text: tr(locale, "修改资料", "修改資料", "Edit details"), CallbackData: strings.Replace(confirmCallback, "formconfirm:", "formedit:", 1)}},
			keyboard.InlineKeyboard[1],
		)
	}
	parts := strings.Split(confirmCallback, ":")
	if len(parts) == 3 {
		rows := couponButtons(locale, parts[1], parts[2], request.CouponCode)
		rows[0] = append(rows[0], models.InlineKeyboardButton{Text: tr(locale, "修改数量", "修改數量", "Edit quantity"), CallbackData: "qtyedit:" + parts[1] + ":" + parts[2]})
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard[:1], append(rows, keyboard.InlineKeyboard[1:]...)...)
	}
	a.edit(ctx, chatID, messageID, text, keyboard)
	return preview, nil
}

func (a *App) createOrder(ctx context.Context, chatID int64, messageID int, user *models.User, productID, skuID uint, quantity int) {
	// Legacy confirmation buttons open a fresh preview instead of bypassing the
	// current draft's coupon and one-time confirmation.
	a.previewOrder(ctx, chatID, messageID, user, productID, skuID, quantity)
}

func (a *App) createOrderWithData(ctx context.Context, chatID int64, messageID int, user *models.User, productID, skuID uint, quantity int, formData map[string]any, couponCode string) error {
	locale := a.sessions.Get(user.ID).Locale
	a.edit(ctx, chatID, messageID, tr(locale, "正在创建订单…", "正在建立訂單…", "Creating order…"), nil)
	request := dujiao.OrderRequest{
		TelegramIdentity:    identityFromUser(user, locale),
		Items:               []dujiao.OrderItemRequest{{ProductID: productID, SKUID: skuID, Quantity: quantity}},
		ManualFormData:      formData,
		CouponCode:          couponCode,
		AffiliateVisitorKey: affiliateVisitorKey(user.ID),
	}
	op := a.ops.Begin(operations.Order, user.ID, 0)
	order, err := a.api.CreateOrder(ctx, request)
	op.End(financialOutcome(err), order.OrderID)
	if err != nil {
		if isAPIError(err, "coupon_invalid") {
			return err
		}
		a.logger.Error("create Dujiao order failed", "user_id", user.ID)
		a.edit(ctx, chatID, messageID, tr(locale, "订单创建失败，请返回商品后重试。", "訂單建立失敗，請返回商品後再試。", "Could not create the order. Return to the product and try again."), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "‹ 返回商品", "‹ 返回商品", "‹ Back"), CallbackData: fmt.Sprintf("product:%d", productID)}}}})
		return err
	}
	channels, err := a.api.PaymentChannels(ctx, strconv.FormatInt(user.ID, 10), order.OrderNo)
	if err != nil {
		a.logger.Error("list payment channels", "order_id", order.OrderID, "error", err)
		a.edit(ctx, chatID, messageID, tr(locale, "订单已创建，但暂时无法获取支付方式。订单号：", "訂單已建立，但暫時無法取得付款方式。訂單號：", "The order was created, but payment methods are unavailable. Order: ")+html.EscapeString(order.OrderNo), a.homeKeyboard(locale))
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(channels.Items)+2)
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "余额支付", "餘額付款", "Pay with balance"), CallbackData: fmt.Sprintf("pay:%d:0:1", order.OrderID)}})
	for _, channel := range channels.Items {
		rows = append(rows, []models.InlineKeyboardButton{{Text: channel.Name, CallbackData: fmt.Sprintf("pay:%d:%d:0", order.OrderID, channel.ID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "返回首页", "返回首頁", "Home"), CallbackData: "home"}})
	text := "<b>" + tr(locale, "订单创建成功", "訂單建立成功", "Order created") + "</b>\n\n"
	text += tr(locale, "订单号：", "訂單號：", "Order: ") + html.EscapeString(order.OrderNo)
	text += "\n" + tr(locale, "应付：", "應付：", "Total: ") + html.EscapeString(order.TotalAmount+" "+order.Currency)
	text += "\n\n" + tr(locale, "请选择支付方式：", "請選擇付款方式：", "Choose a payment method:")
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	return nil
}

func (a *App) createPayment(ctx context.Context, chatID int64, messageID int, userID int64, orderID, channelID uint, useBalance bool) {
	if !a.allowWrite(ctx, chatID, userID, operations.Payment) {
		return
	}
	locale := a.sessions.Get(userID).Locale
	op := a.ops.Begin(operations.Payment, userID, orderID)
	result, err := a.api.CreatePayment(ctx, strconv.FormatInt(userID, 10), orderID, channelID, useBalance)
	op.End(financialOutcome(err), orderID)
	if err != nil {
		a.logger.Error("create Dujiao payment failed", "order_id", orderID)
		a.edit(ctx, chatID, messageID, tr(locale, "创建支付失败，可能是余额不足或支付方式暂不可用。", "建立付款失敗，可能是餘額不足或付款方式暫時無法使用。", "Could not create the payment. Your balance may be insufficient or the method unavailable."), a.homeKeyboard(locale))
		return
	}
	if result.OrderPaid {
		keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: tr(locale, "查看订单", "查看訂單", "View order"), CallbackData: fmt.Sprintf("order:%d", orderID)}},
			{{Text: tr(locale, "返回首页", "返回首頁", "Home"), CallbackData: "home"}},
		}}
		a.edit(ctx, chatID, messageID, "✅ "+tr(locale, "支付成功，系统正在处理订单。", "付款成功，系統正在處理訂單。", "Payment successful. Your order is being processed."), keyboard)
		return
	}
	text := "<b>" + tr(locale, "请完成支付", "請完成付款", "Complete payment") + "</b>"
	if result.Amount != "" {
		text += "\n\n" + tr(locale, "金额：", "金額：", "Amount: ") + html.EscapeString(result.Amount+" "+result.Currency)
	}
	if result.WalletAddress != "" {
		text += "\n" + tr(locale, "收款地址：", "收款地址：", "Address: ") + "<code>" + html.EscapeString(result.WalletAddress) + "</code>"
	}
	if result.ChainAmount != "" {
		text += "\n" + tr(locale, "链上金额：", "鏈上金額：", "On-chain amount: ") + "<code>" + html.EscapeString(result.ChainAmount) + "</code>"
	}
	rows := make([][]models.InlineKeyboardButton, 0, 4)
	if strings.HasPrefix(result.PayURL, "https://") || strings.HasPrefix(result.PayURL, "http://") {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "前往支付", "前往付款", "Pay now"), URL: result.PayURL}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "刷新支付状态", "重新整理付款狀態", "Refresh payment status"), CallbackData: fmt.Sprintf("order:%d", orderID)}})
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "取消订单", "取消訂單", "Cancel order"), CallbackData: fmt.Sprintf("cancelask:%d", orderID)}})
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "返回首页", "返回首頁", "Home"), CallbackData: "home"}})
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	qrMessageID := a.sendPaymentQRCode(ctx, chatID, locale, result.QRCode, result.PayURL, result.InteractionMode, result.Amount, result.Currency)
	a.startPaymentPoll(chatID, userID, orderID, messageID, qrMessageID, result.ExpiresAt)
}

func (a *App) sendPaymentQRCode(ctx context.Context, chatID int64, locale, qrCode, payURL, interactionMode, amount, currency string) int {
	image, ok, err := buildPaymentQRImage(qrCode, payURL, interactionMode)
	if err != nil {
		a.logger.Warn("build payment QR code", "error", err)
		return 0
	}
	if !ok {
		return 0
	}
	caption := tr(locale, "请扫描二维码完成支付。", "請掃描二維碼完成付款。", "Scan the QR code to complete payment.")
	if amount != "" {
		caption += "\n" + tr(locale, "金额：", "金額：", "Amount: ") + html.EscapeString(amount+" "+currency)
	}
	var photo models.InputFile
	if image.URL != "" {
		photo = &models.InputFileString{Data: image.URL}
	} else {
		photo = &models.InputFileUpload{Filename: "payment-qr.png", Data: bytes.NewReader(image.Data)}
	}
	message, err := a.bot.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID: chatID, Photo: photo, Caption: caption, ParseMode: models.ParseModeHTML,
	})
	if err != nil && image.URL != "" {
		fallback, renderErr := renderQRCodePNG(image.URL)
		if renderErr == nil {
			message, err = a.bot.SendPhoto(ctx, &tgbot.SendPhotoParams{
				ChatID:  chatID,
				Photo:   &models.InputFileUpload{Filename: "payment-qr.png", Data: bytes.NewReader(fallback)},
				Caption: caption, ParseMode: models.ParseModeHTML,
			})
		}
	}
	if err != nil {
		a.logger.Warn("send payment QR code", "error", err)
		return 0
	}
	return message.ID
}

func (a *App) startPaymentPoll(chatID, userID int64, orderID uint, messageID, qrMessageID int, expiresAt string) {
	runCtx := a.runtimeContext()
	pollCtx, cancel := context.WithCancel(runCtx)
	poll := &activePaymentPoll{cancel: cancel, qrMessageID: qrMessageID}
	a.pollMu.Lock()
	previous := a.polls[orderID]
	a.polls[orderID] = poll
	a.pollMu.Unlock()
	if previous != nil {
		previous.cancel()
		if previous.qrMessageID > 0 {
			_, _ = a.bot.DeleteMessage(runCtx, &tgbot.DeleteMessageParams{ChatID: chatID, MessageID: previous.qrMessageID})
		}
	}
	go a.pollPayment(pollCtx, poll, chatID, userID, orderID, messageID, expiresAt)
}

func (a *App) runtimeContext() context.Context {
	a.runCtxMu.RLock()
	defer a.runCtxMu.RUnlock()
	return a.runCtx
}

func (a *App) pollPayment(ctx context.Context, poll *activePaymentPoll, chatID, userID int64, orderID uint, messageID int, expiresAt string) {
	defer func() {
		poll.cancel()
		a.pollMu.Lock()
		if a.polls[orderID] == poll {
			delete(a.polls, orderID)
		}
		a.pollMu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Minute)
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(expiresAt)); err == nil && parsed.After(time.Now()) && parsed.Before(time.Now().Add(30*time.Minute)) {
		deadline = parsed
	}
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				if poll.qrMessageID > 0 {
					_, _ = a.bot.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: chatID, MessageID: poll.qrMessageID})
				}
				a.showOrder(ctx, chatID, messageID, userID, orderID)
				return
			}
			locale := a.sessions.Get(userID).Locale
			order, err := a.api.Order(ctx, strconv.FormatInt(userID, 10), locale, orderID)
			if err != nil {
				a.logger.Debug("poll payment status", "order_id", orderID, "error", err)
				continue
			}
			if order.Status != "pending_payment" {
				if poll.qrMessageID > 0 {
					_, _ = a.bot.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: chatID, MessageID: poll.qrMessageID})
				}
				a.showOrder(ctx, chatID, messageID, userID, orderID)
				return
			}
		}
	}
}

func (a *App) stopPaymentPoll(ctx context.Context, chatID int64, orderID uint) {
	a.pollMu.Lock()
	poll := a.polls[orderID]
	delete(a.polls, orderID)
	a.pollMu.Unlock()
	if poll == nil {
		return
	}
	if poll.qrMessageID > 0 {
		_, _ = a.bot.DeleteMessage(ctx, &tgbot.DeleteMessageParams{ChatID: chatID, MessageID: poll.qrMessageID})
	}
	poll.cancel()
}

func (a *App) showOrders(ctx context.Context, chatID, userID int64, status string, page int, edit bool, messageID int) {
	if page < 1 {
		page = 1
	}
	locale := a.sessions.Get(userID).Locale
	orders, err := a.api.Orders(ctx, strconv.FormatInt(userID, 10), locale, status, page, 5)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "list orders", err)
		return
	}
	text := "<b>" + tr(locale, "我的订单", "我的訂單", "My orders") + "</b>"
	for _, order := range orders.Items {
		text += "\n\n<b>" + html.EscapeString(order.ProductTitle) + "</b>"
		text += "\n<code>" + html.EscapeString(order.OrderNo) + "</code>"
		text += "\n" + tr(locale, "金额：", "金額：", "Total: ") + html.EscapeString(order.TotalAmount+" "+order.Currency)
		text += " · " + html.EscapeString(orderStatus(locale, order.Status))
	}
	if len(orders.Items) == 0 {
		text += "\n\n" + tr(locale, "还没有订单。", "尚無訂單。", "No orders yet.")
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(orders.Items)+3)
	filterCode := orderFilterCode(status)
	filterButton := func(label, code string) models.InlineKeyboardButton {
		if filterCode == code {
			label = "✓ " + label
		}
		return models.InlineKeyboardButton{Text: label, CallbackData: "orders:" + code + ":1"}
	}
	rows = append(rows, []models.InlineKeyboardButton{
		filterButton(tr(locale, "全部", "全部", "All"), "a"),
		filterButton(tr(locale, "待支付", "待付款", "Pending"), "p"),
		filterButton(tr(locale, "已完成", "已完成", "Completed"), "c"),
	})
	for _, order := range orders.Items {
		label := order.ProductTitle
		if label == "" {
			label = order.OrderNo
		}
		label += " · " + orderStatus(locale, order.Status)
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(label, 60), CallbackData: fmt.Sprintf("order:%d", order.OrderID)}})
	}
	var pager []models.InlineKeyboardButton
	if page > 1 {
		pager = append(pager, models.InlineKeyboardButton{Text: "‹", CallbackData: fmt.Sprintf("orders:%s:%d", filterCode, page-1)})
	}
	if int64(page) < orders.TotalPages {
		pager = append(pager, models.InlineKeyboardButton{Text: "›", CallbackData: fmt.Sprintf("orders:%s:%d", filterCode, page+1)})
	}
	if len(pager) > 0 {
		rows = append(rows, pager)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "home"}})
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) showOrder(ctx context.Context, chatID int64, messageID int, userID int64, orderID uint) {
	if chatID != userID || userID <= 0 {
		return
	}
	locale := a.sessions.Get(userID).Locale
	order, err := a.api.Order(ctx, strconv.FormatInt(userID, 10), locale, orderID)
	if err != nil || order.OrderID != orderID {
		a.deliveryLookupFailure(ctx, chatID, locale)
		return
	}
	text := "<b>" + tr(locale, "订单详情", "訂單詳情", "Order details") + "</b>"
	text += "\n\n" + tr(locale, "订单号：", "訂單號：", "Order: ") + "<code>" + html.EscapeString(truncate(order.OrderNo, 100)) + "</code>"
	text += "\n" + tr(locale, "状态：", "狀態：", "Status: ") + html.EscapeString(orderStatus(locale, order.Status))
	text += "\n" + tr(locale, "金额：", "金額：", "Total: ") + html.EscapeString(order.TotalAmount+" "+order.Currency)
	for index, item := range order.Items {
		if index >= 5 {
			text += "\n…"
			break
		}
		text += "\n\n<b>" + html.EscapeString(truncate(item.ProductTitle, 100)) + "</b>"
		if item.SKUName != "" {
			text += " · " + html.EscapeString(truncate(item.SKUName, 60))
		}
		text += fmt.Sprintf("\n× %d　%s %s", item.Quantity, html.EscapeString(item.Subtotal), html.EscapeString(order.Currency))
	}
	rows := make([][]models.InlineKeyboardButton, 0, 4)
	if hasDelivery(order) {
		text += "\n\n" + tr(locale, "📄 交付内容已就绪，下载 TXT 查看完整卡密和使用说明。请妥善保管文件。", "📄 交付內容已就緒，下載 TXT 查看完整卡密和使用說明。請妥善保管檔案。", "📄 Delivery is ready. Download the TXT for complete codes and instructions. Keep it private.")
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "📄 下载交付 TXT", "📄 下載交付 TXT", "📄 Download delivery TXT"), CallbackData: fmt.Sprintf("file:%d", order.OrderID)}})
	}
	if order.Status == "pending_payment" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "选择支付方式", "選擇付款方式", "Choose payment method"), CallbackData: fmt.Sprintf("orderpay:%d", order.OrderID)}})
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "取消订单", "取消訂單", "Cancel order"), CallbackData: fmt.Sprintf("cancelask:%d", order.OrderID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "刷新状态", "重新整理狀態", "Refresh status"), CallbackData: fmt.Sprintf("order:%d", order.OrderID)}})
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 我的订单", "‹ 我的訂單", "‹ My orders"), CallbackData: "orders:1"}})
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) showOrderPaymentMethods(ctx context.Context, chatID int64, messageID int, userID int64, orderID uint) {
	locale := a.sessions.Get(userID).Locale
	order, err := a.api.Order(ctx, strconv.FormatInt(userID, 10), locale, orderID)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get order for payment", err)
		return
	}
	if order.Status != "pending_payment" {
		a.showOrder(ctx, chatID, messageID, userID, orderID)
		return
	}
	channels, err := a.api.PaymentChannels(ctx, strconv.FormatInt(userID, 10), order.OrderNo)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "list payment channels", err)
		return
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(channels.Items)+2)
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "余额支付", "餘額付款", "Pay with balance"), CallbackData: fmt.Sprintf("pay:%d:0:1", order.OrderID)}})
	if !channels.WalletOnlyPayment {
		for _, channel := range channels.Items {
			rows = append(rows, []models.InlineKeyboardButton{{Text: channel.Name, CallbackData: fmt.Sprintf("pay:%d:%d:0", order.OrderID, channel.ID)}})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回订单", "‹ 返回訂單", "‹ Back to order"), CallbackData: fmt.Sprintf("order:%d", order.OrderID)}})
	text := "<b>" + tr(locale, "选择支付方式", "選擇付款方式", "Choose payment method") + "</b>\n\n"
	text += tr(locale, "应付：", "應付：", "Total: ") + html.EscapeString(order.TotalAmount+" "+order.Currency)
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) confirmCancelOrder(ctx context.Context, chatID int64, messageID int, userID int64, orderID uint) {
	locale := a.sessions.Get(userID).Locale
	text := tr(locale, "确定取消这个待支付订单吗？", "確定取消這個待付款訂單嗎？", "Cancel this pending order?")
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "确认取消", "確認取消", "Confirm cancellation"), CallbackData: fmt.Sprintf("cancelorder:%d", orderID)}},
		{{Text: tr(locale, "‹ 返回订单", "‹ 返回訂單", "‹ Back to order"), CallbackData: fmt.Sprintf("order:%d", orderID)}},
	}}
	a.edit(ctx, chatID, messageID, text, keyboard)
}

func (a *App) cancelOrder(ctx context.Context, chatID int64, messageID int, userID int64, orderID uint) {
	if !a.allowWrite(ctx, chatID, userID, operations.OrderCancel) {
		return
	}
	locale := a.sessions.Get(userID).Locale
	op := a.ops.Begin(operations.OrderCancel, userID, orderID)
	if _, err := a.api.CancelOrder(ctx, strconv.FormatInt(userID, 10), orderID); err != nil {
		op.End(financialOutcome(err), orderID)
		a.logger.Error("cancel Dujiao order failed", "order_id", orderID)
		a.edit(ctx, chatID, messageID, tr(locale, "订单无法取消，可能已支付、已取消或已过期。", "訂單無法取消，可能已付款、已取消或已過期。", "The order cannot be cancelled. It may already be paid, cancelled, or expired."), models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: tr(locale, "查看订单", "查看訂單", "View order"), CallbackData: fmt.Sprintf("order:%d", orderID)}}}})
		return
	}
	op.End(operations.Succeeded, orderID)
	a.stopPaymentPoll(ctx, chatID, orderID)
	a.showOrder(ctx, chatID, messageID, userID, orderID)
}

func (a *App) showWallet(ctx context.Context, chatID, userID int64, edit bool, messageID int) {
	locale := a.sessions.Get(userID).Locale
	wallet, err := a.api.Wallet(ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "get wallet", err)
		return
	}
	text := "<b>" + tr(locale, "账户余额", "帳戶餘額", "Wallet balance") + "</b>\n\n"
	text += html.EscapeString(wallet.Balance + " " + wallet.Currency)
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "➕ 充值", "➕ 儲值", "➕ Recharge"), CallbackData: "recharge"}},
		{{Text: tr(locale, "🎁 礼品卡兑换", "🎁 禮品卡兌換", "🎁 Redeem gift card"), CallbackData: "giftcard"}},
		{{Text: tr(locale, "📒 交易流水", "📒 交易明細", "📒 Transactions"), CallbackData: "walletlog:1"}},
		{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "home"}},
	}}
	a.sendOrEdit(ctx, chatID, messageID, text, keyboard, edit)
}

func (a *App) showWalletTransactions(ctx context.Context, chatID, userID int64, page int, edit bool, messageID int) {
	if page < 1 {
		page = 1
	}
	locale := a.sessions.Get(userID).Locale
	transactions, err := a.api.WalletTransactions(ctx, strconv.FormatInt(userID, 10), page, 5)
	if err != nil {
		a.apiFailure(ctx, chatID, locale, "list wallet transactions", err)
		return
	}
	text := "<b>" + tr(locale, "钱包交易流水", "錢包交易明細", "Wallet transactions") + "</b>"
	for _, transaction := range transactions.Items {
		sign := walletTransactionSign(transaction.Direction)
		text += "\n\n<b>" + html.EscapeString(walletTransactionType(locale, transaction.Type)) + "</b>"
		text += "\n" + html.EscapeString(sign+transaction.Amount)
		text += " · " + tr(locale, "余额 ", "餘額 ", "Balance ") + html.EscapeString(transaction.BalanceAfter)
		if transaction.Remark != "" {
			text += "\n" + html.EscapeString(truncate(transaction.Remark, 100))
		}
		if transaction.CreatedAt != "" {
			text += "\n<code>" + html.EscapeString(transaction.CreatedAt) + "</code>"
		}
	}
	if len(transactions.Items) == 0 {
		text += "\n\n" + tr(locale, "还没有交易流水。", "尚無交易明細。", "No transactions yet.")
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	var pager []models.InlineKeyboardButton
	if page > 1 {
		pager = append(pager, models.InlineKeyboardButton{Text: "‹", CallbackData: fmt.Sprintf("walletlog:%d", page-1)})
	}
	if int64(page) < transactions.TotalPages {
		pager = append(pager, models.InlineKeyboardButton{Text: "›", CallbackData: fmt.Sprintf("walletlog:%d", page+1)})
	}
	if len(pager) > 0 {
		rows = append(rows, pager)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回钱包", "‹ 返回錢包", "‹ Back to wallet"), CallbackData: "wallet"}})
	a.sendOrEdit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows}, edit)
}

func (a *App) createWalletRecharge(ctx context.Context, chatID int64, messageID int, userID int64, amount, currency string, channelID uint) {
	locale := a.sessions.Get(userID).Locale
	op := a.ops.Begin(operations.Recharge, userID, 0)
	result, err := a.api.CreateWalletRecharge(ctx, strconv.FormatInt(userID, 10), amount, channelID)
	op.End(financialOutcome(err), 0)
	if err != nil {
		a.logger.Error("create wallet recharge failed", "user_id", userID)
		a.edit(ctx, chatID, messageID, tr(locale, "暂时无法确认充值单创建结果。请先查看钱包、交易流水及支付账户，避免重复付款；未确认前不要再次充值。", "暫時無法確認儲值單建立結果。請先查看錢包、交易明細及付款帳戶，避免重複付款；未確認前不要再次儲值。", "The recharge result could not be confirmed. Check your wallet, transactions and payment account before starting another recharge to avoid duplicate payments."), rechargeResultKeyboard(locale))
		return
	}
	if result.Payment.Status == "success" || result.Payment.Status == "succeeded" || result.Payment.Status == "paid" {
		text := "✅ " + tr(locale, "充值成功，余额已更新。", "儲值成功，餘額已更新。", "Recharge succeeded. Your balance has been updated.")
		text += "\n\n" + rechargeReceipt(locale, amount, currency, result)
		a.edit(ctx, chatID, messageID, text, rechargeResultKeyboard(locale))
		return
	}
	text := "<b>" + tr(locale, "充值订单已创建", "儲值訂單已建立", "Recharge created") + "</b>\n\n"
	text += rechargeReceipt(locale, amount, currency, result)
	if result.Payment.WalletAddress != "" {
		text += "\n" + tr(locale, "收款地址：", "收款地址：", "Address: ") + "<code>" + html.EscapeString(result.Payment.WalletAddress) + "</code>"
	}
	if result.Payment.ChainAmount != "" {
		text += "\n" + tr(locale, "链上金额：", "鏈上金額：", "On-chain amount: ") + "<code>" + html.EscapeString(result.Payment.ChainAmount) + "</code>"
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	if safeHTTPURL(result.Payment.PayURL) {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "前往支付", "前往付款", "Pay now"), URL: result.Payment.PayURL}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "‹ 返回钱包", "‹ 返回錢包", "‹ Back to wallet"), CallbackData: "wallet"}})
	a.edit(ctx, chatID, messageID, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
	a.sendPaymentQRCode(ctx, chatID, locale, result.Payment.QRCode, result.Payment.PayURL, result.Payment.InteractionMode, result.Payment.Amount, result.Payment.Currency)
}

func (a *App) showLanguage(ctx context.Context, chatID int64, edit bool, messageID int) {
	keyboard := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "简体中文", CallbackData: "setlang:zh-CN"}},
		{{Text: "繁體中文", CallbackData: "setlang:zh-TW"}},
		{{Text: "English", CallbackData: "setlang:en-US"}},
	}}
	a.sendOrEdit(ctx, chatID, messageID, "请选择语言 / 請選擇語言 / Choose a language:", keyboard, edit)
}

func (a *App) showHelp(ctx context.Context, chatID int64, locale string) {
	a.send(ctx, chatID, a.helpText(locale), a.backKeyboard(locale))
}

func (a *App) showHelpEdit(ctx context.Context, chatID int64, messageID int, locale string) {
	a.edit(ctx, chatID, messageID, a.helpText(locale), a.backKeyboard(locale))
}

func (a *App) helpText(locale string) string {
	config := a.botConfig()
	intro := localized(config.Help.Intro, locale)
	if intro != "" {
		return html.EscapeString(intro)
	}
	return tr(locale,
		"使用菜单浏览分类和商品。如需人工帮助，请联系商店客服。",
		"使用選單瀏覽分類和商品。如需人工協助，請聯絡商店客服。",
		"Use the menu to browse categories and products. Contact store support if you need help.")
}

func (a *App) homeKeyboard(locale string) models.InlineKeyboardMarkup {
	fallbackRows := [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "🛍 浏览商品", "🛍 瀏覽商品", "🛍 Browse products"), CallbackData: "cats"}},
		{
			{Text: tr(locale, "📦 我的订单", "📦 我的訂單", "📦 My orders"), CallbackData: "orders:1"},
			{Text: tr(locale, "💰 余额", "💰 餘額", "💰 Wallet"), CallbackData: "wallet"},
		},
		{
			{Text: tr(locale, "🌐 语言", "🌐 語言", "🌐 Language"), CallbackData: "lang"},
			{Text: tr(locale, "❓ 帮助", "❓ 幫助", "❓ Help"), CallbackData: "help"},
		},
		{{Text: tr(locale, "🤝 推广中心", "🤝 推廣中心", "🤝 Affiliate"), CallbackData: "affiliate"}},
	}
	config := a.botConfig()
	var rows [][]models.InlineKeyboardButton
	if len(config.Menu.Items) == 0 {
		rows = fallbackRows
	} else {
		rows = configuredMenuRows(config.Menu.Items, locale)
	}
	if safeHTTPURL(config.Basic.SupportURL) {
		rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "联系客服", "聯絡客服", "Support"), URL: config.Basic.SupportURL}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: tr(locale, "📣 公告订阅", "📣 公告訂閱", "📣 Announcements"), CallbackData: "bcsubscriptions"}})
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func configuredMenuRows(items []dujiao.BotMenuItem, locale string) [][]models.InlineKeyboardButton {
	if len(items) == 0 {
		return nil
	}
	items = append([]dujiao.BotMenuItem(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Order < items[j].Order })
	callbacks := map[string]string{
		"shop_home":       "cats",
		"my_orders":       "orders:1",
		"my_wallet":       "wallet",
		"affiliate":       "affiliate",
		"my_affiliate":    "affiliate",
		"switch_language": "lang",
		"contact_support": "help",
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		label := localized(item.Label, locale)
		if label == "" {
			continue
		}
		button := models.InlineKeyboardButton{Text: label}
		if item.Action.Type == "url" && safeHTTPURL(item.Action.Value) {
			button.URL = item.Action.Value
		} else if callback := callbacks[item.Key]; callback != "" {
			button.CallbackData = callback
		} else {
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{button})
	}
	return rows
}

func safeHTTPURL(value string) bool {
	return strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")
}

func (a *App) backKeyboard(locale string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: tr(locale, "‹ 返回", "‹ 返回", "‹ Back"), CallbackData: "home"}},
	}}
}

func (a *App) sendOrEdit(ctx context.Context, chatID int64, messageID int, text string, keyboard models.InlineKeyboardMarkup, edit bool) {
	if edit {
		a.edit(ctx, chatID, messageID, text, keyboard)
		return
	}
	a.send(ctx, chatID, text, keyboard)
}

func (a *App) send(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) {
	_, err := a.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil {
		a.logger.Error("send Telegram message", "chat_id", chatID, "error", err)
	}
}

func (a *App) edit(ctx context.Context, chatID int64, messageID int, text string, markup models.ReplyMarkup) {
	_, err := a.bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		a.logger.Error("edit Telegram message", "chat_id", chatID, "error", err)
	}
}

func (a *App) apiFailure(ctx context.Context, chatID int64, locale, operation string, err error) {
	a.logger.Error("Dujiao API request failed", "operation", operation, "error", err)
	a.send(ctx, chatID, tr(locale, "商店暂时不可用，请稍后重试。", "商店暫時無法使用，請稍後再試。", "The store is temporarily unavailable. Please try again later."), nil)
}

func (a *App) botConfig() dujiao.BotConfig {
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	return a.config
}

func identityFromUser(user *models.User, locale string) dujiao.TelegramIdentity {
	return dujiao.TelegramIdentity{
		ChannelUserID: strconv.FormatInt(user.ID, 10),
		Username:      user.Username,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		Locale:        locale,
	}
}

func isAPIError(err error, code string) bool {
	var apiError *dujiao.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode == code
}

func parseCommand(text string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", ""
	}
	command := strings.Split(fields[0], "@")[0]
	if len(fields) == 1 {
		return command, ""
	}
	return command, fields[1]
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(locale)
	switch {
	case strings.HasPrefix(locale, "zh-tw"), strings.HasPrefix(locale, "zh-hk"), strings.HasPrefix(locale, "zh-hant"):
		return "zh-TW"
	case strings.HasPrefix(locale, "zh"):
		return "zh-CN"
	default:
		return "en-US"
	}
}

func validLocale(locale string) bool {
	return locale == "zh-CN" || locale == "zh-TW" || locale == "en-US"
}

func localized(text dujiao.LocalizedText, locale string) string {
	if value := strings.TrimSpace(text[locale]); value != "" {
		return value
	}
	for _, fallback := range []string{"zh-CN", "zh-TW", "en-US"} {
		if value := strings.TrimSpace(text[fallback]); value != "" {
			return value
		}
	}
	return ""
}

func tr(locale, simplified, traditional, english string) string {
	switch locale {
	case "zh-TW":
		return traditional
	case "en-US":
		return english
	default:
		return simplified
	}
}

func truncate(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

func normalizeMoneyInput(input string) (string, bool) {
	input = strings.TrimSpace(input)
	cents, ok := money.Parse(input)
	if !ok || cents <= 0 || cents > 100_000_000 {
		return "", false
	}
	return money.Format(cents), true
}

func formatFulfillmentPayload(payload any) string {
	if payload == nil {
		return ""
	}
	if text, ok := payload.(string); ok {
		return strings.TrimSpace(text)
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return strings.TrimSpace(fmt.Sprint(payload))
	}
	if string(encoded) == "null" || string(encoded) == "{}" || string(encoded) == "[]" {
		return ""
	}
	return string(encoded)
}

func purchaseQuantityLimits(product dujiao.ProductDetail) (minimum, maximum int) {
	minimum = product.MinPurchaseQuantity
	if minimum < 1 {
		minimum = 1
	}
	maximum = product.MaxPurchaseQuantity
	if maximum < 1 {
		maximum = 0
	}
	return minimum, maximum
}

func purchaseQuantityBounds(product dujiao.ProductDetail, skuID uint) (minimum, maximum int, available bool) {
	minimum, maximum = purchaseQuantityLimits(product)
	if product.StockStatus == "out_of_stock" {
		return minimum, maximum, false
	}
	stockCount := product.StockCount
	stockHidden := product.StockHidden
	if len(product.SKUs) > 0 {
		sku := selectedSKU(product, skuID)
		if sku == nil || sku.StockStatus == "out_of_stock" {
			return minimum, maximum, false
		}
		stockCount = sku.StockCount
		stockHidden = sku.StockHidden
	} else if skuID != 0 {
		return minimum, maximum, false
	}
	if !stockHidden && stockCount != nil && *stockCount >= 0 {
		if *stockCount < int64(minimum) {
			return minimum, int(*stockCount), false
		}
		if maximum == 0 || int64(maximum) > *stockCount {
			maximum = int(*stockCount)
		}
	}
	if maximum > 0 && maximum < minimum {
		return minimum, maximum, false
	}
	return minimum, maximum, true
}

func clampPurchaseQuantity(quantity, minimum, maximum int) int {
	if quantity < minimum {
		return minimum
	}
	if maximum > 0 && quantity > maximum {
		return maximum
	}
	return quantity
}

func selectedSKU(product dujiao.ProductDetail, skuID uint) *dujiao.SKU {
	for index := range product.SKUs {
		if product.SKUs[index].ID == skuID {
			return &product.SKUs[index]
		}
	}
	return nil
}

func skuLabel(sku dujiao.SKU) string {
	if label := strings.TrimSpace(sku.SpecValues); label != "" {
		return label
	}
	if label := strings.TrimSpace(sku.SKUCode); label != "" {
		return label
	}
	return fmt.Sprintf("SKU #%d", sku.ID)
}

func orderStatus(locale, status string) string {
	statuses := map[string][3]string{
		"pending_payment": {"待支付", "待付款", "Pending payment"},
		"paid":            {"已支付", "已付款", "Paid"},
		"processing":      {"处理中", "處理中", "Processing"},
		"completed":       {"已完成", "已完成", "Completed"},
		"delivered":       {"已交付", "已交付", "Delivered"},
		"cancelled":       {"已取消", "已取消", "Cancelled"},
		"canceled":        {"已取消", "已取消", "Cancelled"},
		"refunded":        {"已退款", "已退款", "Refunded"},
	}
	values, ok := statuses[status]
	if !ok {
		return status
	}
	switch locale {
	case "zh-TW":
		return values[1]
	case "en-US":
		return values[2]
	default:
		return values[0]
	}
}

func orderFilterStatus(code string) string {
	switch code {
	case "p":
		return "pending_payment"
	case "c":
		return "completed"
	default:
		return ""
	}
}

func orderFilterCode(status string) string {
	switch status {
	case "pending_payment":
		return "p"
	case "completed":
		return "c"
	default:
		return "a"
	}
}

func walletTransactionSign(direction string) string {
	switch strings.ToLower(direction) {
	case "out", "debit":
		return "−"
	default:
		return "+"
	}
}

func walletTransactionType(locale, transactionType string) string {
	types := map[string][3]string{
		"recharge":               {"余额充值", "餘額儲值", "Recharge"},
		"order_pay":              {"订单支付", "訂單付款", "Order payment"},
		"order_refund":           {"订单退款", "訂單退款", "Order refund"},
		"admin_refund":           {"管理员退款", "管理員退款", "Admin refund"},
		"admin_adjust":           {"管理员调账", "管理員調帳", "Admin adjustment"},
		"gift_card_redeem":       {"礼品卡兑换", "禮品卡兌換", "Gift card redemption"},
		"order_underpaid_credit": {"订单收款不足转入", "訂單收款不足轉入", "Underpaid order credit"},
	}
	values, ok := types[transactionType]
	if !ok {
		return transactionType
	}
	switch locale {
	case "zh-TW":
		return values[1]
	case "en-US":
		return values[2]
	default:
		return values[0]
	}
}
