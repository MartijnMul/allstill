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
	operator, _ := s.CreatePerson(ctx, ws, NewPerson{Name: "Operator", WorkspaceAdmin: true})
	colleague, _ := s.CreatePerson(ctx, ws, NewPerson{Name: "Colleague", Email: "colleague@example.com"})
	own, _, _ := s.CreateProject(ctx, ws, "Own company")
	customer, key, err := s.CreateProject(ctx, ws, "Customer")
	if err != nil || len(key) != 22 {
		t.Fatalf("project: %v, key %q", err, key)
	}
	s.CreateProject(ctx, ws, "Home lab")
	if err := s.SetMember(ctx, ws, customer, colleague, Viewer); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember(ctx, ws, customer, colleague, Editor); err != nil {
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
	admin, _ := s.CreatePerson(ctx, ws, NewPerson{Name: "Admin", WorkspaceAdmin: true})
	p, _, _ := s.CreateProject(ctx, ws, "P")
	s.SetMember(ctx, ws, p, admin, Viewer)
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
	other, key, _ := f.s.CreateProject(ctx, f.ws, "Customer")
	if err := f.s.MoveCheck(ctx, f.ws, c.ID, other, time.Now()); err != nil {
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
	if err := f.s.MoveCheck(ctx, f.ws, back.ID, other, time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("a taken slug: %v", err)
	}
	if err := f.s.MoveCheck(ctx, f.ws, back.ID, "prj_none", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such project: %v", err)
	}
}

func TestRenameProject(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	other, _, _ := f.s.CreateProject(ctx, f.ws, "Customer")
	if err := f.s.RenameProject(ctx, f.ws, f.project, "Customer"); err == nil {
		t.Fatal("two projects with one name")
	}
	if err := f.s.RenameProject(ctx, f.ws, other, "Shop"); err != nil {
		t.Fatal(err)
	}
	if id, err := f.s.ProjectByName(ctx, f.ws, "Shop"); err != nil || id != other {
		t.Fatalf("by its new name: %s, %v", id, err)
	}
	if err := f.s.RenameProject(ctx, f.ws, "prj_none", "Gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such project: %v", err)
	}
}
