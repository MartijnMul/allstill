// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// One operator runs checks for their own company, a customer and a home lab.
// A colleague works on the customer's project only.
func TestAccessFollowsMembership(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, err := s.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Workspace(ctx); again != ws {
		t.Fatalf("a second workspace: %s, %s", ws, again)
	}
	operator, _ := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "Operator", WorkspaceAdmin: true})
	colleague, _ := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "Colleague", Email: "colleague@example.com"})
	own, _, _ := s.CreateProject(ctx, Command("test"), ws, "Own company")
	customer, key, err := s.CreateProject(ctx, Command("test"), ws, "Customer")
	if err != nil || len(key) != 22 {
		t.Fatalf("project: %v, key %q", err, key)
	}
	s.CreateProject(ctx, Command("test"), ws, "Home lab")
	if err := s.SetMember(ctx, Command("test"), ws, customer, colleague, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember(ctx, Command("test"), ws, customer, colleague, Editor); err != nil {
		t.Fatal(err)
	}

	a, err := s.AccessOfPerson(ctx, ws, colleague)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Projects) != 1 || a.Projects[0].ID != customer || a.Projects[0].Role != Editor {
		t.Fatalf("colleague sees %+v", a.Projects)
	}
	if _, ok := a.Role(own); ok {
		t.Fatal("colleague may see the operator's own project")
	}

	a, _ = s.AccessOfPerson(ctx, ws, operator)
	if len(a.Projects) != 3 || !a.Admin {
		t.Fatalf("the administrator sees %+v", a.Projects)
	}
	if r, _ := a.Role(customer); r != Owner {
		t.Fatalf("the administrator is %s, not owner", r)
	}

	if _, err := s.AccessOfPerson(ctx, "ws_other", colleague); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a person seen from another workspace: %v", err)
	}
	s.w.Exec("UPDATE people SET disabled_at = 1 WHERE id = ?", colleague)
	if _, err := s.AccessOfPerson(ctx, ws, colleague); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a disabled person: %v", err)
	}
	s.w.Exec("UPDATE projects SET deleted_at = 1 WHERE id = ?", own)
	if a, _ := s.AccessOfPerson(ctx, ws, operator); len(a.Projects) != 2 {
		t.Fatalf("a deleted project is still visible: %+v", a.Projects)
	}
}

func TestOneWorkspaceUnderConcurrency(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Go(func() {
			var err error
			if ids[i], err = s.Workspace(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var n int
	s.r.QueryRow("SELECT count(*) FROM workspaces").Scan(&n)
	if n != 1 || ids[0] != ids[7] {
		t.Fatalf("%d workspaces: %v", n, ids)
	}
}

func TestAnAdministratorIsOwnerEvenAsAMember(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	admin, _ := s.CreatePerson(ctx, Command("test"), ws, NewPerson{Name: "Admin", WorkspaceAdmin: true})
	p, _, _ := s.CreateProject(ctx, Command("test"), ws, "P")
	s.SetMember(ctx, Command("test"), ws, p, admin, Viewer)
	a, _ := s.AccessOfPerson(ctx, ws, admin)
	if r, _ := a.Role(p); r != Owner {
		t.Fatalf("role %s", r)
	}
}

func TestMoveCheckKeepsItsHistory(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	c, err := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Printer", Slug: "printer", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0)); err != nil {
		t.Fatal(err)
	}
	other, key, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Customer")
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, c.ID, other, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, c.ID); got.ProjectID != other || got.Version != 2 || got.State != "up" {
		t.Fatalf("after the move: project %s, version %d, %s", got.ProjectID, got.Version, got.State)
	}
	// The UUID still pings it; the slug goes with the new project's key.
	if _, err := f.s.Ping(ctx, ping(c.PingUUID, check.Success, "", t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Ping(ctx, PingIn{PingKey: f.pingKey, Slug: "printer", Kind: check.Success, ExitStatus: -1, Method: "GET", At: t0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old project's key: %v", err)
	}
	if _, err := f.s.Ping(ctx, PingIn{PingKey: key, Slug: "printer", Kind: check.Success, ExitStatus: -1, Method: "GET", At: t0.Add(2 * time.Minute)}); err != nil {
		t.Fatalf("the new project's key: %v", err)
	}
	runs, _ := db.New(f.s.r).RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "run_~", Limit: 10})
	if len(runs) != 3 {
		t.Fatalf("%d runs", len(runs))
	}
	// The editor of the first project no longer sees it.
	if _, err := f.s.Check(ctx, f.editor, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the editor still sees it: %v", err)
	}

	// A slug the other project uses is refused, and so is a project that does not exist.
	back, _ := f.s.CreateCheck(ctx, f.editor, f.project, NewCheck{Name: "Printer", Slug: "printer", Interval: time.Hour, Grace: time.Hour})
	var invalid *InvalidError
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, back.ID, other, time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("a taken slug: %v", err)
	}
	if err := f.s.MoveCheck(ctx, Command("test"), f.ws, back.ID, "prj_none", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such project: %v", err)
	}
}

func TestRenameProject(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Customer")
	if err := f.s.RenameProject(ctx, Command("test"), f.ws, f.project, "Customer"); err == nil {
		t.Fatal("two projects with one name")
	}
	if err := f.s.RenameProject(ctx, Command("test"), f.ws, other, "Shop"); err != nil {
		t.Fatal(err)
	}
	if id, err := f.s.ProjectByName(ctx, f.ws, "Shop"); err != nil || id != other {
		t.Fatalf("by its new name: %s, %v", id, err)
	}
	if err := f.s.RenameProject(ctx, Command("test"), f.ws, "prj_none", "Gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such project: %v", err)
	}
}

// A viewer gets no alerts, and that follows their role both ways.
func TestAlertsFollowTheRole(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	person, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Bob"}, []string{f.project}, Viewer)
	if err != nil {
		t.Fatal(err)
	}
	alerts := func() int64 {
		t.Helper()
		var n int64
		if err := f.s.r.QueryRow("SELECT count(*) FROM project_alerted WHERE project_id = ? AND person_id = ?", f.project, person).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, err := f.s.AccessOfPerson(ctx, f.ws, person)
	if err != nil || a.Admin || len(a.Projects) != 1 {
		t.Fatalf("%+v, %v", a, err)
	}
	if alerts() != 0 {
		t.Error("a viewer gets alerts")
	}
	for _, c := range []struct {
		role Role
		want int64
	}{{Editor, 1}, {Owner, 1}, {Viewer, 0}, {Viewer, 0}, {Owner, 1}} {
		if err := f.s.SetMember(ctx, Command("test"), f.ws, f.project, person, c.role); err != nil {
			t.Fatal(err)
		}
		if got := alerts(); got != c.want {
			t.Errorf("as %s: alerts %d", c.role, got)
		}
	}
	if err := f.s.RemoveMember(ctx, Command("test"), f.ws, f.project, person); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RemoveMember(ctx, Command("test"), f.ws, f.project, person); !errors.Is(err, ErrNotFound) {
		t.Errorf("again: %v", err)
	}
	if a, _ := f.s.AccessOfPerson(ctx, f.ws, person); len(a.Projects) != 0 {
		t.Errorf("%+v", a)
	}
}

// Someone who gets alerts for a project during an incident should have known
// since it opened, as when a check moves there.
func TestGettingAlertsDuringAnIncident(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	id := f.httpCheck(t)
	f.attempt(t, id, check.Passed, t0.Add(-time.Minute))
	f.attempt(t, id, check.Failed, t0)
	f.attempt(t, id, check.Failed, t0.Add(time.Minute))
	if c := f.get(t, id); c.State != "down" {
		t.Fatalf("%s", c.State)
	}
	due := func(person string) bool {
		t.Helper()
		rows, err := f.s.DueRecipients(ctx, time.Now().Add(time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.PersonID == person {
				return true
			}
		}
		return false
	}
	added, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Added"}, []string{f.project}, Editor)
	if err != nil {
		t.Fatal(err)
	}
	if !due(added) {
		t.Error("someone added as an editor was not told")
	}
	viewer, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Viewer"}, []string{f.project}, Viewer)
	if err != nil {
		t.Fatal(err)
	}
	if due(viewer) {
		t.Error("a viewer is to be told")
	}
	if err := f.s.SetMember(ctx, Command("test"), f.ws, f.project, viewer, Editor); err != nil {
		t.Fatal(err)
	}
	if !due(viewer) {
		t.Error("a viewer who became an editor was not told")
	}
	// Looked at and left, as when they stopped following it, then back.
	if err := f.s.Record(ctx, nil, []Told{{IncidentID: dueIncident(t, f, viewer), PersonID: viewer}}); err != nil {
		t.Fatal(err)
	}
	if due(viewer) {
		t.Fatal("still due after the sender looked")
	}
	if err := f.s.SetMember(ctx, Command("test"), f.ws, f.project, viewer, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetMember(ctx, Command("test"), f.ws, f.project, viewer, Editor); err != nil {
		t.Fatal(err)
	}
	if !due(viewer) {
		t.Error("back as an editor, they were not told")
	}
}

func dueIncident(t *testing.T, f fixture, person string) string {
	t.Helper()
	rows, err := f.s.DueRecipients(t.Context(), time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.PersonID == person {
			return r.IncidentID
		}
	}
	t.Fatal("not due")
	return ""
}
