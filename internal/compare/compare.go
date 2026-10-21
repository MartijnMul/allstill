// SPDX-License-Identifier: AGPL-3.0-only

// Package compare lines up when a check was down by Allstill's reckoning with
// when it was down by another service's, such as the Healthchecks it
// replaces, so the weeks in shadow mode give evidence (intent/alerts).
//
// It compares when checks went down and came back up, which is when each
// service would alert. When a person would have been told by Allstill, after
// quiet hours and the digest, is on the Messages page.
package compare

import (
	"slices"
	"time"

	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
)

// Period is a time a check was down by one side's reckoning: from when it
// went down until it came back up. Up is zero while it is still down.
type Period struct {
	Down, Up time.Time
}

// FromFlips turns Healthchecks' status changes, oldest first, into periods.
// A first change to up ends a period that began before the changes asked
// for, and is left out.
func FromFlips(flips []importer.Flip) []Period {
	var out []Period
	down := false
	for _, f := range flips {
		switch {
		case !f.Up && !down:
			out = append(out, Period{Down: f.At})
			down = true
		case f.Up && down:
			out[len(out)-1].Up = f.At
			down = false
		}
	}
	return out
}

// FromIncidents turns a check's incidents in Allstill into periods. An
// incident that recovered was resolved the settle period after the check came
// back up, so that is taken off.
func FromIncidents(incidents []store.CheckIncident, settle time.Duration) []Period {
	out := make([]Period, len(incidents))
	for i, inc := range incidents {
		p := Period{Down: inc.Down}
		switch {
		case !inc.RecoveringSince.IsZero():
			p.Up = inc.RecoveringSince
		case inc.Reason == "recovered":
			p.Up = inc.Resolved.Add(-settle)
		default:
			p.Up = inc.Resolved
		}
		out[i] = p
	}
	return out
}

// Kind is a way the two sides can differ.
type Kind string

const (
	// Missed: the other service saw the check down, Allstill did not.
	Missed Kind = "missed"
	// Extra: Allstill saw the check down, the other service did not.
	Extra Kind = "extra"
	// Down: both saw it go down, Apart from each other.
	Down Kind = "down"
	// Up: both saw it come back up, Apart from each other.
	Up Kind = "up"
	// Fewer: the other service saw it go down Count times while Allstill saw
	// it down once.
	Fewer Kind = "fewer"
	// StillDown: Allstill still has it down, the other service up since At.
	StillDown Kind = "still_down"
	// OtherStillDown: the other service still has it down, Allstill up since
	// At.
	OtherStillDown Kind = "other_still_down"
	// Again: Allstill saw it go down again at At, after it came back up,
	// within what the other service saw as one period down. That period was
	// compared with Allstill's first; this one's end is not compared.
	Again Kind = "again"
)

// Difference is one place where the two sides disagree.
type Difference struct {
	Kind Kind
	// At is when it began: by the other service, or by Allstill for Extra,
	// OtherStillDown and Again.
	At time.Time
	// Apart is Allstill's time less the other's, for Down and Up.
	Apart time.Duration
	// For is how long it stayed down, for Missed and Extra; zero while it
	// still is.
	For time.Duration
	// Count is for Fewer.
	Count int
}

// Result is how a check compares.
type Result struct {
	Same        int // times both saw it go down, within the margin
	Differences []Difference
}

// Periods compares the periods of the other service with Allstill's, both
// oldest first, and lists the differences oldest first. Only what began from `from` until a margin before `to` is
// judged, so neither side is faulted for what happened before it watched or
// for what the other has not yet seen. Times within the margin of each other
// count as the same.
func Periods(other, allstill []Period, from, to time.Time, margin time.Duration) Result {
	judged := func(t time.Time) bool { return !t.Before(from) && t.Before(to.Add(-margin)) }
	end := func(p Period) time.Time {
		if p.Up.IsZero() {
			return to
		}
		return p.Up
	}
	overlap := func(a, b Period) bool {
		return !a.Down.After(end(b).Add(margin)) && !b.Down.After(end(a).Add(margin))
	}
	lasted := func(p Period) time.Duration {
		if p.Up.IsZero() {
			return 0
		}
		return p.Up.Sub(p.Down)
	}

	var r Result
	// Each of the other's periods is compared with the first of Allstill's
	// it overlaps, and only with that one.
	seen := make([]bool, len(other))
	for _, a := range allstill {
		var hs []Period
		again := false
		for i, h := range other {
			if !overlap(h, a) {
				continue
			}
			if seen[i] {
				again = true
				continue
			}
			hs = append(hs, h)
			seen[i] = true
		}
		if len(hs) == 0 {
			switch {
			case !judged(a.Down):
			case again:
				r.Differences = append(r.Differences, Difference{Kind: Again, At: a.Down})
			default:
				r.Differences = append(r.Differences, Difference{Kind: Extra, At: a.Down, For: lasted(a)})
			}
			continue
		}
		first, last := hs[0], hs[len(hs)-1]
		if judged(a.Down) && judged(first.Down) {
			if apart := a.Down.Sub(first.Down); apart.Abs() > margin {
				r.Differences = append(r.Differences, Difference{Kind: Down, At: first.Down, Apart: apart})
			} else {
				r.Same++
			}
			if len(hs) > 1 {
				r.Differences = append(r.Differences, Difference{Kind: Fewer, At: first.Down, Count: len(hs)})
			}
		}
		if !judged(a.Down) {
			continue
		}
		switch {
		case a.Up.IsZero() && !last.Up.IsZero() && judged(last.Up):
			r.Differences = append(r.Differences, Difference{Kind: StillDown, At: last.Up})
		case !a.Up.IsZero() && last.Up.IsZero() && judged(a.Up):
			r.Differences = append(r.Differences, Difference{Kind: OtherStillDown, At: a.Up})
		case !a.Up.IsZero() && !last.Up.IsZero():
			if apart := a.Up.Sub(last.Up); apart.Abs() > margin {
				r.Differences = append(r.Differences, Difference{Kind: Up, At: last.Up, Apart: apart})
			}
		}
	}
	for i, h := range other {
		if !seen[i] && judged(h.Down) {
			r.Differences = append(r.Differences, Difference{Kind: Missed, At: h.Down, For: lasted(h)})
		}
	}
	slices.SortStableFunc(r.Differences, func(a, b Difference) int { return a.At.Compare(b.At) })
	return r
}
