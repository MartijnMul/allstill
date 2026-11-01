// SPDX-License-Identifier: AGPL-3.0-only

package alert

import (
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

var ams, _ = time.LoadLocation("Europe/Amsterdam")

func night(t *testing.T) QuietHours {
	t.Helper()
	q, err := ParseQuietHours([]byte(`{"time_zone": "Europe/Amsterdam", "periods": [
		{"days": ["mon", "tue", "wed", "thu", "fri", "sat", "sun"], "start": "22:00", "end": "07:00"},
		{"days": ["sat"], "start": "12:00", "end": "14:00"}]}`), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestQuietHours(t *testing.T) {
	q := night(t)
	for _, c := range []struct {
		at    time.Time
		quiet bool
		until time.Time
	}{
		{time.Date(2026, 10, 21, 21, 59, 0, 0, ams), false, time.Time{}},
		{time.Date(2026, 10, 21, 22, 0, 0, 0, ams), true, time.Date(2026, 10, 22, 7, 0, 0, 0, ams)},
		// After midnight the period that started yesterday still runs.
		{time.Date(2026, 10, 22, 3, 0, 0, 0, ams), true, time.Date(2026, 10, 22, 7, 0, 0, 0, ams)},
		{time.Date(2026, 10, 22, 7, 0, 0, 0, ams), false, time.Time{}},
		{time.Date(2026, 10, 24, 13, 0, 0, 0, ams), true, time.Date(2026, 10, 24, 14, 0, 0, 0, ams)},
		{time.Date(2026, 10, 25, 13, 0, 0, 0, ams), false, time.Time{}},
		// The night the clocks go back is an hour longer, and ends at 07:00 all the same.
		{time.Date(2026, 10, 25, 2, 30, 0, 0, ams), true, time.Date(2026, 10, 25, 7, 0, 0, 0, ams)},
	} {
		until, quiet := q.Until(c.at)
		if quiet != c.quiet || !until.Equal(c.until) {
			t.Errorf("%s: quiet %v until %s, want %v until %s", c.at, quiet, until, c.quiet, c.until)
		}
	}
	if _, quiet := (QuietHours{}).Until(time.Now()); quiet {
		t.Error("no quiet hours are quiet")
	}
	for _, bad := range []string{`{"periods": [{"days": ["mon"], "start": "25:00", "end": "07:00"}]}`, `{"periods": [{"days": ["maandag"], "start": "22:00", "end": "07:00"}]}`, `{`} {
		if _, err := ParseQuietHours([]byte(bad), "UTC"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestQuietHoursOverASpringGap(t *testing.T) {
	q, err := ParseQuietHours([]byte(`{"periods": [{"days": ["sun"], "start": "01:00", "end": "02:30"}]}`), "Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 does not exist on 29 March 2026: the period ends at 03:30, the
	// first time after the gap.
	until, quiet := q.Until(time.Date(2026, 3, 29, 1, 30, 0, 0, ams))
	if !quiet || !until.Equal(time.Date(2026, 3, 29, 3, 30, 0, 0, ams)) {
		t.Fatalf("quiet %v until %s", quiet, until)
	}
}

func TestNextDigest(t *testing.T) {
	got, err := NextDigest(time.Date(2026, 10, 21, 7, 0, 0, 0, ams), "08:00", "Europe/Amsterdam")
	if err != nil || !got.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, ams)) {
		t.Fatalf("before 08:00: %s %v", got, err)
	}
	got, _ = NextDigest(time.Date(2026, 10, 21, 8, 0, 0, 0, ams), "08:00", "Europe/Amsterdam")
	if !got.Equal(time.Date(2026, 10, 22, 8, 0, 0, 0, ams)) {
		t.Fatalf("at 08:00: %s", got)
	}
	if _, err := NextDigest(time.Now(), "8 uur", "UTC"); err == nil {
		t.Fatal("a digest time that is no time")
	}
}

func TestDecide(t *testing.T) {
	q := night(t)
	day := time.Date(2026, 10, 21, 12, 0, 0, 0, ams)
	at3 := time.Date(2026, 10, 22, 3, 0, 0, 0, ams)
	at7 := time.Date(2026, 10, 22, 7, 0, 0, 0, ams)
	open := Situation{Open: true, Urgency: Now, Follows: true, RecoveryMessages: true, Told: Nothing, Quiet: q}
	toldDown := Situation{Reason: "recovered", Urgency: Now, Follows: true, RecoveryMessages: true, Told: Down, ToldHow: Now, Quiet: q}
	with := func(s Situation, f func(*Situation)) Situation { f(&s); return s }
	for _, c := range []struct {
		name string
		s    Situation
		at   time.Time
		want Decision
	}{
		{"down by day", open, day, Decision{Tell: Down}},
		{"down, over when heard", with(open, func(s *Situation) { s.OverWhenHeard = true }), day, Decision{Again: day.Add(time.Minute)}},
		{"down, over when heard, critical", with(open, func(s *Situation) { s.OverWhenHeard, s.Critical = true, true }), at3, Decision{Again: at3.Add(time.Minute)}},
		{"down at night waits for the morning", open, at3, Decision{Again: at7}},
		{"a critical check wakes you", with(open, func(s *Situation) { s.Critical = true }), at3, Decision{Tell: Down}},
		{"told already", with(open, func(s *Situation) { s.Told = Down }), day, Decision{}},
		{"digest is the digest's", with(open, func(s *Situation) { s.Urgency = Digest }), day, Decision{}},
		{"none never alerts", with(open, func(s *Situation) { s.Urgency = "none" }), day, Decision{}},
		{"not following any more", with(open, func(s *Situation) { s.Follows = false }), day, Decision{}},
		{"recovered", toldDown, day, Decision{Tell: Recovered}},
		{"recovered waits for the morning, even when critical", with(toldDown, func(s *Situation) { s.Critical = true }), at3, Decision{Again: at7}},
		{"recovered before they heard", with(toldDown, func(s *Situation) { s.Told = Nothing }), day, Decision{}},
		{"no recovery messages here", with(toldDown, func(s *Situation) { s.RecoveryMessages = false }), day, Decision{}},
		{"paused is no recovery", with(toldDown, func(s *Situation) { s.Reason = "paused" }), day, Decision{}},
		{"heard in the digest, recovered in the digest", with(toldDown, func(s *Situation) { s.ToldHow = Digest }), day, Decision{}},
		{"told only in shadow mode, now for real", with(toldDown, func(s *Situation) { s.ToldInShadow = true }), day, Decision{}},
		{"told in shadow mode, still in shadow mode", with(toldDown, func(s *Situation) { s.ToldInShadow, s.Shadow = true, true }), day, Decision{Tell: Recovered}},
	} {
		if got := Decide(c.s, c.at); got.Tell != c.want.Tell || !got.Again.Equal(c.want.Again) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// R17 and R18 of intent/manage-checks/spec.md.
func TestDigestItemsOfClosedAndPausedChecks(t *testing.T) {
	told := store.DigestEntry{Follows: true, Title: "x is down", Told: string(Down), ToldHow: Now, RecoveryMessages: true}
	for reason, want := range map[string]string{"deleted": "closed_deleted", "paused": "closed_paused"} {
		e := told
		e.Reason = reason
		items, _ := DigestItems([]store.DigestEntry{e}, nil, false)
		if len(items) != 1 || items[0].What != want {
			t.Errorf("%s: %+v", reason, items)
		}
	}
	paused := store.ListedCheck{Name: "y", Project: "P", State: "paused"}
	// A pause alone makes no message of its own every day.
	if items, _ := DigestItems(nil, []store.ListedCheck{paused}, false); len(items) != 0 {
		t.Errorf("paused alone: %+v", items)
	}
	e := told
	e.Reason = "deleted"
	items, _ := DigestItems([]store.DigestEntry{e}, []store.ListedCheck{paused}, false)
	if len(items) != 2 || items[1].What != "paused_check" || items[1].Title != "P: y" {
		t.Errorf("paused alongside: %+v", items)
	}
	never := store.ListedCheck{Name: "z", Project: "P", State: "new", Reason: "HTTP 503"}
	if items, _ := DigestItems(nil, []store.ListedCheck{never}, false); len(items) != 1 || items[0].Title != "P: z (HTTP 503)" {
		t.Errorf("never worked: %+v", items)
	}
}
