// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// Role is what a person may do in a project.
type Role string

const (
	Viewer Role = "viewer"
	Editor Role = "editor"
	Owner  Role = "owner"
)

// ErrNotFound means the row does not exist, or the caller may not see it.
var ErrNotFound = errors.New("not found")

// Workspace returns the installation's workspace id, creating the workspace on
// first use.
func (s *Store) Workspace(ctx context.Context) (string, error) {
	// The writer's transactions take the write lock when they begin, so two
	// first calls cannot both create one.
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	ws, err := q.Workspace(ctx)
	if err == nil {
		return ws.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	wsID := id.New(id.Workspace)
	if err := q.CreateWorkspace(ctx, db.CreateWorkspaceParams{ID: wsID, Name: "Allstill", CreatedAt: now()}); err != nil {
		return "", err
	}
	// An installation is never without a team: every project belongs to one.
	// It has no limits, and reaches whatever private ranges the server is given:
	// whoever runs a new installation is its first team.
	if err := q.CreateTeam(ctx, db.CreateTeamParams{ID: id.New(id.Team), WorkspaceID: wsID, Name: "Allstill", CreatedAt: now(), PrivateRanges: "*"}); err != nil {
		return "", err
	}
	return wsID, tx.Commit()
}

// NewPerson is a person to create.
type NewPerson struct {
	Name           string
	Email          string
	TimeZone       string
	WorkspaceAdmin bool
	// InNoTeam makes an administrator who only keeps the installation. Without
	// it an administrator owns the installation's team, which with several
	// teams is refused, as it would be a guess.
	InNoTeam bool
}

// DefaultQuietHours is when a new person may not be disturbed, except by a
// critical check: every night from 22:00 to 07:00 in their time zone.
func DefaultQuietHours(timeZone string) string {
	b, _ := json.Marshal(map[string]any{"time_zone": timeZone, "periods": []map[string]any{{
		"days":  []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
		"start": "22:00", "end": "07:00",
	}}})
	return string(b)
}

// CreatePerson adds a person to the workspace and returns their id. They get
// the default quiet hours.
func (s *Store) CreatePerson(ctx context.Context, by Actor, workspaceID string, p NewPerson) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	personID, err := createPerson(ctx, db.New(tx), by, workspaceID, p)
	if err != nil {
		return "", err
	}
	return personID, tx.Commit()
}

func createPerson(ctx context.Context, q *db.Queries, by Actor, workspaceID string, p NewPerson) (string, error) {
	personID := id.New(id.Person)
	if p.TimeZone == "" {
		p.TimeZone = "UTC"
	}
	err := q.CreatePerson(ctx, db.CreatePersonParams{
		ID:             personID,
		WorkspaceID:    workspaceID,
		Name:           p.Name,
		Email:          sql.NullString{String: p.Email, Valid: p.Email != ""},
		WorkspaceAdmin: boolInt(p.WorkspaceAdmin),
		TimeZone:       p.TimeZone,
		CreatedAt:      now(),
		QuietHours:     sql.NullString{String: DefaultQuietHours(p.TimeZone), Valid: true},
	})
	if err != nil {
		return "", err
	}
	if p.WorkspaceAdmin && !p.InNoTeam {
		// An administrator of a one-team installation owns that team, which is how
		// they see its projects.
		teamID, err := onlyTeam(ctx, q, workspaceID)
		if err != nil {
			return "", err
		}
		err = q.AddTeamMember(ctx, db.AddTeamMemberParams{
			WorkspaceID: workspaceID, TeamID: teamID, PersonID: personID, Role: string(Owner), CreatedAt: now(),
		})
		if err != nil {
			return "", err
		}
		team, err := q.Team(ctx, db.TeamParams{WorkspaceID: workspaceID, ID: teamID})
		if err != nil {
			return "", err
		}
		err = tellOwners(ctx, q, by, workspaceID, []string{teamID}, nil, noticeTeamRoleGiven,
			map[string]any{"person": p.Name, "role": string(Owner), "team": team.Name}, time.Now())
		if err != nil {
			return "", err
		}
	}
	return personID, writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: time.Now(), Kind: "person.added", AboutKind: "person", AboutID: personID, AboutName: p.Name,
		By: by, Person: Actor{ID: personID, Name: p.Name}, Details: map[string]any{"administrator": p.WorkspaceAdmin},
	})
}

// CreateProject adds a project to the installation's only team and returns its
// id and its ping key. With several teams it refuses: see CreateProjectIn.
func (s *Store) CreateProject(ctx context.Context, by Actor, workspaceID, name string) (projectID, pingKey string, err error) {
	return s.CreateProjectIn(ctx, by, workspaceID, "", name)
}

// CreateProjectIn adds a project to a team, or with "" to the only team, and
// returns its id and its ping key. The key is shown once; only its hash is kept.
func (s *Store) CreateProjectIn(ctx context.Context, by Actor, workspaceID, teamID, name string) (projectID, pingKey string, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if teamID == "" {
		if teamID, err = onlyTeam(ctx, q, workspaceID); err != nil {
			return "", "", err
		}
	} else if _, err := q.Team(ctx, db.TeamParams{WorkspaceID: workspaceID, ID: teamID}); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	if projectID, pingKey, err = createProject(ctx, q, by, workspaceID, teamID, name); err != nil {
		return "", "", err
	}
	return projectID, pingKey, tx.Commit()
}

// createProject writes a project with a name no other project of its team has.
func createProject(ctx context.Context, q *db.Queries, by Actor, workspaceID, teamID, name string) (projectID, pingKey string, err error) {
	if name, err = projectName(name); err != nil {
		return "", "", err
	}
	if err := nameFree(ctx, q, workspaceID, teamID, "", name); err != nil {
		return "", "", err
	}
	projectID, pingKey = id.New(id.Project), id.Key()
	h := sha256.Sum256([]byte(pingKey))
	err = q.CreateProject(ctx, db.CreateProjectParams{
		ID: projectID, WorkspaceID: workspaceID, TeamID: teamID, Name: name, PingKeyHash: h[:], CreatedAt: now(),
	})
	if err != nil {
		return "", "", err
	}
	err = writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: time.Now(), ProjectID: projectID, ProjectName: name, Kind: "project.created",
		AboutKind: "project", AboutID: projectID, AboutName: name, By: by,
	})
	return projectID, pingKey, err
}

// SetMember gives a person a role in a project. Someone who becomes more than a
// viewer gets alerts for it, and someone who becomes a viewer stops getting them.
func (s *Store) SetMember(ctx context.Context, by Actor, workspaceID, projectID, personID string, role Role) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setMember(ctx, db.New(tx), by, workspaceID, projectID, personID, role); err != nil {
		return err
	}
	return tx.Commit()
}

// setMember also has the sender look at what is open in the project for someone
// who gets alerts for it: they should have known since it opened, as when a check
// moves to a project.
//
// Whether they are told follows the role as it always did: on when they become
// more than a viewer, off when they become one, and otherwise as it was. That
// is kept as a choice only where it differs from what their highest role gives.
//
// A row of their own is kept only where it gives more than their role in the
// project's team does: any other row gives nothing, and would turn into a real
// role the day they leave the team. So for an owner of the team, viewer here
// only means "do not tell me", and is written as that choice.
func setMember(ctx context.Context, q *db.Queries, by Actor, workspaceID, projectID, personID string, role Role) error {
	if _, err := liveProject(ctx, q, workspaceID, projectID); err != nil {
		return err
	}
	at := now()
	was, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	inTeam, err := q.RoleInTeamOfProject(ctx, db.RoleInTeamOfProjectParams{ProjectID: projectID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	before, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: personID})
	if err != nil {
		return err
	}
	keep := inTeam == "" || !roleAtLeast(Role(inTeam), role)
	told := role != Viewer
	if keep && was != "" && (was == string(Viewer)) == (role == Viewer) {
		told = before > 0
	}
	switch {
	case keep:
		err = q.AddMember(ctx, db.AddMemberParams{
			WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID, Role: string(role), CreatedAt: at,
		})
	case was != "":
		// Written first: once the row is gone, they read as never in.
		if err := logMember(ctx, q, by, workspaceID, projectID, personID, "member.removed", map[string]any{"from": was}); err != nil {
			return err
		}
		_, err = q.RemoveMember(ctx, db.RemoveMemberParams{WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID})
	}
	if err != nil {
		return err
	}
	if err := settleChoice(ctx, q, workspaceID, projectID, personID, told); err != nil {
		return err
	}
	switch {
	case keep && was != string(role):
		err = logMember(ctx, q, by, workspaceID, projectID, personID, "member.set", map[string]any{"from": was, "to": role})
		if err == nil {
			// A command on the server that lets someone in tells the team it is
			// done to. A role that gives nothing is not told: nobody was let in.
			err = tellOfRole(ctx, q, by, workspaceID, projectID, personID, role, at)
		}
	case !keep && told != (before > 0):
		err = logMember(ctx, q, by, workspaceID, projectID, personID, "alerts.chosen", map[string]any{"told": told})
	}
	if err != nil {
		return err
	}
	teamID, err := q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: projectID})
	if err != nil {
		return err
	}
	if err := withdrawUnbacked(ctx, q, workspaceID, teamID, personID, time.UnixMilli(at)); err != nil {
		return err
	}
	if role == Viewer {
		return nil
	}
	return followOpen(ctx, q, workspaceID, projectID, personID, at)
}

// followOpen has the sender look at what is open in a project for someone who
// now gets alerts for it: they should have known since it opened.
func followOpen(ctx context.Context, q *db.Queries, workspaceID, projectID, personID string, at int64) error {
	open, err := q.OpenIncidentsOfProject(ctx, db.OpenIncidentsOfProjectParams{WorkspaceID: workspaceID, ProjectID: projectID})
	if err != nil {
		return err
	}
	due := sql.NullInt64{Int64: at, Valid: true}
	for _, incidentID := range open {
		if err := q.AddFollowers(ctx, db.AddFollowersParams{IncidentID: incidentID, DueAt: due, ProjectID: projectID, WorkspaceID: workspaceID}); err != nil {
			return err
		}
		if err := q.LookAgainAtUntoldPerson(ctx, db.LookAgainAtUntoldPersonParams{DueAt: due, IncidentID: incidentID, PersonID: personID}); err != nil {
			return err
		}
	}
	return nil
}

// tellOfRole tells the owners of a project's team that someone was given a role in it.
func tellOfRole(ctx context.Context, q *db.Queries, by Actor, workspaceID, projectID, personID string, role Role, at int64) error {
	if by.Kind != "command" {
		return nil
	}
	teamID, err := q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: projectID})
	if err != nil {
		return err
	}
	person, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	name, err := q.NameOfProject(ctx, projectID)
	if err != nil {
		return err
	}
	return tellOwners(ctx, q, by, workspaceID, []string{teamID}, nil, noticeRoleGiven,
		map[string]any{"person": person.Name, "role": string(role), "project": name}, time.UnixMilli(at))
}

// settleChoice makes a person told about a project or not, keeping that as a
// choice only where it differs from what their highest role in it gives.
func settleChoice(ctx context.Context, q *db.Queries, workspaceID, projectID, personID string, told bool) error {
	highest, err := q.HighestRoleInProject(ctx, db.HighestRoleInProjectParams{ProjectID: projectID, PersonID: personID})
	if err != nil {
		return err
	}
	if told == (Role(highest) != Viewer) {
		_, err = q.ClearAlertChoice(ctx, db.ClearAlertChoiceParams{ProjectID: projectID, PersonID: personID})
		return err
	}
	return q.SetAlertChoice(ctx, db.SetAlertChoiceParams{
		WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID, Alerts: boolInt(told),
	})
}

// RemoveMember takes a person out of a project, or returns ErrNotFound when they
// had no row of their own in it and no choice about its alerts. Someone in the
// project's team stays in the project through it, and is told again as their
// role there gives.
func (s *Store) RemoveMember(ctx context.Context, by Actor, workspaceID, projectID, personID string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	was, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if was != "" {
		// Written first: once they are out, they would read as never in.
		if err := logMember(ctx, q, by, workspaceID, projectID, personID, "member.removed", map[string]any{"from": was}); err != nil {
			return err
		}
		if _, err := q.RemoveMember(ctx, db.RemoveMemberParams{WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID}); err != nil {
			return err
		}
	}
	// What they chose goes with the row, as the flag on it did.
	chosen, err := q.ClearAlertChoice(ctx, db.ClearAlertChoiceParams{ProjectID: projectID, PersonID: personID})
	if err != nil {
		return err
	}
	if was == "" {
		if chosen == 0 {
			return ErrNotFound
		}
		told, err := q.IsAlerted(ctx, db.IsAlertedParams{ProjectID: projectID, PersonID: personID})
		if err != nil {
			return err
		}
		if err := logMember(ctx, q, by, workspaceID, projectID, personID, "alerts.chosen", map[string]any{"told": told > 0}); err != nil {
			return err
		}
	}
	// What they could invite to only as the project's owner, they no longer can.
	teamID, err := q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: projectID})
	if err != nil {
		return err
	}
	if err := withdrawUnbacked(ctx, q, workspaceID, teamID, personID, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// logMember writes a change to who is in a project.
func logMember(ctx context.Context, q *db.Queries, by Actor, workspaceID, projectID, personID, kind string, details map[string]any) error {
	person, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	return writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: time.Now(), ProjectID: projectID, Kind: kind,
		AboutKind: "person", AboutID: personID, AboutName: person.Name, By: by, Person: person, Details: details,
	})
}

// ParseRole reads a role a person can have in a project.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case Viewer, Editor, Owner:
		return r, nil
	}
	return "", fmt.Errorf("%q is not a role: give viewer, editor or owner", s)
}

// CreateMember adds a person who is not an administrator, with a role in these
// projects and no others, in one change, and returns their id.
func (s *Store) CreateMember(ctx context.Context, by Actor, workspaceID string, p NewPerson, projects []string, role Role) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	p.WorkspaceAdmin = false
	personID, err := createPerson(ctx, q, by, workspaceID, p)
	if err != nil {
		return "", err
	}
	for _, projectID := range projects {
		if err := setMember(ctx, q, by, workspaceID, projectID, personID, role); err != nil {
			return "", err
		}
	}
	return personID, tx.Commit()
}

// ProjectAccess is a project a caller may see, and their role in it by any way
// in. TeamID is the team that owns it, which the caller need not be in.
type ProjectAccess struct {
	ID     string
	Name   string
	Role   Role
	TeamID string
}

// TeamAccess is a team a caller is in, and their role in it.
type TeamAccess struct {
	ID   string
	Name string
	Role Role
}

// Access is what a caller may see: the projects and their role in each, and the
// teams they are in. Every read of project data goes through it. Admin says the
// caller keeps the installation: its people, mail and teams. It gives no project.
type Access struct {
	WorkspaceID string
	PersonID    string
	Admin       bool
	Projects    []ProjectAccess
	Teams       []TeamAccess
	// Actor is who the log says acted with this access.
	Actor Actor
}

// TeamRole returns the caller's role in a team, and false when they are not in it.
func (a Access) TeamRole(teamID string) (Role, bool) {
	for _, t := range a.Teams {
		if t.ID == teamID {
			return t.Role, true
		}
	}
	return "", false
}

// TeamOf returns the team that owns a project the caller may see, with their role
// in it and whether they are in it at all.
func (a Access) TeamOf(projectID string) (TeamAccess, bool) {
	for _, p := range a.Projects {
		if p.ID == projectID {
			for _, t := range a.Teams {
				if t.ID == p.TeamID {
					return t, true
				}
			}
			return TeamAccess{ID: p.TeamID}, false
		}
	}
	return TeamAccess{}, false
}

// OwnsATeam says the caller is an owner of some team, who makes its projects.
func (a Access) OwnsATeam() bool {
	return slices.ContainsFunc(a.Teams, func(t TeamAccess) bool { return t.Role == Owner })
}

// Label is a project's name as the caller reads it in a list: its name, and
// where a project of another team that they can see has the same name, the team
// before it for a team they are in (spec, R5). Someone outside a team is not told it.
func (a Access) Label(projectID string) string {
	var name string
	for _, p := range a.Projects {
		if p.ID == projectID {
			name = p.Name
		}
	}
	teams := map[string]bool{}
	for _, p := range a.Projects {
		if p.Name == name {
			teams[p.TeamID] = true
		}
	}
	if t, in := a.TeamOf(projectID); in && len(teams) > 1 {
		return t.Name + " / " + name
	}
	return name
}

// OwnsTeamOf says the caller is an owner of the team a project belongs to, who may
// do with it what only its team decides, such as a probe's own check (spec, R37).
func (a Access) OwnsTeamOf(projectID string) bool {
	t, in := a.TeamOf(projectID)
	return in && t.Role == Owner
}

// knowsTeamOf says the caller is in a project's team as more than a viewer, so
// that the team's people are named to them (spec, R40).
func (a Access) knowsTeamOf(projectID string) bool {
	t, in := a.TeamOf(projectID)
	return in && t.Role != Viewer
}

// Role returns the caller's role in a project, and false when they may not see
// it at all.
func (a Access) Role(projectID string) (Role, bool) {
	for _, p := range a.Projects {
		if p.ID == projectID {
			return p.Role, true
		}
	}
	return "", false
}

// SeesSecrets says the caller may see a check's secrets whole: its ping URL, the URL
// it fetches, and where that must redirect. Everyone else gets the scheme and host of
// an address, and no ping URL (intent/http-checks/spec.md, R12).
func (a Access) SeesSecrets(projectID string) bool {
	role, _ := a.Role(projectID)
	return role == Editor || role == Owner
}

// AccessOfPerson returns what a person may see: the projects of the teams they
// are in and those they have a role of their own in, with the highest role they
// have in each. A disabled or unknown person gets ErrNotFound. An administrator
// sees a project as anyone does: through a team or a role of their own.
func (s *Store) AccessOfPerson(ctx context.Context, workspaceID, personID string) (Access, error) {
	q := db.New(s.r)
	p, err := q.Person(ctx, db.PersonParams{WorkspaceID: workspaceID, ID: personID})
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, ErrNotFound
	}
	if err != nil {
		return Access{}, err
	}
	rows, err := q.ProjectsOfPerson(ctx, db.ProjectsOfPersonParams{WorkspaceID: workspaceID, PersonID: personID})
	if err != nil {
		return Access{}, err
	}
	a := Access{
		WorkspaceID: workspaceID, PersonID: personID, Admin: p.WorkspaceAdmin == 1,
		Actor: Actor{Kind: "person", ID: personID, Name: p.Name, Admin: p.WorkspaceAdmin == 1},
	}
	for _, r := range rows {
		a.Projects = append(a.Projects, ProjectAccess{ID: r.ID, Name: r.Name, Role: Role(r.Role), TeamID: r.TeamID})
	}
	teams, err := q.TeamsOfPerson(ctx, db.TeamsOfPersonParams{WorkspaceID: workspaceID, PersonID: personID})
	if err != nil {
		return Access{}, err
	}
	for _, t := range teams {
		a.Teams = append(a.Teams, TeamAccess{ID: t.ID, Name: t.Name, Role: Role(t.Role)})
	}
	return a, nil
}

func now() int64 { return time.Now().UnixMilli() }

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ProjectByName returns the id of the project with this name, ErrNotFound when
// there is none, and an error when several share it.
func (s *Store) ProjectByName(ctx context.Context, workspaceID, name string) (string, error) {
	return s.ProjectByNameIn(ctx, workspaceID, "", name)
}

// ProjectByNameIn is ProjectByName within a team, or with "" in every team: a
// name is told apart within a team, so two teams can hold it (spec, R5).
func (s *Store) ProjectByNameIn(ctx context.Context, workspaceID, teamID, name string) (string, error) {
	rows, err := db.New(s.r).ProjectsByName(ctx, db.ProjectsByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return "", err
	}
	var ids []string
	teams := map[string]bool{}
	for _, r := range rows {
		if teamID == "" || r.TeamID == teamID {
			ids = append(ids, r.ID)
			teams[r.TeamID] = true
		}
	}
	switch {
	case len(ids) == 0:
		return "", ErrNotFound
	case len(teams) > 1:
		return "", fmt.Errorf("%d teams have a project named %q, so say which with -team: %w", len(teams), name, ErrSeveralTeams)
	case len(ids) > 1:
		return "", fmt.Errorf("%d projects are named %q", len(ids), name)
	}
	return ids[0], nil
}

// AdminAccess is the access of the administrator's command line: every
// team and every project, as owner, as whoever has the shell has the database. It is for commands run on the server itself, never for a
// request. command names the command, for the log.
func (s *Store) AdminAccess(ctx context.Context, workspaceID, command string) (Access, error) {
	rows, err := db.New(s.r).AllProjects(ctx, workspaceID)
	if err != nil {
		return Access{}, err
	}
	a := Access{WorkspaceID: workspaceID, Admin: true, Actor: Command(command)}
	for _, r := range rows {
		a.Projects = append(a.Projects, ProjectAccess{ID: r.ID, Name: r.Name, Role: Owner, TeamID: r.TeamID})
	}
	teams, err := db.New(s.r).Teams(ctx, workspaceID)
	if err != nil {
		return Access{}, err
	}
	for _, t := range teams {
		a.Teams = append(a.Teams, TeamAccess{ID: t.ID, Name: t.Name, Role: Owner})
	}
	return a, nil
}

// RenameProject gives a project a new name, which no other project of its team
// may have. It is for commands on the server: see RenameProjectAs.
func (s *Store) RenameProject(ctx context.Context, by Actor, workspaceID, projectID, name string) error {
	return s.renameProject(ctx, by, workspaceID, projectID, name, nil)
}

// renameProject renames a project; may, when given, refuses a caller who may
// not, as the database holds it in the transaction.
func (s *Store) renameProject(ctx context.Context, by Actor, workspaceID, projectID, name string, may func(*db.Queries, string) error) error {
	name, err := projectName(name)
	if err != nil {
		return err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	p, err := liveProject(ctx, q, workspaceID, projectID)
	if err != nil {
		return err
	}
	if may != nil {
		if err := may(q, p.TeamID); err != nil {
			return err
		}
	}
	if err := nameFree(ctx, q, workspaceID, p.TeamID, projectID, name); err != nil {
		return err
	}
	if p.Name == name {
		return nil
	}
	n, err := q.RenameProject(ctx, db.RenameProjectParams{Name: name, WorkspaceID: workspaceID, ID: projectID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: workspaceID, At: time.Now(), ProjectID: projectID, ProjectName: name, Kind: "project.renamed",
		AboutKind: "project", AboutID: projectID, AboutName: name, By: by, Details: map[string]any{"from": p.Name, "to": name},
	}); err != nil {
		return err
	}
	return tx.Commit()
}
