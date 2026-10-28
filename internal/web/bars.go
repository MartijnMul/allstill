// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/schedule"
	"github.com/MartijnMul/allstill/internal/store"
)

// bar is one run in a row's last runs, or a ping that was due and did not
// come. Tip says what it is and when.
type bar struct {
	Class, Tip string
	missed     bool
}

// barsOf shows a check's last runs, oldest on the left as time runs. Red is
// down: a failed run takes a check down, and so does a run that timed out,
// also when it ends later. Pings that did not come are no runs, so while a
// check was down for want of one, each ping its schedule said was due gets a
// red bar of its own, where it fell. The bars stay as many as the runs a
// check view holds, so the newest are kept.
func barsOf(c store.CheckView, now time.Time, loc *time.Location) ([]bar, string) {
	// Oldest first, in the order they began; the store keeps them in the
	// order they were made.
	runs := slices.Clone(c.Recent)
	slices.Reverse(runs)
	slices.SortStableFunc(runs, func(a, b store.RunView) int { return a.At().Compare(b.At()) })
	sched := scheduleOfView(c)
	type item struct {
		at  time.Time
		bar bar
	}
	var items []item
	for _, r := range runs {
		items = append(items, item{r.At(), runBar(r, now, loc)})
	}
	// Each down that no run explains fell in a gap between runs: after the
	// last ping of one, before the next run, or since the newest. A gap holds
	// a second down only after a pause resolved the first; then its pings
	// were due from when it went down again.
	drawn := map[int]time.Time{} // the gaps drawn, up to when
	for _, d := range missedDowns(c) {
		i := sort.Search(len(runs), func(i int) bool { return runs[i].At().After(d.At) })
		var after time.Time
		if to, ok := drawn[i]; ok && (to.IsZero() || !to.Before(d.At)) {
			continue
		} else if !ok {
			// Runs side by side can end out of order: the next ping is
			// expected after the last of them.
			for _, r := range runs[:i] {
				after = later(after, lastPing(r))
			}
		}
		until, paused := now, false
		if i < len(runs) {
			until = runs[i].At()
		}
		drawn[i] = time.Time{}
		// An incident a pause resolved: no ping was due after it.
		if !d.Resolved.IsZero() && d.Resolved.Before(until) {
			until, paused = d.Resolved, true
			drawn[i] = d.Resolved
		}
		moments := missed(sched, d.At, c.Grace, after, until, paused)
		if moments == nil {
			items = append(items, item{d.At, bar{Class: "bad", Tip: "No ping · down at " + stamp(d.At, now, loc), missed: true}})
		}
		for _, m := range moments {
			items = append(items, item{m, bar{Class: "bad", Tip: "No ping · due " + stamp(m, now, loc), missed: true}})
		}
	}
	slices.SortStableFunc(items, func(a, b item) int { return a.at.Compare(b.at) })
	items = items[max(0, len(items)-store.RecentRuns):]

	var bars []bar
	counts := map[string]int{}
	for _, it := range items {
		bars = append(bars, it.bar)
		if it.bar.missed {
			counts["missed"]++
		} else {
			counts[it.bar.Class]++
			counts["runs"]++
		}
	}
	label := fmt.Sprintf("Last %d runs: %d ok, %d with a warning, %d failed", counts["runs"], counts["up"], counts["warn"], counts["bad"])
	if counts["missed"] > 0 {
		label += "; " + plural(counts["missed"], "ping", "pings") + " missed"
	}
	if c.Status.State == check.Down {
		label += "; down since " + clock(now, c.Status.Since, loc)
	}
	return bars, label
}

func runBar(r store.RunView, now time.Time, loc *time.Location) bar {
	l := outcomeLook(r.Outcome)
	if !r.TimedOut.IsZero() {
		l = outcomeLook("timeout")
	}
	tip := l.Label + " · " + stamp(r.At(), now, loc)
	if d := r.Duration(); d > 0 {
		tip += " · took " + span(d)
	}
	return bar{Class: l.Class, Tip: tip}
}

// missedDowns returns the times a check went down for want of a ping, oldest
// first: a failed run, or one that timed out, took it down itself. Within
// the runs shown, a check down now is here even when its incident opened
// earlier, or before there were incidents.
func missedDowns(c store.CheckView) []store.Down {
	byRun := map[int64]bool{}
	for _, r := range c.Recent {
		if !r.TimedOut.IsZero() {
			byRun[r.TimedOut.UnixMilli()] = true
		}
		if r.Outcome == "fail" {
			byRun[r.Ended.UnixMilli()] = true
		}
	}
	downs := c.Downs
	shownSince := time.Time{}
	if len(c.Recent) == store.RecentRuns {
		shownSince = c.Recent[len(c.Recent)-1].At()
	}
	if c.Status.State == check.Down && !c.Status.Since.Before(shownSince) && (len(downs) == 0 || downs[0].At.Before(c.Status.Since)) {
		downs = append([]store.Down{{At: c.Status.Since}}, downs...)
	}
	var out []store.Down
	for _, d := range slices.Backward(downs) {
		if !byRun[d.At.UnixMilli()] {
			out = append(out, d)
		}
	}
	return out
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// lastPing is when a run's last ping came, which the next is expected after.
func lastPing(r store.RunView) time.Time {
	if r.Ended.IsZero() {
		return r.Started
	}
	return r.Ended
}

// scheduleOfView is the schedule a check's pings are due by, or nil when it
// cannot be read.
func scheduleOfView(c store.CheckView) check.Schedule {
	if c.Cron == "" {
		if c.Interval <= 0 {
			return nil
		}
		return check.Interval(c.Interval)
	}
	cron, err := schedule.ParseCron(c.Cron, c.TimeZone)
	if err != nil {
		return nil
	}
	return cron
}

// missed returns when pings were due and did not come, in a gap in which a
// check went down at down: from the first due after the last ping before it
// (without one, the moment its grace before going down), up to until, the
// next ping or now. The first took it down; one due after is missed once its
// grace has passed by until, as a check goes down, so a row grows no red
// bars when the check comes back. When paused, until is when it was paused,
// and none was due from then. It returns the newest store.RecentRuns at most,
// and nil when the schedule cannot say.
func missed(s check.Schedule, down time.Time, grace time.Duration, after, until time.Time, paused bool) []time.Time {
	if s == nil {
		return nil
	}
	m := down.Add(-grace)
	if !after.IsZero() {
		next, err := s.Next(after)
		if err != nil {
			return nil
		}
		m = next
	}
	first := m
	m = skipAhead(s, m, until)
	var out []time.Time
	for i := 0; !m.After(until) && i < 10_000; i++ {
		if !m.Equal(first) && (m.Add(grace).After(until) || (paused && !m.Before(until))) {
			break
		}
		out = append(out, m)
		next, err := s.Next(m)
		if err != nil {
			break
		}
		m = next
	}
	if len(out) == 0 {
		return nil
	}
	return out[max(0, len(out)-store.RecentRuns):]
}

// skipAhead returns where to start walking a schedule from m to until, as
// only the newest store.RecentRuns moments are shown. A fixed period counts,
// so the moments stay in step. A cron schedule is uneven, as office hours
// are: it tries a window before until, from as many minutes as it needs
// moments, as a cron schedule has a moment a minute at most, doubled until
// it holds enough.
func skipAhead(s check.Schedule, m, until time.Time) time.Time {
	const enough = 2 * store.RecentRuns
	if iv, ok := s.(check.Interval); ok {
		if skip := int64(until.Sub(m)/time.Duration(iv)) - enough; skip > 0 {
			return m.Add(time.Duration(skip) * time.Duration(iv))
		}
		return m
	}
	for w := enough * time.Minute; ; w *= 2 {
		from := until.Add(-w)
		if !from.After(m) {
			return m
		}
		start, err := s.Next(from)
		if err != nil {
			return m
		}
		n := 0
		for t := start; n < enough && !t.After(until); n++ {
			if t, err = s.Next(t); err != nil {
				return m
			}
		}
		if n == enough {
			return start
		}
	}
}

// stamp is a moment with its date, for a tip.
func stamp(t, now time.Time, loc *time.Location) string {
	t = t.In(loc)
	if t.Year() != now.In(loc).Year() {
		return t.Format("Mon 2 Jan 2006 15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}
