// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// aProbe is a probe that serves the fixture's project, with a check on it.
func aProbe(t *testing.T, f fixture) (Probe, string) {
	t.Helper()
	ctx := t.Context()
	admin, err := f.s.AdminAccess(ctx, f.ws, "test")
	if err != nil {
		t.Fatal(err)
	}
	probeID, key, err := f.s.CreateProbe(ctx, admin, "inside", f.project, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.ProbeByKey(ctx, key)
	if err != nil || p.ID != probeID || p.Name != "inside" {
		t.Fatalf("%+v, %v", p, err)
	}
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "intranet", Kind: "http", Interval: time.Minute, ProbeID: probeID,
		Settings: map[string]any{"url": "http://10.0.0.5/"}})
	if err != nil {
		t.Fatal(err)
	}
	return p, c.ID
}

func TestAProbeHasAKeyAndACheckOfItsOwn(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	if _, _, err := f.s.CreateProbe(ctx, f.editor, "inside", f.project, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor made a probe: %v", err)
	}
	if _, _, err := f.s.CreateProbe(ctx, admin, "inside", "prj_none", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a project that does not exist: %v", err)
	}
	p, _ := aProbe(t, f)
	if _, _, err := f.s.CreateProbe(ctx, admin, "inside", f.project, nil); err == nil {
		t.Error("two probes with one name")
	}
	own := f.get(t, p.CheckID)
	if own.Kind != "ping" || own.Name != "Probe inside" || own.IntervalSeconds.Int64+own.GraceSeconds != 180 {
		t.Errorf("its own check: %+v", own)
	}
	if _, err := f.s.ProbeByKey(ctx, "alp_wrong"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a wrong key: %v", err)
	}
	// A new key replaces the old one.
	old, _, _ := f.s.CreateProbe(ctx, admin, "second", f.project, nil)
	key, err := f.s.RekeyProbe(ctx, admin, "", "second")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.ProbeByKey(ctx, key); err != nil || got.ID != old {
		t.Errorf("the new key: %+v, %v", got, err)
	}
	if _, err := f.s.RekeyProbe(ctx, f.editor, "", "second"); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor rekeyed: %v", err)
	}
	if _, err := f.s.RekeyProbe(ctx, admin, "", "none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such probe: %v", err)
	}
}

// Spec case 10: a probe only runs checks of the projects it serves.
func TestAProbeStaysWithinItsProjects(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Customer")
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	settings := map[string]any{"url": "https://example.com/"}
	var invalid *InvalidError

	if _, err := f.s.CreateCheck(ctx, admin, other, NewCheck{Name: "x", Kind: "http", Interval: time.Minute, ProbeID: p.ID, Settings: settings}); !errors.As(err, &invalid) || invalid.Field != "probe" {
		t.Errorf("made in a project it does not serve: %v", err)
	}
	c, err := f.s.CreateCheck(ctx, admin, other, NewCheck{Name: "x", Kind: "http", Interval: time.Minute, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetActiveCheckOn(ctx, admin, c.ID, time.Minute, settings, p.ID, t0); !errors.As(err, &invalid) || invalid.Field != "probe" {
		t.Errorf("assigned outside its projects: %v", err)
	}
	if err := f.s.SetActiveCheckOn(ctx, f.viewer, id, time.Minute, settings, "", t0); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer: %v", err)
	}
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, id, other, t0); !errors.As(err, &invalid) || invalid.Field != "probe" {
		t.Errorf("moved out of its projects while on the probe: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "y", Interval: time.Hour, Grace: time.Hour, ProbeID: p.ID}); !errors.As(err, &invalid) {
		t.Errorf("a ping check on a probe: %v", err)
	}
	// Back on the server, it can move.
	if err := f.s.SetActiveCheckOn(ctx, admin, id, time.Minute, settings, "", t0); err != nil {
		t.Fatal(err)
	}
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, id, other, t0); err != nil {
		t.Error(err)
	}
}

// A check is run by the server, or by one probe.
func TestACheckRunsInOnePlace(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	p, id := aProbe(t, f)
	server, err := f.s.ActiveChecks(ctx)
	if err != nil || len(server) != 0 {
		t.Fatalf("the server runs %+v, %v", server, err)
	}
	probe, err := f.s.ChecksOfProbe(ctx, p.ID)
	if err != nil || len(probe) != 1 || probe[0].ID != id || probe[0].Kind != "http" || probe[0].Interval != time.Minute {
		t.Fatalf("the probe runs %+v, %v", probe, err)
	}
	before := ProbeETag(probe)
	if err := f.s.SetActiveCheckOn(ctx, admin, id, time.Minute, map[string]any{"url": "http://10.0.0.5/"}, "", t0); err != nil {
		t.Fatal(err)
	}
	if server, _ := f.s.ActiveChecks(ctx); len(server) != 1 {
		t.Errorf("back on the server: %+v", server)
	}
	probe, _ = f.s.ChecksOfProbe(ctx, p.ID)
	if len(probe) != 0 || ProbeETag(probe) == before {
		t.Errorf("the probe still runs %+v", probe)
	}
}

func TestProbeRunsCountOnce(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	apply := func(at time.Time, runs ...ProbeRun) ProbeRuns {
		t.Helper()
		r, err := f.s.ApplyProbeRuns(ctx, p, runs, at, at)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	failed := func(rid string, ago time.Duration) ProbeRun {
		return ProbeRun{ID: rid, CheckID: id, Outcome: check.Failed, Reason: "HTTP 503", EndedAgo: ago, Values: map[string]float64{"ms": 200, "status": 503}}
	}

	// Stamped with the server's clock, whatever the probe's says.
	if r := apply(t0, failed("a", 5*time.Second)); r.Applied != 1 || r.Ignored != 0 {
		t.Fatalf("%+v", r)
	}
	c := f.get(t, id)
	if got := msTime(c.LastPingAt); !got.Equal(t0.Add(-5*time.Second)) || c.FailStreak != 1 {
		t.Errorf("last attempt %v, streak %d", got, c.FailStreak)
	}
	// Spec case 11: a batch sent again after a lost answer does not count twice.
	if r := apply(t0.Add(time.Second), failed("a", 6*time.Second)); r.Applied != 0 || r.Ignored != 1 {
		t.Errorf("sent twice: %+v", r)
	}
	if c := f.get(t, id); c.FailStreak != 1 || c.State == "down" {
		t.Errorf("sent twice: streak %d, %s", c.FailStreak, c.State)
	}
	// Older than the last it applied, too old, or not its own: ignored.
	if r := apply(t0.Add(time.Second), failed("b", time.Minute)); r.Ignored != 1 {
		t.Errorf("older than the last: %+v", r)
	}
	if r := apply(t0.Add(time.Second), failed("c", 25*time.Hour)); r.Ignored != 1 {
		t.Errorf("older than a day: %+v", r)
	}
	someoneElse := f.httpCheck(t)
	if r := apply(t0.Add(time.Second), ProbeRun{ID: "d", CheckID: someoneElse, Outcome: check.Failed}); r.Ignored != 1 {
		t.Errorf("a check on the server: %+v", r)
	}
	if c := f.get(t, someoneElse); c.FailStreak != 0 || c.LastPingAt.Valid {
		t.Errorf("a probe moved another check: %+v", c)
	}
	if r := apply(t0.Add(time.Second), ProbeRun{ID: "e", CheckID: "chk_none", Outcome: check.Failed}); r.Ignored != 1 {
		t.Errorf("no such check: %+v", r)
	}
	// In one post, the oldest goes first, whatever the order they came in.
	r := apply(t0.Add(time.Minute), failed("g", time.Second), ProbeRun{ID: "f", CheckID: id, Outcome: check.Passed, EndedAgo: 2 * time.Second})
	if r.Applied != 2 {
		t.Fatalf("%+v", r)
	}
	if c := f.get(t, id); c.FailStreak != 1 {
		t.Errorf("the newest run did not end up last: streak %d", c.FailStreak)
	}
}

// R7: every post keeps the probe's own check up; without posts it goes down
// after 3 minutes and its checks become unknown, with an incident for the probe
// alone.
func TestAQuietProbe(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	r, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "a", CheckID: id, Outcome: check.Passed}}, t0, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Deadline.Equal(t0.Add(time.Minute)) {
		t.Errorf("the scheduler's next look: %v", r.Deadline)
	}
	if c := f.get(t, p.CheckID); c.State != "up" || !msTime(c.LastPingAt).Equal(t0) {
		t.Fatalf("its own check after a post: %s", c.State)
	}
	if _, err := f.s.ProcessDue(ctx, t0.Add(2*time.Minute+59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, p.CheckID); c.State == "down" {
		t.Error("down before 3 minutes")
	}
	if _, err := f.s.ProcessDue(ctx, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if own, c := f.get(t, p.CheckID), f.get(t, id); own.State != "down" || c.State != "unknown" {
		t.Errorf("after 3 minutes: its own check %s, the check it runs %s", own.State, c.State)
	}
	// An empty post brings it back; its checks wait for a run that judges them.
	back := t0.Add(4 * time.Minute)
	if _, err := f.s.ApplyProbeRuns(ctx, p, nil, back, back); err != nil {
		t.Fatal(err)
	}
	if own, c := f.get(t, p.CheckID), f.get(t, id); own.State != "up" || c.State != "unknown" {
		t.Errorf("back: its own check %s, the check it runs %s", own.State, c.State)
	}
	if _, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "b", CheckID: id, Outcome: check.Passed}}, back, back); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.State != "up" {
		t.Errorf("after a run: %s", c.State)
	}
}

// A wrong clock means certificates cannot be trusted: its checks become unknown
// until it is set right, and the post says by how much.
func TestAProbeWithAWrongClock(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	run := func(rid string, sent time.Time) ProbeRuns {
		t.Helper()
		r, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: rid, CheckID: id, Outcome: check.Passed}}, sent, t0)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := run("a", t0.Add(-5*time.Minute))
	if r.ClockOff != -5*time.Minute {
		t.Errorf("clock off %v", r.ClockOff)
	}
	if c := f.get(t, id); c.State != "unknown" || c.StateReason != ClockOffReason {
		t.Errorf("%s: %s", c.State, c.StateReason)
	}
	if c := f.get(t, p.CheckID); c.State != "up" {
		t.Errorf("the probe is alive, its own check is %s", c.State)
	}
	// Within a minute it counts.
	if r := run("b", t0.Add(59*time.Second)); r.ClockOff != 0 {
		t.Errorf("within a minute: %v", r.ClockOff)
	}
	if c := f.get(t, id); c.State != "up" {
		t.Errorf("after the clock is right: %s", c.State)
	}
}

// Spec case 1: runs that wait for the network to return tell of an outage when
// they arrive. Dated by when they ended, the incident would have resolved before
// the last digest, so nobody would hear of it, and it would look like a sender
// hours behind.
func TestAnOutageHeardOfLateIsStillTold(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	const settle = 10 * time.Minute
	back := t0.Add(6 * time.Hour)
	lastDigest := t0.Add(5 * time.Hour)
	run := func(rid string, o check.Outcome, ago time.Duration) ProbeRun {
		return ProbeRun{ID: rid, CheckID: id, Outcome: o, Reason: "HTTP 503", EndedAgo: ago}
	}
	if _, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{run("worked", check.Passed, 0)}, t0, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{
		run("a", check.Failed, 5*time.Hour),
		run("b", check.Failed, 5*time.Hour-time.Minute),
		run("c", check.Passed, 4*time.Hour),
	}, back, back); err != nil {
		t.Fatal(err)
	}
	// The check's own history keeps when things happened.
	if c := f.get(t, id); c.State != "up" || !msTime(c.LastPingAt).Equal(back.Add(-4*time.Hour)) {
		t.Errorf("%s, last attempt %v", c.State, msTime(c.LastPingAt))
	}
	// The incident dates from now: nothing looks hours behind.
	due, err := f.s.OldestDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !due.IsZero() && back.Sub(due) > 2*time.Minute {
		t.Errorf("the sender looks %v behind", back.Sub(due))
	}
	// It settles from now, not at once.
	if n, err := f.s.SettleIncidents(ctx, back.Add(settle-time.Second), settle); err != nil || n != 0 {
		t.Errorf("settled %d before its time, %v", n, err)
	}
	if n, err := f.s.SettleIncidents(ctx, back.Add(settle), settle); err != nil || n != 1 {
		t.Errorf("settled %d, %v", n, err)
	}
	// And the next digest says it happened.
	entries, _, err := f.s.DigestOf(ctx, f.editor.PersonID, lastDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the digest has %d entries", len(entries))
	}
}

// R6: a check is run by the server, or by one probe. An attempt the server's
// loop made after the check moved to a probe, before it read its checks again,
// does not count.
func TestAnAttemptByTheServerDoesNotCountOnAProbesCheck(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	_, id := aProbe(t, f)
	if _, err := f.s.RecordAttempts(ctx, []AttemptOf{{WorkspaceID: f.ws, CheckID: id, Attempt: Attempt{
		Attempt: check.Attempt{Outcome: check.Failed, Reason: "the address is not on the public internet", At: t0}, Started: t0,
	}}}); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 0 || c.LastPingAt.Valid {
		t.Errorf("streak %d, last attempt %v", c.FailStreak, c.LastPingAt)
	}
}

func TestAProbesOwnCheckStaysWithinItsProjects(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, _ := aProbe(t, f)
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Customer")
	var invalid *InvalidError
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, p.CheckID, other, t0); !errors.As(err, &invalid) || invalid.Field != "probe" {
		t.Errorf("%v", err)
	}
}

// Moving a check and changing it is one change: a setting that is not valid
// leaves it where it was.
func TestSetActiveCheckOnIsOneChange(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	p, _ := aProbe(t, f)
	c, err := f.s.CreateCheck(ctx, admin, f.project, NewCheck{Name: "site", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetActiveCheckOn(ctx, admin, c.ID, time.Minute, map[string]any{"url": "ftp://example.com/"}, p.ID, t0); err == nil {
		t.Fatal("a setting that is not valid was taken")
	}
	if got := f.get(t, c.ID); got.ProbeID.Valid {
		t.Error("it moved all the same")
	}
	if err := f.s.SetActiveCheckOn(ctx, admin, c.ID, 2*time.Minute, map[string]any{"url": "https://example.com/health"}, p.ID, t0); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.ProbeID.String != p.ID || got.IntervalSeconds.Int64 != 120 {
		t.Errorf("%+v", got)
	}
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Customer")
	admin, _ = f.s.AdminAccess(ctx, f.ws, "test")
	elsewhere, err := f.s.CreateCheck(ctx, admin, other, NewCheck{Name: "x", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetActiveCheckOn(ctx, admin, elsewhere.ID, 5*time.Minute, map[string]any{"url": "https://example.com/"}, p.ID, t0); err == nil {
		t.Error("a probe that does not serve its project was taken")
	}
	if got := f.get(t, elsewhere.ID); got.IntervalSeconds.Int64 != 60 {
		t.Error("it changed all the same")
	}
}

// Failures seen by one runner do not count towards the next.
func TestAMoveStartsTheFailuresOver(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	p, _ := aProbe(t, f)
	id := f.httpCheck(t)
	f.attempt(t, id, check.Failed, t0)
	if c := f.get(t, id); c.FailStreak != 1 {
		t.Fatalf("streak %d", c.FailStreak)
	}
	if err := f.s.SetActiveCheckOn(ctx, admin, id, time.Minute, map[string]any{"url": "https://shop.example.com/health"}, p.ID, t0); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 0 || c.ProbeID.String != p.ID {
		t.Errorf("streak %d, probe %q", c.FailStreak, c.ProbeID.String)
	}
}

// Over when heard means the check came back before Allstill heard it went down.
// A live probe's runs arrive seconds after they end, which must not make an
// outage it heard of as it happened look over.
func TestOverWhenHeard(t *testing.T) {
	cases := []struct {
		name string
		down time.Duration // how long ago the failure ended, when it is posted
		up   time.Duration // the same of the recovery
		wait time.Duration // between the two posts
		want bool
	}{
		{name: "both late, in one post", down: 2 * time.Hour, up: time.Hour, want: true},
		{name: "both late, in two posts", down: 2 * time.Hour, up: time.Hour, wait: time.Second, want: true},
		{name: "heard live, its recovery seconds old", down: 2 * time.Second, up: 2 * time.Second, wait: 2 * time.Minute},
		{name: "heard live, its recovery fresh", wait: 2 * time.Minute},
		{name: "late, and back only after", down: time.Hour, up: 3 * time.Second, wait: time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			p, id := aProbe(t, f)
			admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
			start := t0.Add(-3 * time.Hour)
			if err := f.s.SetActiveCheckOn(ctx, admin, id, time.Minute, map[string]any{"url": "http://10.0.0.5/", "fail_after": 1}, p.ID, start); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "worked", CheckID: id, Outcome: check.Passed}}, start, start); err != nil {
				t.Fatal(err)
			}
			post := func(at time.Time, r ProbeRun) {
				t.Helper()
				if _, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{r}, at, at); err != nil {
					t.Fatal(err)
				}
			}
			post(t0, ProbeRun{ID: "down", CheckID: id, Outcome: check.Failed, EndedAgo: c.down})
			at := t0.Add(c.wait)
			post(at, ProbeRun{ID: "up", CheckID: id, Outcome: check.Passed, EndedAgo: c.up})
			due, err := f.s.DueRecipients(ctx, at, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range due {
				if d.Title == "intranet is down" {
					found = true
					if d.OverWhenHeard != c.want {
						t.Errorf("over when heard: %v", d.OverWhenHeard)
					}
				}
			}
			if !found {
				t.Fatalf("no one to tell: %+v", due)
			}
		})
	}
}
