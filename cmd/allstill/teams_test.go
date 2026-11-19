// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/store/storetest"
)

// Giving someone a team of their own, on the server, as the spec's first case
// plays it: the administrator makes the team with its owner, and from then on
// a command that would have to guess a team asks for it (R9, R10, R13, R18,
// R33, R34).
func TestGivingSomeoneATeam(t *testing.T) {
	ctx := t.Context()
	dbPath := storetest.Path(t)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	st.SetSetting(ctx, "base_url", "https://allstill.example.com")
	st.SetSetting(ctx, "allow_private", "10.0.0.0/8")
	st.Close()
	admin := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, append([]string{"admin"}, append(args, "-db", dbPath)...), &out, &bytes.Buffer{})
		return out.String(), err
	}
	must := func(out string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return out
	}
	// One team: everything works as it did, with no team named.
	must(admin("create-user", "-name", "Alice", "-email", "alice@example.com"))
	must(admin("create-project", "Backups"))
	// The first team reaches the ranges the server was given, on saving and in
	// the try that follows it: nothing answers there, but it is not refused.
	if out := must(admin("add-check", "-project", "Backups", "-name", "db", "-tcp", "10.0.0.5:5432", "-timeout", "1s")); strings.Contains(out, "not on the public internet") {
		t.Errorf("the first team's try was held to the public internet:\n%s", out)
	}
	if out := must(admin("teams")); !strings.Contains(out, "Allstill (team_") || !strings.Contains(out, "owned by Alice") || !strings.Contains(out, "any number of checks Allstill runs") || !strings.Contains(out, "may reach the server's private ranges") {
		t.Errorf("the first team:\n%s", out)
	}

	out := must(admin("create-team", "Harbour", "-owner-name", "Bob", "-owner-email", "bob@example.com"))
	if !strings.Contains(out, "Made the team Harbour") || !strings.Contains(out, "Bob (per_") || !strings.Contains(out, "https://allstill.example.com/setup#") {
		t.Fatalf("create-team:\n%s", out)
	}
	if _, err := admin("create-team", "Docks"); err == nil {
		t.Error("a team without an owner")
	}
	out = must(admin("teams"))
	if !strings.Contains(out, "Harbour (team_") || !strings.Contains(out, "owned by Bob") || !strings.Contains(out, "may have 400 checks Allstill runs and 2000 checks that jobs ping; public internet only") {
		t.Errorf("a new team:\n%s", out)
	}
	if out := must(admin("teams", "-people")); !strings.Contains(out, "Alice  administrator; owner of Allstill") || !strings.Contains(out, "Bob  owner of Harbour") {
		t.Errorf("people:\n%s", out)
	}

	// With two teams, nothing is guessed.
	if _, err := admin("create-project", "Jobs"); err == nil || !strings.Contains(err.Error(), "-team") {
		t.Errorf("a project with no team said: %v", err)
	}
	must(admin("create-project", "Jobs", "-team", "Harbour"))
	// A name another team holds is free, and then takes -team to tell apart.
	must(admin("create-project", "Backups", "-team", "Harbour"))
	if _, err := admin("add-check", "-project", "Backups", "-name", "site", "-http", "https://example.com/"); err == nil || !strings.Contains(err.Error(), "-team") {
		t.Errorf("a project two teams have, with no team said: %v", err)
	}
	if _, err := admin("create-user", "-name", "Carol"); err == nil || !strings.Contains(err.Error(), "-admin") {
		t.Errorf("an administrator with two teams and no word on which: %v", err)
	}
	if out := must(admin("create-user", "-name", "Carol", "-admin")); !strings.Contains(out, "an administrator, in no team") {
		t.Errorf("%s", out)
	}
	if out := must(admin("create-user", "-name", "Dave", "-email", "dave@example.com", "-team", "Harbour", "-role", "editor")); !strings.Contains(out, "editor of the team Harbour") {
		t.Errorf("%s", out)
	}
	if out := must(admin("create-user", "-name", "Erin", "-team", "Harbour", "-project", "Backups", "-role", "viewer")); !strings.Contains(out, "viewer of Backups") {
		t.Errorf("%s", out)
	}

	// What the team's people see, and what the administrator does not.
	st, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	access := func(email string) store.Access {
		t.Helper()
		p, err := st.PersonByEmail(ctx, ws, email)
		if err != nil {
			t.Fatal(err)
		}
		a, err := st.AccessOfPerson(ctx, ws, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	names := func(a store.Access) string {
		var out []string
		for _, p := range a.Projects {
			out = append(out, a.Label(p.ID)+":"+string(p.Role))
		}
		return strings.Join(out, " ")
	}
	if got := names(access("alice@example.com")); got != "Backups:owner" {
		t.Errorf("the administrator sees %q", got)
	}
	if got := names(access("bob@example.com")); got != "Backups:owner Jobs:owner" {
		t.Errorf("the team's owner sees %q", got)
	}
	if got := names(access("dave@example.com")); got != "Backups:editor Jobs:editor" {
		t.Errorf("the team's editor sees %q", got)
	}

	// The owner was told of everyone the server let into the team.
	bob, _ := st.PersonByEmail(ctx, ws, "bob@example.com")
	notices, err := st.Notices(ctx, bob.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	for _, n := range notices {
		told = append(told, n.Title)
	}
	for _, want := range []string{"made Dave editor of the team Harbour", "made Erin viewer of Backups", "made Bob owner of the team Harbour"} {
		if !strings.Contains(strings.Join(told, "\n"), want) {
			t.Errorf("the owner was not told %q, but:\n%s", want, strings.Join(told, "\n"))
		}
	}

	// Its limits and its reach are the administrator's to set.
	must(admin("set-team", "Harbour", "-active-checks", "0"))
	if _, err := admin("add-check", "-team", "Harbour", "-project", "Jobs", "-name", "db", "-tcp", "10.0.0.5:5432"); err == nil || !strings.Contains(err.Error(), "set-team") {
		t.Errorf("a private address for a team held to the public internet: %v", err)
	}
	if out := must(admin("set-team", "Harbour", "-private", "on", "-ping-checks", "none", "-name", "Harbour Works")); !strings.Contains(out, "Changed the team Harbour, now Harbour Works") {
		t.Errorf("%s", out)
	}
	if out := must(admin("teams")); !strings.Contains(out, "Harbour Works") || !strings.Contains(out, "may have 0 checks Allstill runs and any number of checks that jobs ping; may reach") {
		t.Errorf("after set-team:\n%s", out)
	}

	// Members come and go, the last owner stays, and a team that holds
	// anything is not removed.
	must(admin("set-team-member", "-email", "dave@example.com", "-team", "Harbour Works", "-role", "owner"))
	must(admin("set-team-member", "-email", "bob@example.com", "-team", "Harbour Works", "-role", "none"))
	if _, err := admin("set-team-member", "-email", "dave@example.com", "-team", "Harbour Works", "-role", "viewer"); err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Errorf("the last owner made less: %v", err)
	}
	if got := names(access("bob@example.com")); got != "" {
		t.Errorf("taken out, they still see %q", got)
	}
	if _, err := admin("remove-team", "Harbour Works"); err == nil || !strings.Contains(err.Error(), "2 projects") {
		t.Errorf("removing a team with projects: %v", err)
	}
	must(admin("create-team", "Empty", "-owner", "alice@example.com"))
	if out := must(admin("remove-team", "Empty")); !strings.Contains(out, "Removed the team Empty") {
		t.Errorf("%s", out)
	}
	if _, err := admin("remove-team", "Nowhere"); err == nil || !strings.Contains(err.Error(), "no team is named") {
		t.Errorf("%v", err)
	}
}
