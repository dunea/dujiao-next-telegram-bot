package dujiao

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrReadOnly = errors.New("read-only compatibility client rejects non-GET requests")
var ErrInvalidReadResponse = errors.New("incomplete read-only compatibility response")

// NewReadOnlyClient is for upgrade diagnostics, never transaction execution.
// It rejects writes at the transport boundary and does not follow redirects.
func NewReadOnlyClient(baseURL, key, secret string, timeout time.Duration) (*Client, error) {
	client, err := NewClient(baseURL, key, secret, timeout)
	if err != nil {
		return nil, err
	}
	client.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.http.Transport = readOnlyTransport{base: http.DefaultTransport}
	return client, nil
}

type readOnlyTransport struct{ base http.RoundTripper }

func (r readOnlyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet {
		return nil, ErrReadOnly
	}
	response, err := r.base.RoundTrip(request)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	_ = response.Body.Close()
	if err != nil || len(body) > 4<<20 {
		return nil, ErrInvalidReadResponse
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Status *int            `json:"status_code"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Status == nil {
		return nil, ErrInvalidReadResponse
	}
	if *envelope.Status != 0 {
		return response, nil
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope.Data, &data) != nil || data == nil {
		return nil, ErrInvalidReadResponse
	}
	var required []string
	path := request.URL.Path
	if index := strings.LastIndex(path, "/api/v1/channel/"); index >= 0 {
		path = path[index:]
	}
	switch path {
	case "/api/v1/channel/telegram/config":
		var config struct {
			Enabled *bool `json:"enabled"`
		}
		if json.Unmarshal(data["config"], &config) != nil || config.Enabled == nil {
			return nil, ErrInvalidReadResponse
		}
	case "/api/v1/channel/catalog/categories", "/api/v1/channel/payment-channels", "/api/v1/channel/wallet/transactions", "/api/v1/channel/affiliate/commissions", "/api/v1/channel/affiliate/withdraws":
		required = []string{"items"}
	case "/api/v1/channel/wallet":
		var wallet struct {
			Balance  *string `json:"balance"`
			Currency *string `json:"currency"`
		}
		if json.Unmarshal(envelope.Data, &wallet) != nil || wallet.Balance == nil || wallet.Currency == nil || *wallet.Balance == "" || *wallet.Currency == "" {
			return nil, ErrInvalidReadResponse
		}
	case "/api/v1/channel/affiliate/dashboard":
		var dashboard struct {
			Opened *bool `json:"opened"`
		}
		if json.Unmarshal(envelope.Data, &dashboard) != nil || dashboard.Opened == nil {
			return nil, ErrInvalidReadResponse
		}
	}
	for _, key := range required {
		if _, ok := data[key]; !ok {
			return nil, ErrInvalidReadResponse
		}
	}
	return response, nil
}
