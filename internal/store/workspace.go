// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	return wsID, tx.Commit()
}

// NewPerson is a person to create.
type NewPerson struct {
	Name           string
	Email          string
	TimeZone       string
	WorkspaceAdmin bool
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
func (s *Store) CreatePerson(ctx context.Context, workspaceID string, p NewPerson) (string, error) {
	personID := id.New(id.Person)
	if p.TimeZone == "" {
		p.TimeZone = "UTC"
	}
	err := db.New(s.w).CreatePerson(ctx, db.CreatePersonParams{
		ID:             personID,
		WorkspaceID:    workspaceID,
		Name:           p.Name,
		Email:          sql.NullString{String: p.Email, Valid: p.Email != ""},
		WorkspaceAdmin: boolInt(p.WorkspaceAdmin),
		TimeZone:       p.TimeZone,
		CreatedAt:      now(),
		QuietHours:     sql.NullString{String: DefaultQuietHours(p.TimeZone), Valid: true},
	})
	return personID, err
}

// CreateProject adds a project and returns its id and its ping key. The key is
// shown once; only its hash is kept.
func (s *Store) CreateProject(ctx context.Context, workspaceID, name string) (projectID, pingKey string, err error) {
	projectID, pingKey = id.New(id.Project), id.Key()
	h := sha256.Sum256([]byte(pingKey))
	err = db.New(s.w).CreateProject(ctx, db.CreateProjectParams{
		ID: projectID, WorkspaceID: workspaceID, Name: name, PingKeyHash: h[:], CreatedAt: now(),
	})
	return projectID, pingKey, err
}

// SetMember gives a person a role in a project. A new member who is no viewer
// gets alerts for it.
func (s *Store) SetMember(ctx context.Context, workspaceID, projectID, personID string, role Role) error {
	return db.New(s.w).AddMember(ctx, db.AddMemberParams{
		WorkspaceID: workspaceID, ProjectID: projectID, PersonID: personID, Role: string(role), CreatedAt: now(),
		Alerts: boolInt(role != Viewer),
	})
}

// ProjectAccess is a project a caller may see, and their role in it.
type ProjectAccess struct {
	ID   string
	Name string
	Role Role
}

// Access is what a caller may see: the projects and their role in each. Every
// read of project data goes through it.
type Access struct {
	WorkspaceID string
	PersonID    string
	Admin       bool
	Projects    []ProjectAccess
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

// AccessOfPerson returns what a person may see. A disabled or unknown person
// gets ErrNotFound. A workspace administrator sees every project, as owner.
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
	a := Access{WorkspaceID: workspaceID, PersonID: personID, Admin: p.WorkspaceAdmin == 1}
	for _, r := range rows {
		a.Projects = append(a.Projects, ProjectAccess{ID: r.ID, Name: r.Name, Role: Role(r.Role)})
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
	ids, err := db.New(s.r).ProjectsByName(ctx, db.ProjectsByNameParams{WorkspaceID: workspaceID, Name: name})
	switch {
	case err != nil:
		return "", err
	case len(ids) == 0:
		return "", ErrNotFound
	case len(ids) > 1:
		return "", fmt.Errorf("%d projects are named %q", len(ids), name)
	}
	return ids[0], nil
}

// AdminAccess is the access of the administrator's command line: every
// project, as owner. It is for commands run on the server itself, never for a
// request.
func (s *Store) AdminAccess(ctx context.Context, workspaceID string) (Access, error) {
	rows, err := db.New(s.r).AllProjects(ctx, workspaceID)
	if err != nil {
		return Access{}, err
	}
	a := Access{WorkspaceID: workspaceID, Admin: true}
	for _, r := range rows {
		a.Projects = append(a.Projects, ProjectAccess{ID: r.ID, Name: r.Name, Role: Owner})
	}
	return a, nil
}

// RenameProject gives a project a new name, which no other project may have.
func (s *Store) RenameProject(ctx context.Context, workspaceID, projectID, name string) error {
	if name == "" {
		return &InvalidError{"name", "a project needs a name"}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	ids, err := q.ProjectsByName(ctx, db.ProjectsByNameParams{WorkspaceID: workspaceID, Name: name})
	if err != nil {
		return err
	}
	if len(ids) > 0 && ids[0] != projectID {
		return &InvalidError{"name", fmt.Sprintf("a project named %q exists already", name)}
	}
	n, err := q.RenameProject(ctx, db.RenameProjectParams{Name: name, WorkspaceID: workspaceID, ID: projectID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
