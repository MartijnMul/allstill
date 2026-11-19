// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A command on the server that reaches into a team tells the team's owners,
// and nobody else (intent/teams/spec.md, R27, R28).
func TestACommandOnTheServerTellsTheTeamsOwners(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	titles := func(a Access) []string {
		t.Helper()
		ns, err := w.s.Notices(ctx, a.PersonID, 50)
		must(t, err)
		var out []string
		for _, n := range ns {
			out = append(out, n.Title)
		}
		return out
	}
	// Building the wall told owners of who was let into their teams, themselves
	// among them. That is read.
	for _, a := range []Access{w.ada, w.zed, w.hal, w.hedy, w.hector, w.carol} {
		must(t, w.s.ReadNotices(ctx, a.PersonID, time.Now().Add(time.Hour)))
	}
	unread := func(a Access) []string {
		t.Helper()
		ns, err := w.s.Notices(ctx, a.PersonID, 50)
		must(t, err)
		var out []string
		for _, n := range ns {
			if !n.Read {
				out = append(out, n.Title)
			}
		}
		return out
	}
	before := len(titles(w.hal))
	told := func(what string, do func(by Actor) error, want string) {
		t.Helper()
		// Done by a person, it tells nobody: they are in the log by name.
		n := len(titles(w.hal))
		must(t, do(w.hal.Actor))
		if got := titles(w.hal); len(got) != n {
			t.Errorf("%s, by a person, told the owner: %v", what, got[0])
		}
		must(t, do(Command("the-command")))
		got := titles(w.hal)
		if len(got) != n+1 || !strings.Contains(got[0], want) || !strings.Contains(got[0], "A command on the server (the-command)") {
			t.Errorf("%s: the owner was told %v, want one more, saying %q", what, got, want)
		}
	}
	dora := w.person(t, "Dora", false, false)
	told("a role in one project", func(by Actor) error {
		role := Viewer
		if by.Kind == "command" {
			role = Editor // a change each time, as the same role again does nothing
		}
		return w.s.SetMember(ctx, by, w.ws, w.theirs, dora, role)
	}, "made Dora editor of Backups")
	told("a role in the team", func(by Actor) error {
		role := Editor
		if by.Kind == "command" {
			role = Owner // a change each time, as the same role again does nothing
		}
		return w.s.SetTeamMember(ctx, by, w.ws, w.second, dora, role)
	}, "made Dora owner of the team Harbour")
	must(t, w.s.SetTeamMember(ctx, Command("test"), w.ws, w.second, dora, Viewer))
	must(t, w.s.ReadNotices(ctx, w.hal.PersonID, time.Now().Add(2*time.Hour)))
	// A role that gives nothing, or the same role again, lets nobody in, and is
	// not told: an owner of the team set to viewer of one project only stops
	// their own alerts.
	n0 := len(titles(w.hal))
	must(t, w.s.SetMember(ctx, Command("set-member"), w.ws, w.theirs, w.hal.PersonID, Viewer))
	must(t, w.s.SetMember(ctx, Command("set-member"), w.ws, w.theirs, dora, Editor)) // told: she is a viewer of the team
	must(t, w.s.SetMember(ctx, Command("set-member"), w.ws, w.theirs, dora, Editor)) // the same again: not told
	if got := titles(w.hal); len(got) != n0+1 {
		t.Errorf("a role that lets nobody in was told: %v", got[0])
	}
	// Taking someone out is told, to an owner who goes as well.
	must(t, w.s.SetTeamMember(ctx, Command("test"), w.ws, w.second, dora, Owner))
	must(t, w.s.SetTeamMember(ctx, Command("set-team-member"), w.ws, w.second, dora, ""))
	for who, id := range map[string]string{"the owner who stays": w.hal.PersonID, "the owner who went": dora} {
		ns, err := w.s.Notices(ctx, id, 50)
		must(t, err)
		if !slices.ContainsFunc(ns, func(n Notice) bool { return strings.Contains(n.Title, "took Dora out of the team Harbour") }) {
			t.Errorf("%s was not told that she was taken out", who)
		}
	}
	must(t, w.s.ReadNotices(ctx, w.hal.PersonID, time.Now().Add(2*time.Hour)))
	// Taking away a passkey is told.
	link, err := w.s.CreatePasskeyLink(ctx, Command("test"), w.ws, w.hedy.PersonID, time.Now().Add(-time.Hour))
	must(t, err)
	_, err = w.s.AddPasskey(ctx, link, key("hedy"), "a browser", time.Now())
	must(t, err)
	keys, err := w.s.Passkeys(ctx, w.ws, w.hedy.PersonID)
	must(t, err)
	told("a passkey taken away", func(by Actor) error {
		if by.Kind != "command" {
			return nil // only a command takes away someone else's
		}
		_, _, err := w.s.RemovePasskey(ctx, by, w.ws, w.hedy.PersonID, keys[0].ID, time.Now().Add(time.Hour))
		return err
	}, "took away a passkey of Hedy")
	// A sign-in link lets whoever holds it become that person.
	later := time.Now().Add(time.Hour)
	told("a sign-in link", func(by Actor) error {
		_, err := w.s.CreatePasskeyLink(ctx, by, w.ws, w.hedy.PersonID, later)
		return err
	}, "made a sign-in link for Hedy")
	told("every sign-in ended", func(by Actor) error {
		_, _, err := w.s.EndEverySignIn(ctx, by, w.ws, w.hedy.PersonID, later)
		return err
	}, "ended every sign-in of Hedy")
	// Also for someone with only a role of their own in one of its projects.
	told("a link for someone with one project", func(by Actor) error {
		_, err := w.s.CreatePasskeyLink(ctx, by, w.ws, w.carol.PersonID, later)
		return err
	}, "made a sign-in link for Carol")
	// The first link of someone added a moment ago is how they come in: adding
	// them was told, and the link is not told again.
	n := len(titles(w.hal))
	fresh, err := w.s.CreateMember(ctx, Command("create-user"), w.ws, NewPerson{Name: "Finn"}, []string{w.theirs}, Editor)
	must(t, err)
	_, err = w.s.CreatePasskeyLink(ctx, Command("create-user"), w.ws, fresh, time.Now())
	must(t, err)
	if got := strings.Join(titles(w.hal), "\n"); len(titles(w.hal)) != n+1 || !strings.Contains(got, "made Finn editor of Backups") || strings.Contains(got, "link for Finn") {
		t.Errorf("adding someone: %v", got)
	}
	// Nobody else was told any of it: not the team's editor or viewer, the
	// other team's owner, or an administrator who is in neither.
	for who, a := range map[string]Access{"the team's editor": w.hedy, "the team's viewer": w.hector, "another team's owner": w.ada, "an administrator in no team": w.zed, "someone with one project": w.carol} {
		if got := unread(a); len(got) != 0 {
			t.Errorf("%s was told %v", who, got)
		}
	}
	// Someone in no team and no project: nobody's to be told about.
	stranger := w.person(t, "Nobody", false, false)
	n = len(titles(w.hal))
	_, err = w.s.CreatePasskeyLink(ctx, Command("passkey-link"), w.ws, stranger, later)
	must(t, err)
	if len(titles(w.hal)) != n {
		t.Error("a link for someone in nothing was told to a team")
	}
	// Read, they stay listed and are new no longer.
	ns, err := w.s.Notices(ctx, w.hal.PersonID, 50)
	must(t, err)
	if !slices.ContainsFunc(ns, func(n Notice) bool { return !n.Read }) || len(ns) <= before {
		t.Fatalf("%+v", ns)
	}
	must(t, w.s.ReadNotices(ctx, w.hal.PersonID, time.Now().Add(2*time.Hour)))
	ns, err = w.s.Notices(ctx, w.hal.PersonID, 50)
	must(t, err)
	if slices.ContainsFunc(ns, func(n Notice) bool { return !n.Read }) {
		t.Errorf("still new after reading: %+v", ns)
	}
}

// An administrator made on a one-team installation owns its team, and the
// owners there are told: one administrator cannot quietly make another.
func TestMakingAnAdministratorTellsTheOwners(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, err := s.Workspace(ctx)
	must(t, err)
	ada, err := s.CreatePerson(ctx, Command("create-user"), ws, NewPerson{Name: "Ada", WorkspaceAdmin: true})
	must(t, err)
	_, err = s.CreatePerson(ctx, Command("create-user"), ws, NewPerson{Name: "Bea", WorkspaceAdmin: true})
	must(t, err)
	ns, err := s.Notices(ctx, ada, 10)
	must(t, err)
	// She was told of herself, and now of Bea.
	if got := ns[0].Title + ns[1].Title; len(ns) != 2 || !strings.Contains(got, "made Bea owner of the team Allstill") {
		t.Errorf("%+v", ns)
	}
}

// A command that only reads says so in the project's log, and tells nobody.
func TestACommandThatReadsIsLogged(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	must(t, w.s.ReadNotices(ctx, w.hal.PersonID, time.Now().Add(2*time.Hour)))
	must(t, w.s.LogCommandRead(ctx, w.ws, "compare", w.theirs))
	page, err := w.s.ReadLog(ctx, w.hal, LogFilter{ProjectID: w.theirs})
	must(t, err)
	if len(page.Entries) == 0 || page.Entries[0].Kind != "command.read" || page.Entries[0].By.Name != "compare" {
		t.Errorf("%+v", page.Entries)
	}
	if page, _ := w.s.ReadLog(ctx, w.hedy, LogFilter{ProjectID: w.theirs}); slices.ContainsFunc(page.Entries, func(e LogEntry) bool { return e.Kind == "command.read" }) {
		t.Error("an editor reads what the server's commands read")
	}
	if ns, _ := w.s.Notices(ctx, w.hal.PersonID, 10); slices.ContainsFunc(ns, func(n Notice) bool { return !n.Read }) {
		t.Errorf("a read told the owner: %+v", ns)
	}
}
