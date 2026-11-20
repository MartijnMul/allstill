// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Invitations, as the store keeps them (intent/teams/plan-3.md, part A).

// passkey is a new passkey with a credential id of its own.
func passkey(name string) NewPasskey {
	return NewPasskey{CredentialID: []byte("cred-" + name), RPID: "allstill.example.com", Name: "Laptop", Record: []byte("{}")}
}

func notices(t *testing.T, w *wall, personID string) []Notice {
	t.Helper()
	ns, err := w.s.Notices(t.Context(), personID, 50)
	must(t, err)
	return ns
}

func readAll(t *testing.T, w *wall, people ...string) {
	t.Helper()
	for _, p := range people {
		must(t, w.s.ReadNotices(t.Context(), p, time.Now().Add(time.Hour)))
	}
}

func unreadTitles(t *testing.T, w *wall, personID string) []string {
	t.Helper()
	var out []string
	for _, n := range notices(t, w, personID) {
		if !n.Read {
			out = append(out, n.Title)
		}
	}
	return out
}

// pendingFor counts the messages waiting to go to a person.
func pendingFor(t *testing.T, w *wall, personID string) int {
	t.Helper()
	var n int
	must(t, w.s.r.QueryRow(`SELECT count(*) FROM deliveries WHERE person_id = ?`, personID).Scan(&n))
	return n
}

// A channel a person has confirmed, so that a loud notice has somewhere to go.
func confirmedChannel(t *testing.T, w *wall, personID string) {
	t.Helper()
	_, err := w.s.w.Exec(`INSERT INTO channels (id, workspace_id, person_id, kind, name, config, created_at, verified_at)
		VALUES (?, ?, ?, 'email', 'Mail', ?, 0, 0)`, "chn_"+personID, w.ws, personID, `{"address":"`+personID+`@example.com"}`)
	must(t, err)
}

func TestSomeoneNewComesInByAnInvitation(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	confirmedChannel(t, w, w.hal.PersonID)
	readAll(t, w, w.hal.PersonID)
	before := pendingFor(t, w, w.hal.PersonID)

	inv, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Editor, " Dora ", w.at)
	must(t, err)
	if inv.Label != "Dora" || inv.Kind != InviteToTeam || !inv.Ends.Equal(w.at.Add(7*24*time.Hour)) {
		t.Errorf("%+v", inv)
	}
	offer, err := w.s.InviteOffer(ctx, secret, w.at)
	must(t, err)
	if offer.MadeBy != "Hal" || offer.Role != Editor || offer.TeamName != "Harbour" || offer.ProjectName != "" {
		t.Errorf("%+v", offer)
	}

	// Someone new makes an account: no email, no administrator, and not yet in.
	dora := "per_dora"
	_, err = w.s.CreatePersonByInvite(ctx, secret, dora, "Dora", "Europe/Amsterdam", passkey("dora"), "test", w.at)
	must(t, err)
	p, err := w.s.Person(ctx, w.ws, dora)
	must(t, err)
	if p.Email != "" || p.Admin || p.TimeZone != "Europe/Amsterdam" {
		t.Errorf("%+v", p)
	}
	if a := w.access(t, dora); len(a.Projects) != 0 {
		t.Errorf("making an account let them in before accepting: %+v", a.Projects)
	}
	// One invitation makes one account, also after signing out to try again.
	if _, err := w.s.CreatePersonByInvite(ctx, secret, "per_mallory", "Mallory", "", passkey("mallory"), "test", w.at); !errors.Is(err, ErrInviteMadeAccount) {
		t.Errorf("a second account: %v", err)
	}

	must(t, w.s.AcceptInvite(ctx, secret, w.ws, dora, w.at))
	a := w.access(t, dora)
	if role, ok := a.Role(w.theirs); !ok || role != Editor {
		t.Errorf("after accepting: %+v", a.Projects)
	}
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.hector.PersonID, w.at); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("used twice: %v", err)
	}
	if _, err := w.s.InviteOffer(ctx, secret, w.at); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("%v", err)
	}

	// The owner sees a card, and is not messaged: an editor arriving is routine.
	got := unreadTitles(t, w, w.hal.PersonID)
	if len(got) != 1 || got[0] != `Dora accepted the invitation Hal made for "Dora" and is now an editor of the team Harbour.` {
		t.Errorf("%q", got)
	}
	if n := pendingFor(t, w, w.hal.PersonID); n != before {
		t.Errorf("an editor arriving messaged the owner: %d", n-before)
	}

	people, err := w.s.PeopleOfTeam(ctx, w.hal, w.second, w.at)
	must(t, err)
	var came Member
	for _, m := range people.Members {
		if m.ID == dora {
			came = m
		}
	}
	if came.CameIn != "Dora" || came.InvitedBy != "Hal" || came.Role != Editor {
		t.Errorf("%+v", came)
	}
}

func TestAnInvitationEndsIsWithdrawnAndIsBounded(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	_, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Viewer, "Someone", w.at)
	must(t, err)
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.ada.PersonID, w.at.Add(InviteLifetime)); !errors.Is(err, ErrLinkExpired) {
		t.Errorf("after seven days: %v", err)
	}
	inv, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Viewer, "Someone else", w.at)
	must(t, err)
	if err := w.s.WithdrawInvite(ctx, w.ada, inv.ID, w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("withdrawn by another team's owner: %v", err)
	}
	if err := w.s.WithdrawInvite(ctx, w.hedy, inv.ID, w.at); !errors.Is(err, ErrForbidden) {
		t.Errorf("withdrawn by an editor: %v", err)
	}
	must(t, w.s.WithdrawInvite(ctx, w.hal, inv.ID, w.at))
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.ada.PersonID, w.at); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("withdrawn: %v", err)
	}
	if _, err := w.s.InviteOffer(ctx, "not-a-secret", w.at); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	// A team holds so many open ones, and no more.
	for i := range maxOpenTeamInvites - 1 {
		_, _, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Viewer, "n", w.at.Add(time.Duration(i)))
		must(t, err)
	}
	var invalid *InvalidError
	if _, _, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Viewer, "one more", w.at); !errors.As(err, &invalid) {
		t.Errorf("past the bound: %v", err)
	}
	// Ended and withdrawn ones go after a while; tidying does it.
	if _, err := w.s.Tidy(ctx, w.at.Add(40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.s, "invites"); n != 0 {
		t.Errorf("%d invitations left", n)
	}
}

func TestWhoMayInvite(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	for _, c := range []struct {
		who     string
		a       Access
		project string
		want    error
	}{
		{"an editor of the team", w.hedy, "", ErrForbidden},
		{"a viewer of the team", w.hector, "", ErrForbidden},
		{"an administrator in no team", w.zed, "", ErrNotFound},
		{"the other team's owner", w.ada, "", ErrNotFound},
		{"an editor of one project", w.carol, w.theirs, ErrForbidden},
		{"another team's owner, to one project", w.ada, w.theirs, ErrNotFound},
		{"a read-only sign-in of the owner", w.hal.ReadOnly(), "", ErrForbidden},
	} {
		if _, _, err := w.s.CreateInvite(ctx, c.a, w.second, c.project, Viewer, "x", w.at); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.who, err, c.want)
		}
	}
	// An owner of one project invites to it, from outside the team.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.carol.PersonID, Owner))
	carol := w.access(t, w.carol.PersonID)
	inv, _, err := w.s.CreateInvite(ctx, carol, "", w.theirs, Editor, "a colleague", w.at)
	must(t, err)
	if inv.TeamID != w.second || inv.ProjectName != "Backups" {
		t.Errorf("%+v", inv)
	}
	// Owner is given only from the dashboard, never with a command line's sign-in.
	cli := w.hal
	cli.Actor.Via = "laptop"
	var invalid *InvalidError
	if _, _, err := w.s.CreateInvite(ctx, cli, w.second, "", Owner, "x", w.at); !errors.As(err, &invalid) {
		t.Errorf("an owner's invitation from a command line: %v", err)
	}
	if _, _, err := w.s.CreateInvite(ctx, cli, w.second, "", Editor, "x", w.at); err != nil {
		t.Errorf("an editor's from a command line: %v", err)
	}
	if _, _, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Editor, "  ", w.at); !errors.As(err, &invalid) {
		t.Errorf("without saying for whom: %v", err)
	}
}

func TestAnOwnersInvitationIsToldAtOnce(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	confirmedChannel(t, w, w.hal.PersonID)
	readAll(t, w, w.hal.PersonID)
	before := pendingFor(t, w, w.hal.PersonID)
	_, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Owner, "a second owner", w.at)
	must(t, err)
	if n := pendingFor(t, w, w.hal.PersonID); n != before+1 {
		t.Errorf("an owner's invitation went to %d channels", n-before)
	}
	must(t, w.s.AcceptInvite(ctx, secret, w.ws, w.hedy.PersonID, w.at))
	if n := pendingFor(t, w, w.hal.PersonID); n != before+2 {
		t.Errorf("an owner arriving went to %d channels", n-before)
	}
	got := unreadTitles(t, w, w.hal.PersonID)
	if len(got) != 2 || !strings.Contains(got[0], "Hedy accepted") || !strings.Contains(got[1], "Hal made an invitation as owner") {
		t.Errorf("%q", got)
	}
}

func TestSomeoneWithTheRoleLeavesTheInvitationOpen(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	_, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Editor, "x", w.at)
	must(t, err)
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.hal.PersonID, w.at); !errors.Is(err, ErrAlreadyHas) {
		t.Errorf("the owner opening their own: %v", err)
	}
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.hedy.PersonID, w.at); !errors.Is(err, ErrAlreadyHas) {
		t.Errorf("an editor already: %v", err)
	}
	// Still open for whom it was meant.
	must(t, w.s.AcceptInvite(ctx, secret, w.ws, w.carol.PersonID, w.at))
	carol := w.access(t, w.carol.PersonID)
	if role, in := carol.TeamRole(w.second); !in || role != Editor {
		t.Errorf("%+v", carol.Teams)
	}
}

func TestAProjectsInvitationGivesARoleOfOnesOwn(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	_, secret, err := w.s.CreateInvite(ctx, w.hal, "", w.theirs, Viewer, "a customer", w.at)
	must(t, err)
	offer, err := w.s.InviteOffer(ctx, secret, w.at)
	must(t, err)
	if offer.Kind != InviteToProject || offer.ProjectName != "Backups" {
		t.Errorf("%+v", offer)
	}
	must(t, w.s.AcceptInvite(ctx, secret, w.ws, w.zed.PersonID, w.at))
	zed := w.access(t, w.zed.PersonID)
	if role, ok := zed.Role(w.theirs); !ok || role != Viewer || len(zed.Teams) != 0 {
		t.Errorf("%+v %+v", zed.Projects, zed.Teams)
	}
	// A viewer by invitation is not told, as a viewer's role gives.
	var told int
	must(t, w.s.r.QueryRow(`SELECT count(*) FROM project_alerted WHERE person_id = ? AND project_id = ?`, w.zed.PersonID, w.theirs).Scan(&told))
	if told != 0 {
		t.Error("a viewer is told")
	}
}

func TestAnInvitationDiesWithItsMakersRight(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	_, secret, err := w.s.CreateInvite(ctx, w.hal, w.second, "", Editor, "x", w.at)
	must(t, err)
	// Hedy becomes owner, from the dashboard, and makes Hal less.
	must(t, setTeamMemberAs(w.s, ctx, w.hal, w.second, w.hedy.PersonID, Owner))
	hedy := w.access(t, w.hedy.PersonID)
	must(t, setTeamMemberAs(w.s, ctx, hedy, w.second, w.hal.PersonID, Editor))
	if _, err := w.s.InviteOffer(ctx, secret, time.Now()); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("an invitation of someone made less: %v", err)
	}
	// An owner of one project loses theirs with the role.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.carol.PersonID, Owner))
	carol := w.access(t, w.carol.PersonID)
	_, secret, err = w.s.CreateInvite(ctx, carol, "", w.theirs, Viewer, "x", w.at)
	must(t, err)
	must(t, w.s.RemoveMember(ctx, Command("test"), w.ws, w.theirs, w.carol.PersonID))
	if _, err := w.s.InviteOffer(ctx, secret, time.Now()); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("an invitation of someone taken out of the project: %v", err)
	}
}

func TestAnOwnerRunsTheTeamsPeople(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	confirmedChannel(t, w, w.hal.PersonID)
	readAll(t, w, w.hal.PersonID, w.hedy.PersonID)
	// Only members, by an owner of the team.
	if err := setTeamMemberAs(w.s, ctx, w.hal, w.second, w.carol.PersonID, Editor); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone outside made a member: %v", err)
	}
	if err := setTeamMemberAs(w.s, ctx, w.hedy, w.second, w.hector.PersonID, Editor); !errors.Is(err, ErrForbidden) {
		t.Errorf("by an editor: %v", err)
	}
	if err := setTeamMemberAs(w.s, ctx, w.ada, w.second, w.hector.PersonID, Editor); !errors.Is(err, ErrNotFound) {
		t.Errorf("by another team's owner: %v", err)
	}
	if err := setTeamMemberAs(w.s, ctx, w.hal, w.second, w.hal.PersonID, Editor); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner: %v", err)
	}
	if _, err := w.s.RemoveTeamMemberAs(ctx, w.hal, w.second, w.hal.PersonID); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the last owner out: %v", err)
	}
	// A viewer or an editor changes from anywhere, and tells nobody.
	cli := w.hal
	cli.Actor.Via = "laptop"
	before := pendingFor(t, w, w.hal.PersonID)
	must(t, setTeamMemberAs(w.s, ctx, cli, w.second, w.hector.PersonID, Editor))
	if got := unreadTitles(t, w, w.hal.PersonID); len(got) != 0 {
		t.Errorf("an editor's change told: %q", got)
	}
	// Owners change only in the dashboard, and every owner is told.
	var invalid *InvalidError
	if err := setTeamMemberAs(w.s, ctx, cli, w.second, w.hedy.PersonID, Owner); !errors.As(err, &invalid) {
		t.Errorf("an owner named from a command line: %v", err)
	}
	must(t, setTeamMemberAs(w.s, ctx, w.hal, w.second, w.hedy.PersonID, Owner))
	if got := unreadTitles(t, w, w.hal.PersonID); len(got) != 1 || got[0] != "Hal made Hedy owner of the team Harbour." {
		t.Errorf("%q", got)
	}
	if pendingFor(t, w, w.hal.PersonID) != before+1 {
		t.Error("naming an owner was not messaged")
	}
	hedy := w.access(t, w.hedy.PersonID)
	if _, err := w.s.RemoveTeamMemberAs(ctx, func() Access { a := hedy; a.Actor.Via = "laptop"; return a }(), w.second, w.hal.PersonID); !errors.As(err, &invalid) {
		t.Errorf("an owner taken out from a command line: %v", err)
	}

	// Taken out, they keep what is theirs in the team's projects, and are told so.
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.hector.PersonID, Owner))
	readAll(t, w, w.hedy.PersonID)
	stays, err := w.s.RemoveTeamMemberAs(ctx, w.hal, w.second, w.hector.PersonID)
	must(t, err)
	if len(stays.Roles) != 1 || stays.Roles[0].ProjectName != "Backups" || stays.Roles[0].Role != Owner {
		t.Errorf("%+v", stays)
	}
	hector := w.access(t, w.hector.PersonID)
	if _, in := hector.TeamRole(w.second); in {
		t.Error("still in the team")
	}
	if got := unreadTitles(t, w, w.hedy.PersonID); len(got) != 0 {
		t.Errorf("taking a viewer out told the owners: %q", got)
	}
	must(t, w.s.RemoveMember(ctx, Command("test"), w.ws, w.theirs, w.hector.PersonID))
	if a := w.access(t, w.hector.PersonID); len(a.Projects) != 0 {
		t.Errorf("%+v", a.Projects)
	}
}

func TestANewPersonsNameIsTheirsAndNotTheLogs(t *testing.T) {
	if _, err := NewPersonName(strings.Repeat("é", 100)); err != nil {
		t.Errorf("a hundred characters are bytes: %v", err)
	}
	for _, name := range []string{"", "  ", strings.Repeat("x", 101), "The team", "an administrator", "Allstill"} {
		if _, err := NewPersonName(name); err == nil {
			t.Errorf("%q was taken", name)
		}
	}
	if got, err := NewPersonName("  Dora Shah "); err != nil || got != "Dora Shah" {
		t.Errorf("%q %v", got, err)
	}
	for tz, want := range map[string]string{"Europe/Amsterdam": "Europe/Amsterdam", "": "UTC", "Mars/Olympus": "UTC", "Local": "UTC"} {
		if got := NewTimeZone(tz); got != want {
			t.Errorf("%q: %q", tz, got)
		}
	}
}

// The access a request carries was read when it came. An owner made less in the
// meantime makes no invitation and changes no role, and an invitation whose
// maker lost the right by any path is not accepted.
func TestARightLostMeanwhileCounts(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	stale := w.hal
	must(t, w.s.SetTeamMember(ctx, Command("test"), w.ws, w.second, w.hedy.PersonID, Owner))
	must(t, w.s.SetTeamMember(ctx, Command("test"), w.ws, w.second, w.hal.PersonID, Editor))
	if _, _, err := w.s.CreateInvite(ctx, stale, w.second, "", Owner, "x", w.at); !errors.Is(err, ErrForbidden) {
		t.Errorf("an invitation by someone made less meanwhile: %v", err)
	}
	if err := setTeamMemberAs(w.s, ctx, stale, w.second, w.hector.PersonID, Editor); !errors.Is(err, ErrForbidden) {
		t.Errorf("a role changed by someone made less meanwhile: %v", err)
	}
	hedy := w.access(t, w.hedy.PersonID)
	_, secret, err := w.s.CreateInvite(ctx, hedy, w.second, "", Viewer, "x", time.Now())
	must(t, err)
	// Her right goes by a path that withdraws nothing.
	_, err = w.s.w.Exec(`UPDATE team_members SET role = 'viewer' WHERE person_id = ?`, w.hedy.PersonID)
	must(t, err)
	if err := w.s.AcceptInvite(ctx, secret, w.ws, w.carol.PersonID, time.Now()); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("an invitation whose maker may no longer make it: %v", err)
	}
	if _, err := w.s.InviteOffer(ctx, secret, time.Now()); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("it still offers: %v", err)
	}
	if _, err := w.s.CreatePersonByInvite(ctx, secret, "per_x", "X", "", passkey("x"), "test", time.Now()); !errors.Is(err, ErrInviteWithdrawn) {
		t.Errorf("it still makes an account: %v", err)
	}
}

// An owner of one project from outside the team who made the invitation reads
// their own name and label; any other reads none of the team's people. Nobody
// is told of their own arrival.
func TestWhoIsToldOfAnArrival(t *testing.T) {
	w := newWall(t)
	ctx := t.Context()
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, w.carol.PersonID, Owner))
	otto := w.person(t, "Otto", false, false)
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.theirs, otto, Owner))
	readAll(t, w, w.hal.PersonID, w.carol.PersonID, otto)
	carol := w.access(t, w.carol.PersonID)
	_, secret, err := w.s.CreateInvite(ctx, carol, "", w.theirs, Viewer, "Carol's colleague", w.at)
	must(t, err)
	must(t, w.s.AcceptInvite(ctx, secret, w.ws, w.zed.PersonID, w.at))
	if got := unreadTitles(t, w, w.carol.PersonID); len(got) != 1 || got[0] != `Zed accepted the invitation Carol made for "Carol's colleague" and is now a viewer of the project Backups.` {
		t.Errorf("the maker: %q", got)
	}
	if got := unreadTitles(t, w, otto); len(got) != 1 || got[0] != "Zed accepted an invitation and is now a viewer of the project Backups." {
		t.Errorf("another owner outside the team: %q", got)
	}
	// An owner arriving is not told of themselves; an owner's invitation to one
	// project says so.
	readAll(t, w, w.hal.PersonID)
	_, secret, err = w.s.CreateInvite(ctx, w.hal, "", w.theirs, Owner, "a second owner", w.at)
	must(t, err)
	if got := unreadTitles(t, w, w.hal.PersonID); len(got) != 1 || !strings.Contains(got[0], "as owner of the project Backups") {
		t.Errorf("%q", got)
	}
	readAll(t, w, w.hedy.PersonID)
	must(t, w.s.AcceptInvite(ctx, secret, w.ws, w.hedy.PersonID, w.at))
	if got := unreadTitles(t, w, w.hedy.PersonID); len(got) != 0 {
		t.Errorf("told of their own arrival: %q", got)
	}
}

// setTeamMemberAs is SetTeamMemberAs for a test that needs only whether it worked.
func setTeamMemberAs(s *Store, ctx context.Context, a Access, teamID, personID string, role Role) error {
	_, err := s.SetTeamMemberAs(ctx, a, teamID, personID, role)
	return err
}
