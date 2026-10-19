package session

import "sync"

// State contains the small amount of ephemeral UI state needed by the bot.
// Account, order and wallet data remain authoritative in Dujiao-Next.
type State struct {
	Locale string
	Flow   string
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
	m.mu.RLock()
	state, ok := m.users[userID]
	m.mu.RUnlock()
	if !ok {
		state.Locale = m.defaultLocale
	}
	return state
}

func (m *Memory) Update(userID int64, update func(*State)) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.users[userID]
	if !ok {
		state.Locale = m.defaultLocale
	}
	update(&state)
	m.users[userID] = state
	return state
}

func (m *Memory) ResetFlow(userID int64) {
	m.Update(userID, func(state *State) { state.Flow = "" })
}
