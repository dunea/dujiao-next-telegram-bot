package dujiao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyClientRejectsIncompleteWallets(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":{"balance":"1.00","currency":"CNY"}}`, `{"status_code":0,"data":{"balance":null,"currency":"CNY"}}`, `{"status_code":0,"data":{"balance":"1.00"}}`, `{"status_code":0,"data":{"balance":1,"currency":"CNY"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		client, _ := NewReadOnlyClient(server.URL+"/nested", "key", "secret", time.Second)
		_, err := client.Wallet(context.Background(), "42")
		server.Close()
		if !errors.Is(err, ErrInvalidReadResponse) {
			t.Fatal("incomplete response accepted", err)
		}
	}
}

func TestReadOnlyClientRejectsAllWritesBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	client, err := NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/telegram/heartbeat", "/identities/telegram/provision", "/orders/preview", "/orders", "/payments", "/wallet/recharge", "/wallet/gift-card/redeem", "/affiliate/withdraws"} {
		_, err := post[map[string]any](context.Background(), client, path, map[string]string{"secret": "not-transmitted"})
		if !errors.Is(err, ErrReadOnly) {
			t.Fatal("write permitted", path, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("read-only write reached server")
	}
}
func TestReadOnlyClientDoesNotFollowRedirects(t *testing.T) {
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, _ := NewReadOnlyClient(server.URL, "key", "secret", time.Second)
	_, err := client.BotConfig(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.HTTPStatus != 307 || requests.Load() != 0 {
		t.Fatal("redirect followed or status lost", err)
	}
}
