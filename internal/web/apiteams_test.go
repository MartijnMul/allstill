// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/store"
)

// A team's owners running their team over the API (intent/teams/plan-3.md, part C).
// The wall between teams is walked in teams_test.go, and what an owner's sign-in
// cannot do there too (TestWhatStaysOnTheServer).

func decoded[T any](t *testing.T, body string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return out
}

// inTeam puts someone new in the fixture's team.
func inTeam(t *testing.T, f fixture, name string, role store.Role) string {
	t.Helper()
	team, err := f.st.TeamOfProject(t.Context(), f.ws, f.project)
	if err != nil {
		t.Fatal(err)
	}
	person, err := f.st.CreatePerson(t.Context(), store.Command("test"), f.ws, store.NewPerson{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetTeamMember(t.Context(), store.Command("test"), f.ws, team, person, role); err != nil {
		t.Fatal(err)
	}
	return person
}

func TestAnOwnerMakesRenamesAndDeletesAProjectOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	owner := tokenFor(t, f, f.ada, store.ScopeManage)
	w := bearerWith(f, "POST", "/projects", owner, map[string]any{"name": "Tides"})
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	made := decoded[api.ProjectCreated](t, w.Body.String())
	if made.PingKey == "" || made.Role != api.RoleOwner || made.Team == nil || made.Alerts == nil || !*made.Alerts || w.Header().Get("Location") != site+"/api/v1/projects/"+made.Id {
		t.Errorf("made: %+v, at %q", made, w.Header().Get("Location"))
	}
	if w := bearerWith(f, "POST", "/projects", owner, map[string]any{"name": "Tides"}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "exists already") {
		t.Errorf("a name taken: %d %s", w.Code, w.Body)
	}
	w = bearerWith(f, "PATCH", "/projects/"+made.Id, owner, map[string]any{"name": "Tide tables"})
	if got := decoded[api.Project](t, w.Body.String()); w.Code != http.StatusOK || got.Name != "Tide tables" {
		t.Errorf("renamed: %d %s", w.Code, w.Body)
	}
	if w := bearerWith(f, "PATCH", "/projects/"+made.Id, owner, map[string]any{}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("nothing to change: %d %s", w.Code, w.Body)
	}
	// One with checks stays, and says how many.
	if w := bearer(f, "DELETE", "/projects/"+f.project, owner); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "1 check") {
		t.Errorf("with checks: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "DELETE", "/projects/"+made.Id, owner); w.Code != http.StatusNoContent {
		t.Fatalf("deleting: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/projects/"+made.Id, owner); w.Code != http.StatusNotFound {
		t.Errorf("a deleted project: %d", w.Code)
	}
	// Who may not: a viewer, an editor of the team, an owner of one project, a
	// read-only sign-in, a key.
	for who, token := range map[string]string{
		"a viewer":                     tokenFor(t, f, f.bob, store.ScopeManage),
		"an editor of the team":        tokenFor(t, f, inTeam(t, f, "Ed", store.Editor), store.ScopeManage),
		"an owner of one project":      tokenFor(t, f, someoneIn(t, f, "Pam", f.project, store.Owner), store.ScopeManage),
		"an owner's read-only sign-in": tokenFor(t, f, f.ada, store.ScopeRead),
		"a key":                        keyFor(t, f, f.project, "deploy", store.ScopeManage),
	} {
		if w := bearerWith(f, "POST", "/projects", token, map[string]any{"name": "Mine"}); w.Code != http.StatusForbidden {
			t.Errorf("%s makes a project: %d %s", who, w.Code, w.Body)
		}
		if w := bearerWith(f, "PATCH", "/projects/"+f.project, token, map[string]any{"name": "Mine"}); w.Code != http.StatusForbidden {
			t.Errorf("%s renames one: %d %s", who, w.Code, w.Body)
		}
	}
}

func TestOwningSeveralTeamsNamesOne(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	second, err := f.st.CreateTeam(t.Context(), store.Command("test"), f.ws, "Harbour Works")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetTeamMember(t.Context(), store.Command("test"), f.ws, second, f.ada, store.Owner); err != nil {
		t.Fatal(err)
	}
	owner := tokenFor(t, f, f.ada, store.ScopeManage)
	w := bearerWith(f, "POST", "/projects", owner, map[string]any{"name": "Docks"})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"pointer":"/team_id"`) {
		t.Errorf("no team said: %d %s", w.Code, w.Body)
	}
	w = bearerWith(f, "POST", "/projects", owner, map[string]any{"name": "Docks", "team_id": second})
	if got := decoded[api.ProjectCreated](t, w.Body.String()); w.Code != http.StatusCreated || got.Team == nil || got.Team.Id != second {
		t.Errorf("in the second: %d %s", w.Code, w.Body)
	}
}

func TestPeopleAndRolesOfAProjectOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	owner := tokenFor(t, f, f.ada, store.ScopeManage)
	ed := inTeam(t, f, "Ed", store.Editor)
	people := read[api.ProjectPeople](t, f, "/projects/"+f.project+"/people", owner)
	byName := map[string]api.ProjectPerson{}
	for _, p := range people.Items {
		byName[p.Name] = p
	}
	if bob := byName["Bob"]; bob.OwnRole == nil || *bob.OwnRole != api.RoleViewer || bob.TeamRole != nil || bob.Alerts {
		t.Errorf("Bob: %+v", bob)
	}
	if e := byName["Ed"]; e.TeamRole == nil || *e.TeamRole != api.RoleEditor || e.OwnRole != nil || !e.Alerts {
		t.Errorf("Ed: %+v", e)
	}
	if people.Team != nil {
		t.Errorf("an owner in the team reads the team as a count: %+v", people.Team)
	}
	// Changed, never given; more than the team gives; never owner.
	w := bearerWith(f, "PUT", "/projects/"+f.project+"/roles/"+f.bob, owner, map[string]any{"role": "editor"})
	if got := decoded[api.ProjectPerson](t, w.Body.String()); w.Code != http.StatusOK || got.Role != api.RoleEditor || got.OwnRole == nil || *got.OwnRole != api.RoleEditor {
		t.Errorf("Bob made editor: %d %s", w.Code, w.Body)
	}
	if w := bearerWith(f, "PUT", "/projects/"+f.project+"/roles/"+ed, owner, map[string]any{"role": "editor"}); w.Code != http.StatusNotFound {
		t.Errorf("a role given to a member with none of their own: %d %s", w.Code, w.Body)
	}
	if w := bearerWith(f, "PUT", "/projects/"+f.project+"/roles/"+f.bob, owner, map[string]any{"role": "admin"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown role: %d %s", w.Code, w.Body)
	}
	viewer := inTeam(t, f, "Vera", store.Viewer)
	if err := f.st.SetMember(t.Context(), store.Command("test"), f.ws, f.project, viewer, store.Editor); err != nil {
		t.Fatal(err)
	}
	if w := bearerWith(f, "PUT", "/projects/"+f.project+"/roles/"+viewer, owner, map[string]any{"role": "viewer"}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not more than") {
		t.Errorf("no more than the team gives: %d %s", w.Code, w.Body)
	}
	// An owner from outside the team reads the team as a count.
	pam := tokenFor(t, f, someoneIn(t, f, "Pam", f.project, store.Owner), store.ScopeManage)
	theirs := read[api.ProjectPeople](t, f, "/projects/"+f.project+"/people", pam)
	raw := bearer(f, "GET", "/projects/"+f.project+"/people", pam).Body.String()
	if theirs.Team == nil || theirs.Team.People != 3 || strings.Contains(raw, "Ada") || strings.Contains(raw, "Ed") || strings.Contains(raw, "Vera") || strings.Contains(raw, ed) {
		t.Errorf("the team as an outside owner reads it: %s", raw)
	}
	if w := bearerWith(f, "PUT", "/projects/"+f.project+"/roles/"+viewer, pam, map[string]any{"role": "editor"}); w.Code != http.StatusNotFound {
		t.Errorf("an outside owner changes a member's role: %d %s", w.Code, w.Body)
	}
	// Ending a role takes someone outside the team out of the project.
	if w := bearer(f, "DELETE", "/projects/"+f.project+"/roles/"+f.bob, owner); w.Code != http.StatusNoContent {
		t.Fatalf("ending: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/projects/"+f.project, tokenFor(t, f, f.bob, store.ScopeManage)); w.Code != http.StatusNotFound {
		t.Errorf("after their role ended: %d", w.Code)
	}
	// A viewer reads no people.
	if w := bearer(f, "GET", "/projects/"+f.project+"/people", tokenFor(t, f, viewer, store.ScopeManage)); w.Code != http.StatusForbidden {
		t.Errorf("an editor reads the people: %d", w.Code)
	}
}

func TestEveryoneChoosesOverTheAPIWhetherTheyAreTold(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	bob := tokenFor(t, f, f.bob, store.ScopeManage)
	if got := read[api.Project](t, f, "/projects/"+f.project, bob); got.Alerts == nil || *got.Alerts {
		t.Errorf("a viewer is told: %+v", got.Alerts)
	}
	if w := bearerWith(f, "PUT", "/me/projects/"+f.project+"/alerts", bob, map[string]any{"alerts": true}); w.Code != http.StatusNoContent {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := read[api.Project](t, f, "/projects/"+f.project, bob); got.Alerts == nil || !*got.Alerts {
		t.Errorf("not told after choosing: %+v", got.Alerts)
	}
	me := read[api.Me](t, f, "/me", bob)
	if len(me.Projects) != 1 || me.Projects[0].Alerts == nil || !*me.Projects[0].Alerts {
		t.Errorf("me: %+v", me.Projects)
	}
	if w := bearerWith(f, "PUT", "/me/projects/"+f.project+"/alerts", tokenFor(t, f, f.bob, store.ScopeRead), map[string]any{"alerts": false}); w.Code != http.StatusForbidden {
		t.Errorf("a read-only sign-in chooses: %d", w.Code)
	}
	if w := bearerWith(f, "PUT", "/me/projects/"+f.project+"/alerts", keyFor(t, f, f.project, "deploy", store.ScopeManage), map[string]any{"alerts": false}); w.Code != http.StatusForbidden {
		t.Errorf("a key chooses: %d", w.Code)
	}
	// A read-only sign-in reads its person's choice, and a key none at all.
	if got := read[api.Project](t, f, "/projects/"+f.project, tokenFor(t, f, f.bob, store.ScopeRead)); got.Alerts == nil || !*got.Alerts {
		t.Errorf("a read-only sign-in reads: %+v", got.Alerts)
	}
	key := keyFor(t, f, f.project, "reader", store.ScopeRead)
	if got := read[api.Project](t, f, "/projects/"+f.project, key); got.Alerts != nil {
		t.Errorf("a key reads alerts: %v", *got.Alerts)
	}
}

func TestATeamOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	ctx := t.Context()
	team, _ := f.st.TeamOfProject(ctx, f.ws, f.project)
	owner := tokenFor(t, f, f.ada, store.ScopeManage)
	vera := inTeam(t, f, "Vera", store.Viewer)
	if err := f.st.SetMember(ctx, store.Command("test"), f.ws, f.project, vera, store.Editor); err != nil {
		t.Fatal(err)
	}
	got := read[api.TeamPeople](t, f, "/teams/"+team, owner)
	if got.Id != team || len(got.Members) != 2 || len(got.Outsiders) != 1 || got.Outsiders[0].Name != "Bob" {
		t.Errorf("the team: %+v", got)
	}
	// Made an editor of the team, her own editor's role gives no more and ends.
	w := bearerWith(f, "PUT", "/teams/"+team+"/members/"+vera, owner, map[string]any{"role": "editor"})
	set := decoded[api.TeamMemberSet](t, w.Body.String())
	if w.Code != http.StatusOK || set.Name != "Vera" || set.Role != api.RoleEditor || len(set.EndedRoles) != 1 || set.EndedRoles[0].ProjectName != "Backups" {
		t.Errorf("changed: %d %s", w.Code, w.Body)
	}
	if w := bearerWith(f, "PUT", "/teams/"+team+"/members/"+f.bob, owner, map[string]any{"role": "editor"}); w.Code != http.StatusNotFound {
		t.Errorf("someone outside made a member: %d %s", w.Code, w.Body)
	}
	// Taken out: she had nothing of her own left there.
	w = bearer(f, "DELETE", "/teams/"+team+"/members/"+vera, owner)
	if stays := decoded[api.WhatStays](t, w.Body.String()); w.Code != http.StatusOK || len(stays.Roles) != 0 {
		t.Errorf("taken out: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/projects/"+f.project, tokenFor(t, f, vera, store.ScopeManage)); w.Code != http.StatusNotFound {
		t.Errorf("taken out, still sees it: %d", w.Code)
	}
	// Only an owner of the team reads or changes it.
	for who, token := range map[string]string{
		"an editor of the team":        tokenFor(t, f, inTeam(t, f, "Ed", store.Editor), store.ScopeManage),
		"an owner's read-only sign-in": tokenFor(t, f, f.ada, store.ScopeRead),
	} {
		if w := bearer(f, "GET", "/teams/"+team, token); w.Code != http.StatusForbidden {
			t.Errorf("%s reads the team: %d", who, w.Code)
		}
	}
	if w := bearer(f, "GET", "/teams/"+team, tokenFor(t, f, f.bob, store.ScopeManage)); w.Code != http.StatusNotFound {
		t.Errorf("someone outside the team reads it: %d", w.Code)
	}
	// Taken out with a role of her own that gives more, it stays, by name.
	wren := inTeam(t, f, "Wren", store.Viewer)
	if err := f.st.SetMember(ctx, store.Command("test"), f.ws, f.project, wren, store.Editor); err != nil {
		t.Fatal(err)
	}
	w = bearer(f, "DELETE", "/teams/"+team+"/members/"+wren, owner)
	if stays := decoded[api.WhatStays](t, w.Body.String()); w.Code != http.StatusOK || len(stays.Roles) != 1 || stays.Roles[0].Name != "Wren" || stays.Roles[0].Role != api.RoleEditor || stays.Roles[0].ProjectName != "Backups" {
		t.Errorf("what stays: %d %s", w.Code, w.Body)
	}
	if got := read[api.Project](t, f, "/projects/"+f.project, tokenFor(t, f, wren, store.ScopeManage)); got.Role != api.RoleEditor {
		t.Errorf("her own role went with the team: %+v", got)
	}
	// Given the role they have, nothing ends.
	w = bearerWith(f, "PUT", "/teams/"+team+"/members/"+inTeam(t, f, "Uma", store.Editor), owner, map[string]any{"role": "editor"})
	if set := decoded[api.TeamMemberSet](t, w.Body.String()); w.Code != http.StatusOK || len(set.EndedRoles) != 0 {
		t.Errorf("the same role again: %d %s", w.Code, w.Body)
	}
}

func TestInvitationsOverTheAPI(t *testing.T) {
	f := setup(t)
	f.srv.Now = time.Now
	team, _ := f.st.TeamOfProject(t.Context(), f.ws, f.project)
	owner := tokenFor(t, f, f.ada, store.ScopeManage)
	w := bearerWith(f, "POST", "/teams/"+team+"/invites", owner, map[string]any{"role": "editor", "label": "Carol"})
	made := decoded[api.InviteMade](t, w.Body.String())
	if w.Code != http.StatusCreated || made.Kind != "team" || made.Label != "Carol" || !strings.HasPrefix(made.Url, site+"/join#") || made.MadeBy.Kind != "person" {
		t.Fatalf("made: %d %s", w.Code, w.Body)
	}
	offer, err := f.st.InviteOffer(t.Context(), strings.TrimPrefix(made.Url, site+"/join#"), time.Now())
	if err != nil || offer.Role != store.Editor {
		t.Errorf("the link does not work: %+v %v", offer, err)
	}
	w = bearerWith(f, "POST", "/projects/"+f.project+"/invites", owner, map[string]any{"role": "viewer", "label": "Dan"})
	if got := decoded[api.InviteMade](t, w.Body.String()); w.Code != http.StatusCreated || got.Kind != "project" || got.ProjectId == nil || *got.ProjectId != f.project {
		t.Errorf("to a project: %d %s", w.Code, w.Body)
	}
	if got := read[api.InviteList](t, f, "/teams/"+team+"/invites", owner); len(got.Items) != 2 {
		t.Errorf("the team's: %+v", got)
	}
	if got := read[api.InviteList](t, f, "/projects/"+f.project+"/invites", owner); len(got.Items) != 1 || got.Items[0].Label != "Dan" {
		t.Errorf("the project's: %+v", got)
	}
	if w := bearer(f, "DELETE", "/invites/"+made.Id, owner); w.Code != http.StatusNoContent {
		t.Errorf("withdrawing: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "DELETE", "/invites/"+made.Id, owner); w.Code != http.StatusConflict {
		t.Errorf("withdrawing again: %d %s", w.Code, w.Body)
	}
	w = bearerWith(f, "POST", "/teams/"+team+"/invites", owner, map[string]any{"role": "viewer", "label": "Late"})
	late := decoded[api.InviteMade](t, w.Body.String())
	// A project takes twenty open invitations.
	for i := range 19 {
		if w := bearerWith(f, "POST", "/projects/"+f.project+"/invites", owner, map[string]any{"role": "viewer", "label": fmt.Sprint("Guest ", i)}); w.Code != http.StatusCreated {
			t.Fatalf("invitation %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := bearerWith(f, "POST", "/projects/"+f.project+"/invites", owner, map[string]any{"role": "viewer", "label": "One more"}); w.Code != http.StatusConflict {
		t.Errorf("the twenty-first: %d %s", w.Code, w.Body)
	}
	// A label is needed; whoever may not, may not.
	if w := bearerWith(f, "POST", "/teams/"+team+"/invites", owner, map[string]any{"role": "viewer", "label": ""}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("no label: %d %s", w.Code, w.Body)
	}
	for who, token := range map[string]string{
		"a viewer":                     tokenFor(t, f, f.bob, store.ScopeManage),
		"an owner's read-only sign-in": tokenFor(t, f, f.ada, store.ScopeRead),
		"a key":                        keyFor(t, f, f.project, "deploy", store.ScopeManage),
	} {
		if w := bearerWith(f, "POST", "/projects/"+f.project+"/invites", token, map[string]any{"role": "viewer", "label": "Mine"}); w.Code != http.StatusForbidden {
			t.Errorf("%s invites: %d %s", who, w.Code, w.Body)
		}
	}
	// One that ended is not withdrawn either. Last, as the clock moves on.
	f.srv.Now = func() time.Time { return time.Now().Add(store.InviteLifetime + time.Minute) }
	if w := bearer(f, "DELETE", "/invites/"+late.Id, owner); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ended") {
		t.Errorf("withdrawing one that ended: %d %s", w.Code, w.Body)
	}
}
