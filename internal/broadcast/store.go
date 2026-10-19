// Package broadcast implements opt-in, bounded, restart-safe announcement jobs.
package broadcast

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const RegistryLimit = 5000
const DraftLifetime = 15 * time.Minute
const JobLifetime = 24 * time.Hour
const Lease = time.Minute

var (
	ErrUnavailable = errors.New("broadcast storage unavailable")
	ErrConflict    = errors.New("broadcast changed; refresh before acting")
	ErrCapacity    = errors.New("broadcast capacity exceeded")
)

type Recipient struct {
	ID      int64
	Consent string
}
type Draft struct {
	ID         string
	Stage      string
	MessageID  int
	Text       string
	Recipients []Recipient
	ExpiresAt  time.Time
}
type Flight struct {
	Token     string
	Recipient Recipient
	Index     int
	Until     time.Time
}
type Job struct {
	ID                                                   string
	OwnerID                                              int64
	Worker                                               string
	Text                                                 string
	Recipients                                           []Recipient
	Total, Next, Sent, Failed, Skipped, Unknown, Retries int
	Status                                               string
	Flight                                               *Flight
	NotBefore, ExpiresAt                                 time.Time
}
type State struct {
	Version int
	Drafts  map[int64]*Draft
	Job     *Job
	// Telegram flood cooldown belongs to the sender, not just one campaign.
	NotBefore time.Time
}

type Store interface {
	View(context.Context) (State, error)
	// Update executes the mutation once; a closure running is NOT a commit acknowledgement.
	Update(context.Context, func(*State) error) (State, error)
	SetSubscribed(context.Context, int64, bool) error
	Consent(context.Context, int64) (string, error)
	Recipients(context.Context) ([]Recipient, error)
	RemoveIf(context.Context, Recipient) error
}

func NewState() State { return State{Version: 1, Drafts: make(map[int64]*Draft)} }
func Active(job *Job) bool {
	return job != nil && (job.Status == "prepared" || job.Status == "queued" || job.Status == "running" || job.Status == "paused")
}
func Payload(text string) string {
	return "📣 商店公告 / Store announcement\n\n" + text + "\n\n仅发送给公告订阅者。发送 /unsubscribe 可退订公告（不影响订单通知）。\nUnsubscribe from announcements: /unsubscribe"
}
func ValidText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	units := 0
	for _, r := range text {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) {
			return false
		}
		units += utf16.RuneLen(r)
	}
	return units > 0 && units <= 3000
}
func Clone(s State) State {
	copy := State{Version: s.Version, Drafts: make(map[int64]*Draft), NotBefore: s.NotBefore}
	for id, d := range s.Drafts {
		if d != nil {
			value := *d
			value.Recipients = append([]Recipient(nil), d.Recipients...)
			copy.Drafts[id] = &value
		}
	}
	if s.Job != nil {
		job := *s.Job
		job.Recipients = append([]Recipient(nil), job.Recipients...)
		if job.Flight != nil {
			flight := *job.Flight
			job.Flight = &flight
		}
		copy.Job = &job
	}
	return copy
}
func Normalize(s State, now time.Time) State {
	if s.Drafts == nil {
		s.Drafts = make(map[int64]*Draft)
	}
	for uid, draft := range s.Drafts {
		if draft == nil || !now.Before(draft.ExpiresAt) {
			delete(s.Drafts, uid)
		}
	}
	if job := s.Job; job != nil {
		if job.Flight != nil && !now.Before(job.Flight.Until) {
			job.Unknown++
			job.Flight = nil
			if Active(job) {
				job.Status = "paused"
			}
		}
		if !now.Before(job.ExpiresAt) {
			if job.Flight != nil {
				job.Unknown++
				job.Flight = nil
			}
			job.Status, job.Text, job.Recipients = "expired", "", nil
		}
	}
	return s
}

type Memory struct {
	mu          sync.Mutex
	state       State
	subscribers map[int64]string
	now         func() time.Time
}

func NewMemory() *Memory {
	return &Memory{state: NewState(), subscribers: make(map[int64]string), now: time.Now}
}
func (m *Memory) View(ctx context.Context) (State, error) {
	if ctx.Err() != nil {
		return State{}, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = Normalize(m.state, m.now())
	return Clone(m.state), nil
}
func (m *Memory) Update(ctx context.Context, fn func(*State) error) (State, error) {
	if ctx.Err() != nil {
		return State{}, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := Normalize(Clone(m.state), m.now())
	if err := fn(&next); err != nil {
		return State{}, err
	}
	if len(next.Drafts) > 10 || (next.Job != nil && len(next.Job.Recipients) > RegistryLimit) {
		return State{}, ErrCapacity
	}
	if !reflect.DeepEqual(m.state, next) {
		m.state = Clone(next)
	}
	return Clone(next), nil
}
func (m *Memory) SetSubscribed(ctx context.Context, uid int64, on bool) error {
	if ctx.Err() != nil || uid <= 0 {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !on {
		delete(m.subscribers, uid)
		return nil
	}
	if m.subscribers[uid] != "" {
		return nil
	}
	if len(m.subscribers) >= RegistryLimit {
		return ErrCapacity
	}
	m.subscribers[uid] = rand.Text()
	return nil
}
func (m *Memory) Consent(ctx context.Context, uid int64) (string, error) {
	if ctx.Err() != nil {
		return "", ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subscribers[uid], nil
}
func (m *Memory) Recipients(ctx context.Context) ([]Recipient, error) {
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]Recipient, 0, len(m.subscribers))
	for id, token := range m.subscribers {
		rows = append(rows, Recipient{id, token})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
func (m *Memory) RemoveIf(ctx context.Context, r Recipient) error {
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subscribers[r.ID] == r.Consent {
		delete(m.subscribers, r.ID)
	}
	return nil
}
