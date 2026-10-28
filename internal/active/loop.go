// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"cmp"
	"context"
	"hash/fnv"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// The loop's limits (R11).
const (
	// MaxRunning is how many attempts run at once.
	MaxRunning = 20
	// Reload is how often the loop reads its checks again, to learn of
	// checks added or changed by the admin commands.
	Reload = 30 * time.Second
	// gather is how long finished attempts wait to be recorded together.
	gather = time.Second
)

// NotJudged is the reason of an attempt the loop could not judge, because
// its own network did not work either.
const NotJudged = "not judged: the server's network was down"

// Check is an active check as the loop runs it.
type Check struct {
	ID, WorkspaceID string
	Every           time.Duration
	HTTP            HTTP
}

// offset is when within its interval a check's attempts start, from its id,
// so checks are spread over the interval and keep their place across
// restarts.
func (c Check) offset() time.Duration {
	h := fnv.New64a()
	h.Write([]byte(c.ID))
	return time.Duration(h.Sum64() % uint64(c.Every))
}

// Slot is the first moment at or after t that an attempt of the check is due.
func (c Check) Slot(t time.Time) time.Time {
	s := t.Truncate(c.Every).Add(c.offset())
	if s.Before(t) {
		s = s.Add(c.Every)
	}
	return s
}

// host is what an attempt waits its turn for: one attempt per host at a time.
func (c Check) host() string {
	h, _ := c.HTTP.Host()
	return h
}

// Attempt makes one attempt of a check.
func (r *Runner) Attempt(ctx context.Context, c Check) Result { return r.HTTP(ctx, c.HTTP) }

// Done is a finished attempt: its check, the slot it was due in, and how it
// went.
type Done struct {
	Check  Check
	Slot   time.Time
	Result Result
}

// Loop starts each check's attempts in their slot, at most one per host at a
// time and MaxRunning at once, and records what they found, a second's worth
// at a time. It keeps the checks in memory with their next slot, with one
// timer for the earliest.
type Loop struct {
	// Checks returns the checks to run; Run calls it every Reload.
	Checks func(context.Context) ([]Check, error)
	// Record keeps finished attempts, in one transaction.
	Record func(context.Context, []Done) error
	// Attempt makes one attempt; Runner.HTTP for an HTTP check.
	Attempt func(context.Context, Check) Result
	// Canary says whether the server's own network works. Without one, a
	// failure that could be the server's own counts against the target
	// (C4).
	Canary func(context.Context) bool
	Log    *slog.Logger

	// canaryTurn lets one attempt ask the canary at a time; a channel, as
	// the others wait as long as the canary takes.
	canaryTurn chan struct{}
	canaryAt   time.Time // when the last canary started
	canaryOK   bool

	mu      sync.Mutex
	offline time.Time // since when the canary did not answer
}

// Offline says whether the server is offline for its checks, and since when:
// its canary did not answer, so its checks cannot be judged (R7).
func (l *Loop) Offline() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.offline, !l.offline.IsZero()
}

func (l *Loop) setOffline(at time.Time, off bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case !off:
		l.offline = time.Time{}
	case l.offline.IsZero():
		l.offline = at
	}
}

type entry struct {
	c                Check
	next             time.Time // the next slot
	slot             time.Time // the slot it waits or runs for
	waiting, running bool
	gone             bool // no longer among the checks
}

type finished struct {
	Done
	host string
	cut  bool // cut short by the end of the loop: it judged nothing
}

// Run runs the checks until ctx ends. Attempts still running then are
// cancelled and not recorded: they judged nothing.
func (l *Loop) Run(ctx context.Context) error {
	l.canaryTurn = make(chan struct{}, 1)
	entries := map[string]*entry{}
	var queue []*entry
	busy := map[string]bool{}
	running := 0
	results := make(chan finished, MaxRunning)
	var pending []Done
	var wg sync.WaitGroup
	attempts, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()

	reload := func() {
		checks, err := l.Checks(ctx)
		if err != nil {
			if ctx.Err() == nil {
				l.Log.Error("reading the checks to run", "error", err)
			}
			return
		}
		now := time.Now()
		seen := map[string]bool{}
		for _, c := range checks {
			seen[c.ID] = true
			e, ok := entries[c.ID]
			if !ok {
				entries[c.ID] = &entry{c: c, next: c.Slot(now)}
				continue
			}
			if e.c.Every != c.Every {
				e.next = c.Slot(now)
			}
			e.c = c
		}
		for id, e := range entries {
			if !seen[id] {
				e.gone = true
				delete(entries, id)
			}
		}
		if len(entries) == 0 {
			// Without checks there is nothing it cannot judge.
			l.setOffline(now, false)
		}
	}
	start := func(now time.Time) {
		var added bool
		for _, e := range entries {
			if now.Before(e.next) {
				continue
			}
			if !e.waiting && !e.running {
				e.waiting, e.slot, added = true, e.next, true
				queue = append(queue, e)
			}
			// A slot that comes while it still waits or runs is skipped.
			for !now.Before(e.next) {
				e.next = e.next.Add(e.c.Every)
			}
		}
		if added {
			slices.SortStableFunc(queue, func(a, b *entry) int {
				return cmp.Or(a.slot.Compare(b.slot), cmp.Compare(a.c.ID, b.c.ID))
			})
		}
		kept := queue[:0]
		for _, e := range queue {
			host := e.c.host()
			switch {
			case e.gone:
				continue
			case running >= MaxRunning || busy[host]:
				kept = append(kept, e)
				continue
			}
			e.waiting, e.running = false, true
			busy[host] = true
			running++
			c, slot := e.c, e.slot
			wg.Go(func() {
				r := l.attempt(attempts, c)
				results <- finished{Done{Check: c, Slot: slot, Result: r}, host, attempts.Err() != nil}
			})
		}
		clear(queue[len(kept):])
		queue = kept
	}
	record := func(ctx context.Context) {
		if len(pending) == 0 {
			return
		}
		if err := l.Record(ctx, pending); err != nil {
			l.Log.Error("recording attempts", "attempts", len(pending), "error", err)
		}
		pending = nil
	}

	reload()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	flush := time.NewTimer(time.Hour)
	flush.Stop()
	flushing := false
	reloads := time.NewTicker(Reload)
	defer reloads.Stop()
	for {
		start(time.Now())
		var next time.Time
		for _, e := range entries {
			if next.IsZero() || e.next.Before(next) {
				next = e.next
			}
		}
		if next.IsZero() {
			timer.Stop()
		} else {
			timer.Reset(max(time.Until(next), 0))
		}
		select {
		case <-ctx.Done():
			// What finished before the end is recorded; what was cut short
			// is not.
			cancel()
			wg.Wait()
			close(results)
			for f := range results {
				if !f.cut {
					pending = append(pending, f.Done)
				}
			}
			end, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			record(end)
			stop()
			return nil
		case <-timer.C:
		case <-reloads.C:
			reload()
		case f := <-results:
			running--
			delete(busy, f.host)
			if e, ok := entries[f.Check.ID]; ok {
				e.running = false
			}
			if f.cut {
				continue
			}
			pending = append(pending, f.Done)
			if !flushing {
				flush.Reset(gather)
				flushing = true
			}
		case <-flush.C:
			flushing = false
			record(ctx)
		}
	}
}

// attempt makes an attempt, and when it failed in a way the server's own
// network could explain, asks the canary (C4).
func (l *Loop) attempt(ctx context.Context, c Check) Result {
	r := l.Attempt(ctx, c)
	switch {
	case ctx.Err() != nil:
	case !r.Unsure && !r.Local:
		// The network works: something answered, or refused for itself.
		l.setOffline(r.Ended, false)
	case r.Outcome == check.Failed && r.Unsure && l.Canary != nil:
		if !l.canary(ctx, r.Started) {
			r.Outcome, r.Reason = check.Unjudged, NotJudged
		}
	}
	return r
}

// canary asks the canary, unless it was asked since the attempt began:
// attempts that fail together, as when the network drops, share one answer.
func (l *Loop) canary(ctx context.Context, since time.Time) bool {
	select {
	case l.canaryTurn <- struct{}{}:
		defer func() { <-l.canaryTurn }()
	case <-ctx.Done():
		return true
	}
	if !l.canaryAt.IsZero() && !l.canaryAt.Before(since) {
		return l.canaryOK
	}
	at := time.Now()
	ok := l.Canary(ctx)
	if ctx.Err() != nil {
		return true // cut short: not a sign of an outage
	}
	l.canaryAt, l.canaryOK = at, ok
	l.setOffline(at, !ok)
	return ok
}

// Canary returns a canary that is answered when one of the addresses, such
// as a public DNS server's port 53, accepts a TCP connection within timeout.
// The administrator chooses them (-canary), so they are not held to the
// checks' address rules.
func Canary(addrs []string, timeout time.Duration) func(context.Context) bool {
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		answered := make(chan bool, len(addrs))
		for _, a := range addrs {
			go func() {
				conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", a)
				if err == nil {
					conn.Close()
				}
				answered <- err == nil
			}()
		}
		for range addrs {
			if <-answered {
				return true
			}
		}
		return false
	}
}
