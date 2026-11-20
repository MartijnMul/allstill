// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// ErrSeveralTeams means an installation has more than one team, so a command
// that did not say which cannot know.
var ErrSeveralTeams = errors.New("several teams")

// onlyTeam returns the installation's team. A team owns projects and probes,
// and its members have one role in all of them (intent/teams/spec.md). Until a
// second one can be made, everything new belongs to this one.
func onlyTeam(ctx context.Context, q *db.Queries, workspaceID string) (string, error) {
	teams, err := q.Teams(ctx, workspaceID)
	switch {
	case err != nil:
		return "", err
	case len(teams) == 0:
		return "", fmt.Errorf("the installation has no team: %w", ErrNotFound)
	case len(teams) > 1:
		return "", fmt.Errorf("the installation has %d teams, so say which with -team: %w", len(teams), ErrSeveralTeams)
	}
	return teams[0].ID, nil
}

// Team is a team, as whoever keeps the installation may know it.
type Team struct {
	ID, Name string
}

// Teams lists the installation's teams, oldest first.
func (s *Store) Teams(ctx context.Context, workspaceID string) ([]Team, error) {
	rows, err := db.New(s.r).Teams(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	teams := make([]Team, len(rows))
	for i, r := range rows {
		teams[i] = Team{ID: r.ID, Name: r.Name}
	}
	return teams, nil
}

// CreateTeam makes a team with no members and no projects, and returns its id.
// It starts with the limits of a new team and may reach no private range.
// Only whoever keeps the installation makes one (intent/teams/spec.md, R9), so
// it takes no access: it is for commands on the server.
func (s *Store) CreateTeam(ctx context.Context, by Actor, workspaceID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", &InvalidError{"name", "must be 1 to 100 characters"}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	teamID := id.New(id.Team)
	// A team beside the first starts with limits and with no private range, until
	// an administrator says otherwise (spec, R13, R34).
	err = q.CreateTeam(ctx, db.CreateTeamParams{
		ID: teamID, WorkspaceID: workspaceID, Name: name, CreatedAt: now(),
		MaxActiveChecks: sql.NullInt64{Int64: NewTeamActiveChecks, Valid: true}, MaxPingChecks: sql.NullInt64{Int64: NewTeamPingChecks, Valid: true},
	})
	if err != nil {
		return "", err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: time.Now(), TeamID: teamID, Kind: "team.created",
		AboutKind: "team", AboutID: teamID, AboutName: name, By: by,
	}); err != nil {
		return "", err
	}
	return teamID, tx.Commit()
}

// ErrLastOwner means a change would leave a team that has an owner without one.
var ErrLastOwner = errors.New("the team's last owner")

// SetTeamMember gives a person a role in a team, which is their role in every
// project it owns, or with "" takes them out of it. The last owner cannot be
// taken out or made less. A row of their own in one of the team's projects that
// gives no more than the new role goes, as setMember would not have kept it.
// It is for commands on the server.
func (s *Store) SetTeamMember(ctx context.Context, by Actor, workspaceID, teamID, personID string, role Role) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setTeamMember(ctx, db.New(tx), by, workspaceID, teamID, personID, role, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// setTeamMember is SetTeamMember within a transaction. accepted is the line of an
// invitation that gives the role, written instead of team.member_set; who is told
// of it is the caller's.
func setTeamMember(ctx context.Context, q *db.Queries, by Actor, workspaceID, teamID, personID string, role Role, accepted *logEntry) error {
	team, err := q.Team(ctx, db.TeamParams{WorkspaceID: workspaceID, ID: teamID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	person, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	was, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if was == string(role) {
		if role == "" {
			return ErrNotFound
		}
		return nil
	}
	if was == string(Owner) {
		owners, err := q.OwnersOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		if len(owners) == 1 && owners[0].ID == personID {
			return ErrLastOwner
		}
	}
	at := time.Now()
	entry := logEntry{
		WorkspaceID: workspaceID, At: at, TeamID: teamID, AboutKind: "person", AboutID: personID, AboutName: person.Name,
		By: by, Person: person, Details: map[string]any{"from": was, "to": role, "team": team.Name},
	}
	// A change to who holds the team is told to every owner, whoever makes it,
	// and to an owner who is made less or taken out (plan-3.md). A command on the
	// server tells every change that lets someone in or takes them out.
	tell := func(kind string, details map[string]any) error {
		var also []string
		if was == string(Owner) {
			also = []string{personID}
		}
		if by.Kind == "command" || accepted != nil {
			return tellOwners(ctx, q, by, workspaceID, []string{teamID}, also, kind, details, at)
		}
		if was != string(Owner) && role != Owner {
			return nil
		}
		details["by"] = by.Name
		return noticeOwners(ctx, q, workspaceID, []string{teamID}, also, kind, details, at, true)
	}
	if role == "" {
		entry.Kind = "team.member_removed"
		if _, err := q.RemoveTeamMember(ctx, db.RemoveTeamMemberParams{WorkspaceID: workspaceID, TeamID: teamID, PersonID: personID}); err != nil {
			return err
		}
		// What they chose about projects they no longer see goes with them. A
		// role of their own in one of the team's projects stays (spec, R21).
		if err := q.ClearAlertChoicesOutside(ctx, db.ClearAlertChoicesOutsideParams{TeamID: teamID, PersonID: personID}); err != nil {
			return err
		}
		if err := withdrawUnbacked(ctx, q, workspaceID, teamID, personID, at); err != nil {
			return err
		}
		if err := writeLog(ctx, q, entry); err != nil {
			return err
		}
		return tell(noticeTeamRoleTaken, map[string]any{"person": person.Name, "team": team.Name})
	}
	entry.Kind = "team.member_set"
	if accepted != nil {
		entry = *accepted
		entry.Details["team"] = team.Name
	}
	// A row of their own that gives no more than the new role goes. Whether they
	// were told about that project is read first, while the row still says.
	rows, err := q.OwnRolesInTeam(ctx, db.OwnRolesInTeamParams{TeamID: teamID, PersonID: personID})
	if err != nil {
		return err
	}
	told := map[string]bool{}
	for _, r := range rows {
		n, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: r.ProjectID, PersonID: personID})
		if err != nil {
			return err
		}
		// As when a role in one project changes: told when they become more than a
		// viewer, not when they become one, and otherwise as it was.
		told[r.ProjectID] = role != Viewer
		if (Role(r.Role) == Viewer) == (role == Viewer) {
			told[r.ProjectID] = n > 0
		}
	}
	err = q.AddTeamMember(ctx, db.AddTeamMemberParams{
		WorkspaceID: workspaceID, TeamID: teamID, PersonID: personID, Role: string(role), CreatedAt: now(),
	})
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !roleAtLeast(role, Role(r.Role)) {
			continue
		}
		if err := logMember(ctx, q, by, workspaceID, r.ProjectID, personID, "member.removed", map[string]any{"from": r.Role}); err != nil {
			return err
		}
		if _, err := q.RemoveMember(ctx, db.RemoveMemberParams{WorkspaceID: workspaceID, ProjectID: r.ProjectID, PersonID: personID}); err != nil {
			return err
		}
		if err := settleChoice(ctx, q, workspaceID, r.ProjectID, personID, told[r.ProjectID]); err != nil {
			return err
		}
	}
	if err := withdrawUnbacked(ctx, q, workspaceID, teamID, personID, at); err != nil {
		return err
	}
	if err := writeLog(ctx, q, entry); err != nil {
		return err
	}
	if accepted == nil {
		if err := tell(noticeTeamRoleGiven, map[string]any{"person": person.Name, "role": string(role), "team": team.Name}); err != nil {
			return err
		}
	}
	if role != Viewer {
		// They should have known of what is open since it opened, as when someone
		// is given a role in one project.
		projects, err := q.ProjectsOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		for _, p := range projects {
			if err := followOpen(ctx, q, workspaceID, p.ID, personID, now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetTeamMemberAs changes the role of someone in a team, for an owner of the
// team. It never makes anyone a member: someone outside comes in by an
// invitation. A change that names, makes less or concerns an owner is made only
// in the dashboard (plan-3.md), and tells every owner.
func (s *Store) SetTeamMemberAs(ctx context.Context, a Access, teamID, personID string, role Role) error {
	if role != "" {
		if _, err := ParseRole(string(role)); err != nil {
			return &InvalidError{"role", "give viewer, editor or owner"}
		}
	}
	if a.Actor.Kind != "person" {
		return ErrForbidden
	}
	have, in := a.TeamRole(teamID)
	if !in {
		return ErrNotFound
	}
	if have != Owner {
		return ErrForbidden
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	// Read again here: whoever asks may have been made less since the request came.
	mine, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: a.PersonID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if Role(mine) != Owner {
		return ErrForbidden
	}
	was, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if (Role(was) == Owner || role == Owner) && !inDashboard(a) {
		return errOwnerInDashboard
	}
	if err := setTeamMember(ctx, q, a.Actor, a.WorkspaceID, teamID, personID, role, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// Stays is what someone taken out of a team keeps there: roles of their own in
// its projects, and keys they made in them, which keep working, as a key is the
// project's (spec, R21, R38).
type Stays struct {
	Roles []Outsider
	Keys  []KeyInTeam
}

// KeyInTeam is a key someone made in one of a team's projects.
type KeyInTeam struct {
	ID, Name, ProjectID, ProjectName string
}

// WhatStays says what a member of a team keeps there once taken out, for an
// owner of the team.
func (s *Store) WhatStays(ctx context.Context, a Access, teamID, personID string) (Stays, error) {
	if have, in := a.TeamRole(teamID); !in {
		return Stays{}, ErrNotFound
	} else if have != Owner {
		return Stays{}, ErrForbidden
	}
	q := db.New(s.r)
	if _, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: personID}); errors.Is(err, sql.ErrNoRows) {
		return Stays{}, ErrNotFound
	} else if err != nil {
		return Stays{}, err
	}
	return whatStays(ctx, q, teamID, personID)
}

func whatStays(ctx context.Context, q *db.Queries, teamID, personID string) (Stays, error) {
	var st Stays
	rows, err := q.OwnRolesInTeam(ctx, db.OwnRolesInTeamParams{TeamID: teamID, PersonID: personID})
	if err != nil {
		return Stays{}, err
	}
	for _, r := range rows {
		name, err := q.NameOfProject(ctx, r.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return Stays{}, err
		}
		st.Roles = append(st.Roles, Outsider{ID: personID, Role: Role(r.Role), ProjectID: r.ProjectID, ProjectName: name})
	}
	keys, err := q.KeysMadeInTeam(ctx, db.KeysMadeInTeamParams{TeamID: teamID, PersonID: personID})
	if err != nil {
		return Stays{}, err
	}
	for _, k := range keys {
		st.Keys = append(st.Keys, KeyInTeam{ID: k.ID, Name: k.Name, ProjectID: k.ProjectID, ProjectName: k.ProjectName})
	}
	return st, nil
}

// RemoveTeamMemberAs takes someone out of a team, for an owner of the team, and
// returns what they keep there. An owner is taken out only in the dashboard.
func (s *Store) RemoveTeamMemberAs(ctx context.Context, a Access, teamID, personID string) (Stays, error) {
	if err := s.SetTeamMemberAs(ctx, a, teamID, personID, ""); err != nil {
		return Stays{}, err
	}
	return whatStays(ctx, db.New(s.r), teamID, personID)
}

// TeamRoleInProject returns a person's role in the team that owns a project, and
// false when they are not in it.
func (s *Store) TeamRoleInProject(ctx context.Context, projectID, personID string) (Role, bool, error) {
	role, err := db.New(s.r).RoleInTeamOfProject(ctx, db.RoleInTeamOfProjectParams{ProjectID: projectID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return Role(role), err == nil, err
}

// New teams start with these limits, a first guess an administrator can change
// (intent/teams/spec.md, C8). The installation's first team has none.
const (
	NewTeamActiveChecks = 400
	NewTeamPingChecks   = 2000
)

// TeamSummary is a team as whoever keeps the installation may know it: no name
// of a project or a check (spec, R10).
type TeamSummary struct {
	ID, Name string
	Owners   []string
	// How many checks Allstill runs and jobs ping it may have; -1 for no limit.
	MaxActive, MaxPing int
	// Private says its checks may reach the private ranges the server opened.
	Private                          bool
	People, Projects, Checks, Probes int
}

// TeamSummaries lists the installation's teams, oldest first.
func (s *Store) TeamSummaries(ctx context.Context, workspaceID string) ([]TeamSummary, error) {
	q := db.New(s.r)
	rows, err := q.TeamSummaries(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	limit := func(v sql.NullInt64) int {
		if !v.Valid {
			return -1
		}
		return int(v.Int64)
	}
	teams := make([]TeamSummary, len(rows))
	for i, r := range rows {
		teams[i] = TeamSummary{
			ID: r.ID, Name: r.Name, MaxActive: limit(r.MaxActiveChecks), MaxPing: limit(r.MaxPingChecks), Private: r.PrivateRanges != "",
			People: int(r.People), Projects: int(r.Projects), Checks: int(r.Checks), Probes: int(r.Probes),
		}
		owners, err := q.OwnersOfTeam(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, o := range owners {
			teams[i].Owners = append(teams[i].Owners, o.Name)
		}
	}
	return teams, nil
}

// TeamNamed finds a team by its id or its name: ErrNotFound when there is none,
// and an error when several share the name, which takes the id.
func (s *Store) TeamNamed(ctx context.Context, workspaceID, nameOrID string) (Team, error) {
	teams, err := s.Teams(ctx, workspaceID)
	if err != nil {
		return Team{}, err
	}
	var found []Team
	for _, t := range teams {
		if t.ID == nameOrID {
			return t, nil
		}
		if t.Name == nameOrID {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return Team{}, ErrNotFound
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, t := range found {
		ids[i] = t.ID
	}
	return Team{}, fmt.Errorf("%d teams are named %q: say which by its id, one of %s", len(found), nameOrID, strings.Join(ids, ", "))
}

// TeamChange is what to change of a team; what is nil stays. A limit of -1 is none.
type TeamChange struct {
	Name               *string
	MaxActive, MaxPing *int
	Private            *bool
}

// SetTeam changes a team's name, its limits, or whether its checks may reach the
// private ranges the server opened. It is for commands on the server: only
// whoever keeps the installation decides these (spec, R13, R34).
func (s *Store) SetTeam(ctx context.Context, by Actor, workspaceID, teamID string, ch TeamChange) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	rows, err := q.TeamSummaries(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, t := range rows {
		if t.ID != teamID {
			continue
		}
		details := map[string]any{}
		limit := func(key string, to *int, v *sql.NullInt64) error {
			if to == nil {
				return nil
			}
			if *to < -1 {
				return &InvalidError{key, "is a number of checks, or none"}
			}
			*v = sql.NullInt64{Int64: int64(*to), Valid: *to >= 0}
			details[key] = *to
			return nil
		}
		if ch.Name != nil {
			name := strings.TrimSpace(*ch.Name)
			if name == "" || len(name) > 100 {
				return &InvalidError{"name", "must be 1 to 100 characters"}
			}
			details["from"], details["to"] = t.Name, name
			t.Name = name
		}
		if err := limit("active_checks", ch.MaxActive, &t.MaxActiveChecks); err != nil {
			return err
		}
		if err := limit("ping_checks", ch.MaxPing, &t.MaxPingChecks); err != nil {
			return err
		}
		if ch.Private != nil {
			t.PrivateRanges = ""
			if *ch.Private {
				t.PrivateRanges = "*"
			}
			details["private"] = *ch.Private
		}
		err := q.SetTeamLimits(ctx, db.SetTeamLimitsParams{
			Name: t.Name, MaxActiveChecks: t.MaxActiveChecks, MaxPingChecks: t.MaxPingChecks, PrivateRanges: t.PrivateRanges,
			WorkspaceID: workspaceID, ID: teamID,
		})
		if err != nil {
			return err
		}
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: workspaceID, At: time.Now(), TeamID: teamID, Kind: "team.changed",
			AboutKind: "team", AboutID: teamID, AboutName: t.Name, By: by, Details: details,
		}); err != nil {
			return err
		}
		return tx.Commit()
	}
	return ErrNotFound
}

// TeamHolds is why a team cannot be removed.
type TeamHolds struct{ Projects, Probes int }

func (e *TeamHolds) Error() string {
	return fmt.Sprintf("the team still holds projects or probes (%d and %d)", e.Projects, e.Probes)
}

// RemoveTeam removes a team that holds no project and no probe, with its members'
// places in it, or says what it still holds (spec, R33). Its people keep their
// accounts. It is for commands on the server.
func (s *Store) RemoveTeam(ctx context.Context, by Actor, workspaceID, teamID string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	rows, err := q.TeamSummaries(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, t := range rows {
		if t.ID != teamID {
			continue
		}
		if len(rows) == 1 {
			return &InvalidError{"team", "is the installation's only team, which every project needs"}
		}
		// A project that was deleted still points to its team.
		projects, err := q.CountProjectsOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		if projects > 0 || t.Probes > 0 {
			return &TeamHolds{Projects: int(projects), Probes: int(t.Probes)}
		}
		if err := q.DeleteTeam(ctx, db.DeleteTeamParams{WorkspaceID: workspaceID, ID: teamID}); err != nil {
			return err
		}
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: workspaceID, At: time.Now(), Kind: "team.removed", AboutKind: "team", AboutID: teamID, AboutName: t.Name, By: by,
		}); err != nil {
			return err
		}
		return tx.Commit()
	}
	return ErrNotFound
}

// ReachesPrivate says the checks of a project may reach the private ranges the
// server opened: those of a team an administrator opened them to (spec, R13).
func (s *Store) ReachesPrivate(ctx context.Context, projectID string) (bool, error) {
	t, err := db.New(s.r).TeamLimitsOfProject(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return t.PrivateRanges != "", err
}

// PersonTeams is someone with an account and the teams they are in.
type PersonTeams struct {
	ID, Name, Email string
	Admin, Disabled bool
	Teams           []string // "owner of Harbour"
}

// PeopleWithTeams lists everyone with an account, for whoever keeps the
// installation (spec, R11).
func (s *Store) PeopleWithTeams(ctx context.Context, workspaceID string) ([]PersonTeams, error) {
	rows, err := db.New(s.r).PeopleWithTeams(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	var people []PersonTeams
	for _, r := range rows {
		if n := len(people); n == 0 || people[n-1].ID != r.ID {
			people = append(people, PersonTeams{ID: r.ID, Name: r.Name, Email: r.Email, Admin: r.WorkspaceAdmin == 1, Disabled: r.DisabledAt.Valid})
		}
		if r.Team != "" {
			p := &people[len(people)-1]
			p.Teams = append(p.Teams, r.Role+" of "+r.Team)
		}
	}
	return people, nil
}

// TeamOfProject returns the id of the team a project belongs to.
func (s *Store) TeamOfProject(ctx context.Context, workspaceID, projectID string) (string, error) {
	id, err := db.New(s.r).TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
