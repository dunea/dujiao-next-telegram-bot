package session

import (
	"slices"
	"sync"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/broadcast"
	"github.com/dunea/dujiao-next-telegram-bot/internal/dujiao"
)

// State contains the small amount of ephemeral UI state needed by the bot.
// Account, order and wallet data remain authoritative in Dujiao-Next.
type State struct {
	// Err is local storage status, never persisted. Failed writes must not authorize side effects.
	Err             error `json:"-"`
	Locale          string
	Flow            string
	Purchase        *PurchaseDraft
	GiftCard        *GiftCardInput
	Recharge        *RechargeDraft
	Withdrawal      *WithdrawalDraft
	WithdrawalGuard *WithdrawalGuard
}

type WithdrawalDraft struct {
	ID        string
	ChatID    int64
	MessageID int
	Step      string
	Amount    string
	Currency  string
	Minimum   string
	Available string
	Channels  []string
	Channel   string
	Account   string
	Busy      bool
	ExpiresAt time.Time
}

// WithdrawalGuard survives menu navigation and draft expiry. It never contains
// payment details. Unknown writes require a deliberate history review to unlock.
type WithdrawalGuard struct {
	ID              string
	Status          string
	ReviewMessageID int
	LeaseUntil      time.Time
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
	if state.Withdrawal != nil {
		draft := *state.Withdrawal
		draft.Channels = slices.Clone(draft.Channels)
		state.Withdrawal = &draft
	}
	if state.WithdrawalGuard != nil {
		guard := *state.WithdrawalGuard
		state.WithdrawalGuard = &guard
	}
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
	broadcast     *broadcast.Memory
	mu            sync.RWMutex
	defaultLocale string
	users         map[int64]State
}

func NewMemory(defaultLocale string) *Memory {
	return &Memory{defaultLocale: defaultLocale, users: make(map[int64]State)}
}

type Store interface {
	// Get/Update failures return State.Err, never an empty usable session.
	// In particular, callers must check Err even if an Update callback ran.
	Get(int64) State
	Update(int64, func(*State)) State
	ResetFlow(int64) error
	Health() error
	Close() error
}

func normalizeState(state State, now time.Time) State {
	if guard := state.WithdrawalGuard; guard != nil && (guard.Status == "checking" || guard.Status == "posting") && !guard.LeaseUntil.IsZero() && !now.Before(guard.LeaseUntil) {
		guard.Status, guard.ReviewMessageID = "unknown", 0
		state.Withdrawal = nil
		if state.Flow == "withdrawal" {
			state.Flow = ""
		}
	}
	if state.Withdrawal != nil && !now.Before(state.Withdrawal.ExpiresAt) {
		state.Withdrawal = nil
		if state.Flow == "withdrawal" {
			state.Flow = ""
		}
	}
	if state.Recharge != nil && !now.Before(state.Recharge.ExpiresAt) {
		state.Recharge = nil
		if state.Flow == "recharge_amount" || state.Flow == "recharge_channels" {
			state.Flow = ""
		}
	}
	if state.GiftCard != nil && !now.Before(state.GiftCard.ExpiresAt) {
		state.GiftCard = nil
		if state.Flow == "gift_card" {
			state.Flow = ""
		}
	}
	if state.Purchase != nil && !now.Before(state.Purchase.ExpiresAt) {
		state.Purchase = nil
		if state.Flow == "manual_form" || state.Flow == "coupon_code" {
			state.Flow = ""
		}
	}
	return state
}

func (m *Memory) Get(userID int64) State {
	m.mu.Lock()
	state, ok := m.users[userID]
	state = normalizeState(cloneState(state), time.Now())
	if ok {
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
	state = normalizeState(cloneState(state), time.Now())
	if !ok {
		state.Locale = m.defaultLocale
	}
	update(&state)
	m.users[userID] = cloneState(state)
	return cloneState(state)
}

func resetFlow(state *State) {
	state.Flow = ""
	state.Purchase = nil
	state.GiftCard = nil
	state.Recharge = nil
	state.Withdrawal = nil
}

func (m *Memory) ResetFlow(userID int64) error {
	return m.Update(userID, resetFlow).Err
}

func (m *Memory) Health() error { return nil }
func (m *Memory) Close() error  { return nil }
