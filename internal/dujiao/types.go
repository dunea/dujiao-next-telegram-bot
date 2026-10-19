package dujiao

import "encoding/json"

type Envelope struct {
	StatusCode int             `json:"status_code"`
	Message    string          `json:"msg"`
	Data       json.RawMessage `json:"data"`
	ErrorCode  string          `json:"error_code"`
	RequestID  string          `json:"request_id"`
}

type LocalizedText map[string]string

type BotConfigResponse struct {
	Config        BotConfig `json:"config"`
	ConfigVersion int       `json:"config_version"`
}

type BotConfig struct {
	Enabled       bool             `json:"enabled"`
	BotToken      string           `json:"bot_token"`
	DefaultLocale string           `json:"default_locale"`
	ConfigVersion int              `json:"config_version"`
	Basic         BotBasicConfig   `json:"basic"`
	Welcome       BotWelcomeConfig `json:"welcome"`
	Help          BotHelpConfig    `json:"help"`
	Menu          BotMenuConfig    `json:"menu"`
}

type BotBasicConfig struct {
	DisplayName string        `json:"display_name"`
	Description LocalizedText `json:"description"`
	SupportURL  string        `json:"support_url"`
	CoverURL    string        `json:"cover_url"`
}

type BotWelcomeConfig struct {
	Enabled bool          `json:"enabled"`
	Message LocalizedText `json:"message"`
}

type BotHelpConfig struct {
	Enabled bool          `json:"enabled"`
	Title   LocalizedText `json:"title"`
	Intro   LocalizedText `json:"intro"`
}

type BotMenuConfig struct {
	Items []BotMenuItem `json:"items"`
}

type BotMenuItem struct {
	Key     string        `json:"key"`
	Enabled bool          `json:"enabled"`
	Order   int           `json:"order"`
	Label   LocalizedText `json:"label"`
	Action  BotMenuAction `json:"action"`
}

type BotMenuAction struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type TelegramIdentity struct {
	ChannelUserID string `json:"channel_user_id"`
	Username      string `json:"username,omitempty"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	Locale        string `json:"locale,omitempty"`
	InviteCode    string `json:"invite_code,omitempty"`
}

type IdentityResult struct {
	Bound         bool   `json:"bound"`
	Created       bool   `json:"created"`
	UserID        uint   `json:"user_id"`
	Email         string `json:"email"`
	Nickname      string `json:"nickname"`
	Locale        string `json:"locale"`
	MemberLevel   string `json:"member_level"`
	WalletBalance string `json:"wallet_balance"`
}

type Category struct {
	ID           uint   `json:"id"`
	ParentID     uint   `json:"parent_id"`
	Name         string `json:"name"`
	Icon         string `json:"icon"`
	Slug         string `json:"slug"`
	ProductCount int64  `json:"product_count"`
}

type CategoryList struct {
	Items []Category `json:"items"`
}

type Product struct {
	ID              uint   `json:"id"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	ImageURL        string `json:"image_url"`
	PriceFrom       string `json:"price_from"`
	MemberPriceFrom string `json:"member_price_from"`
	Currency        string `json:"currency"`
	StockStatus     string `json:"stock_status"`
	StockDisplay    string `json:"stock_display"`
	CategoryName    string `json:"category_name"`
}

type ProductList struct {
	Items     []Product `json:"items"`
	Total     int64     `json:"total"`
	Page      int       `json:"page"`
	PageSize  int       `json:"page_size"`
	TotalPage int64     `json:"total_page"`
}

type ProductDetail struct {
	Product
	Description         string           `json:"description"`
	FulfillmentType     string           `json:"fulfillment_type"`
	MinPurchaseQuantity int              `json:"min_purchase_quantity"`
	MaxPurchaseQuantity int              `json:"max_purchase_quantity"`
	ManualFormSchema    ManualFormSchema `json:"manual_form_schema"`
	SKUs                []SKU            `json:"skus"`
}

type SKU struct {
	ID           uint   `json:"id"`
	SKUCode      string `json:"sku_code"`
	SpecValues   string `json:"spec_values"`
	Price        string `json:"price"`
	MemberPrice  string `json:"member_price"`
	StockStatus  string `json:"stock_status"`
	StockDisplay string `json:"stock_display"`
}

type ManualFormSchema struct {
	Fields []ManualFormField `json:"fields"`
}

type ManualFormField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Label       string   `json:"label"`
	Placeholder string   `json:"placeholder"`
	Regex       string   `json:"regex"`
	Options     []string `json:"options"`
}

type OrderItemRequest struct {
	ProductID       uint   `json:"product_id"`
	SKUID           uint   `json:"sku_id,omitempty"`
	Quantity        int    `json:"quantity"`
	FulfillmentType string `json:"fulfillment_type,omitempty"`
}

type OrderRequest struct {
	TelegramIdentity
	Items          []OrderItemRequest `json:"items"`
	CouponCode     string             `json:"coupon_code,omitempty"`
	AffiliateCode  string             `json:"affiliate_code,omitempty"`
	ManualFormData map[string]any     `json:"manual_form_data,omitempty"`
}

type OrderPreview struct {
	ItemCount         int                `json:"item_count"`
	OriginalAmount    string             `json:"original_amount"`
	Items             []OrderPreviewItem `json:"items"`
	CouponDiscount    string             `json:"coupon_discount"`
	PromotionDiscount string             `json:"promotion_discount"`
	WholesaleDiscount string             `json:"wholesale_discount"`
	TotalAmount       string             `json:"total_amount"`
	Currency          string             `json:"currency"`
	Valid             bool               `json:"valid"`
	ValidationErrors  []string           `json:"validation_errors"`
}

type OrderPreviewItem struct {
	ProductID       uint   `json:"product_id"`
	ProductTitle    string `json:"product_title"`
	SKUID           uint   `json:"sku_id"`
	SKUName         string `json:"sku_name"`
	Quantity        int    `json:"quantity"`
	UnitPrice       string `json:"unit_price"`
	Subtotal        string `json:"subtotal"`
	FulfillmentType string `json:"fulfillment_type"`
}

type OrderCreated struct {
	OrderID         uint   `json:"order_id"`
	OrderNo         string `json:"order_no"`
	Status          string `json:"status"`
	FulfillmentType string `json:"fulfillment_type"`
	Currency        string `json:"currency"`
	ItemCount       int    `json:"item_count"`
	TotalAmount     string `json:"total_amount"`
	ExpiresAt       string `json:"expires_at"`
	CreatedAt       string `json:"created_at"`
}

type OrderListItem struct {
	OrderID          uint   `json:"order_id"`
	OrderNo          string `json:"order_no"`
	Status           string `json:"status"`
	Currency         string `json:"currency"`
	TotalAmount      string `json:"total_amount"`
	PaidAmount       string `json:"paid_amount"`
	WalletPaidAmount string `json:"wallet_paid_amount"`
	OnlinePaidAmount string `json:"online_paid_amount"`
	ProductTitle     string `json:"product_title"`
	ItemCount        int    `json:"item_count"`
	ExpiresAt        string `json:"expires_at"`
	CreatedAt        string `json:"created_at"`
}

type OrderList struct {
	Items      []OrderListItem `json:"items"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	Total      int64           `json:"total"`
	TotalPages int64           `json:"total_pages"`
}

type Wallet struct {
	Balance  string `json:"balance"`
	Currency string `json:"currency"`
}

type PaymentChannel struct {
	ID              uint   `json:"id"`
	Name            string `json:"name"`
	ProviderType    string `json:"provider_type"`
	ChannelType     string `json:"channel_type"`
	InteractionMode string `json:"interaction_mode"`
	FeeRate         string `json:"fee_rate"`
	FixedFee        string `json:"fixed_fee"`
}

type PaymentChannelList struct {
	Items             []PaymentChannel `json:"items"`
	WalletOnlyPayment bool             `json:"wallet_only_payment"`
}

type PaymentResult struct {
	OrderPaid       bool   `json:"order_paid"`
	PaymentID       uint   `json:"payment_id"`
	OrderID         uint   `json:"order_id"`
	Status          string `json:"status"`
	ChannelName     string `json:"channel_name"`
	ProviderType    string `json:"provider_type"`
	InteractionMode string `json:"interaction_mode"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	PayURL          string `json:"pay_url"`
	QRCode          string `json:"qr_code"`
	WalletAddress   string `json:"wallet_address"`
	ChainAmount     string `json:"chain_amount"`
	Chain           string `json:"chain"`
	ExpiresAt       string `json:"expires_at"`
}
