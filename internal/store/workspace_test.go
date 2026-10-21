// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"sync"
	"testing"
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
