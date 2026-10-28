// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

func TestBarsShowWhereACheckWentDown(t *testing.T) {
	now := time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, time.UTC) }
	ok := func(t time.Time) store.RunView { return store.RunView{Outcome: "success", Ended: t} }
	classes := func(bars []bar) string {
		var s []string
		for _, b := range bars {
			s = append(s, b.Class)
		}
		return strings.Join(s, " ")
	}

	// No ping: down at 10:30, back at 11:00, down again since 15:20.
	c := store.CheckView{
		Recent: []store.RunView{ok(at(15, 0)), ok(at(11, 0)), ok(at(10, 0))},
		Downs:  []time.Time{at(15, 20), at(10, 30)},
		Status: check.Status{State: check.Down, Since: at(15, 20)},
	}
	bars, label := barsOf(c, now, time.UTC)
	if got := classes(bars); got != "up bad up up bad" {
		t.Fatalf("bars %q", got)
	}
	if bars[1].Title != "No ping, down at 10:30" || bars[4].Title != "No ping, down since 15:20" {
		t.Fatalf("titles %+v", bars)
	}
	if label != "Last 3 runs: 3 ok, 0 with a warning, 0 failed; 2 times down for a missed ping; down since 15:20" {
		t.Fatalf("label %q", label)
	}

	// A failed run, and a run that never ended, took it down themselves:
	// they are its red bars, with none added.
	c = store.CheckView{
		Recent: []store.RunView{
			{Outcome: "timeout", Started: at(12, 0), TimedOut: at(13, 0)},
			ok(at(11, 0)),
			{Outcome: "fail", Started: at(10, 0), Ended: at(10, 5)},
			ok(at(9, 0)),
		},
		Downs:  []time.Time{at(13, 0), at(10, 5)},
		Status: check.Status{State: check.Down, Since: at(13, 0)},
	}
	if bars, label := barsOf(c, now, time.UTC); classes(bars) != "up bad up bad" ||
		label != "Last 4 runs: 2 ok, 0 with a warning, 2 failed; down since 13:00" {
		t.Fatalf("bars %q, label %q", classes(bars), label)
	}

	// Down again while its incident was still open: the incident has the
	// first time, the status the second.
	c = store.CheckView{
		Recent: []store.RunView{ok(at(10, 10))},
		Downs:  []time.Time{at(10, 0)},
		Status: check.Status{State: check.Down, Since: at(10, 50)},
	}
	if bars, _ := barsOf(c, now, time.UTC); classes(bars) != "bad up bad" || bars[2].Title != "No ping, down since 10:50" {
		t.Fatalf("bars %+v", bars)
	}

	// A run that timed out and ended later took it down when it timed out:
	// it is red, with no bar added.
	c = store.CheckView{
		Recent: []store.RunView{{Outcome: "success", Started: at(12, 0), TimedOut: at(13, 0), Ended: at(13, 30)}},
		Downs:  []time.Time{at(13, 0)},
		Status: check.Status{State: check.Up, Since: at(13, 30)},
	}
	if bars, _ := barsOf(c, now, time.UTC); classes(bars) != "bad" {
		t.Fatalf("bars %q", classes(bars))
	}

	// Failing for longer than the runs shown: the run that took it down is
	// gone, and no missed ping is made up in its place.
	c = store.CheckView{Status: check.Status{State: check.Down, Since: at(9, 0)}}
	for i := range store.RecentRuns {
		c.Recent = append(c.Recent, store.RunView{Outcome: "fail", Ended: at(15, 0).Add(-time.Duration(i) * time.Minute)})
	}
	if bars, label := barsOf(c, now, time.UTC); strings.Contains(classes(bars), "up") || bars[0].Title != "" || strings.Contains(label, "missed") {
		t.Fatalf("bars %+v, label %q", bars, label)
	}

	// Many missed pings: the bars stay as many as the runs, the newest kept.
	c = store.CheckView{Status: check.Status{State: check.Up, Since: at(15, 0)}}
	for i := range store.RecentRuns {
		m := at(15, 0).Add(-time.Duration(i) * 10 * time.Minute)
		c.Recent = append(c.Recent, ok(m))
		c.Downs = append(c.Downs, m.Add(-5*time.Minute))
	}
	bars, label = barsOf(c, now, time.UTC)
	if len(bars) != store.RecentRuns || bars[len(bars)-1].Class != "up" ||
		label != "Last 15 runs: 15 ok, 0 with a warning, 0 failed; 15 times down for a missed ping" {
		t.Fatalf("%d bars, label %q", len(bars), label)
	}
}
