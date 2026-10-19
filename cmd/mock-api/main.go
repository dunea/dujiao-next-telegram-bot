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
)

var telegramRequests atomic.Uint64
var dujiaoRequests atomic.Uint64
var orderSequence atomic.Uint64
var rechargeSequence atomic.Uint64
var walletBalanceCents atomic.Int64
var orderMu sync.Mutex
var demoOrders []map[string]any

func init() {
	walletBalanceCents.Store(10_000)
}

type demoSKU struct {
	ID         uint
	Name       string
	Code       string
	PriceCents int
}

type demoProduct struct {
	ID          uint
	CategoryID  uint
	Title       string
	Summary     string
	Description string
	PriceCents  int
	Stock       string
	ManualForm  bool
	SKUs        []demoSKU
}

var demoProducts = []demoProduct{
	{ID: 1, CategoryID: 1, Title: "演示云服务器", Summary: "即时开通的测试云服务器商品", Description: "这是机器人演示商品，不会产生真实服务或扣款。", PriceCents: 990, Stock: "充足"},
	{ID: 2, CategoryID: 2, Title: "软件授权码", Summary: "包含月付与年付两种规格", Description: "购买后由演示环境模拟自动发货。", PriceCents: 1990, Stock: "充足", SKUs: []demoSKU{
		{ID: 21, Name: "月付版", Code: "MONTHLY", PriceCents: 1990},
		{ID: 22, Name: "年付版", Code: "YEARLY", PriceCents: 9900},
	}},
	{ID: 3, CategoryID: 1, Title: "人工配置服务", Summary: "用于演示需要填写资料的商品", Description: "该商品会展示机器人暂不支持自定义表单的提示。", PriceCents: 2990, Stock: "充足", ManualForm: true},
	{ID: 4, CategoryID: 2, Title: "限量兑换码", Summary: "用于演示售罄状态", Description: "该演示商品当前没有库存。", PriceCents: 500, Stock: "已售罄"},
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
	case "sendmessage", "editmessagetext":
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
		data = demoOrderPreview(request)
	case "/orders":
		if request.Method == http.MethodGet {
			data = demoOrderList()
		} else {
			data = demoCreateOrder(request)
		}
	case "/payment-channels":
		channelName := "演示在线支付"
		if request.URL.Query().Get("context") == "recharge" {
			channelName = "演示即时到账"
		}
		data = map[string]any{"items": []map[string]any{
			{"id": 1, "name": channelName, "provider_type": "demo", "channel_type": "web", "interaction_mode": "redirect", "fee_rate": "0.00", "fixed_fee": "0.00"},
		}}
	case "/payments":
		data = demoCreatePayment(request)
	case "/wallet":
		data = map[string]any{"balance": money(int(walletBalanceCents.Load())), "currency": "CNY"}
	case "/wallet/recharge":
		data = demoWalletRecharge(request)
	default:
		if strings.HasPrefix(path, "/catalog/products/") {
			id, _ := strconv.ParseUint(strings.TrimPrefix(path, "/catalog/products/"), 10, 64)
			data = demoProductDetail(uint(id))
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
			"price": money(sku.PriceCents), "stock_status": "in_stock", "stock_display": "充足",
		})
	}
	fields := []any{}
	if product.ManualForm {
		fields = []any{map[string]any{
			"key": "hostname", "type": "text", "required": true,
			"label": "主机名称", "placeholder": "demo.example.com",
		}}
	}
	return map[string]any{
		"id": product.ID, "title": product.Title, "summary": product.Summary,
		"description": product.Description, "price_from": money(product.PriceCents), "currency": "CNY",
		"stock_status": stockStatus(product), "stock_display": product.Stock,
		"category_name": categoryName(product.CategoryID), "fulfillment_type": "auto",
		"min_purchase_quantity": 1, "max_purchase_quantity": 5,
		"manual_form_schema": map[string]any{"fields": fields}, "skus": skus,
	}
}

func demoOrderPreview(request *http.Request) map[string]any {
	items := decodeOrderItems(request)
	previewItems := make([]map[string]any, 0, len(items))
	total := 0
	for _, item := range items {
		product, _ := findProduct(item.ProductID)
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
			"subtotal": money(subtotal), "fulfillment_type": "auto",
		})
	}
	return map[string]any{
		"item_count": len(previewItems), "items": previewItems, "original_amount": money(total),
		"coupon_discount": "0.00", "promotion_discount": "0.00", "wholesale_discount": "0.00",
		"total_amount": money(total), "currency": "CNY", "valid": true, "validation_errors": []any{},
	}
}

func demoCreateOrder(request *http.Request) map[string]any {
	preview := demoOrderPreview(request)
	items := preview["items"].([]map[string]any)
	orderID := uint(orderSequence.Add(1))
	orderNo := fmt.Sprintf("DEMO%06d", orderID)
	title := "演示商品"
	if len(items) > 0 {
		title, _ = items[0]["product_title"].(string)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	order := map[string]any{
		"order_id": orderID, "order_no": orderNo, "status": "pending_payment", "currency": "CNY",
		"total_amount": preview["total_amount"], "paid_amount": "0.00", "wallet_paid_amount": "0.00",
		"online_paid_amount": "0.00", "product_title": title, "item_count": len(items), "created_at": createdAt,
	}
	orderMu.Lock()
	demoOrders = append([]map[string]any{order}, demoOrders...)
	orderMu.Unlock()
	result := cloneMap(order)
	result["fulfillment_type"] = "auto"
	return result
}

func demoOrderList() map[string]any {
	orderMu.Lock()
	defer orderMu.Unlock()
	items := make([]map[string]any, len(demoOrders))
	for index, order := range demoOrders {
		items[index] = cloneMap(order)
	}
	return map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 5, "total_pages": pageCount(len(items), 5)}
}

func demoCreatePayment(request *http.Request) map[string]any {
	var payload struct {
		OrderID    uint `json:"order_id"`
		ChannelID  uint `json:"channel_id"`
		UseBalance bool `json:"use_balance"`
	}
	_ = json.NewDecoder(request.Body).Decode(&payload)
	orderMu.Lock()
	for _, order := range demoOrders {
		if order["order_id"] == payload.OrderID {
			order["status"] = "completed"
			order["paid_amount"] = order["total_amount"]
			if payload.UseBalance {
				order["wallet_paid_amount"] = order["total_amount"]
			} else {
				order["online_paid_amount"] = order["total_amount"]
			}
		}
	}
	orderMu.Unlock()
	if payload.UseBalance {
		return map[string]any{"order_paid": true, "order_id": payload.OrderID, "status": "succeeded", "currency": "CNY"}
	}
	return map[string]any{
		"order_paid": false, "payment_id": payload.OrderID, "order_id": payload.OrderID,
		"status": "pending", "channel_name": "演示在线支付", "amount": "9.90", "currency": "CNY",
		"pay_url": "https://example.com/demo-payment",
	}
}

func demoWalletRecharge(request *http.Request) map[string]any {
	var payload struct {
		Amount    string `json:"amount"`
		ChannelID uint   `json:"channel_id"`
	}
	_ = json.NewDecoder(request.Body).Decode(&payload)
	id := rechargeSequence.Add(1)
	amount, _ := strconv.ParseFloat(payload.Amount, 64)
	walletBalanceCents.Add(int64(amount*100 + 0.5))
	return map[string]any{
		"recharge_no": fmt.Sprintf("RECHARGE%06d", id),
		"payment": map[string]any{
			"id": id, "amount": payload.Amount, "fee_amount": "0.00", "currency": "CNY",
			"status": "success", "interaction_mode": "demo",
			"expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
}

type orderItem struct {
	ProductID uint `json:"product_id"`
	SKUID     uint `json:"sku_id"`
	Quantity  int  `json:"quantity"`
}

func decodeOrderItems(request *http.Request) []orderItem {
	var payload struct {
		Items []orderItem `json:"items"`
	}
	_ = json.NewDecoder(request.Body).Decode(&payload)
	return payload.Items
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
	if product.Stock == "已售罄" {
		return "out_of_stock"
	}
	return "in_stock"
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
