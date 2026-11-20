// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

// Accepting an invitation in the browser (intent/teams/plan-3.md, part A).

func (f fixture) invite(t *testing.T, role store.Role, label string) string {
	t.Helper()
	a, err := f.st.AccessOfPerson(t.Context(), f.ws, f.ada)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := f.st.Teams(t.Context(), f.ws)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := f.st.CreateInvite(t.Context(), a, teams[0].ID, "", role, label, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func offerOf(t *testing.T, b *browser, token string) map[string]any {
	t.Helper()
	var o map[string]any
	if err := json.Unmarshal(b.ok("POST", "/join/offer", map[string]string{"token": token}), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestSomeoneNewAcceptsAnInvitation(t *testing.T) {
	f := setup(t)
	token := f.invite(t, store.Editor, "Dora from the night shift")
	b := f.browser(t)
	if page := b.get("/join").Body.String(); !strings.Contains(page, `id="join"`) {
		t.Fatal("no join page")
	}

	// Before signing in: who invites and as what, not where, and never the label.
	o := offerOf(t, b, token)
	if o["offer"] != "Ada invites you as an editor." || o["where"] != nil || o["signed_in_as"] != nil {
		t.Errorf("%v", o)
	}
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "night shift") || strings.Contains(string(raw), "Allstill") {
		t.Errorf("the offer says more than who and what: %s", raw)
	}

	// A new account: the passkey is made for the id the ceremony began with.
	a := &authenticator{t: t, origin: site}
	var begun struct {
		Options json.RawMessage `json:"options"`
		Person  string          `json:"person"`
	}
	json.Unmarshal(b.ok("POST", "/join/begin", map[string]string{"token": token, "name": "Dora", "time_zone": "Europe/Amsterdam"}), &begun)
	credential := a.create(begun.Options)
	// Another id than the one begun with is refused.
	if w := b.do("POST", "/join/finish", map[string]any{"token": token, "name": "Dora", "person": "per_00000000000000000000000000", "credential": credential}); w.Code != http.StatusBadRequest {
		t.Errorf("another id: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(b.ok("POST", "/join/begin", map[string]string{"token": token, "name": "Dora", "time_zone": "Europe/Amsterdam"}), &begun)
	b.ok("POST", "/join/finish", map[string]any{"token": token, "name": "Dora", "time_zone": "Europe/Amsterdam", "person": begun.Person, "credential": a.create(begun.Options)})

	// Signed in now, they see where to, and accept.
	o = offerOf(t, b, token)
	if o["signed_in_as"] != "Dora" || o["where"] != "This makes you an editor of every project of the team Allstill." || o["has"] != false {
		t.Errorf("%v", o)
	}
	b.ok("POST", "/join/accept", map[string]string{"token": token})
	if page := b.get("/").Body.String(); !strings.Contains(page, "nightly-backup") {
		t.Error("after accepting, the team's checks are not shown")
	}

	// They sign out and in again with the passkey they made.
	b.do("POST", "/logout", nil)
	if w := b.signIn(a); w.Code != http.StatusOK {
		t.Fatalf("signing in again: %d %s", w.Code, w.Body)
	}
	if page := b.get("/").Body.String(); !strings.Contains(page, "nightly-backup") {
		t.Error("signed in again, the checks are not shown")
	}

	// Used: whoever opens it now is told so, and nothing more.
	other := f.browser(t)
	if w := other.do("POST", "/join/offer", map[string]string{"token": token}); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "accepted already") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestAnInvitationMakesOneAccount(t *testing.T) {
	f := setup(t)
	token := f.invite(t, store.Viewer, "x")
	b := f.browser(t)
	a := &authenticator{t: t, origin: site}
	var begun struct {
		Options json.RawMessage `json:"options"`
		Person  string          `json:"person"`
	}
	json.Unmarshal(b.ok("POST", "/join/begin", map[string]string{"token": token, "name": "First"}), &begun)
	b.ok("POST", "/join/finish", map[string]any{"token": token, "name": "First", "person": begun.Person, "credential": a.create(begun.Options)})
	// Signing out and trying again makes no second account.
	b.do("POST", "/logout", nil)
	if o := offerOf(t, b, token); o["account_made"] != true {
		t.Errorf("%v", o)
	}
	if w := b.do("POST", "/join/begin", map[string]string{"token": token, "name": "Second"}); w.Code != http.StatusConflict {
		t.Errorf("a second account: %d %s", w.Code, w.Body)
	}
	if w := b.do("POST", "/join/begin", map[string]string{"token": token, "name": "the team"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a name the log uses: %d %s", w.Code, w.Body)
	}
}

func TestSomeoneSignedInAcceptsAsThemselves(t *testing.T) {
	f := setup(t)
	token := f.invite(t, store.Editor, "Bob")
	b := f.browser(t)
	f.register(t, b, f.bob)
	if w := b.do("POST", "/join/accept", map[string]string{"token": "not-it"}); w.Code != http.StatusNotFound {
		t.Errorf("an unknown invitation: %d", w.Code)
	}
	o := offerOf(t, b, token)
	if o["signed_in_as"] != "Bob" || o["has"] != false {
		t.Errorf("%v", o)
	}
	b.ok("POST", "/join/accept", map[string]string{"token": token})
	a, _ := f.st.AccessOfPerson(t.Context(), f.ws, f.bob)
	if len(a.Teams) != 1 || a.Teams[0].Role != store.Editor {
		t.Errorf("%+v", a.Teams)
	}
	// The owner has a card saying so.
	owner := f.browser(t)
	f.register(t, owner, f.ada)
	if page := owner.get("/").Body.String(); !strings.Contains(page, "Bob accepted the invitation Ada made for “Bob”") && !strings.Contains(page, "Bob accepted the invitation Ada made for &#34;Bob&#34;") {
		t.Errorf("no card for the owner")
	}
	// The owner opening their own invitation has it already, and it stays open.
	token = f.invite(t, store.Viewer, "x")
	if w := owner.do("POST", "/join/accept", map[string]string{"token": token}); w.Code != http.StatusConflict {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if o := offerOf(t, owner, token); o["has"] != true {
		t.Errorf("%v", o)
	}
	// Signed out, accepting asks to sign in first.
	if w := f.browser(t).do("POST", "/join/accept", map[string]string{"token": token}); w.Code != http.StatusUnauthorized {
		t.Errorf("%d", w.Code)
	}
}

func TestAnOwnerRunsPeopleInTheDashboard(t *testing.T) {
	f := setup(t)
	teams, _ := f.st.Teams(t.Context(), f.ws)
	team := teams[0].ID
	owner := f.browser(t)
	f.register(t, owner, f.ada)
	page := owner.get("/teams/" + team).Body.String()
	for _, want := range []string{"Invite someone", "Ada", "Bob", "Outside the team, in one project"} {
		if !strings.Contains(page, want) {
			t.Errorf("the People page has no %q", want)
		}
	}
	// An invitation is shown once, as a link to send.
	w := owner.post("/teams/"+team+"/invites", url.Values{"role": {"editor"}, "label": {"Carol"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), site+"/join#") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(owner.get("/teams/"+team).Body.String(), "/join#") {
		t.Error("the invitation's link is shown again")
	}
	if log := owner.get("/log").Body.String(); !strings.Contains(log, "made an invitation as editor in the team Allstill, for") {
		t.Error("the log does not say an invitation was made")
	}
	if w := owner.post("/teams/"+team+"/invites", url.Values{"role": {"editor"}, "label": {" "}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("without saying for whom: %d", w.Code)
	}
	// The only owner is not made less.
	if w := owner.post("/teams/"+team+"/members/"+f.ada, url.Values{"role": {"editor"}}); w.Code != http.StatusConflict {
		t.Errorf("the last owner: %d", w.Code)
	}
	// Someone outside the team is not made a member by a role.
	if w := owner.post("/teams/"+team+"/members/"+f.bob, url.Values{"role": {"editor"}}); w.Code != http.StatusNotFound {
		t.Errorf("someone outside: %d", w.Code)
	}
	// Bob owns no team: there is no People page for him.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	if w := bob.get("/teams/" + team); w.Code != http.StatusNotFound {
		t.Errorf("someone in no team: %d", w.Code)
	}
}

func TestTakingSomeoneOutEndsWhatTheOwnerChooses(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	teams, _ := f.st.Teams(ctx, f.ws)
	team := teams[0].ID
	by := store.Command("test")
	if err := f.st.SetTeamMember(ctx, by, f.ws, team, f.bob, store.Editor); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, by, f.ws, f.project, f.bob, store.Owner); err != nil {
		t.Fatal(err)
	}
	bobAccess, _ := f.st.AccessOfPerson(ctx, f.ws, f.bob)
	if _, _, err := f.st.CreateAPIKey(ctx, bobAccess, f.project, "bobs-script", store.ScopeRead, time.Now()); err != nil {
		t.Fatal(err)
	}
	owner := f.browser(t)
	f.register(t, owner, f.ada)
	page := owner.get("/teams/" + team + "/members/" + f.bob + "/remove").Body.String()
	if !strings.Contains(page, "End their role as owner in Backups") || !strings.Contains(page, "Revoke the key bobs-script of Backups") {
		t.Fatalf("%s", page)
	}
	// The owner keeps his role in the project, and ends the key.
	keys, _ := f.st.APIKeys(ctx, bobAccess, f.project)
	w := owner.post("/teams/"+team+"/members/"+f.bob+"/remove", url.Values{"revoke-key": {keys[0].ID}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	a, _ := f.st.AccessOfPerson(ctx, f.ws, f.bob)
	if len(a.Teams) != 0 {
		t.Errorf("still in the team: %+v", a.Teams)
	}
	if role, ok := a.Role(f.project); !ok || role != store.Owner {
		t.Errorf("his own role went: %+v", a.Projects)
	}
	admin, _ := f.st.AdminAccess(ctx, f.ws, "test")
	if keys, _ := f.st.APIKeys(ctx, admin, f.project); len(keys) != 0 {
		t.Errorf("the key stayed: %+v", keys)
	}
	if said := owner.get("/teams/" + team).Body.String(); !strings.Contains(said, "Bob is out of the team.") {
		t.Error("not said")
	}
}
