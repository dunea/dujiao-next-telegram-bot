package session

import (
	"slices"
	"sync"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

// State contains the small amount of ephemeral UI state needed by the bot.
// Account, order and wallet data remain authoritative in Dujiao-Next.
type State struct {
	Locale   string
	Flow     string
	Purchase *PurchaseDraft
	GiftCard *GiftCardInput
	Recharge *RechargeDraft
}

// RechargeDraft binds amount/channel selection to one private-chat message.
type RechargeDraft struct {
	ID         string
	ChatID     int64
	MessageID  int
	Custom     bool
	Amount     string
	Currency   string
	Channels   []dujiao.PaymentChannel
	Page       int
	ExpiresAt  time.Time
	Submitting bool
}

// GiftCardInput holds prompt metadata, never the gift card code.
type GiftCardInput struct {
	ID         string
	ChatID     int64
	PromptID   int
	ExpiresAt  time.Time
	Submitting bool
}

type PurchaseDraft struct {
	ID              string
	ChatID          int64
	ProductID       uint
	SKUID           uint
	Quantity        int
	CouponCode      string
	PreviewTotal    string
	PreviewCurrency string
	Schema          dujiao.ManualFormSchema
	Values          map[string]any
	Step            int
	Revision        int
	PromptID        int
	PreviewID       int
	Ready           bool
	Submitting      bool
	ExpiresAt       time.Time
}

func cloneState(state State) State {
	if state.Recharge != nil {
		draft := *state.Recharge
		draft.Channels = slices.Clone(draft.Channels)
		state.Recharge = &draft
	}
	if state.GiftCard != nil {
		input := *state.GiftCard
		state.GiftCard = &input
	}
	if state.Purchase == nil {
		return state
	}
	draft := *state.Purchase
	draft.Schema.Fields = slices.Clone(draft.Schema.Fields)
	for i := range draft.Schema.Fields {
		field := &draft.Schema.Fields[i]
		field.Options = slices.Clone(field.Options)
		if field.Min != nil {
			value := *field.Min
			field.Min = &value
		}
		if field.Max != nil {
			value := *field.Max
			field.Max = &value
		}
		if field.MaxLen != nil {
			value := *field.MaxLen
			field.MaxLen = &value
		}
	}
	draft.Values = make(map[string]any, len(draft.Values))
	for key, value := range state.Purchase.Values {
		if values, ok := value.([]string); ok {
			value = append([]string(nil), values...)
		}
		draft.Values[key] = value
	}
	state.Purchase = &draft
	return state
}

type Memory struct {
	mu            sync.RWMutex
	defaultLocale string
	users         map[int64]State
}

func NewMemory(defaultLocale string) *Memory {
	return &Memory{defaultLocale: defaultLocale, users: make(map[int64]State)}
}

func (m *Memory) Get(userID int64) State {
	m.mu.Lock()
	state, ok := m.users[userID]
	if state.Recharge != nil && !time.Now().Before(state.Recharge.ExpiresAt) {
		state.Recharge = nil
		if state.Flow == "recharge_amount" || state.Flow == "recharge_channels" {
			state.Flow = ""
		}
		m.users[userID] = state
	}
	if state.GiftCard != nil && !time.Now().Before(state.GiftCard.ExpiresAt) {
		state.GiftCard = nil
		if state.Flow == "gift_card" {
			state.Flow = ""
		}
		m.users[userID] = state
	}
	if state.Purchase != nil && !time.Now().Before(state.Purchase.ExpiresAt) {
		state.Purchase = nil
		if state.Flow == "manual_form" || state.Flow == "coupon_code" {
			state.Flow = ""
		}
		m.users[userID] = state
	}
	state = cloneState(state)
	m.mu.Unlock()
	if !ok {
		state.Locale = m.defaultLocale
	}
	return state
}

func (m *Memory) Update(userID int64, update func(*State)) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.users[userID]
	state = cloneState(state)
	if !ok {
		state.Locale = m.defaultLocale
	}
	update(&state)
	m.users[userID] = cloneState(state)
	return cloneState(state)
}

func (m *Memory) ResetFlow(userID int64) {
	m.Update(userID, func(state *State) { state.Flow = ""; state.Purchase = nil; state.GiftCard = nil; state.Recharge = nil })
}
