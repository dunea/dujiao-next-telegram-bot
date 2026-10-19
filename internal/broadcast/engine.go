package broadcast

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/ratelimit"
)

type Result struct {
	Kind       string
	RetryAfter time.Duration
}
type Engine struct {
	ID       string
	Store    Store
	Limiter  ratelimit.Limiter
	Interval time.Duration
	Send     func(context.Context, int64, string) Result
	Admin    func(int64) bool
	Audit    func(string, string, int64, string)
	Now      func() time.Time
}

func NewEngine(store Store, limiter ratelimit.Limiter) *Engine {
	return &Engine{ID: rand.Text(), Store: store, Limiter: limiter, Interval: time.Second, Now: time.Now}
}
func (e *Engine) record(action, outcome string, uid int64, id string) {
	if e.Audit != nil {
		e.Audit(action, outcome, uid, id)
	}
}

func (e *Engine) Recover(ctx context.Context) error {
	_, err := e.Store.Update(ctx, func(s *State) error {
		if job := s.Job; Active(job) && job.Worker != e.ID {
			job.Status = "paused"
		}
		return nil
	})
	return err
}
func (e *Engine) Run(ctx context.Context) {
	_ = e.Recover(ctx)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = e.Store.Update(cleanup, func(s *State) error {
			if job := s.Job; Active(job) && job.Worker == e.ID {
				job.Status = "paused"
				if job.Flight != nil {
					job.Unknown++
					job.Flight = nil
				}
			}
			return nil
		})
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = e.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (e *Engine) Tick(ctx context.Context) error {
	state, err := e.Store.View(ctx)
	if err != nil {
		return err
	}
	job := state.Job
	if !Active(job) || job.Status == "paused" || job.Status == "prepared" {
		return nil
	}
	if job.Worker != e.ID || e.Admin == nil || !e.Admin(job.OwnerID) {
		_, err = e.Store.Update(ctx, func(s *State) error {
			if s.Job == nil || s.Job.ID != job.ID || (s.Job.Status != "queued" && s.Job.Status != "running") {
				return ErrConflict
			}
			s.Job.Status = "paused"
			return nil
		})
		return err
	}
	if job.Flight != nil || e.Now().Before(job.NotBefore) || e.Now().Before(state.NotBefore) {
		return nil
	}
	if job.Next >= len(job.Recipients) {
		_, err = e.Store.Update(ctx, func(s *State) error {
			if s.Job == nil || s.Job.ID != job.ID || s.Job.Flight != nil || (s.Job.Status != "queued" && s.Job.Status != "running") || s.Job.Next < len(s.Job.Recipients) {
				return ErrConflict
			}
			s.Job.Status = "completed"
			return nil
		})
		if err == nil {
			e.record("broadcast.job", "succeeded", job.OwnerID, job.ID)
		}
		return err
	}
	decision, err := e.Limiter.Allow(ctx, "broadcast-send", ratelimit.Rule{Limit: 1, Window: e.Interval})
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return nil
	}
	token := rand.Text()
	claimed := false
	next, err := e.Store.Update(ctx, func(s *State) error {
		current := s.Job
		if current == nil || current.ID != job.ID || current.Worker != e.ID || (current.Status != "queued" && current.Status != "running") || current.Flight != nil || current.Next != job.Next || !e.Now().Before(current.ExpiresAt) || e.Now().Before(s.NotBefore) || e.Now().Before(current.NotBefore) {
			return ErrConflict
		}
		current.Flight = &Flight{Token: token, Recipient: current.Recipients[current.Next], Index: current.Next, Until: e.Now().Add(Lease)}
		current.Next++
		current.Status = "running"
		claimed = true
		return nil
	})
	// A lost Redis acknowledgement must never authorize a send.
	if err != nil || !claimed {
		return err
	}
	flight := *next.Job.Flight
	// Check stop/pause first, then consent as close as possible to Send.
	live, err := e.Store.View(ctx)
	if err != nil {
		return err
	}
	if live.Job == nil || live.Job.ID != job.ID || live.Job.Status != "running" || live.Job.Flight == nil || live.Job.Flight.Token != token {
		return nil
	}
	consent, err := e.Store.Consent(ctx, flight.Recipient.ID)
	if err != nil {
		return err
	}
	result := Result{Kind: "skipped"}
	if consent != "" && consent == flight.Recipient.Consent {
		e.record("broadcast.delivery", "attempt", flight.Recipient.ID, job.ID)
		request, cancel := context.WithTimeout(ctx, 10*time.Second)
		result = e.Send(request, flight.Recipient.ID, Payload(next.Job.Text))
		cancel()
	}
	if result.Kind == "blocked" {
		if err := e.Store.RemoveIf(ctx, flight.Recipient); err != nil {
			return err
		}
	}
	finished, err := e.Store.Update(ctx, func(s *State) error {
		current := s.Job
		if current == nil || current.ID != job.ID || current.Flight == nil || current.Flight.Token != token {
			return ErrConflict
		}
		current.Flight = nil
		switch result.Kind {
		case "sent":
			current.Sent++
			current.Retries = 0
		case "skipped":
			current.Skipped++
			current.Retries = 0
		case "blocked", "failed":
			current.Failed++
			current.Retries = 0
		case "throttled":
			current.NotBefore = e.Now().Add(max(time.Second, result.RetryAfter))
			if current.NotBefore.After(s.NotBefore) {
				s.NotBefore = current.NotBefore
			}
			if current.Status != "cancelled" && current.Retries < 2 {
				current.Next = flight.Index
				current.Retries++
			} else {
				current.Failed++
				current.Retries = 0
			}
		default:
			current.Unknown++
			if Active(current) {
				current.Status = "paused"
			}
		}
		if (current.Status == "queued" || current.Status == "running") && current.Next >= len(current.Recipients) {
			current.Status = "completed"
		}
		return nil
	})
	if err == nil {
		e.record("broadcast.delivery", result.Kind, flight.Recipient.ID, job.ID)
		if finished.Job.Status == "completed" {
			e.record("broadcast.job", "succeeded", job.OwnerID, job.ID)
		} else if result.Kind == "unknown" {
			e.record("broadcast.job", "unknown", job.OwnerID, job.ID)
		}
	}
	return err
}
