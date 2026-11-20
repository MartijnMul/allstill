// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A team's owners make, rename and delete its projects; the owners of a project
// see who can see it and change the roles of their own in it; and everyone who
// can see a project chooses whether they are told about it (intent/teams/plan-3.md,
// part B).

// projectName checks a project's name: 1 to 100 characters once trimmed.
func projectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", &InvalidError{"name", "a project's name is 1 to 100 characters"}
	}
	return name, nil
}

// nameFree refuses a name another project of the team has, read inside the
// transaction that writes it. What another team holds is never told (spec, R5).
func nameFree(ctx context.Context, q *db.Queries, workspaceID, teamID, projectID, name string) error {
	named, err := q.ProjectsByName(ctx, db.ProjectsByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return err
	}
	for _, p := range named {
		if p.TeamID == teamID && p.ID != projectID {
			return &InvalidError{"name", fmt.Sprintf("a project named %q exists already", name)}
		}
	}
	return nil
}

// liveProject reads a project that was not deleted, inside the transaction that
// puts something into it: one deleted a moment ago takes nothing more.
func liveProject(ctx context.Context, q *db.Queries, workspaceID, projectID string) (db.LiveProjectRow, error) {
	p, err := q.LiveProject(ctx, db.LiveProjectParams{WorkspaceID: workspaceID, ID: projectID})
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ownsTeamNow says whether the caller owns a team as the database holds it now:
// the access was read when the request came. A command on the server owns them all.
func ownsTeamNow(ctx context.Context, q *db.Queries, a Access, teamID string) (bool, error) {
	if a.Actor.Kind == "command" {
		role, in := a.TeamRole(teamID)
		return in && role == Owner, nil
	}
	role, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: a.PersonID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return Role(role) == Owner, err
}

// ownsProjectNow is ownsTeamNow for a project, by any way in.
func ownsProjectNow(ctx context.Context, q *db.Queries, a Access, projectID string) (bool, error) {
	if a.Actor.Kind == "command" {
		role, ok := a.Role(projectID)
		return ok && role == Owner, nil
	}
	role, err := q.HighestRoleInProject(ctx, db.HighestRoleInProjectParams{ProjectID: projectID, PersonID: a.PersonID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return Role(role) == Owner, err
}

// teamRoleNow is a person's role in a team as the database holds it now, or ""
// when they are not in it.
func teamRoleNow(ctx context.Context, q *db.Queries, teamID, personID string) (Role, error) {
	role, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: teamID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return Role(role), err
}

// knowsTeam says a team role names the team's people to whoever has it: more
// than a viewer, as the log has it (spec, R40).
func knowsTeam(r Role) bool { return r == Editor || r == Owner }

// mayRunTeam refuses a team the caller is not an owner of: one they are not in
// is not found.
func mayRunTeam(a Access, teamID string) error {
	if a.Actor.Kind != "person" && a.Actor.Kind != "command" {
		return ErrForbidden
	}
	switch role, in := a.TeamRole(teamID); {
	case !in:
		return ErrNotFound
	case role != Owner:
		return ErrForbidden
	}
	return nil
}

// CreateProjectAs makes a project in a team the caller owns, and returns its id
// and its ping key, which is shown once.
func (s *Store) CreateProjectAs(ctx context.Context, a Access, teamID, name string) (projectID, pingKey string, err error) {
	if err := mayRunTeam(a, teamID); err != nil {
		return "", "", err
	}
	if name, err = projectName(name); err != nil {
		return "", "", err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if owns, err := ownsTeamNow(ctx, q, a, teamID); err != nil {
		return "", "", err
	} else if !owns {
		return "", "", ErrForbidden
	}
	projectID, pingKey, err = createProject(ctx, q, a.Actor, a.WorkspaceID, teamID, name)
	if err != nil {
		return "", "", err
	}
	return projectID, pingKey, tx.Commit()
}

// RenameProjectAs gives a project of a team the caller owns a new name.
func (s *Store) RenameProjectAs(ctx context.Context, a Access, projectID, name string) error {
	t, in := a.TeamOf(projectID)
	if _, sees := a.Role(projectID); !sees {
		return ErrNotFound
	}
	if !in || t.Role != Owner || (a.Actor.Kind != "person" && a.Actor.Kind != "command") {
		return ErrForbidden
	}
	return s.renameProject(ctx, a.Actor, a.WorkspaceID, projectID, name, func(q *db.Queries, teamID string) error {
		if owns, err := ownsTeamNow(ctx, q, a, teamID); err != nil {
			return err
		} else if !owns {
			return ErrForbidden
		}
		return nil
	})
}

// ProjectHolds is why a project cannot be deleted: it still has checks.
type ProjectHolds struct{ Checks int }

func (e *ProjectHolds) Error() string {
	return fmt.Sprintf("the project still holds %d checks: delete or move them first", e.Checks)
}

// DeleteProjectAs deletes a project of a team the caller owns, which holds no
// check. It cannot be undone. In one change its ping key stops working, its keys
// are revoked, and its invitations, roles of one's own, choices about being
// told and the probes' rows that serve it go, so that nothing keeps feeding a
// notice, a People page or a probe. The line is written in the team, so the
// team's owners can read it once the project is gone.
func (s *Store) DeleteProjectAs(ctx context.Context, a Access, projectID string, at time.Time) error {
	t, in := a.TeamOf(projectID)
	if _, sees := a.Role(projectID); !sees {
		return ErrNotFound
	}
	if !in || t.Role != Owner || (a.Actor.Kind != "person" && a.Actor.Kind != "command") {
		return ErrForbidden
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	p, err := liveProject(ctx, q, a.WorkspaceID, projectID)
	if err != nil {
		return err
	}
	if owns, err := ownsTeamNow(ctx, q, a, p.TeamID); err != nil {
		return err
	} else if !owns {
		return ErrForbidden
	}
	// Read in the transaction that deletes: a check added a moment ago either
	// counts here, or finds the project gone (insertCheck).
	n, err := q.CountChecksOfProject(ctx, projectID)
	if err != nil {
		return err
	}
	if n > 0 {
		return &ProjectHolds{Checks: int(n)}
	}
	ws, ms := a.WorkspaceID, at.UnixMilli()
	if done, err := q.MarkProjectDeleted(ctx, db.MarkProjectDeletedParams{At: sql.NullInt64{Int64: ms, Valid: true}, WorkspaceID: ws, ID: projectID}); err != nil {
		return err
	} else if done == 0 {
		return ErrNotFound
	}
	keys, err := q.DeleteAPIKeysOfProject(ctx, db.DeleteAPIKeysOfProjectParams{WorkspaceID: ws, ProjectID: projectID})
	if err != nil {
		return err
	}
	for _, del := range []func() error{
		func() error {
			return q.DeleteInvitesOfProject(ctx, db.DeleteInvitesOfProjectParams{WorkspaceID: ws, ProjectID: sql.NullString{String: projectID, Valid: true}})
		},
		func() error {
			return q.DeleteMembershipsOfProject(ctx, db.DeleteMembershipsOfProjectParams{WorkspaceID: ws, ProjectID: projectID})
		},
		func() error {
			return q.DeleteAlertChoicesOfProject(ctx, db.DeleteAlertChoicesOfProjectParams{WorkspaceID: ws, ProjectID: projectID})
		},
		func() error {
			return q.DeleteProbeProjectsOfProject(ctx, db.DeleteProbeProjectsOfProjectParams{WorkspaceID: ws, ProjectID: projectID})
		},
	} {
		if err := del(); err != nil {
			return err
		}
	}
	team, err := q.Team(ctx, db.TeamParams{WorkspaceID: ws, ID: p.TeamID})
	if err != nil {
		return err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: ws, At: at, TeamID: p.TeamID, Kind: "project.deleted",
		AboutKind: "project", AboutID: projectID, AboutName: p.Name, By: a.Actor,
		Details: map[string]any{"team": team.Name, "keys": keys},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectInfo is a project as one reader sees it on its page: their own role,
// through what, and whether they are told.
type ProjectInfo struct {
	ID, Name string
	TeamID   string
	// TeamName is said only to someone in the team.
	TeamName string
	Role     Role
	TeamRole Role // "" for someone outside the team
	OwnRole  Role // "" for someone without a role of their own
	Told     bool
	// Checks counts what is in it, for whether it can be deleted.
	Checks int
}

// Project returns a project as the caller sees it.
func (s *Store) Project(ctx context.Context, a Access, projectID string) (ProjectInfo, error) {
	role, ok := a.Role(projectID)
	if !ok {
		return ProjectInfo{}, ErrNotFound
	}
	q := db.New(s.r)
	p, err := liveProject(ctx, q, a.WorkspaceID, projectID)
	if err != nil {
		return ProjectInfo{}, err
	}
	info := ProjectInfo{ID: p.ID, Name: p.Name, TeamID: p.TeamID, Role: role}
	if t, in := a.TeamOf(projectID); in {
		info.TeamName, info.TeamRole = t.Name, t.Role
	}
	if a.PersonID != "" {
		own, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: a.PersonID})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ProjectInfo{}, err
		}
		info.OwnRole = Role(own)
		told, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: a.PersonID})
		if err != nil {
			return ProjectInfo{}, err
		}
		info.Told = told > 0
	}
	n, err := q.CountChecksOfProject(ctx, projectID)
	if err != nil {
		return ProjectInfo{}, err
	}
	info.Checks = int(n)
	return info, nil
}

// ProjectPerson is someone who can see a project, as its owners see them.
type ProjectPerson struct {
	ID, Name string
	// Role is the highest of TeamRole and OwnRole, which counts.
	Role, TeamRole, OwnRole Role
	Told                    bool
}

// ProjectPeople is who can see a project (R7), as one of its owners reads it.
type ProjectPeople struct {
	// People are named: everyone, for an owner in the team; for an owner from
	// outside it, those outside the team.
	People []ProjectPerson
	// Unnamed says the reader is outside the team, who reads its people as a
	// count, as the log and the key list have them (activity-log R22).
	Unnamed        bool
	Team, TeamTold int
	// Invites are the open invitations to the project, the maker of one from
	// the team read as the team by someone outside it.
	Invites []Invite
}

// PeopleOfProject returns who can see a project, for one of its owners. Disabled
// people are left out.
func (s *Store) PeopleOfProject(ctx context.Context, a Access, projectID string, at time.Time) (ProjectPeople, error) {
	role, ok := a.Role(projectID)
	if !ok {
		return ProjectPeople{}, ErrNotFound
	}
	if role != Owner || a.Actor.Kind == "key" {
		return ProjectPeople{}, ErrForbidden
	}
	q := db.New(s.r)
	p, err := liveProject(ctx, q, a.WorkspaceID, projectID)
	if err != nil {
		return ProjectPeople{}, err
	}
	// Named to someone in the team as more than a viewer, as the log names them.
	knows := a.knowsTeamOf(projectID)
	rows, err := q.PeopleOfProject(ctx, projectID)
	if err != nil {
		return ProjectPeople{}, err
	}
	pp := ProjectPeople{Unnamed: !knows}
	for _, r := range rows {
		if !knows && r.TeamRole != "" {
			pp.Team++
			if r.Told == 1 {
				pp.TeamTold++
			}
			continue
		}
		pp.People = append(pp.People, ProjectPerson{
			ID: r.ID, Name: r.Name, Role: Role(r.Role), TeamRole: Role(r.TeamRole), OwnRole: Role(r.OwnRole), Told: r.Told == 1,
		})
	}
	invites, err := q.InvitesOfTeam(ctx, p.TeamID)
	if err != nil {
		return ProjectPeople{}, err
	}
	for _, r := range invites {
		if r.ProjectID != projectID || r.AcceptedAt.Valid || r.WithdrawnAt.Valid || r.ExpiresAt <= at.UnixMilli() {
			continue
		}
		maker := LogParty{"person", r.MadeBy, r.MadeByName}
		if !knows {
			if _, err := q.RoleInTeam(ctx, db.RoleInTeamParams{TeamID: p.TeamID, PersonID: r.MadeBy}); err == nil {
				maker = LogParty{Kind: "team"}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return ProjectPeople{}, err
			}
		}
		pp.Invites = append(pp.Invites, Invite{
			ID: r.ID, Kind: r.Kind, TeamID: r.TeamID, ProjectID: r.ProjectID, ProjectName: r.ProjectName, Role: Role(r.Role), Label: r.Label,
			MadeBy: maker, Created: time.UnixMilli(r.CreatedAt).UTC(), Ends: time.UnixMilli(r.ExpiresAt).UTC(),
		})
	}
	return pp, nil
}

// SetOwnAlerts has the caller told about a project or not, whatever their role
// (R25, without shares). Nobody chooses for someone else.
func (s *Store) SetOwnAlerts(ctx context.Context, a Access, projectID string, on bool, at time.Time) error {
	if _, ok := a.Role(projectID); !ok {
		return ErrNotFound
	}
	if a.Actor.Kind != "person" {
		return ErrForbidden
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := liveProject(ctx, q, a.WorkspaceID, projectID); err != nil {
		return err
	}
	// They may have lost the project since the request came.
	if _, err := q.HighestRoleInProject(ctx, db.HighestRoleInProjectParams{ProjectID: projectID, PersonID: a.PersonID}); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	before, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: a.PersonID})
	if err != nil {
		return err
	}
	if (before > 0) == on {
		return nil
	}
	if err := settleChoice(ctx, q, a.WorkspaceID, projectID, a.PersonID, on); err != nil {
		return err
	}
	if err := logMember(ctx, q, a.Actor, a.WorkspaceID, projectID, a.PersonID, "alerts.chosen", map[string]any{"told": on}); err != nil {
		return err
	}
	if on {
		if err := followOpen(ctx, q, a.WorkspaceID, projectID, a.PersonID, at.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetProjectRoleAs changes or, with role "", ends a role of one's own in a
// project, for one of its owners. It never makes one: that is an invitation's.
// A role is kept only where it is higher than what the team gives the person;
// whether they are told keeps what they chose, and otherwise follows the role.
// An owner is named, made less or ended only in the dashboard, and every owner
// is told.
func (s *Store) SetProjectRoleAs(ctx context.Context, a Access, projectID, personID string, role Role) error {
	have, ok := a.Role(projectID)
	if !ok {
		return ErrNotFound
	}
	if a.Actor.Kind != "person" || have != Owner {
		return ErrForbidden
	}
	if role != "" {
		if _, err := ParseRole(string(role)); err != nil {
			return &InvalidError{"role", "give viewer, editor or owner"}
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	p, err := liveProject(ctx, q, a.WorkspaceID, projectID)
	if err != nil {
		return err
	}
	if owns, err := ownsProjectNow(ctx, q, a, projectID); err != nil {
		return err
	} else if !owns {
		return ErrForbidden
	}
	actorTeam, err := teamRoleNow(ctx, q, p.TeamID, a.PersonID)
	if err != nil {
		return err
	}
	was, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	team, err := q.RoleInTeamOfProject(ctx, db.RoleInTeamOfProjectParams{ProjectID: projectID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Someone of the team is the team to an owner who is not named its people,
	// by id too.
	if team != "" && !knowsTeam(actorTeam) {
		return ErrNotFound
	}
	if Role(was) == role {
		return nil
	}
	if role != "" && team != "" && roleAtLeast(Role(team), role) {
		return &InvalidError{"role", fmt.Sprintf("is not more than they have through the team, where they are %s", article(team))}
	}
	if (Role(was) == Owner || role == Owner) && !inDashboard(a) {
		return errOwnerInDashboard
	}
	if Role(was) == Owner {
		// A project always has the team's owners; in a team that has none, its
		// owners of one project cannot end each other.
		others, err := q.OtherOwnersOfProject(ctx, db.OtherOwnersOfProjectParams{ProjectID: projectID, PersonID: personID})
		if err != nil {
			return err
		}
		if others == 0 {
			return ErrLastOwner
		}
	}
	before, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: personID})
	if err != nil {
		return err
	}
	_, choiceErr := q.AlertChoice(ctx, db.AlertChoiceParams{ProjectID: projectID, PersonID: personID})
	if choiceErr != nil && !errors.Is(choiceErr, sql.ErrNoRows) {
		return choiceErr
	}
	chose := choiceErr == nil
	ws := a.WorkspaceID
	if role == "" {
		// Written first: once the row is gone, they read as never in.
		if err := logMember(ctx, q, a.Actor, ws, projectID, personID, "member.removed", map[string]any{"from": was}); err != nil {
			return err
		}
		if _, err := q.RemoveMember(ctx, db.RemoveMemberParams{WorkspaceID: ws, ProjectID: projectID, PersonID: personID}); err != nil {
			return err
		}
	} else {
		if err := q.AddMember(ctx, db.AddMemberParams{WorkspaceID: ws, ProjectID: projectID, PersonID: personID, Role: string(role), CreatedAt: now()}); err != nil {
			return err
		}
		if err := logMember(ctx, q, a.Actor, ws, projectID, personID, "member.set", map[string]any{"from": was, "to": role}); err != nil {
			return err
		}
	}
	switch {
	case team == "" && role == "":
		// Out of the project altogether: what they chose goes with them.
		if _, err := q.ClearAlertChoice(ctx, db.ClearAlertChoiceParams{ProjectID: projectID, PersonID: personID}); err != nil {
			return err
		}
	case chose:
		if err := settleChoice(ctx, q, ws, projectID, personID, before > 0); err != nil {
			return err
		}
	}
	at := time.Now()
	if err := withdrawUnbacked(ctx, q, ws, p.TeamID, personID, at); err != nil {
		return err
	}
	if told, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: personID}); err != nil {
		return err
	} else if told > 0 && before == 0 {
		if err := followOpen(ctx, q, ws, projectID, personID, at.UnixMilli()); err != nil {
			return err
		}
	}
	if Role(was) == Owner || role == Owner {
		if err := tellOfProjectOwner(ctx, q, a, p, personID, Role(team), actorTeam, role, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// tellOfProjectOwner tells every owner of a project, by any way in, and the
// person concerned, that an owner of it was named, made less or ended, as any
// change to who holds a team is told. Whoever is not named the team's people
// reads them, the one who acted and the one concerned, as the team.
func tellOfProjectOwner(ctx context.Context, q *db.Queries, a Access, p db.LiveProjectRow, personID string, personTeam, actorTeam Role, role Role, at time.Time) error {
	person, err := personActor(ctx, q, a.WorkspaceID, personID)
	if err != nil {
		return err
	}
	kind := noticeRoleGiven
	if role == "" {
		kind = noticeRoleTaken
	}
	rows, err := q.PeopleOfProject(ctx, p.ID)
	if err != nil {
		return err
	}
	// Who is told, and the role each has in the team.
	told := map[string]Role{personID: personTeam}
	var order []string
	order = append(order, personID)
	for _, r := range rows {
		if Role(r.Role) == Owner && r.ID != personID {
			told[r.ID] = Role(r.TeamRole)
			order = append(order, r.ID)
		}
	}
	for _, id := range order {
		details := map[string]any{"by": a.Actor.Name, "person": person.Name, "role": string(role), "project": p.Name}
		if !knowsTeam(told[id]) {
			if actorTeam != "" && a.PersonID != id {
				details["by"] = "The team"
			}
			if personTeam != "" && personID != id {
				details["person"] = "someone of the team"
			}
		}
		if err := noticePeople(ctx, q, a.WorkspaceID, p.TeamID, []string{id}, kind, details, at, true); err != nil {
			return err
		}
	}
	return nil
}
