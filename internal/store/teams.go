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
	if err := q.CreateTeam(ctx, db.CreateTeamParams{ID: teamID, WorkspaceID: workspaceID, Name: name, CreatedAt: now()}); err != nil {
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
	q := db.New(tx)
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
	entry := logEntry{
		WorkspaceID: workspaceID, At: time.Now(), TeamID: teamID, AboutKind: "person", AboutID: personID, AboutName: person.Name,
		By: by, Person: person, Details: map[string]any{"from": was, "to": role, "team": team.Name},
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
		if err := writeLog(ctx, q, entry); err != nil {
			return err
		}
		// The owners who stay are told, and an owner who goes is too.
		var also []string
		if was == string(Owner) {
			also = []string{personID}
		}
		err = tellOwners(ctx, q, by, workspaceID, []string{teamID}, also, noticeTeamRoleTaken,
			map[string]any{"person": person.Name, "team": team.Name}, time.Now())
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	entry.Kind = "team.member_set"
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
	if err := writeLog(ctx, q, entry); err != nil {
		return err
	}
	// An owner who was made less is told with the owners who stay.
	var also []string
	if was == string(Owner) {
		also = []string{personID}
	}
	err = tellOwners(ctx, q, by, workspaceID, []string{teamID}, also, noticeTeamRoleGiven,
		map[string]any{"person": person.Name, "role": string(role), "team": team.Name}, time.Now())
	if err != nil {
		return err
	}
	if role != Viewer {
		// They should have known of what is open since it opened, as when someone
		// is given a role in one project.
		projects, err := q.ProjectsOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		due := sql.NullInt64{Int64: now(), Valid: true}
		for _, p := range projects {
			open, err := q.OpenIncidentsOfProject(ctx, db.OpenIncidentsOfProjectParams{WorkspaceID: workspaceID, ProjectID: p.ID})
			if err != nil {
				return err
			}
			for _, incidentID := range open {
				if err := q.AddFollowers(ctx, db.AddFollowersParams{IncidentID: incidentID, DueAt: due, ProjectID: p.ID, WorkspaceID: workspaceID}); err != nil {
					return err
				}
				if err := q.LookAgainAtUntoldPerson(ctx, db.LookAgainAtUntoldPersonParams{DueAt: due, IncidentID: incidentID, PersonID: personID}); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
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
