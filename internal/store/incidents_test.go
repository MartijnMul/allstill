// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

func (f fixture) incidents(t *testing.T) []db.Incident {
	t.Helper()
	rows, err := f.s.r.Query("SELECT id, state, urgency, recovering_since, resolve_reason FROM incidents ORDER BY opened_at, id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.Incident
	for rows.Next() {
		var i db.Incident
		rows.Scan(&i.ID, &i.State, &i.Urgency, &i.RecoveringSince, &i.ResolveReason)
		out = append(out, i)
	}
	return out
}

func (f fixture) recipients(t *testing.T) int {
	t.Helper()
	var n int
	f.s.r.QueryRow("SELECT count(*) FROM recipients").Scan(&n)
	return n
}

func TestADownCheckOpensOneIncident(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	fail := func(at time.Time) {
		t.Helper()
		if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", at)); err != nil {
			t.Fatal(err)
		}
	}
	fail(t0)
	fail(t0.Add(time.Minute)) // still down: no second incident
	got := f.incidents(t)
	if len(got) != 1 || got[0].State != "open" || got[0].Urgency != "now" {
		t.Fatalf("incidents %+v", got)
	}
	// The editor gets alerts, the viewer does not.
	if n := f.recipients(t); n != 1 {
		t.Fatalf("%d recipients", n)
	}

	// Up starts the settle period; down again ends it, in the same incident.
	f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(2*time.Minute)))
	if got := f.incidents(t); !got[0].RecoveringSince.Valid {
		t.Fatalf("not recovering: %+v", got[0])
	}
	fail(t0.Add(3 * time.Minute))
	if got := f.incidents(t); len(got) != 1 || got[0].RecoveringSince.Valid {
		t.Fatalf("after down again: %+v", got)
	}

	// Settled, it resolves; the next failure opens a new incident.
	f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(4*time.Minute)))
	if n, err := f.s.SettleIncidents(ctx, t0.Add(14*time.Minute), 10*time.Minute); err != nil || n != 1 {
		t.Fatalf("settled %d, %v", n, err)
	}
	fail(t0.Add(20 * time.Minute))
	got = f.incidents(t)
	if len(got) != 2 || got[0].ResolveReason.String != "recovered" || got[1].State != "open" {
		t.Fatalf("incidents %+v", got)
	}
}

func TestAPausedCheckResolvesItsIncident(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Backup", Interval: time.Hour, Grace: time.Hour})
	f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", t0))
	// Pausing does not exist yet; the state change is what counts.
	tx, _ := f.s.w.Begin()
	q := db.New(tx)
	row := f.get(t, c.ID)
	before := statusOf(row)
	after := before
	after.State, after.Since = check.Paused, t0.Add(time.Minute)
	if err := saveStatus(ctx, q, row, before, after); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if got := f.incidents(t); got[0].State != "resolved" || got[0].ResolveReason.String != "paused" {
		t.Fatalf("incidents %+v", got)
	}
}

// Its followers are recorded, since its urgency may change while it is open;
// the sender tells them nothing while it stays none.
func TestANoneCheckAlertsNobody(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Laptop", Interval: time.Hour, Grace: time.Hour})
	f.s.w.Exec("UPDATE checks SET urgency = 'none' WHERE id = ?", c.ID)
	f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", t0))
	if got := f.incidents(t); len(got) != 1 || got[0].Urgency != "none" || f.recipients(t) != 1 {
		t.Fatalf("incidents %+v, %d recipients", got, f.recipients(t))
	}
}

func TestNewPeopleGetQuietNights(t *testing.T) {
	f := setup(t)
	var quiet string
	f.s.r.QueryRow("SELECT quiet_hours FROM people WHERE id = ?", f.editor.PersonID).Scan(&quiet)
	if quiet != DefaultQuietHours("UTC") {
		t.Fatalf("quiet hours %s", quiet)
	}
}
