// Command mock-api provides deterministic Dujiao-Next and Telegram endpoints
// for local Docker integration and load tests. It must never be used in production.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
	"github.com/dunea/dujiao-next-telegram-bot/internal/manualform"
)

var telegramRequests atomic.Uint64
var dujiaoRequests atomic.Uint64
var orderSequence atomic.Uint64
var rechargeSequence atomic.Uint64
var walletBalanceCents atomic.Int64
var orderMu sync.Mutex
var demoOrders []map[string]any
var demoPayments = make(map[uint]map[string]any) // guarded by orderMu; synthetic only
var walletTransactionMu sync.Mutex
var demoWalletTransactions []map[string]any

func init() {
	walletBalanceCents.Store(10_000)
	recordWalletTransaction("admin_adjust", "in", 10_000, 10_000, "演示初始余额")
}

type demoSKU struct {
	ID         uint
	Name       string
	Code       string
	PriceCents int
	StockCount int64
}

type demoProduct struct {
	ID          uint
	CategoryID  uint
	Title       string
	Summary     string
	Description string
	PriceCents  int
	Stock       string
	StockCount  int64
	MinQuantity int
	MaxQuantity int
	ManualForm  bool
	SKUs        []demoSKU
}

var demoProducts = []demoProduct{
	{ID: 1, CategoryID: 1, Title: "演示云服务器", Summary: "即时开通的测试云服务器商品", Description: "这是机器人演示商品，不会产生真实服务或扣款。", PriceCents: 990, Stock: "剩余 20", StockCount: 20, MinQuantity: 2, MaxQuantity: 5},
	{ID: 2, CategoryID: 2, Title: "软件授权码", Summary: "包含月付与年付两种规格", Description: "购买后由演示环境模拟自动发货。", PriceCents: 1990, Stock: "充足", StockCount: 15, MinQuantity: 1, MaxQuantity: 10, SKUs: []demoSKU{
		{ID: 21, Name: "月付版", Code: "MONTHLY", PriceCents: 1990, StockCount: 12},
		{ID: 22, Name: "年付版", Code: "YEARLY", PriceCents: 9900, StockCount: 3},
	}},
	{ID: 3, CategoryID: 1, Title: "人工配置服务", Summary: "填写域名、邮箱和配置偏好后下单", Description: "在 Telegram 中填写资料并预览，付款后模拟进入人工处理。请使用虚构资料测试。", PriceCents: 2990, Stock: "剩余 10", StockCount: 10, MaxQuantity: 5, ManualForm: true},
	{ID: 4, CategoryID: 2, Title: "限量兑换码", Summary: "用于演示售罄状态", Description: "该演示商品当前没有库存。", PriceCents: 500, Stock: "已售罄", StockCount: 0, MaxQuantity: 5},
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"telegram_requests": telegramRequests.Load(),
			"dujiao_requests":   dujiaoRequests.Load(),
		})
	})
	mux.HandleFunc("/api/v1/channel/", dujiaoHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/bot") {
			telegramHandler(w, request)
			return
		}
		http.NotFound(w, request)
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("mock API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func telegramHandler(w http.ResponseWriter, request *http.Request) {
	telegramRequests.Add(1)
	method := strings.ToLower(request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:])
	switch method {
	case "getme":
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"id": 999001, "is_bot": true, "first_name": "Integration Bot", "username": "integration_test_bot",
		}})
	case "getupdates":
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, map[string]any{"ok": true, "result": []any{}})
	case "setmycommands":
		writeJSON(w, map[string]any{"ok": true, "result": true})
	case "sendmessage", "editmessagetext", "sendphoto", "senddocument":
		writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
			"message_id": 1,
			"date":       time.Now().Unix(),
			"chat":       map[string]any{"id": 42, "type": "private"},
			"text":       "mock",
		}})
	case "answercallbackquery":
		writeJSON(w, map[string]any{"ok": true, "result": true})
	default:
		writeJSON(w, map[string]any{"ok": true, "result": true})
	}
}

func dujiaoHandler(w http.ResponseWriter, request *http.Request) {
	dujiaoRequests.Add(1)
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/channel")
	if strings.HasPrefix(path, "/affiliate/") {
		demoAffiliateHandler(w, request, path)
		return
	}
	var data any = map[string]any{}
	switch path {
	case "/telegram/config":
		data = map[string]any{
			"config_version": 1,
			"config": map[string]any{
				"enabled": true, "bot_token": "999001:integration-token", "default_locale": "zh-CN", "config_version": 1,
				"basic": map[string]any{"display_name": "Dujiao Bot Demo Store", "support_url": ""},
				"welcome": map[string]any{"enabled": true, "message": map[string]string{
					"zh-CN": "欢迎来到 Dujiao Bot 演示商店！这里的商品和支付均为测试数据。",
					"zh-TW": "歡迎來到 Dujiao Bot 展示商店！這裡的商品與付款均為測試資料。",
					"en-US": "Welcome to the Dujiao Bot demo store. All products and payments are test data.",
				}},
				"help": map[string]any{"enabled": true, "intro": map[string]string{
					"zh-CN": "这是本地演示环境，可以放心测试商品浏览、下单、余额支付和订单查询。",
					"zh-TW": "這是本機展示環境，可以放心測試商品瀏覽、下單、餘額付款與訂單查詢。",
					"en-US": "This is a local demo. You can safely test browsing, orders, wallet payment, and order history.",
				}},
				"menu": map[string]any{"items": []any{}},
			},
		}
	case "/telegram/heartbeat":
		data = map[string]any{"config_version": 1}
	case "/identities/telegram/resolve", "/identities/telegram/provision":
		data = map[string]any{"bound": true, "created": false, "user": map[string]any{"id": 1}}
	case "/catalog/categories":
		data = map[string]any{"items": []map[string]any{
			{"id": 1, "parent_id": 0, "name": "云服务", "slug": "cloud", "product_count": 2},
			{"id": 2, "parent_id": 0, "name": "数字商品", "slug": "digital", "product_count": 2},
		}}
	case "/catalog/products":
		data = demoProductList(request)
	case "/orders/preview":
		preview := demoOrderPreview(request)
		if preview["error_code"] == "coupon_invalid" {
			writeDemoCouponError(w)
			return
		}
		data = preview
	case "/orders":
		if request.Method == http.MethodGet {
			data = demoOrderList(request)
		} else {
			preview := demoOrderPreview(request)
			if preview["error_code"] == "coupon_invalid" {
				writeDemoCouponError(w)
				return
			}
			created := demoCreatePreviewedOrder(preview)
			if created == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": "validation_error", "msg": "商品数量或购买资料无效"})
				return
			}
			data = created
		}
	case "/payment-channels":
		if request.URL.Query().Get("context") == "recharge" {
			data = dujiao.PaymentChannelList{Items: demoRechargeChannels()}
			break
		}
		data = map[string]any{"items": []map[string]any{
			{"id": 1, "name": "演示二维码支付", "provider_type": "demo", "channel_type": "web", "interaction_mode": "qr", "fee_rate": "0.00", "fixed_fee": "0.00"},
		}}
	case "/payments":
		payment, errorCode := demoCreatePayment(request)
		if errorCode != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code": 400, "msg": "演示钱包余额不足", "error_code": errorCode, "request_id": "mock",
			})
			return
		}
		data = payment
	case "/payments/latest":
		id, parseErr := strconv.ParseUint(request.URL.Query().Get("order_id"), 10, 64)
		uid := request.URL.Query().Get("channel_user_id")
		orderMu.Lock()
		var owned map[string]any
		for _, o := range demoOrders {
			if o["order_id"] == uint(id) && uid != "" && o["channel_user_id"] == uid {
				owned = o
				break
			}
		}
		code := "order_not_found"
		if parseErr == nil && id > 0 && owned != nil {
			code = "payment_not_found"
			if p := demoPayments[uint(id)]; p != nil && owned["status"] == "pending_payment" {
				expires, _ := p["expires_at"].(string)
				deadline, e := time.Parse(time.RFC3339, expires)
				if e == nil && time.Now().Before(deadline) {
					data = cloneMap(p)
					code = ""
				}
			}
		}
		orderMu.Unlock()
		if code != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"status_code": 404, "error_code": code})
			return
		}
	case "/wallet":
		data = map[string]any{"balance": money(int(walletBalanceCents.Load())), "currency": "CNY"}
	case "/wallet/transactions":
		data = demoWalletTransactionList(request)
	case "/wallet/recharge":
		demoWalletRecharge(w, request)
		return
	case "/wallet/gift-card/redeem":
		demoRedeemGiftCard(w, request)
		return
	default:
		if strings.HasPrefix(path, "/catalog/products/") {
			id, _ := strconv.ParseUint(strings.TrimPrefix(path, "/catalog/products/"), 10, 64)
			data = demoProductDetail(uint(id))
		} else if strings.HasPrefix(path, "/orders/by-order-no/") && request.Method == http.MethodGet {
			order := demoOrderByNumber(strings.TrimPrefix(path, "/orders/by-order-no/"), request.URL.Query().Get("channel_user_id"))
			if order == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 404, "error_code": "order_not_found"})
				return
			}
			data = order
		} else if strings.HasPrefix(path, "/orders/") {
			orderPath := strings.TrimPrefix(path, "/orders/")
			if strings.HasSuffix(orderPath, "/cancel") {
				id, _ := strconv.ParseUint(strings.TrimSuffix(orderPath, "/cancel"), 10, 64)
				data = demoCancelOrder(uint(id))
			} else {
				id, _ := strconv.ParseUint(orderPath, 10, 64)
				data = demoOrderDetail(uint(id))
			}
		}
	}
	writeJSON(w, map[string]any{"status_code": 0, "msg": "success", "data": data, "request_id": "mock"})
}

func demoProductList(request *http.Request) map[string]any {
	categoryID, _ := strconv.ParseUint(request.URL.Query().Get("category_id"), 10, 64)
	items := make([]map[string]any, 0, len(demoProducts))
	for _, product := range demoProducts {
		if categoryID > 0 && product.CategoryID != uint(categoryID) {
			continue
		}
		items = append(items, map[string]any{
			"id": product.ID, "title": product.Title, "summary": product.Summary,
			"price_from": money(product.PriceCents), "currency": "CNY",
			"stock_status": stockStatus(product), "stock_display": product.Stock,
			"category_name": categoryName(product.CategoryID),
		})
	}
	return map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 8, "total_page": 1}
}

func demoProductDetail(id uint) map[string]any {
	product, ok := findProduct(id)
	if !ok {
		return map[string]any{}
	}
	skus := make([]map[string]any, 0, len(product.SKUs))
	for _, sku := range product.SKUs {
		skus = append(skus, map[string]any{
			"id": sku.ID, "sku_code": sku.Code, "spec_values": sku.Name,
			"price": money(sku.PriceCents), "stock_status": demoStockStatus(sku.StockCount),
			"stock_count": sku.StockCount, "stock_display": fmt.Sprintf("剩余 %d", sku.StockCount),
			"stock_quantity_hidden": false,
		})
	}
	schema := demoManualSchema(product)
	return map[string]any{
		"id": product.ID, "title": product.Title, "summary": product.Summary,
		"description": product.Description, "price_from": money(product.PriceCents), "currency": "CNY",
		"stock_status": stockStatus(product), "stock_count": product.StockCount,
		"stock_display": product.Stock, "stock_quantity_hidden": false,
		"category_name": categoryName(product.CategoryID), "fulfillment_type": demoFulfillmentType(product),
		"min_purchase_quantity": product.MinQuantity, "max_purchase_quantity": product.MaxQuantity,
		"manual_form_schema": schema, "skus": skus,
	}
}

func demoOrderPreview(request *http.Request) map[string]any {
	var payload demoOrderInput
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return demoInvalidPreview()
	}
	return demoBuildPreview(payload)
}

func demoInvalidPreview() map[string]any {
	return map[string]any{"valid": false, "items": []map[string]any{}, "validation_errors": []string{"订单数据无效"}}
}

func demoBuildPreview(payload demoOrderInput) map[string]any {
	items := payload.Items
	if len(items) == 0 {
		return demoInvalidPreview()
	}
	previewItems := make([]map[string]any, 0, len(items))
	validationErrors := make([]string, 0)
	total := 0
	for _, item := range items {
		product, ok := findProduct(item.ProductID)
		if !ok {
			validationErrors = append(validationErrors, "商品不存在")
			continue
		}
		minimum, maximum, available := demoQuantityBounds(product, item.SKUID)
		if !available {
			validationErrors = append(validationErrors, product.Title+" 当前库存不足")
			continue
		}
		if item.Quantity < minimum || (maximum > 0 && item.Quantity > maximum) {
			validationErrors = append(validationErrors, fmt.Sprintf("%s 购买数量必须为 %d–%d", product.Title, minimum, maximum))
			continue
		}
		var submission map[string]any
		if product.ManualForm {
			input := payload.ManualFormData[manualform.ItemKey(item.ProductID, item.SKUID)]
			if input == nil {
				input = payload.ManualFormData[strconv.FormatUint(uint64(item.ProductID), 10)]
			}
			var err error
			submission, err = manualform.Normalize(demoManualSchema(product), input)
			if err != nil {
				validationErrors = append(validationErrors, product.Title+" 购买资料无效")
				continue
			}
		}
		price, skuName := product.PriceCents, ""
		for _, sku := range product.SKUs {
			if sku.ID == item.SKUID {
				price, skuName = sku.PriceCents, sku.Name
			}
		}
		subtotal := price * item.Quantity
		total += subtotal
		previewItems = append(previewItems, map[string]any{
			"product_id": product.ID, "product_title": product.Title, "sku_id": item.SKUID,
			"sku_name": skuName, "quantity": item.Quantity, "unit_price": money(price),
			"subtotal": money(subtotal), "fulfillment_type": demoFulfillmentType(product),
			"manual_form_submission": submission,
		})
	}
	discount := 0
	code := strings.TrimSpace(payload.CouponCode)
	if code != "" {
		switch code {
		case "SAVE10":
			discount = (total + 5) / 10
		case "LESS5":
			if total < 2000 {
				return demoInvalidCouponPreview()
			}
			discount = 500
		default:
			return demoInvalidCouponPreview()
		}
	}
	return map[string]any{
		"item_count": len(previewItems), "items": previewItems, "original_amount": money(total),
		"coupon_code": code, "coupon_discount": money(discount), "promotion_discount": "0.00", "wholesale_discount": "0.00",
		"total_amount": money(total - discount), "currency": "CNY", "valid": len(validationErrors) == 0,
		"validation_errors": validationErrors,
		"channel_user_id":   payload.ChannelUserID, "affiliate_visitor_key": payload.AffiliateVisitorKey,
	}
}

func demoCreateOrder(request *http.Request) map[string]any {
	preview := demoOrderPreview(request)
	return demoCreatePreviewedOrder(preview)
}

func demoCreatePreviewedOrder(preview map[string]any) map[string]any {
	if preview["valid"] != true {
		return nil
	}
	items := preview["items"].([]map[string]any)
	orderID := uint(orderSequence.Add(1))
	orderNo := fmt.Sprintf("DEMO%06d", orderID)
	title := "演示商品"
	if len(items) > 0 {
		title, _ = items[0]["product_title"].(string)
	}
	fulfillmentType := "auto"
	if len(items) > 0 {
		fulfillmentType, _ = items[0]["fulfillment_type"].(string)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	order := map[string]any{
		"order_id": orderID, "order_no": orderNo, "status": "pending_payment", "currency": "CNY",
		"total_amount": preview["total_amount"], "paid_amount": "0.00", "wallet_paid_amount": "0.00",
		"original_amount": preview["original_amount"], "coupon_discount": preview["coupon_discount"], "coupon_code": preview["coupon_code"],
		"online_paid_amount": "0.00", "product_title": title, "item_count": len(items), "created_at": createdAt,
		"fulfillment_type": fulfillmentType, "fulfillment_status": "", "fulfillment_result": nil,
		"fulfillment_instructions": "", "children": []any{}, "items": items,
		"channel_user_id": preview["channel_user_id"], "affiliate_code": demoAffiliateOrderCode(preview),
	}
	orderMu.Lock()
	demoOrders = append([]map[string]any{order}, demoOrders...)
	result := cloneMap(order)
	orderMu.Unlock()
	return result
}

func demoInvalidCouponPreview() map[string]any {
	return map[string]any{"valid": false, "items": []map[string]any{}, "validation_errors": []string{"优惠码无效或不满足使用条件"}, "error_code": "coupon_invalid"}
}

func writeDemoCouponError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 400, "error_code": "coupon_invalid", "msg": "优惠码无效、已过期或未满足门槛"})
}

func demoOrderList(request *http.Request) map[string]any {
	status := request.URL.Query().Get("status")
	page := positiveQueryInt(request, "page", 1)
	pageSize := positiveQueryInt(request, "page_size", 5)
	orderMu.Lock()
	defer orderMu.Unlock()
	matching := make([]map[string]any, 0, len(demoOrders))
	for _, order := range demoOrders {
		if status != "" && order["status"] != status && !(status == "canceled" && order["status"] == "cancelled") {
			continue
		}
		matching = append(matching, cloneMap(order))
	}
	start := (page - 1) * pageSize
	if start > len(matching) {
		start = len(matching)
	}
	end := min(start+pageSize, len(matching))
	return map[string]any{
		"items": matching[start:end], "total": len(matching), "page": page,
		"page_size": pageSize, "total_pages": pageCount(len(matching), pageSize),
	}
}

func demoOrderDetail(orderID uint) map[string]any {
	orderMu.Lock()
	defer orderMu.Unlock()
	for _, order := range demoOrders {
		if order["order_id"] == orderID {
			return cloneMap(order)
		}
	}
	return map[string]any{}
}

func demoOrderByNumber(number, userID string) map[string]any {
	orderMu.Lock()
	defer orderMu.Unlock()
	for _, order := range demoOrders {
		if userID != "" && order["order_no"] == number && order["channel_user_id"] == userID {
			return cloneMap(order)
		}
	}
	return nil
}

func demoCancelOrder(orderID uint) map[string]any {
	orderMu.Lock()
	defer orderMu.Unlock()
	for _, order := range demoOrders {
		if order["order_id"] != orderID {
			continue
		}
		if order["status"] == "pending_payment" {
			order["status"] = "cancelled"
			order["cancelled_at"] = time.Now().UTC().Format(time.RFC3339)
		}
		return map[string]any{
			"order_id": orderID, "order_no": order["order_no"], "status": order["status"],
			"cancelled_at": order["cancelled_at"],
		}
	}
	return map[string]any{}
}

func demoCreatePayment(request *http.Request) (map[string]any, string) {
	var payload struct {
		OrderID    uint `json:"order_id"`
		ChannelID  uint `json:"channel_id"`
		UseBalance bool `json:"use_balance"`
	}
	_ = json.NewDecoder(request.Body).Decode(&payload)
	if payload.UseBalance {
		if moneyToCents(demoOrderAmount(payload.OrderID)) > walletBalanceCents.Load() {
			return nil, "insufficient_wallet_balance"
		}
		if !completeDemoOrder(payload.OrderID, true) {
			return nil, "insufficient_wallet_balance"
		}
	} else {
		go func() {
			time.Sleep(8 * time.Second)
			_ = completeDemoOrder(payload.OrderID, false)
		}()
	}
	if payload.UseBalance {
		return map[string]any{"order_paid": true, "order_id": payload.OrderID, "status": "success", "currency": "CNY"}, ""
	}
	amount := demoOrderAmount(payload.OrderID)
	result := map[string]any{
		"order_paid": false, "payment_id": payload.OrderID, "order_id": payload.OrderID,
		"status": "pending", "channel_name": "演示二维码支付", "amount": amount, "currency": "CNY",
		"interaction_mode": "qr", "qr_code": fmt.Sprintf("dujiao-demo://payment/%d", payload.OrderID),
		"expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
	}
	orderMu.Lock()
	demoPayments[payload.OrderID] = cloneMap(result)
	orderMu.Unlock()
	return result, ""
}

func demoOrderAmount(orderID uint) string {
	orderMu.Lock()
	defer orderMu.Unlock()
	for _, order := range demoOrders {
		if order["order_id"] == orderID {
			if amount, ok := order["total_amount"].(string); ok {
				return amount
			}
		}
	}
	return "0.00"
}

func completeDemoOrder(orderID uint, useBalance bool) bool {
	orderMu.Lock()
	defer orderMu.Unlock()
	for _, order := range demoOrders {
		if order["order_id"] == orderID && order["status"] == "pending_payment" {
			amount, _ := order["total_amount"].(string)
			amountCents := moneyToCents(amount)
			var balanceAfter int64
			if useBalance {
				for {
					balance := walletBalanceCents.Load()
					if balance < amountCents {
						return false
					}
					balanceAfter = balance - amountCents
					if walletBalanceCents.CompareAndSwap(balance, balanceAfter) {
						break
					}
				}
			}
			order["status"] = "completed"
			order["paid_amount"] = order["total_amount"]
			order["paid_at"] = time.Now().UTC().Format(time.RFC3339)
			order["fulfillment_status"] = "delivered"
			order["fulfillment_result"] = map[string]any{
				"license_key": fmt.Sprintf("DEMO-%06d-ABCD-EFGH", orderID),
				"note":        "这是演示交付内容，不可用于真实服务。",
			}
			order["fulfillment_instructions"] = "复制演示授权码后，在对应软件的激活页面中填写。"
			if order["fulfillment_type"] == "manual" {
				order["status"] = "processing"
				order["fulfillment_status"] = "pending"
				order["fulfillment_result"] = nil
				order["fulfillment_instructions"] = "演示人工配置订单已进入处理，购买资料已随订单保存。"
			}
			if useBalance {
				order["wallet_paid_amount"] = order["total_amount"]
				recordWalletTransaction("order_pay", "out", amountCents, balanceAfter, fmt.Sprintf("订单 %v", order["order_no"]))
			} else {
				order["online_paid_amount"] = order["total_amount"]
			}
			recordDemoAffiliateCommission(order)
			return true
		}
	}
	return false
}

func demoWalletTransactionList(request *http.Request) map[string]any {
	page := positiveQueryInt(request, "page", 1)
	pageSize := positiveQueryInt(request, "page_size", 5)
	walletTransactionMu.Lock()
	defer walletTransactionMu.Unlock()
	start := (page - 1) * pageSize
	if start > len(demoWalletTransactions) {
		start = len(demoWalletTransactions)
	}
	end := min(start+pageSize, len(demoWalletTransactions))
	items := make([]map[string]any, 0, end-start)
	for _, transaction := range demoWalletTransactions[start:end] {
		items = append(items, cloneMap(transaction))
	}
	return map[string]any{
		"items": items, "total": len(demoWalletTransactions), "page": page,
		"page_size": pageSize, "total_pages": pageCount(len(demoWalletTransactions), pageSize),
	}
}

func recordWalletTransaction(transactionType, direction string, amountCents, balanceAfterCents int64, remark string) {
	transaction := map[string]any{
		"type": transactionType, "direction": direction, "amount": money(int(amountCents)),
		"balance_after": money(int(balanceAfterCents)), "remark": remark,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	walletTransactionMu.Lock()
	demoWalletTransactions = append([]map[string]any{transaction}, demoWalletTransactions...)
	walletTransactionMu.Unlock()
}

func moneyToCents(value string) int64 {
	amount, _ := strconv.ParseFloat(value, 64)
	return int64(amount*100 + 0.5)
}

func positiveQueryInt(request *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(request.URL.Query().Get(key))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

type orderItem struct {
	ProductID uint `json:"product_id"`
	SKUID     uint `json:"sku_id"`
	Quantity  int  `json:"quantity"`
}

type demoOrderInput struct {
	ChannelUserID       string                    `json:"channel_user_id"`
	AffiliateVisitorKey string                    `json:"affiliate_visitor_key"`
	Items               []orderItem               `json:"items"`
	ManualFormData      map[string]map[string]any `json:"manual_form_data"`
	CouponCode          string                    `json:"coupon_code"`
}

func demoFulfillmentType(product demoProduct) string {
	if product.ManualForm {
		return "manual"
	}
	return "auto"
}

func demoManualSchema(product demoProduct) dujiao.ManualFormSchema {
	if !product.ManualForm {
		return dujiao.ManualFormSchema{Fields: []dujiao.ManualFormField{}}
	}
	maxLen := 64
	return dujiao.ManualFormSchema{Fields: []dujiao.ManualFormField{
		{Key: "hostname", Type: "text", Required: true, Label: "主机名称", Placeholder: "例如 demo.example.com", Regex: `^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}$`, MaxLen: &maxLen},
		{Key: "email", Type: "email", Required: true, Label: "联系邮箱", Placeholder: "例如 demo@example.com"},
		{Key: "region", Type: "select", Required: true, Label: "部署区域", Options: []string{"香港", "新加坡", "日本"}},
		{Key: "addons", Type: "checkbox", Label: "附加配置（可多选）", Options: []string{"监控", "备份", "自动更新"}},
		{Key: "note", Type: "textarea", Label: "补充说明（可跳过）", MaxLen: &maxLen},
	}}
}

func findProduct(id uint) (demoProduct, bool) {
	for _, product := range demoProducts {
		if product.ID == id {
			return product, true
		}
	}
	return demoProduct{}, false
}

func money(cents int) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func stockStatus(product demoProduct) string {
	return demoStockStatus(product.StockCount)
}

func demoStockStatus(stockCount int64) string {
	if stockCount == 0 {
		return "out_of_stock"
	}
	return "in_stock"
}

func demoQuantityBounds(product demoProduct, skuID uint) (minimum, maximum int, available bool) {
	minimum = product.MinQuantity
	if minimum < 1 {
		minimum = 1
	}
	maximum = product.MaxQuantity
	stockCount := product.StockCount
	if len(product.SKUs) > 0 {
		found := false
		for _, sku := range product.SKUs {
			if sku.ID == skuID {
				stockCount = sku.StockCount
				found = true
				break
			}
		}
		if !found {
			return minimum, maximum, false
		}
	} else if skuID != 0 {
		return minimum, maximum, false
	}
	if stockCount >= 0 {
		if stockCount < int64(minimum) {
			return minimum, int(stockCount), false
		}
		if maximum < 1 || int64(maximum) > stockCount {
			maximum = int(stockCount)
		}
	}
	return minimum, maximum, true
}

func categoryName(id uint) string {
	if id == 1 {
		return "云服务"
	}
	return "数字商品"
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func pageCount(total, size int) int {
	if total == 0 {
		return 0
	}
	return (total + size - 1) / size
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
