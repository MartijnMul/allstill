// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// A team's owners run its projects (intent/teams/plan-3.md, part B).

func pingKeyed(key, slug string, at time.Time) PingIn {
	return PingIn{PingKey: key, Slug: slug, Create: true, Kind: check.Success, ExitStatus: -1, Method: "POST", At: at}
}

func logKinds(t *testing.T, w *wall, a Access) []string {
	t.Helper()
	page, err := w.s.ReadLog(t.Context(), a, LogFilter{Limit: 200})
	must(t, err)
	var kinds []string
	for _, e := range page.Entries {
		kinds = append(kinds, e.Kind+" "+e.AboutName)
	}
	return kinds
}

func TestAnOwnerMakesRenamesAndDeletesAProject(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()

	// Only an owner of the team makes one, and only in it.
	if _, _, err := w.s.CreateProjectAs(ctx, w.hedy, w.second, "Tides"); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor: %v", err)
	}
	if _, _, err := w.s.CreateProjectAs(ctx, w.hal, w.first, "Tides"); !errors.Is(err, ErrNotFound) {
		t.Errorf("in another team: %v", err)
	}
	var invalid *InvalidError
	for _, name := range []string{"  ", strings.Repeat("ä", 101), "Backups"} {
		if _, _, err := w.s.CreateProjectAs(ctx, w.hal, w.second, name); !errors.As(err, &invalid) {
			t.Errorf("the name %q: %v", name, err)
		}
	}
	tides, key, err := w.s.CreateProjectAs(ctx, w.hal, w.second, " Tides ")
	must(t, err)
	hal := w.access(t, w.hal.PersonID)
	if role, ok := hal.Role(tides); !ok || role != Owner || hal.Label(tides) != "Tides" {
		t.Errorf("the owner does not see it as owner: %+v", hal.Projects)
	}
	if role, ok := w.access(t, w.hector.PersonID).Role(tides); !ok || role != Viewer {
		t.Errorf("the team's viewer does not see it")
	}
	if _, ok := w.access(t, w.carol.PersonID).Role(tides); ok {
		t.Errorf("someone outside the team sees it")
	}

	// Renamed under the same rule, by an owner of the team alone.
	if err := w.s.RenameProjectAs(ctx, hal, tides, "Backups"); !errors.As(err, &invalid) {
		t.Errorf("a name taken: %v", err)
	}
	if err := w.s.RenameProjectAs(ctx, w.access(t, w.hedy.PersonID), tides, "Tide tables"); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor renames: %v", err)
	}
	must(t, w.s.RenameProjectAs(ctx, hal, tides, "Tide tables"))

	// What is in it: a check made by a ping, a key, an invitation, a role of
	// one's own, a choice, and a probe that serves it.
	if _, err := w.s.Ping(ctx, pingKeyed(key, "tide-sync", w.at)); err != nil {
		t.Fatal(err)
	}
	_, keyValue, err := w.s.CreateAPIKey(ctx, hal, tides, "tide-script", ScopeRead, w.at)
	must(t, err)
	_, invite, err := w.s.CreateInvite(ctx, hal, "", tides, Viewer, "a contractor", w.at)
	must(t, err)
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, tides, w.carol.PersonID, Viewer))
	must(t, w.s.SetOwnAlerts(ctx, w.access(t, w.hedy.PersonID), tides, false, w.at))
	admin, err := w.s.AdminAccess(ctx, w.ws, "test")
	must(t, err)
	_, _, err = w.s.CreateProbe(ctx, admin, "pier-probe", w.theirs, []string{tides})
	must(t, err)

	// With a check in it, it stays, and says how many.
	hal = w.access(t, w.hal.PersonID)
	var holds *ProjectHolds
	if err := w.s.DeleteProjectAs(ctx, hal, tides, w.at); !errors.As(err, &holds) || holds.Checks != 1 {
		t.Fatalf("with a check: %v", err)
	}
	checks, _, err := w.s.ChecksOfProject(ctx, hal, tides, 0, 10)
	must(t, err)
	must(t, w.s.DeleteCheck(ctx, hal, checks[0].ID, w.at))
	if err := w.s.DeleteProjectAs(ctx, w.access(t, w.hedy.PersonID), tides, w.at); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor deletes: %v", err)
	}
	if err := w.s.DeleteProjectAs(ctx, w.carol, w.theirs, w.at); !errors.Is(err, ErrForbidden) {
		t.Errorf("someone with a role of their own deletes: %v", err)
	}
	must(t, w.s.DeleteProjectAs(ctx, hal, tides, w.at))

	// Gone: from every access, its ping key, its key, its invitation, its rows.
	for _, who := range []Access{w.hal, w.hedy, w.hector, w.carol} {
		if _, ok := w.access(t, who.PersonID).Role(tides); ok {
			t.Errorf("%s still sees it", who.Actor.Name)
		}
	}
	if _, err := w.s.Ping(ctx, pingKeyed(key, "tide-sync", w.at)); !errors.Is(err, ErrNotFound) {
		t.Errorf("its ping key still finds it: %v", err)
	}
	if _, _, err := w.s.AccessOfAPIKey(ctx, keyValue, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("its key still works: %v", err)
	}
	if _, err := w.s.InviteOffer(ctx, invite, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("its invitation still works: %v", err)
	}
	for _, table := range []string{"memberships", "alert_choices", "probe_projects", "invites", "api_keys"} {
		var n int
		must(t, w.s.r.QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id = ?`, tides).Scan(&n))
		if n != 0 {
			t.Errorf("%d rows of %s are left", n, table)
		}
	}
	// Nothing more goes in.
	if _, err := w.s.CreateCheck(ctx, admin, tides, NewCheck{Name: "late", Interval: time.Hour, Grace: time.Hour}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a check into it: %v", err)
	}
	if _, _, err := w.s.CreateAPIKey(ctx, admin, tides, "late", ScopeRead, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("a key into it: %v", err)
	}
	if err := w.s.SetMember(ctx, Command("test"), w.ws, tides, w.carol.PersonID, Editor); !errors.Is(err, ErrNotFound) {
		t.Errorf("a role in it: %v", err)
	}
	if err := w.s.SetOwnAlerts(ctx, hal, tides, false, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("a choice about it: %v", err)
	}
	if err := w.s.MoveCheck(ctx, Command("test"), w.ws, mustCheck(t, w, w.theirs), tides, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("a check moved into it: %v", err)
	}
	// Its name is free, and the team's owners read that it went; the others do not.
	again, _, err := w.s.CreateProjectAs(ctx, w.access(t, w.hal.PersonID), w.second, "Tide tables")
	must(t, err)
	if again == tides {
		t.Error("the deleted project came back")
	}
	if kinds := logKinds(t, w, w.access(t, w.hal.PersonID)); !slices.Contains(kinds, "project.deleted Tide tables") {
		t.Errorf("the owner does not read that it went: %v", kinds)
	}
	for _, who := range []Access{w.hedy, w.carol, w.zed} {
		if kinds := logKinds(t, w, w.access(t, who.PersonID)); slices.Contains(kinds, "project.deleted Tide tables") {
			t.Errorf("%s reads that it went", who.Actor.Name)
		}
	}
}

func mustCheck(t *testing.T, w *wall, projectID string) string {
	t.Helper()
	admin, err := w.s.AdminAccess(t.Context(), w.ws, "test")
	must(t, err)
	c, err := w.s.CreateCheck(t.Context(), admin, projectID, NewCheck{Name: "a check " + time.Now().String(), Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	return c.ID
}

// A team whose projects were all deleted can be removed, with what they held.
func TestATeamWhoseProjectsWentCanBeRemoved(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	by := Command("remove-team")
	pier, err := w.s.CreateTeam(ctx, by, w.ws, "Pier")
	must(t, err)
	dora := w.person(t, "Dora", false, false)
	must(t, w.s.SetTeamMember(ctx, by, w.ws, pier, dora, Owner))
	project, key, err := w.s.CreateProjectAs(ctx, w.access(t, dora), pier, "Nets")
	must(t, err)
	if _, err := w.s.Ping(ctx, pingKeyed(key, "net-mend", w.at)); err != nil {
		t.Fatal(err)
	}
	doraAccess := w.access(t, dora)
	checks, _, err := w.s.ChecksOfProject(ctx, doraAccess, project, 0, 10)
	must(t, err)
	must(t, w.s.DeleteCheck(ctx, doraAccess, checks[0].ID, w.at))
	var holds *TeamHolds
	if err := w.s.RemoveTeam(ctx, by, w.ws, pier); !errors.As(err, &holds) || holds.Projects != 1 {
		t.Fatalf("with a live project: %v", err)
	}
	must(t, w.s.DeleteProjectAs(ctx, doraAccess, project, w.at))
	// The deleted check and its history are still there, until tidying or now.
	must(t, w.s.RemoveTeam(ctx, by, w.ws, pier))
	for _, q := range []string{`SELECT count(*) FROM projects WHERE id = ?`, `SELECT count(*) FROM checks WHERE project_id = ?`} {
		var n int
		must(t, w.s.r.QueryRow(q, project).Scan(&n))
		if n != 0 {
			t.Errorf("%s: %d left", q, n)
		}
	}
}

func TestEveryoneChoosesWhetherTheyAreTold(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	told := func(a Access) bool {
		t.Helper()
		info, err := w.s.Project(ctx, w.access(t, a.PersonID), w.theirs)
		must(t, err)
		return info.Told
	}
	// A viewer is not told until they ask; an editor and an owner are until they stop.
	if told(w.hector) || !told(w.hedy) || !told(w.hal) {
		t.Fatal("not as the roles give it")
	}
	must(t, w.s.SetOwnAlerts(ctx, w.hector, w.theirs, true, w.at))
	must(t, w.s.SetOwnAlerts(ctx, w.hedy, w.theirs, false, w.at))
	must(t, w.s.SetOwnAlerts(ctx, w.carol, w.theirs, false, w.at))
	if !told(w.hector) || told(w.hedy) || told(w.carol) || !told(w.hal) {
		t.Error("a choice was not kept")
	}
	// Nobody chooses for someone else, and a key chooses nothing.
	if err := w.s.SetOwnAlerts(ctx, Access{WorkspaceID: w.ws, Projects: w.hal.Projects, Actor: Actor{Kind: "key"}}, w.theirs, false, w.at); !errors.Is(err, ErrForbidden) {
		t.Errorf("a key: %v", err)
	}
	if err := w.s.SetOwnAlerts(ctx, w.hal, w.ours, false, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("another team's project: %v", err)
	}
	// The owner sees who is told, as the view says.
	pp, err := w.s.PeopleOfProject(ctx, w.hal, w.theirs, w.at)
	must(t, err)
	got := map[string]bool{}
	for _, p := range pp.People {
		got[p.Name] = p.Told
	}
	if !got["Hector"] || got["Hedy"] || got["Carol"] || !got["Hal"] {
		t.Errorf("%v", got)
	}
	// A change of role keeps what they chose.
	must(t, w.s.SetTeamMember(ctx, Command("test"), w.ws, w.second, w.hedy.PersonID, Owner))
	if told(w.hedy) {
		t.Error("made owner, Hedy is told again against her choice")
	}
	if kinds := logKinds(t, w, w.hal); !slices.Contains(kinds, "alerts.chosen Hector") {
		t.Errorf("the owner does not read the choice: %v", kinds)
	}
}

func TestARoleOfOnesOwnInAProject(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	var invalid *InvalidError
	// Carol is an editor by a row of her own. Hal changes it; Hedy may not.
	if err := w.s.SetProjectRoleAs(ctx, w.hedy, w.theirs, w.carol.PersonID, Viewer); !errors.Is(err, ErrForbidden) {
		t.Errorf("an editor changes a role: %v", err)
	}
	must(t, w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.carol.PersonID, Viewer))
	if role, _ := w.access(t, w.carol.PersonID).Role(w.theirs); role != Viewer {
		t.Errorf("Carol is %s", role)
	}
	// Someone with no row of their own is not given one here: an invitation does.
	if err := w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.hector.PersonID, Editor); !errors.Is(err, ErrNotFound) {
		t.Errorf("a role made: %v", err)
	}
	// A row of one's own gives more than the team, or is refused.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.hector.PersonID, Editor))
	if err := w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.hector.PersonID, Viewer); !errors.As(err, &invalid) {
		t.Errorf("a role no more than the team's: %v", err)
	}
	// An owner is named only in the dashboard.
	viaToken := w.hal
	viaToken.Actor.Via = "a laptop"
	if err := w.s.SetProjectRoleAs(ctx, viaToken, w.theirs, w.carol.PersonID, Owner); !errors.As(err, &invalid) {
		t.Errorf("an owner named from a command line: %v", err)
	}
	confirmedChannel(t, w, w.hal.PersonID)
	readAll(t, w, w.hal.PersonID)
	must(t, w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.carol.PersonID, Owner))
	if titles := unreadTitles(t, w, w.hal.PersonID); len(titles) != 1 || !strings.Contains(titles[0], "made Carol owner of Backups") {
		t.Errorf("the team's owner is not told: %v", titles)
	}
	if titles := unreadTitles(t, w, w.carol.PersonID); len(titles) != 1 || !strings.HasPrefix(titles[0], "The team made Carol owner") {
		t.Errorf("Carol is not told, or reads a name of the team: %v", titles)
	}
	// An owner from outside the team reads its people as a count, and cannot
	// reach one of them by id.
	carol := w.access(t, w.carol.PersonID)
	pp, err := w.s.PeopleOfProject(ctx, carol, w.theirs, w.at)
	must(t, err)
	if !pp.Unnamed || pp.Team != 3 || len(pp.People) != 1 || pp.People[0].Name != "Carol" {
		t.Errorf("%+v", pp)
	}
	if err := w.s.SetProjectRoleAs(ctx, carol, w.theirs, w.hector.PersonID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone of the team, by id: %v", err)
	}
	// Someone of the team made owner by a row of their own is told to her
	// without a name.
	readAll(t, w, w.carol.PersonID)
	must(t, w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.hector.PersonID, Owner))
	if titles := unreadTitles(t, w, w.carol.PersonID); len(titles) != 1 || titles[0] != "The team made someone of the team owner of Backups." {
		t.Errorf("Carol reads: %v", titles)
	}
	// Hector is a viewer in the team, who is not named its people, as in the log.
	if titles := unreadTitles(t, w, w.hector.PersonID); len(titles) != 1 || titles[0] != "The team made Hector owner of Backups." {
		t.Errorf("Hector reads: %v", titles)
	}
	if titles := unreadTitles(t, w, w.hal.PersonID); len(titles) != 2 || !strings.Contains(titles[1], "Hal made Hector owner") && !strings.Contains(titles[0], "Hal made Hector owner") {
		t.Errorf("Hal reads: %v", titles)
	}
	// Hector's row ends; he stays a viewer through the team, and keeps his choice.
	must(t, w.s.SetOwnAlerts(ctx, w.access(t, w.hector.PersonID), w.theirs, false, w.at))
	must(t, w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.hector.PersonID, ""))
	if role, _ := w.access(t, w.hector.PersonID).Role(w.theirs); role != Viewer {
		t.Errorf("Hector is %s", role)
	}
	if err := w.s.SetProjectRoleAs(ctx, w.hal, w.theirs, w.hector.PersonID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("ending a role that is not there: %v", err)
	}
	// In a team with no owner (R32), the last owner of a project stays.
	_, err = w.s.w.Exec(`DELETE FROM team_members WHERE team_id = ? AND person_id = ?`, w.second, w.hal.PersonID)
	must(t, err)
	if err := w.s.SetProjectRoleAs(ctx, carol, w.theirs, w.carol.PersonID, Editor); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner: %v", err)
	}
}
