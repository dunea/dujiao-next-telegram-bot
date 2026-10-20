package dujiao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const channelPrefix = "/api/v1/channel"

type APIError struct {
	HTTPStatus int
	StatusCode int
	ErrorCode  string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("dujiao: %s: %s", e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("dujiao: status %d: %s", e.StatusCode, e.Message)
}

type Client struct {
	baseURL string
	key     string
	secret  string
	http    *http.Client
	now     func() time.Time
}

func NewClient(baseURL, key, secret string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Dujiao base URL %q", baseURL)
	}
	if key == "" || secret == "" {
		return nil, errors.New("Dujiao channel key and secret are required")
	}
	return &Client{
		baseURL: parsed.String(),
		key:     key,
		secret:  secret,
		http:    &http.Client{Timeout: timeout},
		now:     time.Now,
	}, nil
}

func (c *Client) BotConfig(ctx context.Context) (BotConfigResponse, error) {
	return get[BotConfigResponse](ctx, c, "/telegram/config", nil)
}

func (c *Client) Heartbeat(ctx context.Context, version, webhookStatus string, warnings []string) error {
	_, err := post[map[string]any](ctx, c, "/telegram/heartbeat", map[string]any{
		"bot_version":    version,
		"webhook_status": webhookStatus,
		"license_status": "community",
		"warnings":       warnings,
	})
	return err
}

func (c *Client) ResolveIdentity(ctx context.Context, identity TelegramIdentity) (IdentityResult, error) {
	return post[IdentityResult](ctx, c, "/identities/telegram/resolve", identity)
}

func (c *Client) ProvisionIdentity(ctx context.Context, identity TelegramIdentity) (IdentityResult, error) {
	return post[IdentityResult](ctx, c, "/identities/telegram/provision", identity)
}

func (c *Client) Categories(ctx context.Context, locale string) ([]Category, error) {
	result, err := get[CategoryList](ctx, c, "/catalog/categories", url.Values{"locale": {locale}})
	return result.Items, err
}

func (c *Client) Products(ctx context.Context, locale, channelUserID string, categoryID uint, page, pageSize int) (ProductList, error) {
	query := url.Values{
		"locale":          {locale},
		"channel_user_id": {channelUserID},
		"page":            {strconv.Itoa(page)},
		"page_size":       {strconv.Itoa(pageSize)},
	}
	if categoryID > 0 {
		query.Set("category_id", strconv.FormatUint(uint64(categoryID), 10))
		query.Set("exact", "1")
	}
	return get[ProductList](ctx, c, "/catalog/products", query)
}

func (c *Client) Product(ctx context.Context, locale, channelUserID string, productID uint) (ProductDetail, error) {
	query := url.Values{"locale": {locale}, "channel_user_id": {channelUserID}}
	return get[ProductDetail](ctx, c, "/catalog/products/"+strconv.FormatUint(uint64(productID), 10), query)
}

func (c *Client) PreviewOrder(ctx context.Context, request OrderRequest) (OrderPreview, error) {
	return post[OrderPreview](ctx, c, "/orders/preview", request)
}

func (c *Client) CreateOrder(ctx context.Context, request OrderRequest) (OrderCreated, error) {
	return post[OrderCreated](ctx, c, "/orders", request)
}

func (c *Client) Orders(ctx context.Context, channelUserID, locale, status string, page, pageSize int) (OrderList, error) {
	query := url.Values{
		"channel_user_id": {channelUserID},
		"locale":          {locale},
		"page":            {strconv.Itoa(page)},
		"page_size":       {strconv.Itoa(pageSize)},
	}
	if status != "" {
		query.Set("status", status)
	}
	return get[OrderList](ctx, c, "/orders", query)
}

func (c *Client) Order(ctx context.Context, channelUserID, locale string, orderID uint) (OrderDetail, error) {
	return get[OrderDetail](ctx, c, "/orders/"+strconv.FormatUint(uint64(orderID), 10), url.Values{
		"channel_user_id": {channelUserID},
		"locale":          {locale},
	})
}

func (c *Client) CancelOrder(ctx context.Context, channelUserID string, orderID uint) (OrderCancelled, error) {
	return post[OrderCancelled](ctx, c, "/orders/"+strconv.FormatUint(uint64(orderID), 10)+"/cancel", map[string]any{
		"channel_user_id": channelUserID,
		"reason":          "cancelled_by_telegram_user",
	})
}

func (c *Client) Wallet(ctx context.Context, channelUserID string) (Wallet, error) {
	return get[Wallet](ctx, c, "/wallet", url.Values{"channel_user_id": {channelUserID}})
}

func (c *Client) RedeemGiftCard(ctx context.Context, channelUserID, code string) (GiftCardRedemption, error) {
	result, err := post[GiftCardRedemption](ctx, c, "/wallet/gift-card/redeem", map[string]any{"channel_user_id": channelUserID, "code": code})
	if err == nil && (result.GiftCard.Status != "redeemed" || result.WalletDelta == "" || result.Wallet.Balance == "" || result.GiftCard.Currency == "") {
		err = errors.New("incomplete gift card redemption response")
	}
	return result, err
}

func (c *Client) WalletTransactions(ctx context.Context, channelUserID string, page, pageSize int) (WalletTransactionList, error) {
	return get[WalletTransactionList](ctx, c, "/wallet/transactions", url.Values{
		"channel_user_id": {channelUserID},
		"page":            {strconv.Itoa(page)},
		"page_size":       {strconv.Itoa(pageSize)},
	})
}

func (c *Client) RechargeChannels(ctx context.Context) (PaymentChannelList, error) {
	return get[PaymentChannelList](ctx, c, "/payment-channels", url.Values{"context": {"recharge"}})
}

func (c *Client) CreateWalletRecharge(ctx context.Context, channelUserID, amount string, channelID uint) (WalletRechargeResult, error) {
	return post[WalletRechargeResult](ctx, c, "/wallet/recharge", map[string]any{
		"channel_user_id": channelUserID,
		"amount":          amount,
		"channel_id":      channelID,
	})
}

func (c *Client) PaymentChannels(ctx context.Context, channelUserID, orderNo string) (PaymentChannelList, error) {
	query := url.Values{"channel_user_id": {channelUserID}}
	if orderNo != "" {
		query.Set("order_no", orderNo)
	}
	return get[PaymentChannelList](ctx, c, "/payment-channels", query)
}

func (c *Client) CreatePayment(ctx context.Context, channelUserID string, orderID, channelID uint, useBalance bool) (PaymentResult, error) {
	return post[PaymentResult](ctx, c, "/payments", map[string]any{
		"channel_user_id": channelUserID,
		"order_id":        orderID,
		"channel_id":      channelID,
		"use_balance":     useBalance,
	})
}

func (c *Client) LatestPayment(ctx context.Context, channelUserID string, orderID uint) (PaymentResult, error) {
	return get[PaymentResult](ctx, c, "/payments/latest", url.Values{"channel_user_id": {channelUserID}, "order_id": {strconv.FormatUint(uint64(orderID), 10)}})
}

func get[T any](ctx context.Context, c *Client, path string, query url.Values) (T, error) {
	return request[T](ctx, c, http.MethodGet, path, query, nil)
}

func post[T any](ctx context.Context, c *Client, path string, payload any) (T, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("marshal request: %w", err)
	}
	return request[T](ctx, c, http.MethodPost, path, nil, body)
}

func request[T any](ctx context.Context, c *Client, method, relativePath string, query url.Values, body []byte) (T, error) {
	var zero T
	path := channelPrefix + relativePath
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("create request: %w", err)
	}
	timestamp := c.now().Unix()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Dujiao-Next-Channel-Key", c.key)
	req.Header.Set("Dujiao-Next-Channel-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("Dujiao-Next-Channel-Signature", Sign(c.secret, method, path, timestamp, body))

	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("request Dujiao: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return zero, fmt.Errorf("read Dujiao response: %w", err)
	}

	var envelope Envelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		// Older stores/proxies may return plain-text 404/501 for missing routes.
		// Preserve the status without exposing an untrusted HTML/error body.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return zero, &APIError{HTTPStatus: resp.StatusCode, StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return zero, fmt.Errorf("decode Dujiao response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.StatusCode != 0 {
		return zero, &APIError{
			HTTPStatus: resp.StatusCode,
			StatusCode: envelope.StatusCode,
			ErrorCode:  envelope.ErrorCode,
			Message:    envelope.Message,
			RequestID:  envelope.RequestID,
		}
	}
	if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return zero, nil
	}
	var result T
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return zero, fmt.Errorf("decode Dujiao data: %w", err)
	}
	return result, nil
}
