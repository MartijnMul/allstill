// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeStore holds a list of deadlines and records what the scheduler did.
type fakeStore struct {
	mu        sync.Mutex
	deadlines []time.Time
	passes    []time.Time
	alive     int
	recovered bool
	fail      int // fail this many passes
	tidied    []time.Time
}

// Tidy needs three calls for a round.
func (f *fakeStore) Tidy(_ context.Context, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tidied = append(f.tidied, now)
	return len(f.tidied)%3 != 0, nil
}

func (f *fakeStore) ProcessDue(_ context.Context, now time.Time) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes = append(f.passes, now)
	if f.fail > 0 {
		f.fail--
		return time.Time{}, errors.New("disk full")
	}
	var next time.Time
	var left []time.Time
	for _, d := range f.deadlines {
		if d.After(now) {
			left = append(left, d)
			if next.IsZero() || d.Before(next) {
				next = d
			}
		}
	}
	f.deadlines = left
	return next, nil
}

func (f *fakeStore) Alive(context.Context, time.Time) error {
	f.mu.Lock()
	f.alive++
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) Recover(context.Context, time.Time) error {
	f.mu.Lock()
	f.recovered = true
	f.mu.Unlock()
	return nil
}

// snapshot returns what the scheduler did so far.
func (f *fakeStore) snapshot() (passes []time.Time, alive int, recovered bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.passes...), f.alive, f.recovered
}

func (f *fakeStore) add(d time.Time) {
	f.mu.Lock()
	f.deadlines = append(f.deadlines, d)
	f.mu.Unlock()
}

func (f *fakeStore) passCount() int {
	passes, _, _ := f.snapshot()
	return len(passes)
}

func start(t *testing.T, f *fakeStore) (*Scheduler, context.CancelFunc, chan error) {
	s := New(f, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	synctest.Wait()
	return s, cancel, done
}

func TestWakesAtTheEarliestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		f := &fakeStore{deadlines: []time.Time{begin.Add(time.Hour), begin.Add(10 * time.Minute)}}
		_, cancel, done := start(t, f)
		if passes, _, recovered := f.snapshot(); !recovered || len(passes) != 1 {
			t.Fatalf("recovered %v, %d passes", recovered, len(passes))
		}
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if passes, _, _ := f.snapshot(); len(passes) != 2 || !passes[1].Equal(begin.Add(10*time.Minute)) {
			t.Fatalf("passes %v", passes)
		}
		time.Sleep(50 * time.Minute)
		synctest.Wait()
		if f.passCount() != 3 {
			t.Fatalf("%d passes", f.passCount())
		}
		time.Sleep(24 * time.Hour) // nothing left: no passes, only heartbeats
		synctest.Wait()
		if passes, alive, _ := f.snapshot(); len(passes) != 3 || alive < 24*60*2 {
			t.Fatalf("%d passes, %d heartbeats", len(passes), alive)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestANudgeBringsTheTimerForward(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		f := &fakeStore{deadlines: []time.Time{begin.Add(time.Hour)}}
		s, cancel, _ := start(t, f)
		defer cancel()
		f.add(begin.Add(5 * time.Minute))
		s.Nudge(begin.Add(5 * time.Minute))
		s.Nudge(begin.Add(30 * time.Minute)) // a later nudge does not delay it
		synctest.Wait()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if passes, _, _ := f.snapshot(); len(passes) != 2 || !passes[1].Equal(begin.Add(5*time.Minute)) {
			t.Fatalf("passes %v", passes)
		}
	})
}

func TestRetriesAfterAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeStore{fail: 2}
		_, cancel, _ := start(t, f)
		defer cancel()
		time.Sleep(2 * retry)
		synctest.Wait()
		if f.passCount() != 3 {
			t.Fatalf("%d passes", f.passCount())
		}
	})
}

func TestTidiesEveryTenMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		begin := time.Now()
		f := &fakeStore{}
		_, cancel, done := start(t, f)
		time.Sleep(tidyFirst + TidyEvery + time.Second)
		synctest.Wait()
		f.mu.Lock()
		tidied := append([]time.Time(nil), f.tidied...)
		f.mu.Unlock()
		// A round of three calls a minute after the start, and one ten
		// minutes after that round began.
		if len(tidied) != 6 || !tidied[0].Equal(begin.Add(tidyFirst)) || !tidied[2].Equal(begin.Add(tidyFirst+2*tidyPause)) ||
			!tidied[3].Equal(begin.Add(tidyFirst+TidyEvery)) {
			t.Fatalf("tidied at %v", tidied)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
