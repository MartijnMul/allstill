// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A project's page, and a team's owners running its projects in the dashboard
// (intent/teams/plan-3.md, part B).

func TestAProjectsPageAsEachReadsIt(t *testing.T) {
	f := setup(t)
	owner := f.browser(t)
	f.register(t, owner, f.ada)
	page := owner.get("/projects/" + f.project).Body.String()
	for _, want := range []string{"Who can see it", "Bob", "a role of their own here", "Rename", "Only a project without checks", "Invite someone to this project"} {
		if !strings.Contains(page, want) {
			t.Errorf("the owner's page has no %q", want)
		}
	}
	// Bob is a viewer by a role of his own: his role, and his choice to be told.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	page = bob.get("/projects/" + f.project).Body.String()
	if !strings.Contains(page, "You are a viewer of this project") || !strings.Contains(page, "Tell me about it") {
		t.Errorf("Bob's page: %s", page)
	}
	for _, not := range []string{"Who can see it", "Rename", "Delete this project", "Ada"} {
		if strings.Contains(page, not) {
			t.Errorf("Bob's page has %q", not)
		}
	}
	if w := bob.post("/projects/"+f.project+"/alerts", url.Values{"alerts": {"on"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if page := bob.get("/projects/" + f.project).Body.String(); !strings.Contains(page, "You are told about this project again.") || !strings.Contains(page, "Stop telling me about it") {
		t.Error("the choice is not shown")
	}
	// Bob may not change his own role, nor rename or delete.
	for _, path := range []string{"/projects/" + f.project + "/roles/" + f.bob, "/projects/" + f.project + "/rename", "/projects/" + f.project + "/delete"} {
		if w := bob.post(path, url.Values{"role": {"owner"}, "name": {"Mine"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	// The owner makes him owner of the project, in the dashboard.
	if w := owner.post("/projects/"+f.project+"/roles/"+f.bob, url.Values{"role": {"owner"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// As an owner from outside the team, he reads its people as a count.
	page = bob.get("/projects/" + f.project).Body.String()
	if !strings.Contains(page, "The team: 1 person, 1 of them told") || strings.Contains(page, "Ada") {
		t.Errorf("the team as Bob reads it: %s", page)
	}
	if strings.Contains(page, "Rename") {
		t.Error("an owner of one project renames it")
	}
	// Ending his role takes him out of the project.
	if w := owner.post("/projects/"+f.project+"/roles/"+f.bob, url.Values{"role": {"none"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := bob.get("/projects/" + f.project); w.Code != http.StatusNotFound {
		t.Errorf("after his role ended: %d", w.Code)
	}
}

func TestAnOwnerMakesAndDeletesAProjectInTheDashboard(t *testing.T) {
	f := setup(t)
	owner := f.browser(t)
	f.register(t, owner, f.ada)
	if page := owner.get("/").Body.String(); !strings.Contains(page, `href="/projects/new"`) {
		t.Error("no way to make a project")
	}
	if w := owner.get("/projects/new"); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	w := owner.post("/projects/new", url.Values{"name": {"Tides"}})
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), "Its ping key") || !strings.Contains(w.Body.String(), `id="ping-key"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := owner.post("/projects/new", url.Values{"name": {"Backups"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "exists already") {
		t.Errorf("a name taken: %d", w.Code)
	}
	a, err := f.st.AccessOfPerson(t.Context(), f.ws, f.ada)
	if err != nil {
		t.Fatal(err)
	}
	var tides string
	for _, p := range a.Projects {
		if p.Name == "Tides" {
			tides = p.ID
		}
	}
	if w := owner.post("/projects/"+tides+"/rename", url.Values{"name": {"Tide tables"}}); w.Code != http.StatusSeeOther {
		t.Errorf("renaming: %d %s", w.Code, w.Body)
	}
	if page := owner.get("/projects/" + tides + "/delete").Body.String(); !strings.Contains(page, "cannot be undone") {
		t.Errorf("the confirmation: %s", page)
	}
	if w := owner.post("/projects/"+tides+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("deleting: %d %s", w.Code, w.Body)
	}
	if page := owner.get("/").Body.String(); !strings.Contains(page, "The project Tide tables was deleted.") {
		t.Error("not said")
	}
	if w := owner.get("/projects/" + tides); w.Code != http.StatusNotFound {
		t.Errorf("a deleted project's page: %d", w.Code)
	}
	// One with checks stays, and says so.
	if w := owner.post("/projects/"+f.project+"/delete", nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "still holds") {
		t.Errorf("with checks: %d", w.Code)
	}
	// Someone who owns no team makes none.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	if w := bob.get("/projects/new"); w.Code != http.StatusNotFound {
		t.Errorf("Bob's new project: %d", w.Code)
	}
	if w := bob.post("/projects/new", url.Values{"name": {"Mine"}}); w.Code != http.StatusNotFound {
		t.Errorf("Bob makes one: %d", w.Code)
	}
	if _, err := f.st.ProjectByName(t.Context(), f.ws, "Mine"); err == nil {
		t.Error("Bob made a project")
	}
}
