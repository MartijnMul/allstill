// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// withPasskey adds a person with a passkey, and returns them with its id.
func withPasskey(t *testing.T, s *Store, ws, name, credential string) (personID, passkeyID string) {
	t.Helper()
	ctx := t.Context()
	personID = newPerson(t, s, ws, name)
	link, err := s.CreatePasskeyLink(ctx, Command("test"), ws, personID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPasskey(ctx, link, key(credential), "Firefox", t0); err != nil {
		t.Fatal(err)
	}
	keys, err := s.Passkeys(ctx, ws, personID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("passkeys %+v: %v", keys, err)
	}
	return personID, keys[0].ID
}

// newPersonLimited adds someone who is no administrator.
func newPersonLimited(t *testing.T, s *Store, ws, name string) string {
	t.Helper()
	id, err := s.CreatePerson(t.Context(), Command("test"), ws, NewPerson{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func signInFrom(t *testing.T, s *Store, ws, personID, passkeyID, name, scope string, at time.Time) string {
	t.Helper()
	token, err := s.CreatePersonToken(t.Context(), NewPersonToken{WorkspaceID: ws, PersonID: personID, PasskeyID: passkeyID, Record: []byte(`{"id":"x"}`), Name: name, Scope: scope}, at)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestASignInActsAsItsPersonNow(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	project, _, _ := s.CreateProject(ctx, Command("test"), ws, "Backups")
	if err := s.SetMember(ctx, Command("test"), ws, project, ada, Editor); err != nil {
		t.Fatal(err)
	}

	token := signInFrom(t, s, ws, ada, passkey, "laptop-1", ScopeManage, t0)
	if !IsPersonToken(token) {
		t.Fatalf("token %q does not say what it is", token)
	}
	a, tok, err := s.AccessOfPersonToken(ctx, token, t0)
	if err != nil {
		t.Fatal(err)
	}
	if a.PersonID != ada || !a.Admin || a.Actor.Via != "laptop-1" || a.Actor.Kind != "person" || tok.Name != "laptop-1" || tok.Scope != ScopeManage {
		t.Fatalf("access %+v of %+v", a, tok)
	}

	// A role changed or taken away counts on the next request.
	if err := s.SetMember(ctx, Command("test"), ws, project, ada, Viewer); err != nil {
		t.Fatal(err)
	}
	if a, _, _ = s.AccessOfPersonToken(ctx, token, t0); !slices.ContainsFunc(a.Projects, func(p ProjectAccess) bool { return p.ID == project }) {
		t.Fatalf("the project is gone from %+v", a.Projects)
	}
	if role, _ := a.Role(project); role != Viewer && !a.Admin {
		t.Fatalf("role %q after being made a viewer", role)
	}
}

// A read-only sign-in reads as a viewer, also an administrator's (R5).
func TestAReadOnlySignInIsAViewerEverywhere(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	project, _, _ := s.CreateProject(ctx, Command("test"), ws, "Backups")

	full, _, err := s.AccessOfPersonToken(ctx, signInFrom(t, s, ws, ada, passkey, "laptop-1", ScopeManage, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := full.Role(project); !full.Admin || role != Owner {
		t.Fatalf("a full sign-in: admin %v, role %q", full.Admin, role)
	}
	a, _, err := s.AccessOfPersonToken(ctx, signInFrom(t, s, ws, ada, passkey, "assistant", ScopeRead, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := a.Role(project); a.Admin || !ok || role != Viewer {
		t.Fatalf("a read-only sign-in: admin %v, role %q, sees it %v", a.Admin, role, ok)
	}
	if role, _ := full.Role(project); role != Owner {
		t.Fatal("making one read-only changed the other")
	}
}

// A sign-in lasts as a browser session does (R6).
func TestASignInEndsWhenIdleAndAfterAYear(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")

	idle := signInFrom(t, s, ws, ada, passkey, "idle", ScopeManage, t0)
	if _, _, err := s.AccessOfPersonToken(ctx, idle, t0.Add(SessionIdle-time.Minute)); err != nil {
		t.Fatalf("just within its idle time: %v", err)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, idle, t0.Add(2*SessionIdle)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("past its idle time, after one use moved it: %v", err)
	}

	used := signInFrom(t, s, ws, ada, passkey, "used", ScopeManage, t0)
	var last PersonToken
	at := t0
	for at.Before(t0.Add(SessionMax - SessionIdle)) {
		at = at.Add(SessionIdle / 2)
		_, tok, err := s.AccessOfPersonToken(ctx, used, at)
		if err != nil {
			t.Fatalf("still in use at %v: %v", at.Sub(t0), err)
		}
		last = tok
	}
	if !last.LastUsed.Equal(at.UTC().Truncate(time.Millisecond)) {
		t.Errorf("last used %v, want %v", last.LastUsed, at)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, used, t0.Add(SessionMax)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after a year: %v", err)
	}
}

// Last use is written at most hourly, so reading does not cost a write per request.
func TestASignInsLastUseIsWrittenHourly(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	token := signInFrom(t, s, ws, ada, passkey, "laptop-1", ScopeManage, t0)

	if _, tok, _ := s.AccessOfPersonToken(ctx, token, t0.Add(30*time.Minute)); !tok.LastUsed.Equal(t0) {
		t.Errorf("after half an hour, last used %v", tok.LastUsed)
	}
	if _, tok, _ := s.AccessOfPersonToken(ctx, token, t0.Add(time.Hour)); !tok.LastUsed.Equal(t0.Add(time.Hour)) {
		t.Errorf("after an hour, last used %v", tok.LastUsed)
	}
}

func TestEndingCountsAtOnce(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	bob, bobKey := withPasskey(t, s, ws, "Bob", "k2")
	bobs := signInFrom(t, s, ws, bob, bobKey, "bobs", ScopeManage, t0)
	byAda := Actor{Kind: "person", ID: ada, Name: "Ada"}

	token := signInFrom(t, s, ws, ada, passkey, "laptop-1", ScopeManage, t0)
	_, tok, err := s.AccessOfPersonToken(ctx, token, t0)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else's cannot be reached with one's own person.
	if err := s.EndPersonToken(ctx, byAda, ws, bob, tok.ID, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ending it as someone else's: %v", err)
	}
	if err := s.EndPersonToken(ctx, byAda, ws, ada, tok.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, token, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an ended sign-in: %v", err)
	}
	if err := s.EndPersonToken(ctx, byAda, ws, ada, tok.ID, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ending it twice: %v", err)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, bobs, t0); err != nil {
		t.Fatalf("someone else's stopped working: %v", err)
	}

	// A browser session, from elsewhere.
	sessions, err := s.BrowserSessions(ctx, ws, ada, t0)
	if err != nil || len(sessions) != 1 || sessions[0].UserAgent != "Firefox" {
		t.Fatalf("sessions %+v: %v", sessions, err)
	}
	if err := s.EndBrowserSession(ctx, byAda, ws, bob, sessions[0].ID, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ending it as someone else's: %v", err)
	}
	if err := s.EndBrowserSession(ctx, byAda, ws, ada, sessions[0].ID, t0); err != nil {
		t.Fatal(err)
	}
	if sessions, _ := s.BrowserSessions(ctx, ws, ada, t0); len(sessions) != 0 {
		t.Fatalf("still there: %+v", sessions)
	}
}

func TestRemovingAPasskeyEndsWhatItApproved(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, first := withPasskey(t, s, ws, "Ada", "k1")
	link, _ := s.CreatePasskeyLink(ctx, Command("test"), ws, ada, t0)
	if _, err := s.AddPasskey(ctx, link, key("k2"), "Safari", t0); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.Passkeys(ctx, ws, ada)
	second := keys[1].ID

	gone := signInFrom(t, s, ws, ada, first, "gone", ScopeManage, t0)
	stays := signInFrom(t, s, ws, ada, second, "stays", ScopeManage, t0)
	tokens, sessions, err := s.RemovePasskey(ctx, Command("remove-passkey"), ws, ada, first, t0)
	if err != nil || tokens != 1 || sessions != 1 {
		t.Fatalf("removed %d sign-ins and %d sessions: %v", tokens, sessions, err)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, gone, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a sign-in approved with it: %v", err)
	}
	if _, _, err := s.AccessOfPersonToken(ctx, stays, t0); err != nil {
		t.Fatalf("one approved with another passkey: %v", err)
	}
	if left, _ := s.BrowserSessions(ctx, ws, ada, t0); len(left) != 1 || left[0].UserAgent != "Safari" {
		t.Fatalf("sessions left: %+v", left)
	}
	// Nor can it approve anything more.
	if _, err := s.CreatePersonToken(ctx, NewPersonToken{WorkspaceID: ws, PersonID: ada, PasskeyID: first, Record: []byte(`{"id":"x"}`), Name: "late", Scope: ScopeManage}, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a sign-in with a removed passkey: %v", err)
	}
	if _, _, err := s.RemovePasskey(ctx, Command("remove-passkey"), ws, ada, first, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing it twice: %v", err)
	}
}

func TestEndEverySignIn(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	one := signInFrom(t, s, ws, ada, passkey, "one", ScopeManage, t0)
	two := signInFrom(t, s, ws, ada, passkey, "two", ScopeRead, t0)

	tokens, sessions, err := s.EndEverySignIn(ctx, Command("end-sessions"), ws, ada, t0)
	if err != nil || tokens != 2 || sessions != 1 {
		t.Fatalf("ended %d sign-ins and %d sessions: %v", tokens, sessions, err)
	}
	for _, token := range []string{one, two} {
		if _, _, err := s.AccessOfPersonToken(ctx, token, t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("still works: %v", err)
		}
	}
	// The passkey stays, so they can sign in again.
	if keys, _ := s.Passkeys(ctx, ws, ada); len(keys) != 1 {
		t.Fatalf("passkeys %+v", keys)
	}
}

func TestOldSignInsAreCleared(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	signInFrom(t, s, ws, ada, passkey, "old", ScopeManage, t0)

	if got, _ := s.PersonTokens(ctx, ws, ada, t0); len(got) != 1 {
		t.Fatalf("before its end: %+v", got)
	}
	if got, _ := s.PersonTokens(ctx, ws, ada, t0.Add(SessionIdle)); len(got) != 0 {
		t.Fatalf("listed past its end: %+v", got)
	}
	var rows int
	count := func() int {
		t.Helper()
		if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM person_tokens`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if count() != 1 {
		t.Fatal("the row should still be there")
	}
	if err := s.Alive(ctx, t0.Add(SessionIdle)); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("the old sign-in is still stored")
	}
}

// What is logged, for whom, and that no token is in it (R39 to R41).
func TestSignInsInTheLog(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, passkey := withPasskey(t, s, ws, "Ada", "k1")
	admin := newPerson(t, s, ws, "Erin")
	bob := newPersonLimited(t, s, ws, "Bob")

	token := signInFrom(t, s, ws, ada, passkey, "laptop-1", ScopeRead, t0)
	a, tok, err := s.AccessOfPersonToken(ctx, token, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndPersonToken(ctx, a.Actor, ws, ada, tok.ID, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EndEverySignIn(ctx, Command("end-sessions"), ws, ada, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RemovePasskey(ctx, Command("remove-passkey"), ws, ada, passkey, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	read := func(person string) []LogEntry {
		t.Helper()
		access, err := s.AccessOfPerson(ctx, ws, person)
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.ReadLog(ctx, access, LogFilter{What: "signins", Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	kindsOf := func(entries []LogEntry) []string {
		var out []string
		for _, e := range entries {
			out = append(out, e.Kind)
		}
		return out
	}
	want := []string{"passkey.removed", "session.ended", "token.ended", "token.made"}
	got := slices.Compact(kindsOf(read(ada)))
	if !slices.Equal(got[:4], want) {
		t.Errorf("Ada reads %v, want it to start with %v", got, want)
	}
	if got := slices.Compact(kindsOf(read(admin))); !slices.Contains(got, "token.made") || !slices.Contains(got, "passkey.removed") {
		t.Errorf("an administrator reads %v", got)
	}
	if got := read(bob); len(got) != 0 {
		t.Errorf("someone else reads %v", kindsOf(got))
	}

	for _, e := range read(ada) {
		switch e.Kind {
		case "token.made":
			if e.AboutName != "laptop-1" || e.By.ID != ada || e.Details["scope"] != ScopeRead {
				t.Errorf("token.made: %+v", e)
			}
		case "token.ended":
			if e.AboutName != "laptop-1" || e.Details["via"] != "laptop-1" {
				t.Errorf("token.ended: %+v", e)
			}
		case "session.ended":
			if e.By.Kind != "command" {
				t.Errorf("session.ended by a command on the server, as an administrator reads it: %+v", e)
			}
		}
	}

	// Someone who is no administrator reads their own, and how the team works stays out.
	dan := newPersonLimited(t, s, ws, "Dan")
	link, _ := s.CreatePasskeyLink(ctx, Command("test"), ws, dan, t0)
	if _, err := s.AddPasskey(ctx, link, key("k3"), "Safari", t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EndEverySignIn(ctx, Command("end-sessions"), ws, dan, t0); err != nil {
		t.Fatal(err)
	}
	ended := read(dan)
	if len(ended) == 0 || ended[0].Kind != "session.ended" || ended[0].By.Kind != "team" {
		t.Errorf("Dan reads %+v", ended)
	}

	var details string
	rows, err := s.r.QueryContext(ctx, `SELECT details || about_name || by_name FROM log_entries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		rows.Scan(&details)
		if strings.Contains(details, token) || strings.Contains(details, personTokenPrefix) {
			t.Fatalf("a token in the log: %s", details)
		}
	}
}

// The sign-in's name goes where the person's name goes (R39).
func TestTheSignInsNameIsMaskedWithThePerson(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada := newPerson(t, s, ws, "Ada") // an administrator with no membership
	project, _, _ := s.CreateProject(ctx, Command("test"), ws, "Backups")
	viewer := newPersonLimited(t, s, ws, "Vera")
	editor := newPersonLimited(t, s, ws, "Edith")
	member := newPersonLimited(t, s, ws, "Bob")
	for person, role := range map[string]Role{viewer: Viewer, editor: Editor, member: Editor} {
		if err := s.SetMember(ctx, Command("test"), ws, project, person, role); err != nil {
			t.Fatal(err)
		}
	}
	act := func(person, name string) {
		t.Helper()
		a, err := s.AccessOfPerson(ctx, ws, person)
		if err != nil {
			t.Fatal(err)
		}
		a.Actor.Via = "secret-host"
		if _, err := s.CreateCheck(ctx, a, project, NewCheck{Name: name, Interval: time.Hour, Grace: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	act(ada, "by-admin")
	act(member, "by-member")

	tests := []struct {
		reader, check string
		by            string
		via           bool
	}{
		{viewer, "by-member", "team", false},
		{viewer, "by-admin", "team", false},
		{editor, "by-member", "person", true},
		{editor, "by-admin", "administrator", false},
		{member, "by-member", "person", true},
		{ada, "by-admin", "person", true},
		{ada, "by-member", "person", true},
	}
	for _, tt := range tests {
		a, err := s.AccessOfPerson(ctx, ws, tt.reader)
		if err != nil {
			t.Fatal(err)
		}
		page, err := s.ReadLog(ctx, a, LogFilter{Name: tt.check, Limit: 10})
		if err != nil || len(page.Entries) == 0 {
			t.Fatalf("%s reading %s: %+v, %v", tt.reader, tt.check, page.Entries, err)
		}
		e := page.Entries[len(page.Entries)-1]
		if _, via := e.Details["via"]; e.By.Kind != tt.by || via != tt.via {
			t.Errorf("%s reading %s: by %+v, details %v; want %s, via %v", a.Actor.Name, tt.check, e.By, e.Details, tt.by, tt.via)
		}
	}
}
