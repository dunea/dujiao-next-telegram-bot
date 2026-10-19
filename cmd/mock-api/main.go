// Command mock-api provides deterministic Dujiao-Next and Telegram endpoints
// for local Docker integration and load tests. It must never be used in production.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var telegramRequests atomic.Uint64
var dujiaoRequests atomic.Uint64

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
				"basic":   map[string]any{"display_name": "Integration Store", "support_url": ""},
				"welcome": map[string]any{"enabled": true, "message": map[string]string{"zh-CN": "集成测试商店"}},
				"help":    map[string]any{"enabled": true, "intro": map[string]string{"zh-CN": "测试帮助"}},
				"menu":    map[string]any{"items": []any{}},
			},
		}
	case "/telegram/heartbeat":
		data = map[string]any{"config_version": 1}
	case "/identities/telegram/resolve", "/identities/telegram/provision":
		data = map[string]any{"bound": true, "created": false, "user": map[string]any{"id": 1}}
	case "/catalog/categories":
		data = map[string]any{"items": []map[string]any{{"id": 1, "name": "Test", "product_count": 1}}}
	case "/catalog/products":
		data = map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 8, "total_page": 0}
	case "/orders":
		if request.Method == http.MethodGet {
			data = map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 5, "total_pages": 0}
		}
	case "/wallet":
		data = map[string]any{"balance": "100.00", "currency": "CNY"}
	}
	writeJSON(w, map[string]any{"status_code": 0, "msg": "success", "data": data, "request_id": "mock"})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
