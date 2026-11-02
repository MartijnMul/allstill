// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

type fixture struct {
	s       *Store
	ws      string
	editor  Access
	viewer  Access
	project string
	pingKey string
}

func setup(t *testing.T) fixture {
	t.Helper()
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ed, _ := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "Editor"})
	vw, _ := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "Viewer"})
	p, key, _ := s.CreateProject(ctx, Command("test"), ws, "Backups")
	s.SetMember(ctx, Command("test"), ws, p, ed, Editor)
	s.SetMember(ctx, Command("test"), ws, p, vw, Viewer)
	edA, _ := s.AccessOfPerson(ctx, ws, ed)
	vwA, _ := s.AccessOfPerson(ctx, ws, vw)
	return fixture{s, ws, edA, vwA, p, key}
}

func (f fixture) get(t *testing.T, checkID string) db.Check {
	t.Helper()
	c, err := db.New(f.s.r).CheckByID(t.Context(), db.CheckByIDParams{WorkspaceID: f.ws, ID: checkID})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ping(uuid string, k check.Kind, rid string, at time.Time) PingIn {
	return PingIn{CheckUUID: uuid, Kind: k, ExitStatus: -1, RID: rid, Method: "POST", At: at}
}

func TestCreateCheckFollowsTheRole(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	n := NewCheck{Name: "Nightly backup", Slug: "nightly-backup", Cron: "0 3 * * *", TimeZone: "Europe/Amsterdam", Grace: time.Hour}
	if _, err := f.s.CreateCheck(ctx, f.viewer, f.project, n); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer creates a check: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, f.editor, "prj_other", n); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a check in a project the caller cannot see: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, n); err != nil {
		t.Fatal(err)
	}
	var inv *InvalidError
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, n); !errors.As(err, &inv) || inv.Field != "slug" {
		t.Fatalf("a slug used twice: %v", err)
	}
	for _, bad := range []NewCheck{
		{Name: "", Interval: time.Hour, Grace: time.Hour},
		{Name: "x", Slug: "log", Interval: time.Hour, Grace: time.Hour},
		{Name: "x", Slug: "Upper", Interval: time.Hour, Grace: time.Hour},
		{Name: "x", Interval: 30 * time.Second, Grace: time.Hour},
		{Name: "x", Interval: time.Hour, Grace: time.Second},
		{Name: "x", Interval: time.Hour, Cron: "* * * * *", TimeZone: "UTC", Grace: time.Hour},
		{Name: "x", Cron: "61 * * * *", TimeZone: "UTC", Grace: time.Hour},
		{Name: "x", Cron: "* * * * *", TimeZone: "Nowhere/Town", Grace: time.Hour},
		{Name: "x", Interval: time.Hour, Grace: time.Hour, PingUUID: "00000000-0000-0000-0000-000000000000"},
		{Name: "x", Interval: time.Hour, Grace: time.Hour, PingUUID: "5C2B1D2E-8F3A-4B7C-9D1E-2F3A4B5C6D7E"},
	} {
		if _, err := f.s.CreateCheck(ctx, f.editor, f.project, bad); !errors.As(err, &inv) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
}

// A nightly backup run through `allstill run`, then a night it hangs.
func TestPingsAndDeadlines(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: 24 * time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "r1", t0)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.State != "new" || got.Deadline.Valid {
		t.Fatalf("after start: %s, deadline %v", got.State, got.Deadline)
	}
	in := ping(c.PingUUID, check.Success, "r1", t0.Add(40*time.Minute))
	in.Body, in.ExitStatus = []byte("12 GB written\n"), 0
	out, err := f.s.Ping(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	want := t0.Add(24*time.Hour + 40*time.Minute)
	if got := f.get(t, c.ID); got.State != "up" || !out.Deadline.Equal(want) || msTime(got.Deadline) != want {
		t.Fatalf("after success: %s, deadline %v / %v", got.State, out.Deadline, got.Deadline)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 10})
	if len(runs) != 1 || runs[0].Outcome != "success" || runs[0].EndedAt.Int64-runs[0].StartedAt.Int64 != 40*60*1000 {
		t.Fatalf("runs %+v", runs)
	}
	pings, _ := db.New(f.s.r).PingsOfRun(ctx, nullString(runs[0].ID))
	if len(pings) != 2 || string(pings[1].Body) != "12 GB written\n" || pings[1].ExitStatus.Int64 != 0 {
		t.Fatalf("pings %+v", pings)
	}

	// The next night it starts and hangs.
	f.s.Ping(ctx, ping(c.PingUUID, check.Start, "r2", t0.Add(24*time.Hour)))
	next, err := f.s.ProcessDue(ctx, t0.Add(24*time.Hour+30*time.Minute))
	if err != nil || !next.Equal(t0.Add(25*time.Hour)) {
		t.Fatalf("next deadline %v, %v", next, err)
	}
	if next, _ = f.s.ProcessDue(ctx, t0.Add(25*time.Hour)); !next.IsZero() {
		t.Fatalf("a deadline after down: %v", next)
	}
	got := f.get(t, c.ID)
	if got.State != "down" || got.StateReason != "A run started but did not end within the grace period of 1 h." {
		t.Fatalf("after the hang: %s %q", got.State, got.StateReason)
	}
	var events int
	f.s.r.QueryRow("SELECT count(*) FROM events WHERE subject_id = ?", c.ID).Scan(&events)
	if events != 2 { // new to up, up to down
		t.Fatalf("%d events", events)
	}
}

func TestPingsWrittenOutOfOrderKeepTheirOrder(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Two runs overlap: the second start is written first, then an end that
	// arrived 5 ms earlier.
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "", t0.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(995*time.Millisecond))); err != nil {
		t.Fatalf("the late end: %v", err)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 10})
	if len(runs) != 1 || runs[0].Outcome != "success" || runs[0].EndedAt.Int64 != runs[0].StartedAt.Int64 {
		t.Fatalf("runs %+v", runs)
	}
	// Only the run's end moves: the check's deadline follows the ping.
	if got := f.get(t, c.ID); msTime(got.Deadline) != t0.Add(time.Hour+995*time.Millisecond) {
		t.Fatalf("deadline %v", msTime(got.Deadline))
	}
	// So does a clock set back an hour: the deadline does not stay an hour late.
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); msTime(got.Deadline) != t0 {
		t.Fatalf("deadline after the clock went back %v", msTime(got.Deadline))
	}
}

func TestAStartOvertakenByItsEndJoinsThatRun(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// A wrapper sent its start in the background, and the end got there first.
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "r1", t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "r1", t0.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 10})
	if len(runs) != 1 || runs[0].Outcome != "success" {
		t.Fatalf("runs %+v", runs)
	}
	pings, _ := db.New(f.s.r).PingsOfRun(ctx, sql.NullString{String: runs[0].ID, Valid: true})
	if len(pings) != 2 {
		t.Fatalf("the run has %d pings", len(pings))
	}
	// No run is left open to time out.
	if got := f.get(t, c.ID); got.State != "up" || msTime(got.Deadline) != t0.Add(time.Hour) {
		t.Fatalf("%s, deadline %v", got.State, msTime(got.Deadline))
	}
	// A start with that rid past the grace period is another run.
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "r1", t0.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if open, _ := db.New(f.s.r).OpenRuns(ctx, c.ID); len(open) != 1 {
		t.Fatalf("open runs %+v", open)
	}
}

func TestAStartWithoutRidOvertakenByItsEndJoinsThatRun(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	pingAt := func(k check.Kind, rid string, at time.Time) {
		t.Helper()
		if _, err := f.s.Ping(ctx, ping(c.PingUUID, k, rid, at)); err != nil {
			t.Fatal(err)
		}
	}
	openRuns := func() int {
		t.Helper()
		open, err := db.New(f.s.r).OpenRuns(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(open)
	}

	// A wrapper sent its start in the background, and the end got there first.
	pingAt(check.Success, "", t0)
	pingAt(check.Start, "", t0.Add(time.Second))
	if n := openRuns(); n != 0 {
		t.Fatalf("%d open runs after the late start", n)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 10})
	if len(runs) != 1 || runs[0].Outcome != "success" {
		t.Fatalf("runs %+v", runs)
	}
	if pings, _ := db.New(f.s.r).PingsOfRun(ctx, sql.NullString{String: runs[0].ID, Valid: true}); len(pings) != 2 {
		t.Fatalf("the run has %d pings", len(pings))
	}
	// So nothing times out, and the check is judged by its schedule.
	if _, err := f.s.ProcessDue(ctx, t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.State != "up" || msTime(got.Deadline) != t0.Add(time.Hour) {
		t.Fatalf("%s, deadline %v", got.State, msTime(got.Deadline))
	}

	// That end has its start: the next start is the next run.
	pingAt(check.Start, "", t0.Add(2*time.Second))
	if n := openRuns(); n != 1 {
		t.Fatalf("%d open runs after a second start", n)
	}
}

func TestAStartWithoutRidJoinsOnlyAnEndJustBeforeIt(t *testing.T) {
	cases := []struct {
		name    string
		end     check.Kind
		endRID  string
		after   time.Duration
		running bool
	}{
		{name: "within the ten seconds", end: check.Success, after: 10 * time.Second},
		{name: "a failed end", end: check.Fail, after: time.Second},
		{name: "past the ten seconds, however long the grace", end: check.Success, after: 11 * time.Second, running: true},
		{name: "an end that belongs to a rid", end: check.Success, endRID: "r1", after: time.Second, running: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := t.Context()
			c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Ping(ctx, ping(c.PingUUID, tc.end, tc.endRID, t0)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "", t0.Add(tc.after))); err != nil {
				t.Fatal(err)
			}
			open, err := db.New(f.s.r).OpenRuns(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (len(open) == 1) != tc.running {
				t.Fatalf("open runs %+v, want a running run: %v", open, tc.running)
			}
			if want := map[check.Kind]string{check.Success: "up", check.Fail: "down"}[tc.end]; f.get(t, c.ID).State != want {
				t.Fatalf("%s, want %s", f.get(t, c.ID).State, want)
			}
		})
	}
}

// A start that came too long after an end to be its own still guards its run:
// after a lost start the next run is tracked as usual, and when it hangs the
// grace period finds out, not the schedule.
func TestAStartAfterALostStartIsTrackedAgain(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: 24 * time.Hour, Grace: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	start := t0.Add(30 * time.Second)
	for _, p := range []PingIn{ping(c.PingUUID, check.Success, "", t0), ping(c.PingUUID, check.Start, "", start)} {
		if _, err := f.s.Ping(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.ProcessDue(ctx, start.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.State != "down" {
		t.Fatalf("a hung run left the check %s", got.State)
	}
}

// The price of joining by time alone: a job that starts again within seconds of
// its end looks the same as one whose ends overtake their starts, so after one
// end without a start its runs stay untracked until it leaves a longer gap.
func TestAFastJobIsTrackedAgainAfterALongerGap(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	at := t0
	send := func(k check.Kind, after time.Duration) int {
		t.Helper()
		at = at.Add(after)
		if _, err := f.s.Ping(ctx, ping(c.PingUUID, k, "", at)); err != nil {
			t.Fatal(err)
		}
		open, err := db.New(f.s.r).OpenRuns(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(open)
	}
	send(check.Success, 0)
	for range 3 {
		if n := send(check.Start, 5*time.Second); n != 0 {
			t.Fatalf("%d open runs after a start within seconds", n)
		}
		send(check.Success, 20*time.Second)
	}
	if n := send(check.Start, 30*time.Second); n != 1 {
		t.Fatalf("%d open runs after a longer gap", n)
	}
}

// Only the latest run can be joined: once another run came after an end, a
// start is not that end's own.
func TestAStartJoinsOnlyTheLatestRun(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	pings := []PingIn{
		ping(c.PingUUID, check.Success, "", t0),
		ping(c.PingUUID, check.Start, "r1", t0.Add(time.Second)),
		ping(c.PingUUID, check.Success, "r1", t0.Add(2*time.Second)),
		ping(c.PingUUID, check.Start, "", t0.Add(3*time.Second)),
	}
	for _, p := range pings {
		if _, err := f.s.Ping(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if open, _ := db.New(f.s.r).OpenRuns(ctx, c.ID); len(open) != 1 {
		t.Fatalf("open runs %+v", open)
	}
}

// An end that closed a run has its start, so a start right after it is the
// next run.
func TestAStartAfterAFinishedRunIsTheNextRun(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range []check.Kind{check.Start, check.Success, check.Start} {
		if _, err := f.s.Ping(ctx, ping(c.PingUUID, k, "", t0.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if open, _ := db.New(f.s.r).OpenRuns(ctx, c.ID); len(open) != 1 {
		t.Fatalf("open runs %+v", open)
	}
}

func TestPingsBySlug(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	in := PingIn{PingKey: f.pingKey, Slug: "db-dump", Kind: check.Success, ExitStatus: -1, Method: "GET", At: t0}
	if _, err := f.s.Ping(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown slug: %v", err)
	}
	in.Create = true
	out, err := f.s.Ping(ctx, in)
	if err != nil || !out.Created || !out.Deadline.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("create=1: %+v, %v", out, err)
	}
	if out, err = f.s.Ping(ctx, in); err != nil || out.Created {
		t.Fatalf("a second create=1: %+v, %v", out, err)
	}
	in.PingKey = "AAAAAAAAAAAAAAAAAAAAAA"
	if _, err := f.s.Ping(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wrong ping key: %v", err)
	}
	if _, err := f.s.Ping(ctx, PingIn{Kind: check.Success, At: t0}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("a ping without a check: %v", err)
	}
	if _, err := f.s.Ping(ctx, ping("5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e", check.Success, "", t0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown UUID: %v", err)
	}
}

func TestPingsCreateAtMostALimitOfChecks(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	// Fill the project to one below the limit, as a leaked key would.
	_, err := f.s.w.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind, interval_seconds,
			grace_seconds, state_since, created_at, updated_at)
		SELECT printf('chk_%05d', i), ?, ?, printf('00000000-0000-4000-8000-%012d', i), 'x', 'ping', 'interval', 60, 60, 0, 0, 0
		FROM n`, CreateLimit-1, f.ws, f.project)
	if err != nil {
		t.Fatal(err)
	}
	in := PingIn{PingKey: f.pingKey, Slug: "last", Create: true, Kind: check.Success, ExitStatus: -1, Method: "GET", At: t0}
	if out, err := f.s.Ping(ctx, in); err != nil || !out.Created {
		t.Fatalf("the last check: %+v, %v", out, err)
	}
	in.Slug = "one-too-many"
	if _, err := f.s.Ping(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("past the limit: %v", err)
	}
	in.Slug = "last"
	if _, err := f.s.Ping(ctx, in); err != nil {
		t.Fatalf("an existing check in a full project: %v", err)
	}
}

func TestRecoverMarksMissedDeadlinesUnknown(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Hourly", Interval: time.Hour, Grace: 10 * time.Minute})
	f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0))
	f.s.Alive(ctx, t0.Add(30*time.Minute))
	if err := f.s.Recover(ctx, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, c.ID)
	if got.State != "unknown" || msTime(got.Deadline) != t0.Add(3*time.Hour+10*time.Minute) {
		t.Fatalf("%s, deadline %v", got.State, got.Deadline)
	}
}

// A schedule this host cannot read marks its check unknown, and never stops
// the other checks from being judged.
func TestAnUnreadableScheduleHoldsUpNothing(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	good, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Good", Interval: time.Hour, Grace: time.Minute})
	bad, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Bad", Cron: "0 3 * * *", TimeZone: "UTC", Grace: time.Minute})
	f.s.Ping(ctx, ping(good.PingUUID, check.Success, "", t0))
	f.s.Ping(ctx, ping(bad.PingUUID, check.Success, "", t0))
	f.s.w.Exec("UPDATE checks SET time_zone = 'Nowhere/Town' WHERE id = ?", bad.ID)

	if _, err := f.s.ProcessDue(ctx, t0.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, good.ID); got.State != "down" {
		t.Fatalf("good: %s", got.State)
	}
	got := f.get(t, bad.ID)
	if got.State != "unknown" || got.Deadline.Valid || got.StateReason == "" {
		t.Fatalf("bad: %s %q %v", got.State, got.StateReason, got.Deadline)
	}
	if _, err := f.s.Ping(ctx, ping(bad.PingUUID, check.Success, "", t0.Add(26*time.Hour))); err != nil {
		t.Fatalf("a ping to it: %v", err)
	}
	if err := f.s.Recover(ctx, t0.Add(27*time.Hour)); err != nil {
		t.Fatalf("recover: %v", err)
	}
}
