// SPDX-License-Identifier: AGPL-3.0-only

// Package scheduler wakes up when the earliest deadline of any check passes,
// and applies it. There is one timer for all checks: the deadlines live in the
// database, so a restart loses nothing.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Heartbeat is how often the scheduler records that the server runs. After a
// restart, deadlines that passed since the last heartbeat were missed.
const Heartbeat = 30 * time.Second

// retry is how long the scheduler waits after a failed pass.
const retry = 5 * time.Second

// TidyEvery is how often history beyond what checks keep is removed, the
// first time a minute after the start. A round goes a few checks at a time,
// with a pause between, so pings get in.
const (
	TidyEvery = 10 * time.Minute
	tidyFirst = time.Minute
	tidyPause = 100 * time.Millisecond
)

// Store is what the scheduler needs of the store.
type Store interface {
	ProcessDue(ctx context.Context, now time.Time) (time.Time, error)
	Alive(ctx context.Context, now time.Time) error
	Recover(ctx context.Context, now time.Time) error
	Tidy(ctx context.Context, now time.Time) (more bool, err error)
}

// Scheduler applies deadlines as they pass.
type Scheduler struct {
	store Store
	log   *slog.Logger

	mu      sync.Mutex
	pending time.Time // the earliest nudged deadline not yet seen by Run
	wake    chan struct{}
}

// New returns a scheduler; Run starts it.
func New(store Store, log *slog.Logger) *Scheduler {
	return &Scheduler{store: store, log: log, wake: make(chan struct{}, 1)}
}

// Nudge tells the scheduler a check's deadline changed. It never blocks.
func (s *Scheduler) Nudge(deadline time.Time) {
	s.mu.Lock()
	if s.pending.IsZero() || deadline.Before(s.pending) {
		s.pending = deadline
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run marks checks whose deadlines passed while the server was down as
// unknown, then applies deadlines until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	now := time.Now()
	if err := s.store.Recover(ctx, now); err != nil {
		return err
	}
	if err := s.store.Alive(ctx, now); err != nil {
		return err
	}
	next := s.pass(ctx)

	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { s.tidy(ctx) })

	timer := time.NewTimer(0)
	timer.Stop()
	heartbeat := time.NewTicker(Heartbeat)
	defer heartbeat.Stop()
	for {
		if next.IsZero() {
			timer.Stop()
		} else {
			timer.Reset(max(time.Until(next), 0))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			next = s.pass(ctx)
		case <-s.wake:
			s.mu.Lock()
			if p := s.pending; !p.IsZero() && (next.IsZero() || p.Before(next)) {
				next = p
			}
			s.pending = time.Time{}
			s.mu.Unlock()
		case now := <-heartbeat.C:
			if err := s.store.Alive(ctx, now); err != nil && ctx.Err() == nil {
				s.log.Error("recording the heartbeat", "error", err)
			}
		}
	}
}

// pass applies the deadlines that have passed and returns the next one.
func (s *Scheduler) pass(ctx context.Context) time.Time {
	next, err := s.store.ProcessDue(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("applying deadlines", "error", err)
		}
		return time.Now().Add(retry)
	}
	return next
}

// tidy removes old history every TidyEvery, until ctx ends.
func (s *Scheduler) tidy(ctx context.Context) {
	timer := time.NewTimer(tidyFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			timer.Reset(TidyEvery)
		}
		for more := true; more; {
			var err error
			if more, err = s.store.Tidy(ctx, time.Now()); err != nil {
				if ctx.Err() == nil {
					s.log.Error("removing old history", "error", err)
				}
				break
			}
			if more {
				select {
				case <-ctx.Done():
					return
				case <-time.After(tidyPause):
				}
			}
		}
	}
}
