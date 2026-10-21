// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func key(credentialID string) NewPasskey {
	return NewPasskey{CredentialID: []byte(credentialID), RPID: "allstill.example.com", Name: "MacBook", Record: []byte(`{"id":"x"}`)}
}

func newPerson(t *testing.T, s *Store, ws, name string) string {
	t.Helper()
	id, err := s.CreatePerson(t.Context(), ws, NewPerson{Name: name, Email: strings.ToLower(name) + "@example.com", WorkspaceAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestALinkWorksOnce(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada := newPerson(t, s, ws, "Ada")

	first, err := s.CreatePasskeyLink(ctx, ws, ada, t0)
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.CreatePasskeyLink(ctx, ws, ada, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PasskeyLink(ctx, first, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an earlier unused link still works: %v", err)
	}
	if p, err := s.PasskeyLink(ctx, link, t0); err != nil || p.ID != ada || p.Name != "Ada" {
		t.Fatalf("the link is for %+v: %v", p, err)
	}
	if _, err := s.PasskeyLink(ctx, link, t0.Add(LinkLifetime)); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("a link after a day: %v", err)
	}
	if _, err := s.AddPasskey(ctx, link, key("k1"), "", t0.Add(LinkLifetime)); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("an expired link adds a passkey: %v", err)
	}

	// Two browsers finish at the same moment: one passkey is added.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { _, errs[i] = s.AddPasskey(ctx, link, key("k"+string(rune('a'+i))), "", t0) })
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), ErrLinkUsed) {
		t.Fatalf("two finishes: %v", errs)
	}
	if keys, _ := s.Passkeys(ctx, ws, ada); len(keys) != 1 {
		t.Fatalf("%d passkeys", len(keys))
	}
	if _, err := s.PasskeyLink(ctx, link, t0); !errors.Is(err, ErrLinkUsed) {
		t.Fatalf("a used link: %v", err)
	}
	if _, err := s.CreatePasskeyLink(ctx, ws, "per_nobody", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a link for nobody: %v", err)
	}
}

func TestAPasskeyBelongsToOnePerson(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada, bob := newPerson(t, s, ws, "Ada"), newPerson(t, s, ws, "Bob")
	l1, _ := s.CreatePasskeyLink(ctx, ws, ada, t0)
	l2, _ := s.CreatePasskeyLink(ctx, ws, bob, t0)
	if _, err := s.AddPasskey(ctx, l1, key("same"), "", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPasskey(ctx, l2, key("same"), "", t0); !errors.Is(err, ErrExists) {
		t.Fatalf("a credential registered twice: %v", err)
	}
	// The failed attempt left Bob's link unused.
	if _, err := s.PasskeyLink(ctx, l2, t0); err != nil {
		t.Fatalf("Bob's link after a failed attempt: %v", err)
	}
	keys, _ := s.Passkeys(ctx, ws, ada)
	if _, err := s.SignIn(ctx, ws, bob, keys[0].ID, []byte(`{}`), "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bob signs in with Ada's passkey: %v", err)
	}
	if n, _ := s.PasskeysOfOtherHosts(ctx, "monitor.example.com"); n != 1 {
		t.Fatalf("%d passkeys of another host", n)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada := newPerson(t, s, ws, "Ada")
	link, _ := s.CreatePasskeyLink(ctx, ws, ada, t0)
	token, err := s.AddPasskey(ctx, link, key("k1"), strings.Repeat("x", 600), t0)
	if err != nil {
		t.Fatal(err)
	}
	if sess, err := s.Session(ctx, token, t0); err != nil || sess.PersonID != ada || sess.WorkspaceID != ws {
		t.Fatalf("session %+v: %v", sess, err)
	}
	if _, err := s.Session(ctx, "not-a-token", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown token: %v", err)
	}

	// Used every 29 days, a session lasts until its first year is over.
	at := t0
	for at.Before(t0.Add(SessionMax - 29*24*time.Hour)) {
		at = at.Add(29 * 24 * time.Hour)
		if _, err := s.Session(ctx, token, at); err != nil {
			t.Fatalf("a session in use ended at %v: %v", at, err)
		}
	}
	if _, err := s.Session(ctx, token, t0.Add(SessionMax)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session past a year: %v", err)
	}

	// Left alone for 30 days, it ends.
	link, _ = s.CreatePasskeyLink(ctx, ws, ada, t0)
	token, _ = s.AddPasskey(ctx, link, key("k2"), "", t0)
	if _, err := s.Session(ctx, token, t0.Add(SessionIdle)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an idle session: %v", err)
	}

	// Signing out, and removing the passkey, end it.
	keys, _ := s.Passkeys(ctx, ws, ada)
	token, err = s.SignIn(ctx, ws, ada, keys[1].ID, []byte(`{"id":"y"}`), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if keys, _ := s.Passkeys(ctx, ws, ada); string(keys[1].Record) != `{"id":"y"}` || !keys[1].LastUsed.Equal(t0) {
		t.Fatalf("the record after a sign-in: %+v", keys[1])
	}
	other, _ := s.SignIn(ctx, ws, ada, keys[1].ID, []byte(`{"id":"y"}`), "", t0)
	if err := s.EndSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, token, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session after signing out: %v", err)
	}
	s.w.Exec("DELETE FROM passkeys WHERE id = ?", keys[1].ID)
	if _, err := s.Session(ctx, other, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session after its passkey was removed: %v", err)
	}

	// A disabled person is signed out everywhere.
	link, _ = s.CreatePasskeyLink(ctx, ws, ada, t0)
	token, _ = s.AddPasskey(ctx, link, key("k3"), "", t0)
	s.w.Exec("UPDATE people SET disabled_at = 1 WHERE id = ?", ada)
	if _, err := s.Session(ctx, token, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a disabled person's session: %v", err)
	}
	if _, err := s.CreatePasskeyLink(ctx, ws, ada, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a link for a disabled person: %v", err)
	}
}

func TestEndedSessionsAndLinksArePruned(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada := newPerson(t, s, ws, "Ada")
	link, _ := s.CreatePasskeyLink(ctx, ws, ada, t0)
	s.AddPasskey(ctx, link, key("k1"), "", t0)
	s.CreatePasskeyLink(ctx, ws, ada, t0)
	if err := s.Alive(ctx, t0.Add(SessionIdle)); err != nil {
		t.Fatal(err)
	}
	var sessions, links int
	s.r.QueryRow("SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM passkey_links)").Scan(&sessions, &links)
	if sessions != 0 || links != 0 {
		t.Fatalf("%d sessions and %d links left", sessions, links)
	}
}

func TestAPersonByEmail(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	ws, _ := s.Workspace(ctx)
	ada := newPerson(t, s, ws, "Ada")
	if p, err := s.PersonByEmail(ctx, ws, "ADA@example.com"); err != nil || p.ID != ada || !p.Admin || p.TimeZone != "UTC" {
		t.Fatalf("%+v: %v", p, err)
	}
	if _, err := s.PersonByEmail(ctx, ws, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.SetSetting(ctx, "base_url", "https://allstill.example.com"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Setting(ctx, "base_url"); v != "https://allstill.example.com" {
		t.Fatalf("%q", v)
	}
	if v, err := s.Setting(ctx, "nothing"); v != "" || err != nil {
		t.Fatalf("%q, %v", v, err)
	}
}
