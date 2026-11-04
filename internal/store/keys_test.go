// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// keyFixture holds an owner and an administrator who is no member, besides what
// setup makes.
func keyFixture(t *testing.T) (f fixture, owner, admin Access) {
	t.Helper()
	f = setup(t)
	ctx := t.Context()
	personID := newPersonLimited(t, f.s, f.ws, "Olga")
	if err := f.s.SetMember(ctx, Command("test"), f.ws, f.project, personID, Owner); err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.AccessOfPerson(ctx, f.ws, personID)
	if err != nil {
		t.Fatal(err)
	}
	adminID := newPerson(t, f.s, f.ws, "Ada")
	if admin, err = f.s.AccessOfPerson(ctx, f.ws, adminID); err != nil {
		t.Fatal(err)
	}
	return f, owner, admin
}

// A key is shown once, stored as a hash, and reaches its project alone (R11, R13).
func TestAKeyActsInItsProjectAlone(t *testing.T) {
	f, owner, _ := keyFixture(t)
	ctx := t.Context()
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")

	for _, tt := range []struct {
		scope string
		role  Role
	}{{ScopeRead, Viewer}, {ScopeManage, Editor}} {
		made, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "script-"+tt.scope, tt.scope, t0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(key, "alk_") || !IsAPIKey(key) || IsPersonToken(key) {
			t.Errorf("the key reads %q", key[:4])
		}
		var stored []byte
		if err := f.s.r.QueryRow(`SELECT key_hash FROM api_keys WHERE id = ?`, made.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(stored), key) || len(stored) != 32 {
			t.Errorf("what is stored of it: %d bytes", len(stored))
		}
		a, current, err := f.s.AccessOfAPIKey(ctx, key, t0)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := a.Role(f.project); !ok || got != tt.role || len(a.Projects) != 1 {
			t.Errorf("%s: role %s, projects %+v", tt.scope, got, a.Projects)
		}
		if _, ok := a.Role(other); ok {
			t.Errorf("%s reaches another project", tt.scope)
		}
		if a.Admin || a.PersonID != "" || a.Actor.Kind != "key" || a.Actor.Name != made.Name || current.ID != made.ID {
			t.Errorf("%s: %+v, %+v", tt.scope, a, current)
		}
		if !viaPerson(a) {
			t.Errorf("%s is treated as the server's own command line", tt.scope)
		}
	}
	if _, _, err := f.s.AccessOfAPIKey(ctx, "alk_madeup", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a made-up key: %v", err)
	}
}

// A key stops at once when revoked, and stays when who made it has gone (R13).
func TestRevokingAKey(t *testing.T) {
	f, owner, _ := keyFixture(t)
	ctx := t.Context()
	made, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "script", ScopeRead, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.RemoveMember(ctx, Command("test"), f.ws, f.project, owner.PersonID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.AccessOfAPIKey(ctx, key, t0); err != nil {
		t.Errorf("after who made it left: %v", err)
	}
	admin, err := f.s.AdminAccess(ctx, f.ws, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.RevokeAPIKey(ctx, admin, made.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.AccessOfAPIKey(ctx, key, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked: %v", err)
	}
	if err := f.s.RevokeAPIKey(ctx, admin, made.ID, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked twice: %v", err)
	}
	// The name is free again.
	if _, _, err := f.s.CreateAPIKey(ctx, admin, f.project, "script", ScopeRead, t0); err != nil {
		t.Errorf("the name again: %v", err)
	}
}

// Who may do what with keys (R11): an owner or administrator, and nobody who only
// looks or edits, whether by role, by a read-only sign-in, or as a key.
func TestWhoManagesKeys(t *testing.T) {
	f, owner, admin := keyFixture(t)
	ctx := t.Context()
	other, _, _ := f.s.CreateProject(ctx, Command("test"), f.ws, "Other")
	stranger, err := f.s.CreateMember(ctx, Command("test"), f.ws, NewPerson{Name: "Stranger"}, []string{other}, Owner)
	if err != nil {
		t.Fatal(err)
	}
	outside, _ := f.s.AccessOfPerson(ctx, f.ws, stranger)
	made, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "kept", ScopeManage, t0)
	if err != nil {
		t.Fatal(err)
	}
	asKey, _, err := f.s.AccessOfAPIKey(ctx, key, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		who  string
		a    Access
		want error
	}{
		{"a viewer", f.viewer, ErrForbidden},
		{"an editor", f.editor, ErrForbidden},
		{"someone elsewhere", outside, ErrNotFound},
		{"a manage key", asKey, ErrForbidden},
		{"an owner's read-only sign-in", owner.ReadOnly(), ErrForbidden},
		{"an administrator's read-only sign-in", admin.ReadOnly(), ErrForbidden},
	} {
		if _, _, err := f.s.CreateAPIKey(ctx, tt.a, f.project, "new", ScopeRead, t0); !errors.Is(err, tt.want) {
			t.Errorf("%s makes one: %v", tt.who, err)
		}
		if _, err := f.s.APIKeys(ctx, tt.a, f.project); !errors.Is(err, tt.want) {
			t.Errorf("%s lists them: %v", tt.who, err)
		}
		if err := f.s.RevokeAPIKey(ctx, tt.a, made.ID, t0); !errors.Is(err, tt.want) {
			t.Errorf("%s revokes one: %v", tt.who, err)
		}
		if keys, err := f.s.APIKeys(ctx, tt.a, ""); err != nil || len(keys) != 0 {
			t.Errorf("%s lists all theirs: %+v, %v", tt.who, keys, err)
		}
	}
	for _, tt := range []struct {
		who string
		a   Access
	}{{"an owner", owner}, {"an administrator", admin}} {
		keys, err := f.s.APIKeys(ctx, tt.a, "")
		if err != nil || len(keys) != 1 || keys[0].ID != made.ID {
			t.Errorf("%s lists all theirs: %+v, %v", tt.who, keys, err)
		}
	}
	for _, tt := range []struct {
		name, scope, field string
	}{{"  ", ScopeRead, "name"}, {strings.Repeat("x", 101), ScopeRead, "name"}, {"fine", "admin", "scope"}, {"kept", ScopeRead, "name"}} {
		_, _, err := f.s.CreateAPIKey(ctx, owner, f.project, tt.name, tt.scope, t0)
		var inv *InvalidError
		if !errors.As(err, &inv) || inv.Field != tt.field {
			t.Errorf("%q %q: %v", tt.name, tt.scope, err)
		}
	}
}

// Who made a key reads as the log reads (spec, Design).
func TestWhoMadeAKeyReadsAsTheLogDoes(t *testing.T) {
	f, owner, admin := keyFixture(t)
	ctx := t.Context()
	admin.Actor.Via = "laptop"
	if _, _, err := f.s.CreateAPIKey(ctx, admin, f.project, "by-admin", ScopeRead, t0); err != nil {
		t.Fatal(err)
	}
	owner.Actor.Via = "desktop"
	if _, _, err := f.s.CreateAPIKey(ctx, owner, f.project, "by-owner", ScopeRead, t0); err != nil {
		t.Fatal(err)
	}
	second := newPersonLimited(t, f.s, f.ws, "Second")
	f.s.SetMember(ctx, Command("test"), f.ws, f.project, second, Owner)
	otherOwner, _ := f.s.AccessOfPerson(ctx, f.ws, second)

	byName := func(a Access) map[string]APIKey {
		t.Helper()
		keys, err := f.s.APIKeys(ctx, a, f.project)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]APIKey{}
		for _, k := range keys {
			out[k.Name] = k
		}
		return out
	}
	got := byName(otherOwner)
	if m := got["by-admin"].MadeBy; m.Kind != "administrator" || m.Name != "" || m.ID != "" {
		t.Errorf("an owner reads an administrator's key as made by %+v", m)
	}
	if m := got["by-owner"].MadeBy; m.Kind != "person" || m.Name != "Olga" {
		t.Errorf("an owner reads another owner's key as made by %+v", m)
	}
	if got["by-admin"].Via != "" || got["by-owner"].Via != "" {
		t.Errorf("an owner reads sign-ins: %+v", got)
	}
	if k := byName(owner)["by-owner"]; k.Via != "desktop" {
		t.Errorf("who made it reads %+v", k)
	}
	if k := byName(admin)["by-admin"]; k.MadeBy.Name != "Ada" || k.Via != "laptop" {
		t.Errorf("an administrator reads %+v", k)
	}
	if k := byName(admin)["by-owner"]; k.Via != "desktop" {
		t.Errorf("an administrator reads %+v", k)
	}
}

// What a key does is logged as the key, and making and revoking one for owners and
// administrators (R39, R40).
func TestTheLogNamesTheKey(t *testing.T) {
	f, owner, _ := keyFixture(t)
	ctx := t.Context()
	made, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "deploy", ScopeManage, t0)
	if err != nil {
		t.Fatal(err)
	}
	asKey, _, err := f.s.AccessOfAPIKey(ctx, key, t0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.s.CreateCheck(ctx, asKey, f.project, NewCheck{Name: "by-key", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.RevokeAPIKey(ctx, owner, made.ID, t0); err != nil {
		t.Fatal(err)
	}
	read := func(a Access) []LogEntry {
		t.Helper()
		page, err := f.s.ReadLog(ctx, a, LogFilter{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	has := func(es []LogEntry, kind string) bool {
		return slices.ContainsFunc(es, func(e LogEntry) bool { return e.Kind == kind })
	}
	if es := read(owner); !has(es, "key.made") || !has(es, "key.revoked") {
		t.Errorf("an owner reads %+v", es)
	}
	if es := read(f.editor); has(es, "key.made") || has(es, "key.revoked") {
		t.Errorf("an editor reads %+v", es)
	}
	by := func(a Access) LogParty {
		t.Helper()
		for _, e := range read(a) {
			if e.Kind == "check.created" && e.AboutID == c.ID {
				return e.By
			}
		}
		t.Fatal("no line")
		return LogParty{}
	}
	if got := by(f.editor); got.Kind != "key" || got.Name != "deploy" {
		t.Errorf("an editor reads it as %+v", got)
	}
	if got := by(f.viewer); got.Kind != "team" {
		t.Errorf("a viewer reads it as %+v", got)
	}
	// Nothing of the key's value in any line.
	var details int
	if err := f.s.r.QueryRow(`SELECT count(*) FROM log_entries WHERE details LIKE ? OR about_name LIKE ?`, "%"+key+"%", "%"+key+"%").Scan(&details); err != nil {
		t.Fatal(err)
	}
	if details != 0 {
		t.Errorf("%d lines hold the key", details)
	}
}

// A key is held to what a person is, not to what the server's commands are (R37).
func TestAKeyIsHeldToWhatAPersonIs(t *testing.T) {
	f, owner, _ := keyFixture(t)
	ctx := t.Context()
	_, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "deploy", ScopeManage, t0)
	if err != nil {
		t.Fatal(err)
	}
	asKey, _, err := f.s.AccessOfAPIKey(ctx, key, t0)
	if err != nil {
		t.Fatal(err)
	}
	var inv *InvalidError
	_, err = f.s.CreateCheck(ctx, asKey, f.project, NewCheck{Name: "long", Interval: time.Hour, Grace: time.Hour, Description: strings.Repeat("x", MaxDescription+1)})
	if !errors.As(err, &inv) || inv.Field != "description" {
		t.Errorf("a long description: %v", err)
	}
	// A project that has no room left for a check Allstill runs.
	settings := map[string]any{"url": "https://example.com/"}
	command, _ := f.s.AdminAccess(ctx, f.ws, "test")
	for i := range MaxActiveChecks {
		if _, err := f.s.CreateCheck(ctx, command, f.project, NewCheck{Name: "site", Kind: "http", Interval: time.Hour, Settings: settings}); err != nil {
			t.Fatalf("site %d: %v", i, err)
		}
	}
	_, err = f.s.CreateCheck(ctx, asKey, f.project, NewCheck{Name: "one too many", Kind: "http", Interval: time.Hour, Settings: settings})
	if !errors.As(err, &inv) || inv.Field != "project" {
		t.Errorf("past the count: %v", err)
	}
	if _, err := f.s.CreateCheck(ctx, command, f.project, NewCheck{Name: "the server's own", Kind: "http", Interval: time.Hour, Settings: settings}); err != nil {
		t.Errorf("the server's own command is not held to it: %v", err)
	}
	// Critical takes an owner, which a key is not.
	c, err := f.s.CreateCheck(ctx, asKey, f.project, NewCheck{Name: "job", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	urgency, critical := "now", true
	_, err = f.s.UpdateCheck(ctx, asKey, c.ID, Change{Urgency: &urgency, Critical: &critical}, t0)
	if !errors.As(err, &inv) || inv.Field != "critical" {
		t.Errorf("critical: %v", err)
	}
}

// Last use is written at most once per SessionTouch.
func TestAKeysLastUse(t *testing.T) {
	f, owner, _ := keyFixture(t)
	ctx := t.Context()
	made, key, err := f.s.CreateAPIKey(ctx, owner, f.project, "script", ScopeRead, t0)
	if err != nil {
		t.Fatal(err)
	}
	used := func() time.Time {
		t.Helper()
		keys, err := f.s.APIKeys(ctx, owner, f.project)
		if err != nil || len(keys) != 1 || keys[0].ID != made.ID {
			t.Fatalf("%+v: %v", keys, err)
		}
		return keys[0].LastUsed
	}
	if !used().IsZero() {
		t.Errorf("never used: %v", used())
	}
	f.s.AccessOfAPIKey(ctx, key, t0)
	if !used().Equal(t0) {
		t.Errorf("first use: %v", used())
	}
	f.s.AccessOfAPIKey(ctx, key, t0.Add(time.Minute))
	if !used().Equal(t0) {
		t.Errorf("a minute later: %v", used())
	}
	f.s.AccessOfAPIKey(ctx, key, t0.Add(SessionTouch))
	if !used().Equal(t0.Add(SessionTouch)) {
		t.Errorf("after an hour: %v", used())
	}
}
