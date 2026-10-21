// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

func TestTidyKeepsTheNewestHistory(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	f.s.Retention = Retention{Runs: 30, Output: 10}

	// More checks than one call looks at, so a round takes several calls.
	var uuids []string
	for i := range tidyChecks + 5 {
		c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: fmt.Sprint("job ", i), Interval: time.Hour, Grace: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		uuids = append(uuids, c.PingUUID)
	}
	busy := uuids[3]
	for i := range 45 {
		at := t0.Add(time.Duration(i) * time.Hour)
		f.s.Ping(ctx, ping(busy, check.Start, "", at))
		end := ping(busy, check.Success, "", at.Add(time.Minute))
		end.Body = []byte(fmt.Sprint("run ", i))
		f.s.Ping(ctx, end)
	}
	for _, u := range uuids[:2] {
		f.s.Ping(ctx, ping(u, check.Success, "", t0))
	}
	var busyID string
	f.s.r.QueryRow("SELECT id FROM checks WHERE ping_uuid = ?", busy).Scan(&busyID)
	// Pings outside any run, and events old and new.
	_, err := f.s.w.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 40)
		INSERT INTO pings (workspace_id, check_id, kind, received_at, method, body) SELECT ?, ?, 'ignored', i, 'GET', x'78' FROM n`, f.ws, busyID)
	if err != nil {
		t.Fatal(err)
	}
	// A run older than all others that still waits for its end.
	f.s.w.Exec(`INSERT INTO runs (id, workspace_id, check_id, outcome, started_at) VALUES ('run_0', ?, ?, 'running', 1)`, f.ws, busyID)
	now := t0.Add(30 * 24 * time.Hour)
	f.s.w.Exec(`INSERT INTO events (workspace_id, type, subject_id, created_at) VALUES (?, 'old', 'x', ?), (?, 'new', 'x', ?)`,
		f.ws, now.Add(-EventLifetime-time.Second).UnixMilli(), f.ws, now.Add(-time.Hour).UnixMilli())

	calls := 0
	for more := true; more; calls++ {
		if more, err = f.s.Tidy(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("a round of %d checks took %d calls", tidyChecks+5, calls)
	}

	var runs, withOutput, loose, looseWithOutput, events int
	var oldest string
	f.s.r.QueryRow("SELECT count(*), min(started_at) FROM runs WHERE check_id = ? AND outcome <> 'running'", busyID).Scan(&runs, &oldest)
	var open int
	f.s.r.QueryRow("SELECT count(*) FROM runs WHERE id = 'run_0'").Scan(&open)
	if open != 1 {
		t.Error("an open run was removed")
	}
	f.s.r.QueryRow("SELECT count(*) FROM pings WHERE check_id = ? AND run_id IS NULL AND body IS NOT NULL", busyID).Scan(&looseWithOutput)
	if looseWithOutput != 10 {
		t.Errorf("%d loose pings kept their output", looseWithOutput)
	}
	f.s.r.QueryRow(`SELECT count(DISTINCT run_id) FROM pings WHERE check_id = ? AND run_id IS NOT NULL AND body IS NOT NULL`, busyID).Scan(&withOutput)
	f.s.r.QueryRow("SELECT count(*) FROM pings WHERE check_id = ? AND run_id IS NULL", busyID).Scan(&loose)
	var kept string
	f.s.r.QueryRow("SELECT count(*), group_concat(type) FROM events WHERE type IN ('old', 'new')").Scan(&events, &kept)
	// The newest 30 are kept, and the open run besides.
	if runs != 30 || oldest != fmt.Sprint(t0.Add(15*time.Hour).UnixMilli()) {
		t.Errorf("%d runs kept, the oldest started %s", runs, oldest)
	}
	if withOutput != 10 {
		t.Errorf("%d runs kept their output", withOutput)
	}
	if loose != 30 {
		t.Errorf("%d loose pings kept", loose)
	}
	if events != 1 || kept != "new" {
		t.Errorf("events kept: %s", kept)
	}
	v, _ := f.s.Check(ctx, f.viewer, busyID)
	if log, _ := f.s.RunLog(ctx, f.viewer, busyID, v.Recent[0].ID); string(log[1].Body) != "run 44" {
		t.Errorf("the newest run's output: %+v", log)
	}
	var others int
	f.s.r.QueryRow("SELECT count(*) FROM runs WHERE check_id <> ?", busyID).Scan(&others)
	if others != 2 {
		t.Errorf("other checks kept %d runs", others)
	}
}

func TestRetentionKeepsEnoughForCharts(t *testing.T) {
	for _, r := range []Retention{{Runs: 10, Output: 5}, {Runs: 100, Output: 200}, {Runs: 100, Output: -1}} {
		if r.Check() == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	if err := DefaultRetention.Check(); err != nil {
		t.Error(err)
	}
}
