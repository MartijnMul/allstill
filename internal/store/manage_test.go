// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

func ptr[T any](v T) *T { return &v }

func (f fixture) pingCheck(t *testing.T) (string, string) {
	t.Helper()
	c, err := f.s.CreateCheck(t.Context(), f.editor, f.project, NewCheck{Name: "nightly", Interval: time.Hour, Grace: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID, c.PingUUID
}

// Each operation, for each kind of caller: an editor may, a viewer is refused, and
// someone who cannot see the project does not learn it exists.
func TestManagingFollowsTheRole(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	stranger, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Stranger"}, []string{other}, Owner)
	if err != nil {
		t.Fatal(err)
	}
	outside, err := f.s.AccessOfPerson(ctx, f.ws, stranger)
	if err != nil {
		t.Fatal(err)
	}
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	ops := map[string]func(Access, string) error{
		"change": func(a Access, id string) error {
			_, err := f.s.UpdateCheck(ctx, a, id, Change{Name: ptr("renamed")}, t0)
			return err
		},
		"pause":  func(a Access, id string) error { return f.s.PauseCheck(ctx, a, id, t0) },
		"resume": func(a Access, id string) error { return f.s.ResumeCheck(ctx, a, id, t0) },
		"rotate": func(a Access, id string) error {
			_, err := f.s.RotatePingURL(ctx, a, id, t0)
			return err
		},
		"alerting": func(a Access, id string) error { return f.s.SetAlertingAs(ctx, a, id, "digest", false, t0) },
		"move":     func(a Access, id string) error { return f.s.MoveCheckAs(ctx, a, id, f.project, t0) },
		"delete":   func(a Access, id string) error { return f.s.DeleteCheck(ctx, a, id, t0) },
	}
	for name, op := range ops {
		id, _ := f.pingCheck(t)
		before := f.get(t, id)
		if err := op(f.viewer, id); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by a viewer: %v", name, err)
		}
		if err := op(outside, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s by an owner of another project: %v", name, err)
		}
		if after := f.get(t, id); after.Name != before.Name || after.State != before.State || after.Urgency != before.Urgency || after.PingUuid != before.PingUuid {
			t.Errorf("%s changed it all the same: %+v", name, after)
		}
		if err := op(f.editor, id); err != nil {
			t.Errorf("%s by an editor: %v", name, err)
		}
	}
	// Moving also takes the right in the project it goes to.
	id, _ := f.pingCheck(t)
	if err := f.s.MoveCheckAs(ctx, f.editor, id, f.project, t0); err != nil {
		t.Errorf("within its project: %v", err)
	}
	if err := f.s.MoveCheckAs(ctx, f.editor, id, other, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("to a project they cannot see: %v", err)
	}
	if err := f.s.SetMember(ctx, Command("test"), f.ws, other, f.editor.PersonID, Viewer); err != nil {
		t.Fatal(err)
	}
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	if err := f.s.MoveCheckAs(ctx, f.editor, id, other, t0); !errors.Is(err, ErrForbidden) {
		t.Errorf("to a project they only view: %v", err)
	}
	if err := f.s.SetMember(ctx, Command("test"), f.ws, other, f.editor.PersonID, Editor); err != nil {
		t.Fatal(err)
	}
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	if err := f.s.MoveCheckAs(ctx, f.editor, id, other, t0); err != nil {
		t.Errorf("to a project they edit: %v", err)
	}
	if got := f.get(t, id); got.ProjectID != other {
		t.Errorf("it stayed in %s", got.ProjectID)
	}
}

func TestChangingACheck(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id, uuid := f.pingCheck(t)
	if _, err := f.s.Ping(ctx, ping(uuid, check.Success, "", t0)); err != nil {
		t.Fatal(err)
	}
	version := f.get(t, id).Version
	deadline, err := f.s.UpdateCheck(ctx, f.editor, id, Change{
		Version: version, Name: ptr("  renamed  "), Slug: ptr("renamed"), Description: ptr("why it is here"), Tags: ptr([]string{"a", "b"}),
	}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c := f.get(t, id)
	if c.Name != "renamed" || c.Slug.String != "renamed" || c.Description != "why it is here" || c.Tags != `["a","b"]` || c.Version != version+1 {
		t.Errorf("%+v", c)
	}
	// R10: without a change of schedule, the clock stays, so renaming cannot
	// postpone a late job.
	if !deadline.Equal(t0.Add(time.Hour)) || !msTime(c.NextExpectedAt).Equal(t0.Add(time.Hour)) {
		t.Errorf("deadline %v, next %v", deadline, msTime(c.NextExpectedAt))
	}
	// R9: from a page that is out of date, it is refused, and a ping does not make
	// a page out of date.
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Version: version, Name: ptr("again")}, t0); !errors.Is(err, ErrChanged) {
		t.Errorf("from an old version: %v", err)
	}
	now := f.get(t, id).Version
	if _, err := f.s.Ping(ctx, ping(uuid, check.Success, "", t0.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Version: now, Name: ptr("again")}, t0); err != nil {
		t.Errorf("after a ping: %v", err)
	}
	// The same rules as a new check.
	for field, ch := range map[string]Change{
		"name":           {Name: ptr("  ")},
		"slug":           {Slug: ptr("Not A Slug")},
		"schedule.every": {Interval: ptr(time.Second)},
		"grace":          {Grace: ptr(time.Second)},
		"schedule":       {Cron: ptr("not cron"), TimeZone: ptr("UTC")},
		"tags":           {Tags: ptr(make([]string, 21))},
		"kind":           {Settings: map[string]any{"url": "https://example.com"}},
		"description":    {Description: ptr(strings.Repeat("x", MaxDescription+1))},
		"probe":          {Probe: ptr("prb_none")},
	} {
		var invalid *InvalidError
		if _, err := f.s.UpdateCheck(ctx, f.editor, id, ch, t0); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	// A check Allstill runs takes its grace from its interval: a long one would keep
	// it green for as long after its runner stopped.
	site := f.httpCheck(t)
	var refused *InvalidError
	if _, err := f.s.UpdateCheck(ctx, f.editor, site, Change{Grace: ptr(30 * 24 * time.Hour)}, t0); !errors.As(err, &refused) || refused.Field != "grace" {
		t.Errorf("a grace on an active check: %v", err)
	}
	// A slug in use.
	other, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "other", Slug: "other", Interval: time.Hour, Grace: time.Hour})
	_ = other
	var invalid *InvalidError
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Slug: ptr("other")}, t0); !errors.As(err, &invalid) || invalid.Field != "slug" {
		t.Errorf("a slug in use: %v", err)
	}
	if _, err := f.s.UpdateCheck(ctx, f.viewer, "chk_none", Change{}, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such check: %v", err)
	}
	// The administrator's command line may keep a long description, as an import.
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	if _, err := f.s.UpdateCheck(ctx, admin, id, Change{Description: ptr(strings.Repeat("x", MaxDescription+1))}, t0); err != nil {
		t.Errorf("from the command line: %v", err)
	}
}

// Case 3 and 4 of the spec, through the store: what a changed schedule does to a
// nightly job, and that nothing goes down for the change itself.
func TestAChangedScheduleCountsFromNow(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "nightly", Cron: "0 3 * * *", TimeZone: "UTC", Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	yesterday := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", yesterday)); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 4, 50, 0, 0, time.UTC)
	// It is late already, as the 03:00 of today did not come.
	if _, err := f.s.ProcessDue(ctx, time.Date(2026, 10, 21, 3, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.State != "late" {
		t.Fatalf("%s", got.State)
	}
	if _, err := f.s.UpdateCheck(ctx, f.editor, c.ID, Change{Cron: ptr("0 5 * * *"), TimeZone: ptr("UTC")}, now); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, c.ID)
	want := time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)
	if got.State != "up" || got.StateReason != check.ScheduleChanged || !msTime(got.NextExpectedAt).Equal(want) {
		t.Errorf("%s: %q, next %v", got.State, got.StateReason, msTime(got.NextExpectedAt))
	}
	// No ping by then is a true alarm.
	for _, at := range []time.Time{want, want.Add(time.Hour)} {
		if _, err := f.s.ProcessDue(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.get(t, c.ID); got.State != "down" {
		t.Errorf("after the new time and its grace: %s", got.State)
	}
}

// Case 5: a grace cut while a run is open lets the run go, without an alert.
func TestAGraceCutWithARunOpen(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "backup", Interval: 24 * time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0)); err != nil {
		t.Fatal(err)
	}
	start := t0.Add(time.Hour)
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Start, "", start)); err != nil {
		t.Fatal(err)
	}
	now := start.Add(40 * time.Minute)
	if _, err := f.s.UpdateCheck(ctx, f.editor, c.ID, Change{Grace: ptr(30 * time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ProcessDue(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.State != "up" {
		t.Errorf("%s: %s", got.State, got.StateReason)
	}
	// When it ends later, its time is still recorded.
	end := now.Add(time.Hour)
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", end)); err != nil {
		t.Fatal(err)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 1})
	if len(runs) != 1 || !msTime(runs[0].EndedAt).Equal(end) || !msTime(runs[0].StartedAt).Equal(start) {
		t.Errorf("%+v", runs)
	}
}

// A schedule that cannot be read is what a form must be able to fix.
func TestFixingAnUnreadableSchedule(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id, uuid := f.pingCheck(t)
	if _, err := f.s.Ping(ctx, ping(uuid, check.Success, "", t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET schedule_kind = 'cron', cron_expression = 'nonsense', time_zone = 'UTC', interval_seconds = NULL WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(uuid, check.Success, "", t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, id); got.State != "unknown" {
		t.Fatalf("%s", got.State)
	}
	now := t0.Add(time.Hour)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Interval: ptr(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, id)
	if got.State != "up" || !msTime(got.Deadline).Equal(now.Add(time.Hour)) {
		t.Errorf("%s, deadline %v", got.State, msTime(got.Deadline))
	}
}

// R11: failures from before a change of address do not count, nor does an attempt
// that was running when it changed.
func TestAChangedAddressStartsOver(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id := f.httpCheck(t)
	f.attempt(t, id, check.Passed, t0)
	f.attempt(t, id, check.Failed, t0.Add(time.Minute))
	if c := f.get(t, id); c.FailStreak != 1 {
		t.Fatalf("streak %d", c.FailStreak)
	}
	old := revisionOf(f.get(t, id).Settings)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Name: ptr("renamed")}, t0.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 1 {
		t.Errorf("a rename reset the failures: %d", c.FailStreak)
	}
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Settings: map[string]any{"url": "https://shop.example.com/other"}}, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 0 {
		t.Errorf("a new address kept the failures: %d", c.FailStreak)
	}
	at := t0.Add(3 * time.Minute)
	if _, err := f.s.RecordAttempts(ctx, []AttemptOf{{WorkspaceID: f.ws, CheckID: id, Attempt: Attempt{
		Attempt: check.Attempt{Outcome: check.Failed, Reason: "HTTP 503", At: at}, Started: at, Revision: old,
	}}}); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 0 {
		t.Errorf("an attempt from before the change counted: %d", c.FailStreak)
	}
	// A rename leaves the settings alone, so an attempt under them still counts.
	current := revisionOf(f.get(t, id).Settings)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Name: ptr("again")}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RecordAttempts(ctx, []AttemptOf{{WorkspaceID: f.ws, CheckID: id, Attempt: Attempt{
		Attempt: check.Attempt{Outcome: check.Failed, Reason: "HTTP 503", At: at}, Started: at, Revision: current,
	}}}); err != nil {
		t.Fatal(err)
	}
	if c := f.get(t, id); c.FailStreak != 1 {
		t.Errorf("an attempt across a rename: %d", c.FailStreak)
	}
}

// The same on a probe, which reads its checks every 30 seconds: a run made under the
// old address after the change does not count.
func TestAProbesRunUnderOldSettingsDoesNotCount(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, id := aProbe(t, f)
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	checks, err := f.s.ChecksOfProbe(ctx, p.ID)
	if err != nil || len(checks) != 1 || checks[0].Revision == "" {
		t.Fatalf("%+v, %v", checks, err)
	}
	old := checks[0].Revision
	if err := f.s.SetActiveCheckOn(ctx, admin, id, time.Minute, map[string]any{"url": "http://10.0.0.6/", "fail_after": 1}, p.ID, t0); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "a", CheckID: id, Revision: old, Outcome: check.Failed, Reason: "HTTP 503"}}, t0.Add(time.Second), t0.Add(time.Second))
	if err != nil || r.Ignored != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
	checks, _ = f.s.ChecksOfProbe(ctx, p.ID)
	if checks[0].Revision == old {
		t.Fatal("the revision did not change")
	}
	r, err = f.s.ApplyProbeRuns(ctx, p, []ProbeRun{{ID: "b", CheckID: id, Revision: checks[0].Revision, Outcome: check.Passed}}, t0.Add(time.Minute), t0.Add(time.Minute))
	if err != nil || r.Applied != 1 {
		t.Errorf("under the new settings: %+v, %v", r, err)
	}
}

// New settings drop the attempt in flight, so the check is given until the next one,
// and a rename, which drops nothing, leaves its clock alone.
func TestAChangeGivesACheckItsNextAttempt(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id := f.httpCheck(t)
	f.attempt(t, id, check.Passed, t0)
	before := f.get(t, id)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Name: ptr("renamed")}, t0.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if after := f.get(t, id); after.Deadline != before.Deadline || after.GraceSeconds != before.GraceSeconds {
		t.Errorf("a rename moved it: %+v", after)
	}
	at := t0.Add(40 * time.Second)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Settings: map[string]any{"url": "https://shop.example.com/other"}}, at); err != nil {
		t.Fatal(err)
	}
	if after := f.get(t, id); !msTime(after.Deadline).Equal(at.Add(time.Minute + 2*time.Minute)) {
		t.Errorf("deadline %v", msTime(after.Deadline))
	}
}

func TestPausingAndResuming(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id := f.httpCheck(t)
	f.attempt(t, id, check.Passed, t0)
	f.attempt(t, id, check.Failed, t0.Add(time.Minute))
	f.attempt(t, id, check.Failed, t0.Add(2*time.Minute))
	if open, _ := db.New(f.s.r).OpenIncidentOfCheck(ctx, id); open.ID == "" {
		t.Fatal("no incident")
	}
	if err := f.s.PauseCheck(ctx, f.editor, id, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	c := f.get(t, id)
	if c.State != "paused" || c.Deadline.Valid {
		t.Errorf("%+v", c)
	}
	if open, err := db.New(f.s.r).OpenIncidentOfCheck(ctx, id); err == nil {
		t.Errorf("the incident stayed open: %+v", open)
	}
	// It is not run while paused.
	if checks, _ := f.s.ActiveChecks(ctx); len(checks) != 0 {
		t.Errorf("%+v", checks)
	}
	if err := f.s.ResumeCheck(ctx, f.editor, id, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	c = f.get(t, id)
	if c.State != "new" || c.Deadline.Valid || c.EverPassed != 1 {
		t.Errorf("%+v", c)
	}
	if checks, _ := f.s.ActiveChecks(ctx); len(checks) != 1 {
		t.Errorf("not run again: %+v", checks)
	}
	// It worked before, so failing alerts as usual.
	f.attempt(t, id, check.Failed, t0.Add(5*time.Minute))
	f.attempt(t, id, check.Failed, t0.Add(6*time.Minute))
	if c := f.get(t, id); c.State != "down" {
		t.Errorf("%s", c.State)
	}
}

// R16: gone from every list at once, with what it fetched; tidy takes the rest, and
// leaves the others alone.
func TestDeletingACheck(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id, uuid := f.pingCheck(t)
	keep, keepUUID := f.pingCheck(t)
	for i := range 3 {
		at := t0.Add(time.Duration(i) * time.Minute)
		if _, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", at)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Ping(ctx, ping(keepUUID, check.Success, "", at)); err != nil {
			t.Fatal(err)
		}
	}
	site := f.httpCheck(t)
	if _, err := f.s.UpdateCheck(ctx, f.editor, site, Change{Settings: map[string]any{"url": "https://shop.example.com/health?token=s3cr3t"}}, t0); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteCheck(ctx, f.editor, site, t0); err != nil {
		t.Fatal(err)
	}
	var settings string
	if err := f.s.r.QueryRow("SELECT settings FROM checks WHERE id = ?", site).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(settings, "s3cr3t") {
		t.Errorf("its token stayed: %s", settings)
	}
	if err := f.s.DeleteCheck(ctx, f.editor, id, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Check(ctx, f.editor, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("still there: %v", err)
	}
	if views, _ := f.s.Checks(ctx, f.editor); len(views) != 1 || views[0].ID != keep {
		t.Errorf("listed: %+v", views)
	}
	if _, err := f.s.Ping(ctx, ping(uuid, check.Success, "", t0.Add(2*time.Hour))); !errors.Is(err, ErrNotFound) {
		t.Errorf("its ping URL: %v", err)
	}
	if err := f.s.DeleteCheck(ctx, f.editor, id, t0.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	var reason string
	if err := f.s.r.QueryRow("SELECT resolve_reason FROM incidents WHERE cause_check_id = ?", id).Scan(&reason); err != nil || reason != "deleted" {
		t.Errorf("its incident: %q, %v", reason, err)
	}
	count := func(q, id string) int {
		t.Helper()
		var n int
		if err := f.s.r.QueryRow(q, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if more, err := f.s.Tidy(ctx, t0); err != nil || more {
		t.Fatalf("tidy: %v, %v", more, err)
	}
	for _, q := range []string{"SELECT count(*) FROM runs WHERE check_id = ?", "SELECT count(*) FROM pings WHERE check_id = ?", "SELECT count(*) FROM checks WHERE id = ?"} {
		if n := count(q, id); n != 0 {
			t.Errorf("%s: %d left", q, n)
		}
		if n := count(q, keep); n == 0 {
			t.Errorf("%s: the other check lost it", q)
		}
	}
	// Past incidents keep its name.
	if n := count("SELECT count(*) FROM incident_checks WHERE check_id = ? AND check_name = 'nightly'", id); n != 1 {
		t.Errorf("the incident lost its check: %d", n)
	}
	// Its slug and ping URL are free for a new one, as for an import.
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "again", PingUUID: uuid, Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Errorf("its UUID again: %v", err)
	}
}

// R19: what would silence the sign of life of a probe others may rely on.
func TestAProbesOwnCheckIsProtected(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	p, _ := aProbe(t, f)
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	var invalid *InvalidError
	if err := f.s.DeleteCheck(ctx, admin, p.CheckID, t0); !errors.As(err, &invalid) {
		t.Errorf("deleted: %v", err)
	}
	if err := f.s.PauseCheck(ctx, admin, p.CheckID, t0); !errors.As(err, &invalid) {
		t.Errorf("paused: %v", err)
	}
	if _, err := f.s.UpdateCheck(ctx, admin, p.CheckID, Change{Interval: ptr(time.Hour)}, t0); !errors.As(err, &invalid) {
		t.Errorf("rescheduled: %v", err)
	}
	if err := f.s.SetAlertingAs(ctx, f.editor, p.CheckID, "none", false, t0); !errors.As(err, &invalid) {
		t.Errorf("silenced by an editor: %v", err)
	}
	if err := f.s.MoveCheckAs(ctx, f.editor, p.CheckID, f.project, t0); !errors.As(err, &invalid) {
		t.Errorf("moved by an editor: %v", err)
	}
	// Renaming it, and an administrator's decisions, stay possible.
	if _, err := f.s.UpdateCheck(ctx, f.editor, p.CheckID, Change{Name: ptr("Office probe")}, t0); err != nil {
		t.Errorf("renamed: %v", err)
	}
	if err := f.s.SetAlertingAs(ctx, admin, p.CheckID, "digest", false, t0); err != nil {
		t.Errorf("an administrator's choice: %v", err)
	}
	uuid, err := f.s.RotatePingURL(ctx, f.editor, p.CheckID, t0)
	if err != nil {
		t.Errorf("a new ping URL: %v", err)
	}
	// A probe's key finds its own check as it is at each post.
	p.PingUUID = uuid
	if _, err := f.s.ApplyProbeRuns(ctx, p, nil, t0, t0); err != nil {
		t.Errorf("a post after a new ping URL: %v", err)
	}
}

// C4: critical wakes people at night, so only an owner turns it on.
func TestWhoMayMakeACheckCritical(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	id, _ := f.pingCheck(t)
	var invalid *InvalidError
	if err := f.s.SetAlertingAs(ctx, f.editor, id, "now", true, t0); !errors.As(err, &invalid) || invalid.Field != "critical" {
		t.Errorf("an editor: %v", err)
	}
	owner, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Owner"}, []string{f.project}, Owner)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.s.AccessOfPerson(ctx, f.ws, owner)
	if err := f.s.SetAlertingAs(ctx, a, id, "now", true, t0); err != nil {
		t.Errorf("an owner: %v", err)
	}
	if c := f.get(t, id); c.Critical != 1 {
		t.Error("not critical")
	}
	// An editor can leave it on, and turn it off.
	if err := f.s.SetAlertingAs(ctx, f.editor, id, "digest", true, t0); err != nil {
		t.Errorf("leaving it on: %v", err)
	}
	if err := f.s.SetAlertingAs(ctx, f.editor, id, "now", false, t0); err != nil {
		t.Errorf("turning it off: %v", err)
	}
}

func TestAProjectHasRoomForSoManyChecks(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	site := NewCheck{Name: "site", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://shop.example.com/"}}
	for range MaxActiveChecks {
		if _, err := f.s.CreateCheck(ctx, admin, f.project, site); err != nil {
			t.Fatal(err)
		}
	}
	var invalid *InvalidError
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, site); !errors.As(err, &invalid) || invalid.Field != "project" {
		t.Errorf("one more by a person: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, admin, f.project, site); err != nil {
		t.Errorf("one more from the command line: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "job", Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Errorf("a ping check: %v", err)
	}
	// A move does not pass the limit.
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	if err := f.s.SetMember(ctx, Command("test"), f.ws, other, f.editor.PersonID, Editor); err != nil {
		t.Fatal(err)
	}
	f.editor, _ = f.s.AccessOfPerson(ctx, f.ws, f.editor.PersonID)
	admin, _ = f.s.AdminAccess(ctx, f.ws, "test")
	c, err := f.s.CreateCheck(ctx, admin, other, site)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.MoveCheckAs(ctx, f.editor, c.ID, f.project, t0); !errors.As(err, &invalid) || invalid.Field != "project" {
		t.Errorf("moved in: %v", err)
	}
}

// R35, as it reaches a person: no incident, and the digest says why.
func TestACheckThatHasNeverWorkedOpensNothing(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id := f.httpCheck(t)
	st := f.attempt(t, id, check.Failed, t0)
	st = f.attempt(t, id, check.Failed, t0.Add(time.Minute))
	st = f.attempt(t, id, check.Failed, t0.Add(2*time.Minute))
	if st.State != check.New || st.Reason != "HTTP 503" {
		t.Fatalf("%+v", st)
	}
	if open, err := db.New(f.s.r).OpenIncidentOfCheck(ctx, id); err == nil {
		t.Errorf("an incident: %+v", open)
	}
	_, listed, err := f.s.DigestOf(ctx, f.editor.PersonID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Reason != "HTTP 503" {
		t.Errorf("%+v", listed)
	}
}

// Nothing running today may go quiet: the migration marks as having passed every
// check but a new one that no run of ever did.
func TestWhichChecksCountAsHavingPassed(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	raw, err := migrations.ReadFile("migrations/0010_ever_passed.sql")
	if err != nil {
		t.Fatal(err)
	}
	update := string(raw)[strings.Index(string(raw), "UPDATE checks"):]
	newNever := f.httpCheck(t)
	newPassed := f.httpCheck(t)
	f.attempt(t, newPassed, check.Passed, t0)
	down := f.httpCheck(t)
	unknown := f.httpCheck(t)
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'down' WHERE id = ?", down); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'unknown' WHERE id = ?", unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'new', ever_passed = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'down' WHERE id = ?", down); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'unknown' WHERE id = ?", unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.ExecContext(ctx, update); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int64{newNever: 0, newPassed: 1, down: 1, unknown: 1} {
		if got := f.get(t, id).EverPassed; got != want {
			t.Errorf("%s: %d, want %d", f.get(t, id).State, got, want)
		}
	}
}

// A check that was down when its schedule stopped being readable is still down
// when it is fixed: fixing it must not read as a recovery.
func TestFixingAScheduleDoesNotRecoverAnOutage(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id, uuid := f.pingCheck(t)
	if _, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.w.Exec("UPDATE checks SET schedule_kind = 'cron', cron_expression = 'nonsense', time_zone = 'UTC', interval_seconds = NULL WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(uuid, check.Fail, "", t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, id); got.State != "unknown" {
		t.Fatalf("%s", got.State)
	}
	now := t0.Add(time.Hour)
	if _, err := f.s.UpdateCheck(ctx, f.editor, id, Change{Interval: ptr(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	got := f.get(t, id)
	if got.State != "down" {
		t.Errorf("%s: %q", got.State, got.StateReason)
	}
	open, err := db.New(f.s.r).OpenIncidentOfCheck(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if open.RecoveringSince.Valid {
		t.Error("the incident started to recover")
	}
	if n, err := f.s.SettleIncidents(ctx, now.Add(time.Hour), 10*time.Minute); err != nil || n != 0 {
		t.Errorf("settled %d, %v", n, err)
	}
}

// A pause of a check that never alerts is nobody's reminder.
func TestTheDigestListsPausedChecksThatAlert(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	loud, _ := f.pingCheck(t)
	silent, _ := f.pingCheck(t)
	if err := f.s.SetAlerting(ctx, Command("test"), f.ws, silent, "none", false, t0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{loud, silent} {
		if err := f.s.PauseCheck(ctx, f.editor, id, t0); err != nil {
			t.Fatal(err)
		}
	}
	_, listed, err := f.s.DigestOf(ctx, f.editor.PersonID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].State != "paused" {
		t.Errorf("%+v", listed)
	}
}
