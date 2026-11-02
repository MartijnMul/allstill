// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

func TestReadsStayWithinAccess(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Nightly backup", Tags: []string{"prod"}, Cron: "0 3 * * *", TimeZone: "Europe/Amsterdam", Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Elsewhere")
	admin, _ := f.s.AdminAccess(ctx, f.ws, "test")
	hidden, err := f.s.CreateCheck(ctx, admin, other, NewCheck{Name: "Hidden", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	start := ping(c.PingUUID, check.Start, "", t0)
	f.s.Ping(ctx, start)
	end := ping(c.PingUUID, check.Success, "", t0.Add(40*time.Minute))
	end.ExitStatus, end.Body, end.Values = 0, []byte("uploaded 1284 files\n"), map[string]float64{"size": 18.4e9}
	f.s.Ping(ctx, end)
	f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", t0.Add(24*time.Hour)))

	views, err := f.s.Checks(ctx, f.viewer)
	if err != nil || len(views) != 1 {
		t.Fatalf("the viewer sees %d checks: %v", len(views), err)
	}
	v := views[0]
	if v.ID != c.ID || v.ProjectName != "Backups" || v.Tags[0] != "prod" || v.Cron != "0 3 * * *" || v.Status.State != check.Down {
		t.Fatalf("%+v", v)
	}
	if len(v.Recent) != 2 || v.Recent[0].Outcome != "fail" || v.Recent[1].Duration() != 40*time.Minute ||
		v.Recent[1].ExitStatus != 0 || v.Recent[1].Values["size"] != 18.4e9 || v.Recent[0].ExitStatus != -1 {
		t.Fatalf("runs %+v", v.Recent)
	}

	if _, err := f.s.Check(ctx, f.viewer, hidden.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the viewer reads a check of another project: %v", err)
	}
	if got, _ := f.s.Check(ctx, admin, hidden.ID); got.ProjectName != "Elsewhere" {
		t.Fatalf("the administrator reads %+v", got)
	}
	log, err := f.s.RunLog(ctx, f.viewer, c.ID, v.Recent[1].ID)
	if err != nil || len(log) != 2 || log[0].Kind != "start" || string(log[1].Body) != "uploaded 1284 files\n" || log[1].ExitStatus != 0 {
		t.Fatalf("log %+v: %v", log, err)
	}
	if _, err := f.s.RunLog(ctx, f.viewer, hidden.ID, v.Recent[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run log through another project's check: %v", err)
	}
	if _, err := f.s.RunLog(ctx, admin, hidden.ID, v.Recent[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run of one check read through another: %v", err)
	}
}

func TestRecentRunsAreTheLatest(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Heartbeat", Interval: time.Minute, Grace: time.Minute})
	for i := range RecentRuns + 5 {
		f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(time.Duration(i)*time.Minute)))
	}
	v, err := f.s.Check(ctx, f.viewer, c.ID)
	if err != nil || len(v.Recent) != RecentRuns || !v.Recent[0].Ended.Equal(t0.Add((RecentRuns+4)*time.Minute)) {
		t.Fatalf("%d runs, newest %v: %v", len(v.Recent), v.Recent[0].Ended, err)
	}
}

func TestDownsGoBackAsFarAsTheRuns(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Heartbeat", Interval: time.Hour, Grace: time.Hour})
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", at(0)))
	f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", at(5)))
	f.s.SettleIncidents(ctx, at(25), 10*time.Minute)
	f.s.Ping(ctx, ping(c.PingUUID, check.Fail, "", at(30)))
	v, err := f.s.Check(ctx, f.viewer, c.ID)
	if err != nil || len(v.Downs) != 2 || v.Downs[0] != (Down{At: at(30)}) || v.Downs[1] != (Down{At: at(0), Resolved: at(15)}) {
		t.Fatalf("downs %v: %v", v.Downs, err)
	}
	// Once the runs shown no longer reach back to a down, it is left out too.
	for i := range RecentRuns {
		f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", at(31+i)))
	}
	v, err = f.s.Check(ctx, f.viewer, c.ID)
	if err != nil || len(v.Downs) != 0 {
		t.Fatalf("downs %v: %v", v.Downs, err)
	}
}
