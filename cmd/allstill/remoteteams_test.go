// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

// A team's owners running their team from a command line, against a real server
// (intent/teams/plan-3.md, part C).
func TestRunningATeamFromTheCommandLine(t *testing.T) {
	st, ws, _, signIn := aRealServer(t)
	ctx := t.Context()
	test := store.Command("test")
	teams, _ := st.Teams(ctx, ws)
	team := teams[0]
	person := func(name string) string {
		id, err := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	olive, bob, ed := person("Olive"), person("Bob"), person("Ed")
	if err := st.SetTeamMember(ctx, test, ws, team.ID, olive, store.Owner); err != nil {
		t.Fatal(err)
	}
	backups, _, _ := st.CreateProject(ctx, test, ws, "Backups")
	st.SetMember(ctx, test, ws, backups, bob, store.Viewer)
	st.SetTeamMember(ctx, test, ws, team.ID, ed, store.Viewer)
	st.SetMember(ctx, test, ws, backups, ed, store.Editor)
	admin, _ := st.AdminAccess(ctx, ws, "test")
	if _, err := st.CreateCheck(ctx, admin, backups, store.NewCheck{Name: "nightly", Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Fatal(err)
	}

	must := func(args ...string) string {
		t.Helper()
		out, err := command(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	has := func(out string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
	}
	refused := func(want string, args ...string) {
		t.Helper()
		if out, err := command(t, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, wanted %q\n%s", args, err, want, out)
		}
	}

	signIn(olive, store.ScopeManage)
	// Projects: made, renamed, deleted when empty.
	has(must("projects", "add", "Tides"), "Made the project Tides in "+team.Name, "shown only now", "/ping/KEY/SLUG")
	has(must("projects", "rename", "Tides", "Tide tables"), "Renamed Tides to Tide tables.")
	refused("exists already", "projects", "add", "Backups")
	refused("still holds", "projects", "delete", "Backups")
	has(must("projects", "delete", "Tide tables"), "Deleted the project Tide tables.")
	refused("no project named", "projects", "rename", "Tide tables", "Tides")

	// Who can see a project, and roles of their own.
	has(must("projects", "people", "Backups"), "Bob", "their own, as viewer", "Olive", "the team, as owner")
	has(must("projects", "role", "Backups", "Bob", "editor"), "Bob is now an editor of Backups.")
	has(must("teams"), "OUTSIDE THE TEAM", "Bob", "editor", "Backups")
	refused("named in the dashboard", "projects", "role", "Backups", "Bob", "owner")
	refused("no one", "projects", "role", "Backups", "Nobody", "editor")
	has(must("projects", "role", "Backups", "Bob", "none"), "Ended the role Bob had of their own in Backups.")

	// One's own choice to be told.
	has(must("projects", "alerts", "Backups", "off"), "no longer told about Backups")
	has(must("projects"), "TOLD", "no")
	has(must("projects", "alerts", "Backups", "on"), "You are told about Backups.")

	// The team: its people, roles changed and taken away.
	has(must("teams"), team.Name, "Olive", "owner", "Ed", "viewer", "No open invitations.")
	has(must("teams", "role", "Ed", "editor"), "Ed is now an editor of "+team.Name, "Their role of their own in Backups, as editor, ended")
	refused("dashboard", "teams", "role", "Olive", "viewer")
	refused("dashboard", "teams", "remove", "Olive")
	has(must("teams", "remove", "Ed"), "Took Ed out of "+team.Name)
	refused("no one", "teams", "role", "Bob", "editor")

	// Invitations: made with a link once, listed without it, withdrawn.
	invite := must("teams", "invite", "-for", "Carol", "-role", "editor")
	has(invite, "An invitation for Carol, as an editor of "+team.Name, "https://allstill.example.com/join#")
	has(must("projects", "invite", "Backups", "-for", "Dan"), "An invitation for Dan, as a viewer of Backups", "/join#")
	listed := must("teams")
	has(listed, "Carol", "Dan", "Backups")
	if strings.Contains(listed, "/join#") {
		t.Errorf("a list holds a link:\n%s", listed)
	}
	has(must("projects", "invites", "Backups"), "Dan")
	has(must("projects", "withdraw", "Backups", "Dan"), "Withdrew the invitation for Dan.")
	has(must("teams", "withdraw", "Carol"), "Withdrew the invitation for Carol.")
	refused("give -for", "teams", "invite")
	refused("no open invitation", "teams", "withdraw", "Carol")

	// Someone who owns no team runs none.
	signIn(bob, store.ScopeManage)
	refused("you own no team", "teams")
	refused("You own no team", "projects", "add", "Mine")
	// A read-only sign-in cannot choose, even for oneself.
	signIn(olive, store.ScopeRead)
	refused("can only read", "projects", "alerts", "Backups", "off")
}
