// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A team's owners let people in themselves, by an invitation they hand over
// (intent/teams/spec.md, R19 to R21; plan-3.md, part A). Nothing is looked up
// by name or address: whoever holds the invitation is whom it was given to,
// and the owners see who accepted it.

// The kinds of invitation. A share is the next (step 4).
const (
	InviteToTeam    = "team"    // a role in a team, so in all its projects
	InviteToProject = "project" // a role of one's own in one project
)

const (
	// InviteLifetime is how long an invitation can be accepted.
	InviteLifetime = 7 * 24 * time.Hour
	// How many open invitations a team may hold to itself, and a project to
	// itself, so that owners of one project cannot use up the team's.
	maxOpenTeamInvites    = 50
	maxOpenProjectInvites = 20
	// staleInvites is how long one that ended or was withdrawn is kept.
	staleInvites = 30 * 24 * time.Hour
)

var (
	// ErrInviteWithdrawn means the invitation was withdrawn by whoever may.
	ErrInviteWithdrawn = errors.New("this invitation was withdrawn")
	// ErrAlreadyHas means whoever accepts has the role, or more, already; the
	// invitation stays open for whom it was meant.
	ErrAlreadyHas = errors.New("you have this role already")
	// ErrInviteMadeAccount means an account was made through this invitation
	// already; it makes one at most.
	ErrInviteMadeAccount = errors.New("an account was made with this invitation already")
)

// Invite is an invitation as those who may make it see it.
type Invite struct {
	ID          string
	Kind        string
	TeamID      string
	TeamName    string
	ProjectID   string // for an invitation to one project
	ProjectName string
	Role        Role
	Label       string
	MadeBy      LogParty
	Created     time.Time
	Ends        time.Time
}

// inDashboard says an access is a person signed in to the dashboard with a
// passkey, not a sign-in from a command line, which is a token that lasts.
// Who holds a team changes only there (plan-3.md, readings).
func inDashboard(a Access) bool { return a.Actor.Kind == "person" && a.Actor.Via == "" }

// errOwnerInDashboard refuses a change to a team's owners from anywhere else.
var errOwnerInDashboard = &InvalidError{"role", "an owner is named, made less or taken out in the dashboard, signed in with a passkey"}

// inviteLabel checks what the maker writes for whom an invitation is.
func inviteLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" || utf8.RuneCountInString(label) > 100 {
		return "", &InvalidError{"label", "say for whom it is, in 1 to 100 characters"}
	}
	return label, nil
}

// CreateInvite makes an invitation: to a team with projectID "", or to one of
// its projects. It returns the invitation and its secret, which is shown once.
// A team's invitation takes an owner of the team, a project's an owner of the
// project by any way in (R4). Owner is given only from the dashboard.
func (s *Store) CreateInvite(ctx context.Context, a Access, teamID, projectID string, role Role, label string, at time.Time) (Invite, string, error) {
	if a.Actor.Kind != "person" {
		return Invite{}, "", ErrForbidden
	}
	if _, err := ParseRole(string(role)); err != nil {
		return Invite{}, "", &InvalidError{"role", "give viewer, editor or owner"}
	}
	kind := InviteToTeam
	if projectID != "" {
		kind = InviteToProject
		have, ok := a.Role(projectID)
		if !ok {
			return Invite{}, "", ErrNotFound
		}
		if have != Owner {
			return Invite{}, "", ErrForbidden
		}
	} else {
		have, in := a.TeamRole(teamID)
		if !in {
			return Invite{}, "", ErrNotFound
		}
		if have != Owner {
			return Invite{}, "", ErrForbidden
		}
	}
	if role == Owner && !inDashboard(a) {
		return Invite{}, "", errOwnerInDashboard
	}
	label, err := inviteLabel(label)
	if err != nil {
		return Invite{}, "", err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	var projectName string
	if kind == InviteToProject {
		if teamID, err = q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: a.WorkspaceID, ID: projectID}); err != nil {
			return Invite{}, "", ErrNotFound
		}
	}
	// The right is read again here: the access was read when the request came, and
	// whoever made it may have been made less since (plan-3.md).
	if may, err := mayMakeInvite(ctx, q, teamID, kind, projectID, a.PersonID); err != nil {
		return Invite{}, "", err
	} else if !may {
		return Invite{}, "", ErrForbidden
	}
	if kind == InviteToProject {
		// Read live here: a project deleted a moment ago takes no invitation.
		p, err := q.LiveProject(ctx, db.LiveProjectParams{WorkspaceID: a.WorkspaceID, ID: projectID})
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, "", ErrNotFound
		} else if err != nil {
			return Invite{}, "", err
		}
		teamID, projectName = p.TeamID, p.Name
		n, err := q.CountOpenInvitesOfProject(ctx, db.CountOpenInvitesOfProjectParams{ProjectID: sql.NullString{String: projectID, Valid: true}, At: at.UnixMilli()})
		if err != nil {
			return Invite{}, "", err
		}
		if n >= maxOpenProjectInvites {
			return Invite{}, "", &InvalidError{"invite", "this project has as many open invitations as it may: withdraw one first"}
		}
	} else {
		n, err := q.CountOpenInvitesOfTeam(ctx, db.CountOpenInvitesOfTeamParams{TeamID: teamID, Kind: InviteToTeam, At: at.UnixMilli()})
		if err != nil {
			return Invite{}, "", err
		}
		if n >= maxOpenTeamInvites {
			return Invite{}, "", &InvalidError{"invite", "this team has as many open invitations as it may: withdraw one first"}
		}
	}
	team, err := q.Team(ctx, db.TeamParams{WorkspaceID: a.WorkspaceID, ID: teamID})
	if err != nil {
		return Invite{}, "", err
	}
	secret := id.Key()
	inv := Invite{
		TeamName: team.Name,
		ID:       id.New(id.Invite), Kind: kind, TeamID: teamID, ProjectID: projectID, ProjectName: projectName, Role: role, Label: label,
		MadeBy: LogParty{"person", a.PersonID, a.Actor.Name}, Created: at.UTC(), Ends: at.Add(InviteLifetime).UTC(),
	}
	err = q.CreateInvite(ctx, db.CreateInviteParams{
		ID: inv.ID, WorkspaceID: a.WorkspaceID, TeamID: teamID, ProjectID: sql.NullString{String: projectID, Valid: projectID != ""},
		Kind: kind, Role: string(role), Label: label, SecretHash: hash(secret), MadeBy: a.PersonID,
		CreatedAt: at.UnixMilli(), ExpiresAt: at.Add(InviteLifetime).UnixMilli(),
	})
	if err != nil {
		return Invite{}, "", err
	}
	if err := writeLog(ctx, q, inviteEntry(a.WorkspaceID, inv, "invite.made", a.Actor, at)); err != nil {
		return Invite{}, "", err
	}
	if role == Owner {
		// Whoever holds it can become an owner: every owner hears of it at once.
		err = noticeOwners(ctx, q, a.WorkspaceID, []string{teamID}, nil, noticeOwnerInvite,
			map[string]any{"by": a.Actor.Name, "team": team.Name, "project": projectName, "label": label}, at, true)
		if err != nil {
			return Invite{}, "", err
		}
	}
	return inv, secret, tx.Commit()
}

// inviteEntry is a log line about an invitation: in its project for one to a
// project, in its team otherwise, with the team's name for its owners.
func inviteEntry(workspaceID string, inv Invite, kind string, by Actor, at time.Time) logEntry {
	e := logEntry{
		WorkspaceID: workspaceID, At: at, Kind: kind, AboutKind: "invite", AboutID: inv.ID, AboutName: inv.Label, By: by,
		Details: map[string]any{"role": string(inv.Role), "label": inv.Label},
	}
	if inv.ProjectID != "" {
		e.ProjectID, e.ProjectName = inv.ProjectID, inv.ProjectName
	} else {
		e.TeamID = inv.TeamID
		e.Details["team"] = inv.TeamName
	}
	return e
}

// OpenInvites lists a team's open invitations, newest first, for an owner of
// the team: to the team and to each of its projects.
func (s *Store) OpenInvites(ctx context.Context, a Access, teamID string, at time.Time) ([]Invite, error) {
	if role, in := a.TeamRole(teamID); !in {
		return nil, ErrNotFound
	} else if role != Owner {
		return nil, ErrForbidden
	}
	rows, err := db.New(s.r).InvitesOfTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	var open []Invite
	for _, r := range rows {
		if r.AcceptedAt.Valid || r.WithdrawnAt.Valid || r.ExpiresAt <= at.UnixMilli() {
			continue
		}
		open = append(open, Invite{
			ID: r.ID, Kind: r.Kind, TeamID: r.TeamID, ProjectID: r.ProjectID, ProjectName: r.ProjectName, Role: Role(r.Role), Label: r.Label,
			MadeBy: LogParty{"person", r.MadeBy, r.MadeByName}, Created: time.UnixMilli(r.CreatedAt).UTC(), Ends: time.UnixMilli(r.ExpiresAt).UTC(),
		})
	}
	return open, nil
}

// mayMakeInvite says whether a person may still make an invitation of this kind,
// as the database holds it now.
func mayMakeInvite(ctx context.Context, q *db.Queries, teamID, kind, projectID, personID string) (bool, error) {
	if kind == InviteToProject {
		role, err := q.HighestRoleInProject(ctx, db.HighestRoleInProjectParams{ProjectID: projectID, PersonID: personID})
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return Role(role) == Owner, err
	}
	role, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return Role(role) == Owner, err
}

// WithdrawInvite withdraws an open invitation, for whoever may make one like it.
func (s *Store) WithdrawInvite(ctx context.Context, a Access, inviteID string, at time.Time) error {
	if a.Actor.Kind != "person" {
		return ErrForbidden
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	r, err := q.InviteByID(ctx, db.InviteByIDParams{WorkspaceID: a.WorkspaceID, ID: inviteID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	// Someone who cannot see where it is for is told it does not exist.
	var sees, may bool
	if r.ProjectID.Valid {
		role, ok := a.Role(r.ProjectID.String)
		sees, may = ok, role == Owner
	} else {
		role, in := a.TeamRole(r.TeamID)
		sees, may = in, role == Owner
	}
	if !sees {
		return ErrNotFound
	}
	if !may {
		return ErrForbidden
	}
	n, err := q.WithdrawInvite(ctx, db.WithdrawInviteParams{At: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, WorkspaceID: a.WorkspaceID, ID: inviteID})
	if err != nil {
		return err
	}
	if n == 0 {
		return &InvalidError{"invite", "it was accepted or withdrawn already"}
	}
	inv := Invite{ID: r.ID, TeamID: r.TeamID, ProjectID: r.ProjectID.String, Role: Role(r.Role), Label: r.Label}
	team, err := q.Team(ctx, db.TeamParams{WorkspaceID: a.WorkspaceID, ID: r.TeamID})
	if err != nil {
		return err
	}
	inv.TeamName = team.Name
	if inv.ProjectID != "" {
		if inv.ProjectName, err = q.NameOfProject(ctx, inv.ProjectID); err != nil {
			return err
		}
	}
	if err := writeLog(ctx, q, inviteEntry(a.WorkspaceID, inv, "invite.withdrawn", a.Actor, at)); err != nil {
		return err
	}
	return tx.Commit()
}

// withdrawUnbacked withdraws the open invitations a person made in a team that
// they may no longer make, in the transaction that took away their right: an
// invitation made beforehand must not be a way back in (plan-3.md).
func withdrawUnbacked(ctx context.Context, q *db.Queries, workspaceID, teamID, personID string, at time.Time) error {
	open, err := q.OpenInvitesMadeIn(ctx, db.OpenInvitesMadeInParams{TeamID: teamID, MadeBy: personID, At: at.UnixMilli()})
	if err != nil {
		return err
	}
	for _, o := range open {
		may, err := mayMakeInvite(ctx, q, teamID, o.Kind, o.ProjectID, personID)
		if err != nil {
			return err
		}
		if may {
			continue
		}
		if _, err := q.WithdrawInvite(ctx, db.WithdrawInviteParams{At: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, WorkspaceID: workspaceID, ID: o.ID}); err != nil {
			return err
		}
		r, err := q.InviteByID(ctx, db.InviteByIDParams{WorkspaceID: workspaceID, ID: o.ID})
		if err != nil {
			return err
		}
		team, err := q.Team(ctx, db.TeamParams{WorkspaceID: workspaceID, ID: teamID})
		if err != nil {
			return err
		}
		inv := Invite{ID: o.ID, TeamID: teamID, TeamName: team.Name, ProjectID: o.ProjectID, Role: Role(r.Role), Label: r.Label}
		if o.ProjectID != "" {
			if inv.ProjectName, err = q.NameOfProject(ctx, o.ProjectID); err != nil {
				return err
			}
		}
		line := inviteEntry(workspaceID, inv, "invite.withdrawn", byAllstill, at)
		line.Details["maker_may_not"] = true
		if err := writeLog(ctx, q, line); err != nil {
			return err
		}
	}
	return nil
}

// Offer is what an invitation offers. Before signing in, whoever opens it is
// told who invites and as what; where to, once they are signed in.
type Offer struct {
	Kind        string
	Role        Role
	MadeBy      string
	TeamName    string
	ProjectName string
	// MadeAccount is the account made through it, if one was.
	MadeAccount string
}

// openInvite reads an invitation by its secret, or says why it cannot be accepted.
func openInvite(ctx context.Context, q *db.Queries, secret string, at time.Time) (db.InviteBySecretRow, error) {
	r, err := q.InviteBySecret(ctx, hash(secret))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return r, ErrNotFound
	case err != nil:
		return r, err
	case r.AcceptedAt.Valid:
		return r, ErrLinkUsed
	case r.WithdrawnAt.Valid:
		return r, ErrInviteWithdrawn
	case r.ExpiresAt <= at.UnixMilli():
		return r, ErrLinkExpired
	}
	// It is worth what its maker may still give, whatever withdrew it or did not:
	// it offers nothing, and makes no account, once they may not.
	if may, err := mayMakeInvite(ctx, q, r.TeamID, r.Kind, r.ProjectID.String, r.MadeBy); err != nil {
		return r, err
	} else if !may {
		return r, ErrInviteWithdrawn
	}
	return r, nil
}

// InviteOffer returns what an open invitation offers: ErrNotFound for one that is
// unknown or to a project that was deleted, ErrLinkUsed, ErrInviteWithdrawn or
// ErrLinkExpired otherwise.
func (s *Store) InviteOffer(ctx context.Context, secret string, at time.Time) (Offer, error) {
	r, err := openInvite(ctx, db.New(s.r), secret, at)
	if err != nil {
		return Offer{}, err
	}
	return Offer{
		Kind: r.Kind, Role: Role(r.Role), MadeBy: r.MadeByName, TeamName: r.TeamName, ProjectName: r.ProjectName,
		MadeAccount: r.MadePerson.String,
	}, nil
}

// HasOffered says whether a person has what an invitation offers already, or more.
func (s *Store) HasOffered(ctx context.Context, secret, personID string, at time.Time) (bool, error) {
	q := db.New(s.r)
	r, err := openInvite(ctx, q, secret, at)
	if err != nil {
		return false, err
	}
	return hasOffered(ctx, q, r, personID)
}

func hasOffered(ctx context.Context, q *db.Queries, r db.InviteBySecretRow, personID string) (bool, error) {
	var have string
	var err error
	if r.ProjectID.Valid {
		have, err = q.HighestRoleInProject(ctx, db.HighestRoleInProjectParams{ProjectID: r.ProjectID.String, PersonID: personID})
	} else {
		have, err = q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: r.TeamID, PersonID: personID})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && roleAtLeast(Role(have), Role(r.Role)), err
}

// AcceptInvite gives a person what an invitation offers, and uses it up. Someone
// who has it already gets ErrAlreadyHas, and the invitation stays open.
func (s *Store) AcceptInvite(ctx context.Context, secret, workspaceID, personID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	r, err := openInvite(ctx, q, secret, at)
	if err != nil {
		return err
	}
	if r.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	person, err := q.Person(ctx, db.PersonParams{WorkspaceID: workspaceID, ID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if has, err := hasOffered(ctx, q, r, personID); err != nil {
		return err
	} else if has {
		return ErrAlreadyHas
	}
	n, err := q.AcceptInvite(ctx, db.AcceptInviteParams{
		At: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, PersonID: sql.NullString{String: personID, Valid: true}, ID: r.ID,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLinkUsed
	}
	by := Actor{Kind: "person", ID: personID, Name: person.Name, Admin: person.WorkspaceAdmin == 1}
	inv := Invite{ID: r.ID, TeamID: r.TeamID, TeamName: r.TeamName, ProjectID: r.ProjectID.String, ProjectName: r.ProjectName, Role: Role(r.Role), Label: r.Label}
	accepted := inviteEntry(workspaceID, inv, "invite.accepted", by, at)
	accepted.Person = by
	role := Role(r.Role)
	if r.ProjectID.Valid {
		if err := acceptIntoProject(ctx, q, workspaceID, r.ProjectID.String, personID, role, accepted, at); err != nil {
			return err
		}
	} else {
		err := setTeamMember(ctx, q, by, workspaceID, r.TeamID, personID, role, &accepted)
		if err != nil {
			return err
		}
	}
	// The team's owners see who came in. An owner's arrival is told at once, as
	// any change to who holds the team is; any other is a card on their overview.
	details := map[string]any{
		"person": person.Name, "role": string(role), "by": r.MadeByName, "label": r.Label, "team": r.TeamName, "project": r.ProjectName,
	}
	owners, err := q.OwnersOfTeam(ctx, r.TeamID)
	if err != nil {
		return err
	}
	var tell []string
	for _, o := range owners {
		if o.ID != personID {
			tell = append(tell, o.ID)
		}
	}
	if err := noticePeople(ctx, q, workspaceID, r.TeamID, tell, noticeInviteAccepted, details, at, role == Owner); err != nil {
		return err
	}
	if r.ProjectID.Valid {
		// Owners of the project from outside the team see it too, with the team's
		// people unnamed to them, as the log and the key list have it.
		outside, err := outsideOwners(ctx, q, r.TeamID, r.ProjectID.String)
		if err != nil {
			return err
		}
		name := person.Name
		if inTeam, err := teamRoleNow(ctx, q, r.TeamID, personID); err != nil {
			return err
		} else if inTeam != "" {
			name = "Someone of the team"
		}
		for _, o := range outside {
			if o == personID {
				continue
			}
			told := map[string]any{"person": name, "role": string(role), "by": "", "project": r.ProjectName}
			if o == r.MadeBy {
				told["by"], told["label"] = r.MadeByName, r.Label
			}
			if err := noticePeople(ctx, q, workspaceID, r.TeamID, []string{o}, noticeInviteAccepted, told, at, role == Owner); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// outsideOwners are the owners of a project by a role of their own, who are not
// in its team.
func outsideOwners(ctx context.Context, q *db.Queries, teamID, projectID string) ([]string, error) {
	rows, err := q.OutsidersOfTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	var owners []string
	for _, r := range rows {
		if r.ProjectID == projectID && Role(r.Role) == Owner {
			owners = append(owners, r.ID)
		}
	}
	return owners, nil
}

// acceptIntoProject gives a person a role of their own in a project, which is
// higher than any they have there: whether they are told keeps what they chose,
// and otherwise follows the role.
func acceptIntoProject(ctx context.Context, q *db.Queries, workspaceID, projectID, personID string, role Role, line logEntry, at time.Time) error {
	told := role != Viewer
	if chosen, err := q.AlertChoice(ctx, db.AlertChoiceParams{ProjectID: projectID, PersonID: personID}); err == nil {
		told = chosen == 1
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err := q.AddMember(ctx, db.AddMemberParams{WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID, Role: string(role), CreatedAt: at.UnixMilli()})
	if err != nil {
		return err
	}
	if err := settleChoice(ctx, q, workspaceID, projectID, personID, told); err != nil {
		return err
	}
	if err := writeLog(ctx, q, line); err != nil {
		return err
	}
	if !told {
		return nil
	}
	return followOpen(ctx, q, workspaceID, projectID, personID, at.UnixMilli())
}

// NewPersonName checks the name someone gives themselves: 1 to 100 characters,
// and none of the words the log uses for someone it does not name.
func NewPersonName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", &InvalidError{"name", "your name must be 1 to 100 characters"}
	}
	switch strings.ToLower(name) {
	case "the team", "an administrator", "someone", "someone outside this project", "allstill", "a job":
		return "", &InvalidError{"name", "that name is what Allstill calls someone it does not name: choose another"}
	}
	return name, nil
}

// NewTimeZone is the time zone a browser says, if it is one Go knows, else UTC.
func NewTimeZone(tz string) string {
	if tz == "" || tz == "Local" {
		return "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "UTC"
	}
	return tz
}

// CreatePersonByInvite makes an account through an open invitation: the person,
// with the id the passkey ceremony was begun for, their passkey, and a session,
// all or nothing. It returns the session's token. They have no email address,
// are no administrator, and are not yet given what the invitation offers: that
// takes accepting it, once they have seen where it is for. An invitation makes
// one account at most.
func (s *Store) CreatePersonByInvite(ctx context.Context, secret, personID, name, timeZone string, k NewPasskey, userAgent string, at time.Time) (string, error) {
	name, err := NewPersonName(name)
	if err != nil {
		return "", err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	r, err := openInvite(ctx, q, secret, at)
	if err != nil {
		return "", err
	}
	if r.MadePerson.Valid {
		return "", ErrInviteMadeAccount
	}
	n, err := q.MakePersonOfInvite(ctx, db.MakePersonOfInviteParams{PersonID: sql.NullString{String: personID, Valid: true}, ID: r.ID, At: at.UnixMilli()})
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrInviteMadeAccount
	}
	self := Actor{Kind: "person", ID: personID, Name: name}
	err = q.CreatePerson(ctx, db.CreatePersonParams{
		ID: personID, WorkspaceID: r.WorkspaceID, Name: name, TimeZone: NewTimeZone(timeZone), CreatedAt: at.UnixMilli(),
		QuietHours: sql.NullString{String: DefaultQuietHours(NewTimeZone(timeZone)), Valid: true},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return "", ErrExists
		}
		return "", err
	}
	// An administrator reads that someone was added, and nothing of where to (R10).
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: r.WorkspaceID, At: at, Kind: "person.added", AboutKind: "person", AboutID: personID, AboutName: name,
		By: self, Person: self, Details: map[string]any{"administrator": false, "invited": true},
	}); err != nil {
		return "", err
	}
	passkeyID := id.New(id.Passkey)
	err = q.CreatePasskey(ctx, db.CreatePasskeyParams{
		ID: passkeyID, WorkspaceID: r.WorkspaceID, PersonID: personID, CredentialID: k.CredentialID,
		RpID: k.RPID, Name: k.Name, Record: string(k.Record), CreatedAt: at.UnixMilli(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: passkeys.credential_id") {
			return "", ErrExists
		}
		return "", err
	}
	token, err := createSession(ctx, q, r.WorkspaceID, personID, passkeyID, userAgent, at)
	if err != nil {
		return "", err
	}
	if err := logOwnSignIn(ctx, q, r.WorkspaceID, personID, "passkey.added", at); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// Member is someone in a team, as its owners see them.
type Member struct {
	ID, Name string
	Role     Role
	// CameIn says how: the label of the invitation they accepted and who made
	// it, or "" when a command on the server put them there.
	CameIn, InvitedBy string
}

// Outsider is someone outside a team with a role of their own in one of its projects.
type Outsider struct {
	ID, Name    string
	Role        Role
	ProjectID   string
	ProjectName string
}

// TeamPeople is what a team's People page shows its owners.
type TeamPeople struct {
	Team      TeamAccess
	Members   []Member
	Outsiders []Outsider
	Invites   []Invite
}

// PeopleOfTeam returns a team's members, the people with a role of their own in
// its projects, and its open invitations, for an owner of the team (R3, R20).
func (s *Store) PeopleOfTeam(ctx context.Context, a Access, teamID string, at time.Time) (TeamPeople, error) {
	var t TeamAccess
	for _, have := range a.Teams {
		if have.ID == teamID {
			t = have
		}
	}
	if t.ID == "" {
		return TeamPeople{}, ErrNotFound
	}
	if t.Role != Owner {
		return TeamPeople{}, ErrForbidden
	}
	q := db.New(s.r)
	members, err := q.MembersOfTeam(ctx, teamID)
	if err != nil {
		return TeamPeople{}, err
	}
	came, err := q.HowMembersCameIn(ctx, teamID)
	if err != nil {
		return TeamPeople{}, err
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.ID] = m.Name
	}
	how := map[string]db.HowMembersCameInRow{}
	for _, c := range came {
		if c.Kind == InviteToTeam {
			how[c.PersonID.String] = c
		}
	}
	tp := TeamPeople{Team: t}
	for _, m := range members {
		mem := Member{ID: m.ID, Name: m.Name, Role: Role(m.Role)}
		if c, ok := how[m.ID]; ok {
			mem.CameIn, mem.InvitedBy = c.Label, names[c.MadeBy]
			if mem.InvitedBy == "" {
				mem.InvitedBy = "someone no longer in the team"
			}
		}
		tp.Members = append(tp.Members, mem)
	}
	outsiders, err := q.OutsidersOfTeam(ctx, teamID)
	if err != nil {
		return TeamPeople{}, err
	}
	for _, o := range outsiders {
		tp.Outsiders = append(tp.Outsiders, Outsider{ID: o.ID, Name: o.Name, Role: Role(o.Role), ProjectID: o.ProjectID, ProjectName: o.ProjectName})
	}
	if tp.Invites, err = s.OpenInvites(ctx, a, teamID, at); err != nil {
		return TeamPeople{}, err
	}
	return tp, nil
}
