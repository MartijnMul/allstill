// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

func TestBarsShowEveryMissedPing(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) } // the 21st is a Wednesday
	ok := func(t time.Time) store.RunView { return store.RunView{Outcome: "success", Ended: t} }
	// Pings every 5 minutes from first to last, newest first as a check view
	// holds them.
	every5 := func(first, last time.Time) []store.RunView {
		var runs []store.RunView
		for m := last; !m.Before(first); m = m.Add(-5 * time.Minute) {
			runs = append(runs, ok(m))
		}
		return runs
	}
	heartbeat := func(runs []store.RunView, downs []store.Down, st check.Status) store.CheckView {
		return store.CheckView{Interval: 5 * time.Minute, Grace: 20 * time.Minute, Recent: runs, Downs: downs, Status: st}
	}
	classes := func(bars []bar) string {
		var s []string
		for _, b := range bars {
			s = append(s, b.Class)
		}
		return strings.Join(s, " ")
	}
	repeat := func(class string, n int) string { return strings.TrimSpace(strings.Repeat(class+" ", n)) }

	t.Run("down now", func(t *testing.T) {
		// The last ping at 15:00; the one due at 15:05 was 20 minutes late at
		// 15:25. One due since is missed once its grace has passed too: at
		// 16:00, the one due at 15:40.
		c := heartbeat(every5(at(21, 14, 0), at(21, 15, 0)), []store.Down{{At: at(21, 15, 25)}}, check.Status{State: check.Down, Since: at(21, 15, 25)})
		bars, label := barsOf(c, at(21, 16, 0), time.UTC)
		if got := classes(bars); got != repeat("up", 13)+" "+repeat("bad", 8) {
			t.Fatalf("bars %q", got)
		}
		if bars[0].Tip != "OK · Wed 21 Oct 14:00" || bars[13].Tip != "No ping · due Wed 21 Oct 15:05" || bars[20].Tip != "No ping · due Wed 21 Oct 15:40" {
			t.Fatalf("tips %q, %q, %q", bars[0].Tip, bars[13].Tip, bars[20].Tip)
		}
		if label != "Last 13 runs: 13 ok, 0 with a warning, 0 failed; 8 pings missed; down since 15:25" {
			t.Fatalf("label %q", label)
		}
	})

	t.Run("back again", func(t *testing.T) {
		// Silent from 10:00 to 10:31: the ping due at 10:05 took it down, and
		// the one due at 10:10 was past its grace when the next came. Those
		// due later were late, as the one at 10:31 was.
		runs := append([]store.RunView{ok(at(21, 10, 31))}, every5(at(21, 9, 50), at(21, 10, 0))...)
		c := heartbeat(runs, []store.Down{{At: at(21, 10, 25), Resolved: at(21, 10, 41)}}, check.Status{State: check.Up, Since: at(21, 10, 31)})
		bars, label := barsOf(c, at(21, 12, 0), time.UTC)
		if got := classes(bars); got != "up up up bad bad up" || bars[4].Tip != "No ping · due Wed 21 Oct 10:10" {
			t.Fatalf("bars %+v", bars)
		}
		if label != "Last 4 runs: 4 ok, 0 with a warning, 0 failed; 2 pings missed" {
			t.Fatalf("label %q", label)
		}
	})

	t.Run("late within its grace", func(t *testing.T) {
		c := heartbeat([]store.RunView{ok(at(21, 10, 12)), ok(at(21, 10, 0))}, nil, check.Status{State: check.Up, Since: at(21, 10, 0)})
		if bars, _ := barsOf(c, at(21, 10, 15), time.UTC); classes(bars) != "up up" {
			t.Fatalf("bars %q", classes(bars))
		}
	})

	t.Run("runs that took it down", func(t *testing.T) {
		// A failed run, and one that timed out and ended later, are its red
		// bars, with none added.
		c := heartbeat([]store.RunView{
			{Outcome: "success", Started: at(21, 12, 0), TimedOut: at(21, 12, 20), Ended: at(21, 12, 30)},
			{Outcome: "fail", Started: at(21, 11, 0), Ended: at(21, 11, 2)},
			ok(at(21, 10, 0)),
		}, []store.Down{{At: at(21, 12, 20)}, {At: at(21, 11, 2)}}, check.Status{State: check.Up, Since: at(21, 12, 30)})
		bars, label := barsOf(c, at(21, 13, 0), time.UTC)
		if classes(bars) != "up bad bad" || bars[1].Tip != "Failed · Wed 21 Oct 11:00 · took 2 min" || !strings.HasPrefix(bars[2].Tip, "Timed out · ") {
			t.Fatalf("bars %+v", bars)
		}
		if label != "Last 3 runs: 1 ok, 0 with a warning, 2 failed" {
			t.Fatalf("label %q", label)
		}
	})

	t.Run("failing longer than the runs shown", func(t *testing.T) {
		// The run that took it down is gone; no missed ping is made up.
		var runs []store.RunView
		for i := range store.RecentRuns {
			runs = append(runs, store.RunView{Outcome: "fail", Ended: at(21, 15, 0).Add(-time.Duration(i) * 5 * time.Minute)})
		}
		c := heartbeat(runs, nil, check.Status{State: check.Down, Since: at(21, 9, 0)})
		if bars, label := barsOf(c, at(21, 15, 2), time.UTC); classes(bars) != repeat("bad", store.RecentRuns) || strings.Contains(label, "missed") {
			t.Fatalf("bars %q, label %q", classes(bars), label)
		}
	})

	t.Run("paused while down", func(t *testing.T) {
		// No ping was due once it was paused at 10:40, when those due up to
		// 10:20 were past their grace.
		c := heartbeat(every5(at(21, 9, 50), at(21, 10, 0)), []store.Down{{At: at(21, 10, 25), Resolved: at(21, 10, 40)}}, check.Status{State: check.Paused, Since: at(21, 10, 40)})
		if bars, _ := barsOf(c, at(21, 12, 0), time.UTC); classes(bars) != "up up up "+repeat("bad", 4) {
			t.Fatalf("bars %q", classes(bars))
		}
		// Down again after it was resumed, with no ping between: its pings
		// were due from then.
		c.Downs = append([]store.Down{{At: at(21, 11, 25)}}, c.Downs...)
		c.Status = check.Status{State: check.Down, Since: at(21, 11, 25)}
		bars, _ := barsOf(c, at(21, 11, 30), time.UTC)
		if classes(bars) != "up up up "+repeat("bad", 6) || bars[7].Tip != "No ping · due Wed 21 Oct 11:05" {
			t.Fatalf("bars %+v", bars)
		}
	})

	t.Run("a cron job", func(t *testing.T) {
		// It ran on Monday and Wednesday nights; Tuesday's run did not come.
		c := store.CheckView{Cron: "0 2 * * *", TimeZone: "UTC", Grace: 3 * time.Hour,
			Recent: []store.RunView{
				{Outcome: "success", Started: at(21, 2, 0), Ended: at(21, 2, 30)},
				{Outcome: "success", Started: at(19, 2, 0), Ended: at(19, 2, 40)},
			},
			Downs:  []store.Down{{At: at(20, 5, 0), Resolved: at(21, 2, 10)}},
			Status: check.Status{State: check.Up, Since: at(21, 2, 30)},
		}
		bars, _ := barsOf(c, at(21, 12, 0), time.UTC)
		if classes(bars) != "up bad up" || bars[1].Tip != "No ping · due Tue 20 Oct 02:00" || bars[2].Tip != "OK · Wed 21 Oct 02:00 · took 30 min" {
			t.Fatalf("bars %+v", bars)
		}
	})

	t.Run("down for days", func(t *testing.T) {
		// The bars stay as many as the runs, the newest kept.
		c := heartbeat(every5(at(19, 9, 0), at(19, 10, 0)), []store.Down{{At: at(19, 10, 25)}}, check.Status{State: check.Down, Since: at(19, 10, 25)})
		bars, label := barsOf(c, at(21, 16, 0), time.UTC)
		if classes(bars) != repeat("bad", store.RecentRuns) || bars[len(bars)-1].Tip != "No ping · due Wed 21 Oct 15:40" ||
			label != "Last 0 runs: 0 ok, 0 with a warning, 0 failed; 30 pings missed; down since Mon 10:25" {
			t.Fatalf("bars %q, last %q, label %q", classes(bars), bars[len(bars)-1].Tip, label)
		}
	})

	t.Run("a schedule it cannot read", func(t *testing.T) {
		c := store.CheckView{Cron: "not a schedule", TimeZone: "UTC", Recent: []store.RunView{ok(at(21, 10, 0))},
			Status: check.Status{State: check.Down, Since: at(21, 11, 0)}}
		if bars, _ := barsOf(c, at(21, 12, 0), time.UTC); classes(bars) != "up bad" || bars[1].Tip != "No ping · down at Wed 21 Oct 11:00" {
			t.Fatalf("bars %+v", bars)
		}
	})

	t.Run("a minutely cron job down for weeks", func(t *testing.T) {
		// The newest are shown, without walking every minute since.
		c := store.CheckView{Cron: "* * * * *", TimeZone: "Europe/Amsterdam", Grace: time.Minute,
			Recent: []store.RunView{ok(at(1, 10, 0))},
			Downs:  []store.Down{{At: at(1, 10, 2)}},
			Status: check.Status{State: check.Down, Since: at(1, 10, 2)},
		}
		bars, _ := barsOf(c, at(21, 16, 0), time.UTC)
		if classes(bars) != repeat("bad", store.RecentRuns) || bars[len(bars)-1].Tip != "No ping · due Wed 21 Oct 15:59" {
			t.Fatalf("bars %q, last %q", classes(bars), bars[len(bars)-1].Tip)
		}
	})

	t.Run("a ping later than its grace", func(t *testing.T) {
		// Hourly with a grace of 10 minutes: due at 11:00, down at 11:10,
		// back at 11:15.
		c := store.CheckView{Interval: time.Hour, Grace: 10 * time.Minute,
			Recent: []store.RunView{ok(at(21, 11, 15)), ok(at(21, 10, 0))},
			Downs:  []store.Down{{At: at(21, 11, 10), Resolved: at(21, 11, 25)}},
			Status: check.Status{State: check.Up, Since: at(21, 11, 15)},
		}
		if bars, _ := barsOf(c, at(21, 12, 0), time.UTC); classes(bars) != "up bad up" || bars[1].Tip != "No ping · due Wed 21 Oct 11:00" {
			t.Fatalf("bars %+v", bars)
		}
	})

	t.Run("office hours, down for months", func(t *testing.T) {
		// Every 5 minutes on weekdays from 9 to 5: the newest are shown on a
		// Saturday too, without walking every week since.
		c := store.CheckView{Cron: "*/5 9-16 * * 1-5", TimeZone: "UTC", Grace: 20 * time.Minute,
			Recent: []store.RunView{ok(time.Date(2026, 1, 9, 16, 55, 0, 0, time.UTC))},
			Downs:  []store.Down{{At: time.Date(2026, 1, 12, 9, 20, 0, 0, time.UTC)}},
			Status: check.Status{State: check.Down, Since: time.Date(2026, 1, 12, 9, 20, 0, 0, time.UTC)},
		}
		bars, _ := barsOf(c, at(24, 12, 0), time.UTC)
		if classes(bars) != repeat("bad", store.RecentRuns) || bars[len(bars)-1].Tip != "No ping · due Fri 23 Oct 16:55" {
			t.Fatalf("bars %q, last %q", classes(bars), bars[len(bars)-1].Tip)
		}
	})

	t.Run("office hours, last seen on a Friday evening", func(t *testing.T) {
		// The first one due is the Monday after, a long way from the next.
		fri := time.Date(2025, 8, 29, 17, 50, 0, 0, time.UTC)
		c := store.CheckView{Cron: "*/5 9-17 * * 1-5", TimeZone: "UTC", Grace: 10 * time.Minute,
			Recent: []store.RunView{ok(fri)},
			Downs:  []store.Down{{At: fri.Add(15 * time.Minute)}},
			Status: check.Status{State: check.Down, Since: fri.Add(15 * time.Minute)},
		}
		bars, _ := barsOf(c, at(21, 12, 0), time.UTC)
		if classes(bars) != repeat("bad", store.RecentRuns) || bars[len(bars)-1].Tip != "No ping · due Wed 21 Oct 11:50" {
			t.Fatalf("bars %q, last %q", classes(bars), bars[len(bars)-1].Tip)
		}
	})
}
