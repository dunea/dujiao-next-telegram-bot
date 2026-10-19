package dujiao

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

type AffiliateProfile struct {
	ID     uint   `json:"id"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

type AffiliateDashboard struct {
	Opened              bool        `json:"opened"`
	AffiliateCode       string      `json:"affiliate_code"`
	PromotionPath       string      `json:"promotion_path"`
	ClickCount          int64       `json:"click_count"`
	ValidOrderCount     int64       `json:"valid_order_count"`
	ConversionRate      float64     `json:"conversion_rate"`
	PendingCommission   string      `json:"pending_commission"`
	AvailableCommission string      `json:"available_commission"`
	WithdrawnCommission string      `json:"withdrawn_commission"`
	MinWithdrawAmount   json.Number `json:"min_withdraw_amount"`
	WithdrawChannels    []string    `json:"withdraw_channels"`
}

type AffiliateClick struct {
	ChannelUserID string `json:"channel_user_id"`
	AffiliateCode string `json:"affiliate_code"`
	VisitorKey    string `json:"visitor_key"`
	LandingPath   string `json:"landing_path"`
	Referrer      string `json:"referrer"`
}

type AffiliateCommission struct {
	ID               uint   `json:"id"`
	OrderNo          string `json:"order_no"`
	CommissionAmount string `json:"commission_amount"`
	RatePercent      string `json:"rate_percent"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

type AffiliateCommissionList struct {
	Items      []AffiliateCommission `json:"items"`
	Page       int                   `json:"page"`
	TotalPages int64                 `json:"total_pages"`
	Total      int64                 `json:"total"`
}

type AffiliateWithdrawal struct {
	ID           uint   `json:"id"`
	Amount       string `json:"amount"`
	Channel      string `json:"channel"`
	Account      string `json:"account"`
	Status       string `json:"status"`
	RejectReason string `json:"reject_reason"`
	CreatedAt    string `json:"created_at"`
}

type AffiliateWithdrawalList struct {
	Items      []AffiliateWithdrawal `json:"items"`
	Page       int                   `json:"page"`
	TotalPages int64                 `json:"total_pages"`
	Total      int64                 `json:"total"`
}

func (c *Client) AffiliateDashboard(ctx context.Context, userID string) (AffiliateDashboard, error) {
	return get[AffiliateDashboard](ctx, c, "/affiliate/dashboard", url.Values{"channel_user_id": {userID}})
}

func (c *Client) OpenAffiliate(ctx context.Context, identity TelegramIdentity) (AffiliateProfile, error) {
	profile, err := post[AffiliateProfile](ctx, c, "/affiliate/open", identity)
	if err == nil && (profile.ID == 0 || profile.Code == "" || profile.Status == "") {
		err = errors.New("incomplete affiliate profile response")
	}
	return profile, err
}

func (c *Client) TrackAffiliateClick(ctx context.Context, click AffiliateClick) error {
	result, err := post[struct {
		OK bool `json:"ok"`
	}](ctx, c, "/affiliate/click", click)
	if err == nil && !result.OK {
		err = errors.New("affiliate click was not acknowledged")
	}
	return err
}

func (c *Client) AffiliateCommissions(ctx context.Context, userID string, page int) (AffiliateCommissionList, error) {
	return get[AffiliateCommissionList](ctx, c, "/affiliate/commissions", affiliatePageQuery(userID, page))
}

func (c *Client) AffiliateWithdrawals(ctx context.Context, userID string, page int) (AffiliateWithdrawalList, error) {
	return get[AffiliateWithdrawalList](ctx, c, "/affiliate/withdraws", affiliatePageQuery(userID, page))
}

func affiliatePageQuery(userID string, page int) url.Values {
	if page < 1 {
		page = 1
	}
	return url.Values{"channel_user_id": {userID}, "page": {strconv.Itoa(page)}, "page_size": {"5"}}
}
