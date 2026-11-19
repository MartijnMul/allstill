// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// Two teams on one installation, at the store (intent/teams/plan-2.md, part A).
// The walk over every page and operation is in internal/web/teams_test.go; this
// holds the rules it rests on.

type wall struct {
	s  *Store
	ws string
	// The first team is the installation's, owned by ada, an administrator. The
	// second is made beside it. Each has a project named Backups.
	first, second     string
	ours, theirs      string // a project of each
	ada, zed          Access // administrators: ada owns the first team, zed is in none
	hal, hedy, hector Access // the second team's owner, editor and viewer
	carol             Access // an editor of theirs by a row of her own, in no team
	at                time.Time
}

func (w *wall) person(t *testing.T, name string, admin, inNoTeam bool) string {
	t.Helper()
	id, err := w.s.CreatePerson(t.Context(), Command("test"), w.ws, NewPerson{Name: name, WorkspaceAdmin: admin, InNoTeam: inNoTeam})
	must(t, err)
	return id
}

func (w *wall) access(t *testing.T, personID string) Access {
	t.Helper()
	a, err := w.s.AccessOfPerson(t.Context(), w.ws, personID)
	must(t, err)
	return a
}

func newWall(t *testing.T) *wall {
	t.Helper()
	ctx := t.Context()
	w := &wall{s: open(t), at: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	var err error
	w.ws, err = w.s.Workspace(ctx)
	must(t, err)
	by := Command("test")
	ada := w.person(t, "Ada", true, false)
	teams, err := w.s.Teams(ctx, w.ws)
	must(t, err)
	w.first = teams[0].ID
	w.second, err = w.s.CreateTeam(ctx, by, w.ws, "Harbour")
	must(t, err)
	w.ours, _, err = w.s.CreateProjectIn(ctx, by, w.ws, w.first, "Backups")
	must(t, err)
	w.theirs, _, err = w.s.CreateProjectIn(ctx, by, w.ws, w.second, "Backups")
	must(t, err)
	zed := w.person(t, "Zed", true, true)
	hal, hedy, hector, carol := w.person(t, "Hal", false, false), w.person(t, "Hedy", false, false), w.person(t, "Hector", false, false), w.person(t, "Carol", false, false)
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hal, Owner))
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hedy, Editor))
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hector, Viewer))
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, carol, Editor))
	w.ada, w.zed, w.hal, w.hedy, w.hector, w.carol = w.access(t, ada), w.access(t, zed), w.access(t, hal), w.access(t, hedy), w.access(t, hector), w.access(t, carol)
	return w
}

// An administrator has no project by being one: what they see comes from a
// team or a role of their own, as for anyone (R8).
func TestAnAdministratorHasNoProjectByBeingOne(t *testing.T) {
	w := newWall(t)
	if len(w.zed.Projects) != 0 || len(w.zed.Teams) != 0 || !w.zed.Admin {
		t.Errorf("an administrator in no team: %+v", w.zed)
	}
	if _, ok := w.ada.Role(w.theirs); ok || len(w.ada.Projects) != 1 {
		t.Errorf("an administrator who owns the first team: %+v", w.ada.Projects)
	}
	for who, a := range map[string]Access{"owner": w.hal, "editor": w.hedy, "viewer": w.hector} {
		role, ok := a.Role(w.theirs)
		if _, sees := a.Role(w.ours); !ok || string(role) != who || sees {
			t.Errorf("the second team's %s: %+v", who, a.Projects)
		}
	}
	if t2, in := w.carol.TeamOf(w.theirs); in || t2.Name != "" || len(w.carol.Teams) != 0 {
		t.Errorf("someone with a row of their own is told the team: %+v", t2)
	}
	// A read-only sign-in is a viewer in its teams too.
	if ro := w.hal.ReadOnly(); ro.OwnsTeamOf(w.theirs) || ro.Teams[0].Role != Viewer {
		t.Errorf("%+v", ro.Teams)
	}
}

// A name is told apart within a team, and what another team holds is never
// told (R5).
func TestANameIsToldApartWithinATeam(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	by := Command("test")
	if _, err := w.s.ProjectByName(ctx, w.ws, "Backups"); !errors.Is(err, ErrSeveralTeams) {
		t.Errorf("a name two teams hold, with no team said: %v", err)
	}
	if id, err := w.s.ProjectByNameIn(ctx, w.ws, w.second, "Backups"); err != nil || id != w.theirs {
		t.Errorf("%v %v", id, err)
	}
	other, _, err := w.s.CreateProjectIn(ctx, by, w.ws, w.second, "Sites")
	must(t, err)
	_, _, err = w.s.CreateProjectIn(ctx, by, w.ws, w.first, "Sites")
	must(t, err)
	var invalid *InvalidError
	if err := w.s.RenameProject(ctx, by, w.ws, other, "Backups"); !errors.As(err, &invalid) {
		t.Errorf("a name its own team holds: %v", err)
	}
	must(t, w.s.RenameProject(ctx, by, w.ws, other, "Ledgers"))
	mine, _, err := w.s.CreateProjectIn(ctx, by, w.ws, w.first, "Ledgers2")
	must(t, err)
	if err := w.s.RenameProject(ctx, by, w.ws, mine, "Ledgers"); err != nil {
		t.Errorf("a name only another team holds is free: %v", err)
	}
	if _, _, err := w.s.CreateProjectIn(ctx, by, w.ws, "team_none", "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a team that does not exist: %v", err)
	}
	// Someone who sees two projects of one name reads the team with it, for a
	// team they are in, and is not told a team they are not in.
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, w.ada.PersonID, Viewer))
	ada := w.access(t, w.ada.PersonID)
	if got := ada.Label(w.ours); got != "Allstill / Backups" {
		t.Errorf("their own team's: %q", got)
	}
	if got := ada.Label(w.theirs); got != "Backups" {
		t.Errorf("another team's: %q", got)
	}
	if got := w.hal.Label(w.theirs); got != "Backups" {
		t.Errorf("the only one they see: %q", got)
	}
}

// A check stays in its team, and a probe serves its own team's projects (R24, R37).
func TestChecksAndProbesStayInTheirTeam(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	admin, err := w.s.AdminAccess(ctx, w.ws, "test")
	must(t, err)
	c, err := w.s.CreateCheck(ctx, admin, w.ours, NewCheck{Name: "nightly", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	var invalid *InvalidError
	if err := w.s.MoveCheck(ctx, Command("test"), w.ws, c.ID, w.theirs, w.at); !errors.As(err, &invalid) || !strings.Contains(invalid.Problem, "another team") {
		t.Errorf("a command moving a check to another team: %v", err)
	}
	// Someone who can change both is told why; someone who cannot see the other
	// is told it does not exist.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.ada.PersonID, Editor))
	if err := w.s.MoveCheckAs(ctx, w.access(t, w.ada.PersonID), c.ID, w.theirs, w.at); !errors.As(err, &invalid) || !strings.Contains(invalid.Problem, "another team") {
		t.Errorf("someone in both: %v", err)
	}
	if err := w.s.MoveCheckAs(ctx, w.ada, c.ID, w.theirs, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone who cannot see it: %v", err)
	}
	if got, _ := db.New(w.s.r).ProjectOfCheck(ctx, db.ProjectOfCheckParams{WorkspaceID: w.ws, ID: c.ID}); got != w.ours {
		t.Errorf("the check moved to %s", got)
	}

	if _, _, err := w.s.CreateProbe(ctx, admin, "edge", w.ours, []string{w.theirs}); !errors.As(err, &invalid) || invalid.Field != "serves" {
		t.Errorf("a probe serving two teams: %v", err)
	}
	_, _, err = w.s.CreateProbe(ctx, admin, "edge", w.ours, nil)
	must(t, err)
	theirProbe, _, err := w.s.CreateProbe(ctx, admin, "edge", w.theirs, nil)
	if err != nil {
		t.Fatalf("the same name in another team: %v", err)
	}
	if _, _, err := w.s.CreateProbe(ctx, admin, "edge", w.theirs, nil); !errors.As(err, &invalid) || invalid.Field != "name" {
		t.Errorf("the same name twice in one team: %v", err)
	}
	if id, err := w.s.ProbeIDByName(ctx, w.ws, w.theirs, "edge"); err != nil || id != theirProbe {
		t.Errorf("a probe by name, from a project of its team: %v %v", id, err)
	}
	if _, err := w.s.RekeyProbe(ctx, admin, "", "edge"); !errors.Is(err, ErrSeveralTeams) {
		t.Errorf("a new key for a name two teams hold: %v", err)
	}
	if _, err := w.s.RekeyProbe(ctx, admin, w.second, "edge"); err != nil {
		t.Error(err)
	}
	// A probe's own check is its team's owner's to change, not an administrator's.
	own := w.s.r.QueryRow(`SELECT check_id FROM probes WHERE id = ?`, theirProbe)
	var ownCheck string
	must(t, own.Scan(&ownCheck))
	if err := w.s.SetAlertingAs(ctx, w.hedy, ownCheck, "digest", false, w.at); !errors.As(err, &invalid) {
		t.Errorf("an editor changing a probe's own check: %v", err)
	}
	if err := w.s.SetAlertingAs(ctx, w.hal, ownCheck, "digest", false, w.at); err != nil {
		t.Errorf("the team's owner: %v", err)
	}
}

// A row of one's own is kept only where it gives more than the team does, and
// who is told follows what was asked.
func TestARoleOfOnesOwnBesideATeam(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	by := Command("test")
	told := func(person string) bool {
		t.Helper()
		n, err := db.New(w.s.r).IsAlerted(ctx, db.IsAlertedParams{ProjectID: w.theirs, PersonID: person})
		must(t, err)
		return n > 0
	}
	row := func(person string) string {
		t.Helper()
		var role string
		must(t, w.s.r.QueryRow(`SELECT COALESCE((SELECT role FROM memberships WHERE project_id = ? AND person_id = ?), '')`, w.theirs, person).Scan(&role))
		return role
	}
	hal, hedy, hector := w.hal.PersonID, w.hedy.PersonID, w.hector.PersonID
	// Viewer for the team's owner only says not to tell them; none tells them again.
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hal, Viewer))
	if row(hal) != "" || told(hal) || w.access(t, hal).Projects[0].Role != Owner {
		t.Errorf("an owner set to viewer: row %q, told %v", row(hal), told(hal))
	}
	must(t, w.s.RemoveMember(ctx, by, w.ws, w.theirs, hal))
	if !told(hal) {
		t.Error("none did not give the alerts back")
	}
	if err := w.s.RemoveMember(ctx, by, w.ws, w.theirs, hal); !errors.Is(err, ErrNotFound) {
		t.Errorf("none with no row and no choice: %v", err)
	}
	// A role above the team's is a real row; lowering it to the team's takes it away.
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hector, Editor))
	if row(hector) != "editor" || !told(hector) {
		t.Errorf("a viewer of the team made editor of one project: row %q, told %v", row(hector), told(hector))
	}
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hector, Viewer))
	if row(hector) != "" || told(hector) {
		t.Errorf("back to what the team gives: row %q, told %v", row(hector), told(hector))
	}
	if n := lines(t, w.s.r, `SELECT count(*) FROM alert_choices`); n[0] != "0" {
		t.Errorf("%s choices that say what the role says", n[0])
	}
	// Raised in the team, a row that no longer gives more goes, and whether they
	// are told stays.
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hector, Editor))
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hector, Owner))
	if row(hector) != "" || !told(hector) {
		t.Errorf("a row the team role overtook: row %q, told %v", row(hector), told(hector))
	}
	// A viewer of one project who is made editor of its team is told about it,
	// as about the team's other projects: not telling a viewer was never a choice.
	vera := w.person(t, "Vera", false, false)
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, vera, Viewer))
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, vera, Editor))
	if row(vera) != "" || !told(vera) {
		t.Errorf("a viewer of one project made editor of its team: row %q, told %v", row(vera), told(vera))
	}
	// The last owner stays one. With two, one can go, and loses the project at once.
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hector, Viewer))
	if err := w.s.SetTeamMember(ctx, by, w.ws, w.second, hal, Editor); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner made less: %v", err)
	}
	if err := w.s.SetTeamMember(ctx, by, w.ws, w.second, hal, ""); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner taken out: %v", err)
	}
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hedy, Viewer)) // a choice: do not tell
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hedy, ""))
	if len(w.access(t, hedy).Projects) != 0 {
		t.Error("taken out of the team, they still see its project")
	}
	if n := lines(t, w.s.r, `SELECT count(*) FROM alert_choices WHERE person_id = '`+hedy+`'`); n[0] != "0" {
		t.Error("their choice about a project they no longer see stayed")
	}
	// A row of their own stays when they leave the team (R21).
	must(t, w.s.SetMember(ctx, by, w.ws, w.theirs, hector, Editor))
	must(t, w.s.SetTeamMember(ctx, by, w.ws, w.second, hector, ""))
	if got := w.access(t, hector); len(got.Projects) != 1 || got.Projects[0].Role != Editor || len(got.Teams) != 0 {
		t.Errorf("%+v", got)
	}
}

// Who reads which line of the log, and whom it names to them (R40; activity-log R22).
func TestTheLogBehindTheWall(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	admin, err := w.s.AdminAccess(ctx, w.ws, "test")
	must(t, err)
	c, err := w.s.CreateCheck(ctx, admin, w.theirs, NewCheck{Name: "tide-table", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	must(t, w.s.PauseCheck(ctx, w.hedy, c.ID, w.at))               // by someone in through the team
	must(t, w.s.ResumeCheck(ctx, w.carol, c.ID, w.at))             // by someone with a row of their own
	_, _, err = w.s.CreateProbe(ctx, admin, "dock", w.theirs, nil) // a line of the team, about no project
	must(t, err)
	q := db.New(w.s.w)
	// A line from before this step: an administrator with no row of their own.
	_, err = w.s.w.ExecContext(ctx, `INSERT INTO log_entries (workspace_id, happened_at, recorded_at, project_id, project_name, kind, about_kind, about_id, about_name, by_kind, by_id, by_name, by_outsider)
		VALUES (?, 1, 1, ?, 'Backups', 'check.paused', 'check', ?, 'tide-table', 'person', 'per_old', 'Olga', 1)`, w.ws, w.theirs, c.ID)
	must(t, err)
	// A message that reached Hedy by no way, about an incident of two checks.
	for _, name := range []string{"tide-table", "berth", "crane", "quay"} {
		must(t, writeLog(ctx, q, logEntry{
			WorkspaceID: w.ws, At: w.at, ProjectID: w.theirs, Kind: unreached, AboutKind: "check", AboutID: c.ID, AboutName: name,
			By: byAllstill, Person: Actor{ID: w.hedy.PersonID, Name: "Hedy"}, IncidentID: "inc_1", Details: map[string]any{"why": "the person has no way to be reached"},
		}))
	}

	read := func(a Access, f LogFilter) []LogEntry {
		t.Helper()
		f.Limit = 200
		page, err := w.s.ReadLog(ctx, a, f)
		must(t, err)
		return page.Entries
	}
	who := func(es []LogEntry, kind string) string {
		for _, e := range es {
			if e.Kind == kind && e.At.UnixMilli() != 1 {
				return e.By.Kind + ":" + e.By.Name
			}
		}
		return "unread"
	}
	old := func(es []LogEntry) string {
		for _, e := range es {
			if e.Kind == "check.paused" && e.At.UnixMilli() == 1 {
				return e.By.Kind + ":" + e.By.Name
			}
		}
		return "unread"
	}
	for _, tc := range []struct {
		name                           string
		a                              Access
		paused, resumed, before, probe string
	}{
		// In the team as more than a viewer: its people are named, on old lines too.
		{"the team's owner", w.hal, "person:Hedy", "person:Carol", "person:Olga", "command:test"},
		{"the team's editor", w.hedy, "person:Hedy", "person:Carol", "person:Olga", "unread"},
		// A viewer reads "the team" for every person, in the team or not.
		{"the team's viewer", w.hector, "team:", "team:", "team:", "unread"},
		// An editor by a row of their own, outside the team: who is in the team is
		// the team's to say, and an old line still says "an administrator".
		{"an editor outside the team", w.carol, "team:", "person:Carol", "administrator:", "unread"},
		// An administrator reads what concerns the installation, and no project.
		{"an administrator in no team", w.zed, "unread", "unread", "unread", "command:test"},
		{"an administrator who owns another team", w.ada, "unread", "unread", "unread", "command:test"},
	} {
		es := read(tc.a, LogFilter{})
		if got := [4]string{who(es, "check.paused"), who(es, "check.resumed"), old(es), who(es, "probe.created")}; got != [4]string{tc.paused, tc.resumed, tc.before, tc.probe} {
			t.Errorf("%s reads the pause, the resume, the old line and the probe as %v, want %v", tc.name, got, [4]string{tc.paused, tc.resumed, tc.before, tc.probe})
		}
	}
	// What an administrator reads of a message that reached nobody: who and why,
	// once, and nothing it was about, however they narrow.
	var failed []LogEntry
	for _, e := range read(w.zed, LogFilter{}) {
		if e.Kind == unreached {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("%d lines for one person and incident: %+v", len(failed), failed)
	}
	// Wherever a page begins, the same one line: starting at each line in turn
	// must not count the checks.
	shown := map[int64]bool{}
	for _, row := range entries(t, w.s) {
		page, err := w.s.ReadLog(ctx, w.zed, LogFilter{Before: row.ID + 1, Limit: 200})
		must(t, err)
		for _, e := range page.Entries {
			if e.Kind == unreached {
				shown[e.ID] = true
			}
		}
	}
	if len(shown) != 1 {
		t.Errorf("an administrator can count the checks of an incident by where a page begins: %d lines", len(shown))
	}
	e := failed[0]
	if e.Person.Name != "Hedy" || e.Details["why"] == "" || e.ProjectID != "" || e.ProjectName != "" || e.AboutID != "" || e.AboutName != "" || e.Group != "" {
		t.Errorf("%+v", e)
	}
	for name, f := range map[string]LogFilter{
		"by project": {ProjectID: w.theirs}, "by check": {CheckID: c.ID}, "by name": {Name: "tide-table"},
	} {
		if es := read(w.zed, f); len(es) != 0 {
			t.Errorf("an administrator narrowing %s reads %+v", name, es)
		}
	}
	if es := read(w.zed, LogFilter{PersonID: w.hedy.PersonID}); !slices.ContainsFunc(es, func(e LogEntry) bool { return e.Kind == unreached }) {
		t.Error("an administrator cannot find whose messages fail")
	}
	// The team's own people read it whole, with the reason left to administrators.
	for _, e := range read(w.hal, LogFilter{}) {
		if e.Kind == unreached && (e.AboutName == "" || e.Details["why"] != nil) {
			t.Errorf("%+v", e)
		}
	}
	// The probe's line lists what it serves to an administrator who can see it.
	for _, e := range read(w.zed, LogFilter{}) {
		if serves, _ := e.Details["serves"].([]any); e.Kind == "probe.created" && len(serves) != 0 {
			t.Errorf("an administrator in no team reads what a probe serves: %v", serves)
		}
	}
	// A line about a person names them only to those who may read who it concerns.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.carol.PersonID, Owner))
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.hedy.PersonID, Viewer)) // a choice, of someone in the team
	carol := w.access(t, w.carol.PersonID)
	for _, e := range read(carol, LogFilter{}) {
		if e.Kind == "alerts.chosen" && (e.Person.Kind != "team" || e.AboutName != "" || e.AboutID != "") {
			t.Errorf("an owner outside the team reads whose alerts were stopped: %+v", e)
		}
	}
	if es := read(carol, LogFilter{Name: "Hedy"}); len(es) != 0 {
		t.Errorf("an owner outside the team finds someone of the team by name: %+v", es)
	}
	if es := read(w.hal, LogFilter{Name: "Hedy"}); !slices.ContainsFunc(es, func(e LogEntry) bool { return e.Kind == "alerts.chosen" }) {
		t.Error("the team's owner does not find them")
	}
	// The rest of the rule, by kind of line: a team's own, the installation's,
	// and one's own.
	link := func() {
		_, err := w.s.CreatePasskeyLink(ctx, Command("passkey-link"), w.ws, w.hector.PersonID, w.at)
		must(t, err)
	}
	link()
	stranger := w.access(t, w.person(t, "Nobody", false, false))
	for _, tc := range []struct {
		name                            string
		a                               Access
		teamLine, installation, hectors string
	}{
		{"the team's owner", w.hal, "command:test", "unread", "unread"},
		{"the team's editor", w.hedy, "unread", "unread", "unread"},
		{"the team's viewer, whom the link is for", w.hector, "unread", "unread", "team:"},
		{"an owner outside the team", carol, "unread", "unread", "unread"},
		{"an administrator in no team", w.zed, "unread", "command:test", "command:passkey-link"},
		{"an administrator who owns another team", w.ada, "unread", "command:test", "command:passkey-link"},
		{"someone in nothing", stranger, "unread", "unread", "unread"},
	} {
		es := read(tc.a, LogFilter{})
		if got := [3]string{who(es, "team.member_set"), who(es, "person.added"), who(es, "passkey.link_made")}; got != [3]string{tc.teamLine, tc.installation, tc.hectors} {
			t.Errorf("%s reads a line of the team, of the installation and of one person as %v, want %v", tc.name, got, [3]string{tc.teamLine, tc.installation, tc.hectors})
		}
	}
	if es := read(stranger, LogFilter{}); len(es) != 0 {
		t.Errorf("someone in nothing reads %+v", es)
	}
	// Every kind written in a project has a role that reads it.
	for _, e := range entries(t, w.s) {
		if _, listed := logMinRole[e.Kind]; e.ProjectID != "" && !listed {
			t.Errorf("%s is written in a project and nobody reads it: give it a role in logMinRole", e.Kind)
		}
	}
}

// Narrowing by a name or a person reads where the reader may read, so a name
// that is busy in another team is not felt as pages cut short.
func TestNarrowingDoesNotReadThroughAnotherTeam(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	admin, err := w.s.AdminAccess(ctx, w.ws, "test")
	must(t, err)
	mine, err := w.s.CreateCheck(ctx, admin, w.theirs, NewCheck{Name: "backup.sh", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	busy, err := w.s.CreateCheck(ctx, admin, w.ours, NewCheck{Name: "backup.sh", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	must(t, w.s.PauseCheck(ctx, w.hedy, mine.ID, w.at))
	for range 40 {
		must(t, w.s.PauseCheck(ctx, w.ada, busy.ID, w.at))
		must(t, w.s.ResumeCheck(ctx, w.ada, busy.ID, w.at))
	}
	was := logScanLimit
	logScanLimit = 20
	t.Cleanup(func() { logScanLimit = was })
	for name, f := range map[string]LogFilter{"a name": {Name: "backup.sh"}, "a person": {PersonID: w.ada.PersonID}} {
		page, err := w.s.ReadLog(ctx, w.hedy, f)
		must(t, err)
		if page.Older != 0 {
			t.Errorf("narrowing to %s that is busy elsewhere gives a way on, as if there were more: %+v", name, page)
		}
	}
	page, err := w.s.ReadLog(ctx, w.hedy, LogFilter{Name: "backup.sh"})
	must(t, err)
	if len(page.Entries) != 2 {
		t.Errorf("their own check by its name: %+v", page.Entries)
	}
}

// What a team may take is its own: the first team has no limit, a new one
// starts with one, and reaching it refuses a new check without stopping one
// that exists (R34, R36).
func TestATeamHasItsOwnLimits(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	by := Command("set-team")
	teams, err := w.s.TeamSummaries(ctx, w.ws)
	must(t, err)
	if first, second := teams[0], teams[1]; first.MaxActive != -1 || first.MaxPing != -1 || !first.Private ||
		second.MaxActive != NewTeamActiveChecks || second.MaxPing != NewTeamPingChecks || second.Private {
		t.Fatalf("the first team %+v, a new team %+v", first, second)
	}
	if second := teams[1]; second.People != 3 || second.Projects != 1 || len(second.Owners) != 1 || second.Owners[0] != "Hal" {
		t.Errorf("%+v", second)
	}
	one, none := 1, -1
	must(t, w.s.SetTeam(ctx, by, w.ws, w.second, TeamChange{MaxActive: &one, MaxPing: &one}))
	ping := NewCheck{Name: "job", Interval: time.Hour, Grace: time.Hour}
	site := NewCheck{Name: "site", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://example.com/"}}
	first, err := w.s.CreateCheck(ctx, w.hedy, w.theirs, ping)
	must(t, err)
	_, err = w.s.CreateCheck(ctx, w.hedy, w.theirs, site)
	must(t, err)
	var invalid *InvalidError
	for kind, n := range map[string]NewCheck{"a check jobs ping": ping, "a check Allstill runs": site} {
		n.Name += "-2"
		if _, err := w.s.CreateCheck(ctx, w.hedy, w.theirs, n); !errors.As(err, &invalid) || !strings.Contains(invalid.Problem, "the administrator of this installation can raise that") {
			t.Errorf("%s past the team's limit: %v", kind, err)
		}
	}
	// A second project of the team counts toward the same limit, and another
	// team is not held to it. Moving a check there adds nothing to the team, so
	// a full team can still move one.
	other, _, err := w.s.CreateProjectIn(ctx, Command("test"), w.ws, w.second, "Sites")
	must(t, err)
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, other, w.hedy.PersonID, Editor))
	if _, err := w.s.CreateCheck(ctx, w.access(t, w.hedy.PersonID), other, ping); !errors.As(err, &invalid) {
		t.Errorf("past the limit in another project of the team: %v", err)
	}
	if _, err := w.s.CreateCheck(ctx, w.ada, w.ours, ping); err != nil {
		t.Errorf("another team: %v", err)
	}
	if err := w.s.MoveCheckAs(ctx, w.access(t, w.hedy.PersonID), first.ID, other, w.at); err != nil {
		t.Errorf("a full team moving a check between its projects: %v", err)
	}
	must(t, w.s.MoveCheckAs(ctx, w.access(t, w.hedy.PersonID), first.ID, w.theirs, w.at))
	// A ping that would make a check makes none, and says no more than that.
	_, key, err := w.s.CreateProjectIn(ctx, Command("test"), w.ws, w.second, "Jobs")
	must(t, err)
	if _, err := w.s.Ping(ctx, PingIn{PingKey: key, Slug: "made-by-a-ping", Create: true, Kind: "success", ExitStatus: -1, Method: "POST", At: w.at}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a ping with create=1 past the team's limit: %v", err)
	}
	// A command on the server is not held to it, as it is not held to a project's.
	admin, err := w.s.AdminAccess(ctx, w.ws, "add-check")
	must(t, err)
	if _, err := w.s.CreateCheck(ctx, admin, w.theirs, NewCheck{Name: "by-command", Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Errorf("a command: %v", err)
	}
	// Nothing that exists stopped, and with the limit gone there is room again.
	var standing int
	must(t, w.s.r.QueryRow(`SELECT count(*) FROM checks WHERE id = ? AND deleted_at IS NULL AND state <> 'paused'`, first.ID).Scan(&standing))
	if standing != 1 {
		t.Error("a check that existed was stopped")
	}
	must(t, w.s.SetTeam(ctx, by, w.ws, w.second, TeamChange{MaxPing: &none}))
	ping.Name = "job-3"
	if _, err := w.s.CreateCheck(ctx, w.hedy, w.theirs, ping); err != nil {
		t.Errorf("with no limit: %v", err)
	}
	// Whether its checks may reach the server's private ranges is the team's too.
	for project, want := range map[string]bool{w.ours: true, w.theirs: false} {
		if got, err := w.s.ReachesPrivate(ctx, project); err != nil || got != want {
			t.Errorf("private ranges for %s: %v %v", project, got, err)
		}
	}
	yes := true
	must(t, w.s.SetTeam(ctx, by, w.ws, w.second, TeamChange{Private: &yes}))
	if got, _ := w.s.ReachesPrivate(ctx, w.theirs); !got {
		t.Error("opened, they are still closed")
	}
	// The loop is told whose each check is, and what it may reach.
	checks, err := w.s.ActiveChecks(ctx)
	must(t, err)
	for _, c := range checks {
		if c.TeamID != w.second || !c.Private {
			t.Errorf("%+v", c)
		}
	}
	// The owners read that their team was changed; an administrator does too.
	for who, a := range map[string]Access{"the owner": w.hal, "an administrator": w.zed} {
		page, err := w.s.ReadLog(ctx, a, LogFilter{Limit: 200})
		must(t, err)
		if !slices.ContainsFunc(page.Entries, func(e LogEntry) bool { return e.Kind == "team.changed" }) {
			t.Errorf("%s does not read that the team was changed", who)
		}
	}
}

// A team that holds anything stays, and the installation keeps one (R33).
func TestRemovingATeam(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	by := Command("remove-team")
	var holds *TeamHolds
	if err := w.s.RemoveTeam(ctx, by, w.ws, w.second); !errors.As(err, &holds) || holds.Projects != 1 {
		t.Fatalf("a team with a project: %v", err)
	}
	empty, err := w.s.CreateTeam(ctx, Command("create-team"), w.ws, "Harbour")
	must(t, err)
	if _, err := w.s.TeamNamed(ctx, w.ws, "Harbour"); err == nil || !strings.Contains(err.Error(), empty) {
		t.Errorf("two teams of one name: %v", err)
	}
	if got, err := w.s.TeamNamed(ctx, w.ws, empty); err != nil || got.ID != empty {
		t.Errorf("by id: %v %v", got, err)
	}
	dora := w.person(t, "Dora", false, false)
	must(t, w.s.SetTeamMember(ctx, by, w.ws, empty, dora, Owner))
	must(t, w.s.RemoveTeam(ctx, by, w.ws, empty))
	if _, err := w.s.TeamNamed(ctx, w.ws, empty); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed, it is still there: %v", err)
	}
	if a := w.access(t, dora); len(a.Teams) != 0 {
		t.Errorf("its owner is still in it: %+v", a.Teams)
	}
	// On an installation with one team, that one stays.
	s := open(t)
	ws, err := s.Workspace(ctx)
	must(t, err)
	teams, err := s.Teams(ctx, ws)
	must(t, err)
	var invalid *InvalidError
	if err := s.RemoveTeam(ctx, by, ws, teams[0].ID); !errors.As(err, &invalid) {
		t.Errorf("the only team: %v", err)
	}
}
