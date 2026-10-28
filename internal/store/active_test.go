// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

func (f fixture) httpCheck(t *testing.T) string {
	t.Helper()
	c, err := f.s.CreateCheck(t.Context(), f.editor, f.project, NewCheck{
		Name: "shop", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://shop.example.com/health"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (f fixture) attempt(t *testing.T, checkID string, o check.Outcome, at time.Time) check.Status {
	t.Helper()
	s, err := f.s.RecordAttempt(t.Context(), f.ws, checkID, Attempt{
		Attempt: check.Attempt{Outcome: o, Reason: "HTTP 503", At: at}, Started: at.Add(-200 * time.Millisecond),
		Measured: map[string]float64{"status": 503, "ms": 200},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An HTTP check goes down after two failures, and an attempt that cannot
// judge it holds its incident open instead of letting it settle
// (intent/http-checks/spec.md).
func TestAnActiveChecksIncident(t *testing.T) {
	f := setup(t)
	id := f.httpCheck(t)
	if c := f.get(t, id); c.GraceSeconds != 120 || c.Kind != "http" {
		t.Fatalf("created: grace %d, kind %s", c.GraceSeconds, c.Kind)
	}
	f.attempt(t, id, check.Passed, t0)
	f.attempt(t, id, check.Failed, t0.Add(time.Minute))
	if len(f.incidents(t)) != 0 {
		t.Fatal("an incident after one failure")
	}
	if s := f.attempt(t, id, check.Failed, t0.Add(2*time.Minute)); s.State != check.Down || s.FailStreak != 2 {
		t.Fatalf("after two failures: %+v", s)
	}
	if is := f.incidents(t); len(is) != 1 || is[0].State != "open" {
		t.Fatalf("incidents %+v", is)
	}
	// Back up: it starts to settle; then unjudged: it stops, and stays open.
	f.attempt(t, id, check.Passed, t0.Add(3*time.Minute))
	if is := f.incidents(t); !is[0].RecoveringSince.Valid {
		t.Fatal("not settling after it came back")
	}
	f.attempt(t, id, check.Unjudged, t0.Add(4*time.Minute))
	if is := f.incidents(t); is[0].RecoveringSince.Valid {
		t.Fatal("still settling while it cannot be judged")
	}
	if n, _ := f.s.SettleIncidents(t.Context(), t0.Add(time.Hour), 10*time.Minute); n != 0 {
		t.Fatal("an incident settled while its check could not be judged")
	}
	// The runs: one per judged attempt, with their reason.
	var runs int
	var reason string
	f.s.r.QueryRow("SELECT count(*), max(reason) FROM runs WHERE check_id = ?", id).Scan(&runs, &reason)
	if runs != 4 || reason != "HTTP 503" {
		t.Fatalf("%d runs, reason %q", runs, reason)
	}
	// A ping to it is not applied.
	uuid := f.get(t, id).PingUuid
	if _, err := f.s.Ping(t.Context(), ping(uuid, check.Success, "", t0.Add(5*time.Minute))); !errors.Is(err, check.ErrActive) {
		t.Fatalf("a ping to an HTTP check: %v", err)
	}
	// Nor when its settings cannot be read.
	if _, err := f.s.w.Exec("UPDATE checks SET settings = '{\"fail_after\":\"3\"}' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(t.Context(), ping(uuid, check.Success, "", t0.Add(6*time.Minute))); !errors.Is(err, check.ErrActive) {
		t.Fatalf("a ping to an HTTP check with unreadable settings: %v", err)
	}
	if _, err := f.s.CreateCheck(t.Context(), f.editor, f.project, NewCheck{Name: "x", Kind: "dns", Interval: time.Minute, Grace: time.Minute}); err == nil {
		t.Error("an unknown kind was taken")
	}
	for _, v := range []any{"3", 2.5, 0, -1, 101} {
		var invalid *InvalidError
		_, err := f.s.CreateCheck(t.Context(), f.editor, f.project, NewCheck{Name: "x", Kind: "http", Interval: time.Minute, Settings: map[string]any{"fail_after": v}})
		if !errors.As(err, &invalid) || invalid.Field != "fail_after" {
			t.Errorf("fail_after %v: %v", v, err)
		}
	}
}

// Migration 0007 rebuilds the checks table under a database in use: every
// check, run, ping and incident stays, and kinds are no longer limited by it.
func TestMigrationRebuildsChecks(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "allstill.db")
	upTo6 := fstest.MapFS{}
	ms, err := list(migrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.version > 6 {
			continue
		}
		body, _ := fs.ReadFile(migrations, m.name)
		upTo6[m.name] = &fstest.MapFile{Data: body}
	}
	s, err := openWith(ctx, path, upTo6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`
		INSERT INTO workspaces (id, name, created_at) VALUES ('ws_a', 'A', 0);
		INSERT INTO projects (id, workspace_id, name, created_at) VALUES ('prj_a', 'ws_a', 'A', 0);
		INSERT INTO checks (id, workspace_id, project_id, ping_uuid, name, kind, schedule_kind,
			interval_seconds, grace_seconds, state, state_since, created_at, updated_at, urgency, critical)
			VALUES ('chk_a', 'ws_a', 'prj_a', '5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e', 'backup', 'ping', 'interval', 60, 60,
				'down', 5, 0, 0, 'digest', 1);
		INSERT INTO runs (id, workspace_id, check_id, outcome, ended_at) VALUES ('run_a', 'ws_a', 'chk_a', 'fail', 1);
		INSERT INTO pings (workspace_id, check_id, run_id, kind, received_at, method) VALUES ('ws_a', 'chk_a', 'run_a', 'fail', 1, 'GET');
		INSERT INTO incidents (id, workspace_id, title, state, urgency, opened_at) VALUES ('inc_a', 'ws_a', 'backup is down', 'open', 'digest', 5);
		INSERT INTO incident_checks (workspace_id, incident_id, check_id, check_name, project_id, joined_at)
			VALUES ('ws_a', 'inc_a', 'chk_a', 'backup', 'prj_a', 5);`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state, urgency string
	var critical, streak int
	s.r.QueryRow("SELECT state, urgency, critical, fail_streak FROM checks WHERE id = 'chk_a'").Scan(&state, &urgency, &critical, &streak)
	if state != "down" || urgency != "digest" || critical != 1 || streak != 0 {
		t.Fatalf("the check after the rebuild: %s %s %d %d", state, urgency, critical, streak)
	}
	for table, want := range map[string]int{"runs": 1, "pings": 1, "incident_checks": 1} {
		if n := count(t, s, table); n != want {
			t.Errorf("%s: %d rows, want %d", table, n, want)
		}
	}
	if _, err := s.w.Exec("UPDATE checks SET kind = 'tcp' WHERE id = 'chk_a'"); err != nil {
		t.Errorf("a new kind is still refused by the table: %v", err)
	}
	if _, err := s.w.Exec("DELETE FROM checks WHERE id = 'chk_a'"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "runs"); n != 0 {
		t.Errorf("deleting the check left %d runs: its foreign keys did not survive the rebuild", n)
	}
}

// An active check that never ran can never alert, but one that cannot be
// judged is not listed as such in the digest; an attempt for a paused check
// is neither judged nor kept.
func TestAnUnjudgedActiveCheckIsNotSilent(t *testing.T) {
	f := setup(t)
	id := f.httpCheck(t)
	ed := f.editor.PersonID
	_, silent, err := f.s.DigestOf(t.Context(), ed, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(silent) != 1 {
		t.Errorf("an HTTP check that never ran: %+v", silent)
	}
	f.attempt(t, id, check.Unjudged, t0)
	if _, silent, err = f.s.DigestOf(t.Context(), ed, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(silent) != 0 {
		t.Errorf("listed as never alerting: %+v", silent)
	}
	// Paused while an attempt ran (pausing has no store call yet).
	if _, err := f.s.w.Exec("UPDATE checks SET state = 'paused' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	if st := f.attempt(t, id, check.Failed, t0.Add(time.Minute)); st.State != check.Paused {
		t.Errorf("a paused check was judged: %+v", st)
	}
	var runs int
	f.s.r.QueryRow("SELECT count(*) FROM runs WHERE check_id = ?", id).Scan(&runs)
	if runs != 0 {
		t.Errorf("%d runs kept for a paused check", runs)
	}
}
