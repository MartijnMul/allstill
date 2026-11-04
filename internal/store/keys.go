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

// A project's keys, for scripts (intent/cli-anywhere/spec.md, R11 to R13). A key acts
// as a viewer of its project with the read scope, and as an editor with manage, and
// reaches nothing else, so a leaked one stays a leak of one project. It can never make
// a key: that takes an owner, which no key is.

// apiKeyPrefix starts every key, so the server knows where to look and a leaked one
// can be recognised.
const apiKeyPrefix = "alk_"

// IsAPIKey says a bearer value claims to be a project's key.
func IsAPIKey(token string) bool { return strings.HasPrefix(token, apiKeyPrefix) }

// APIKey is a key, without its value.
type APIKey struct {
	ID        string
	ProjectID string
	Name      string
	Scope     string
	// MadeBy is who made it, as this reader may read it, as in the log.
	MadeBy LogParty
	// Via is the sign-in it was made through, for those who may read that.
	Via      string
	Created  time.Time
	LastUsed time.Time
}

// mayManageKeys says what a caller may do with a project's keys: not found when they
// may not see it, forbidden unless they own it, which an administrator counts as.
func mayManageKeys(a Access, projectID string) error {
	role, ok := a.Role(projectID)
	switch {
	case !ok:
		return ErrNotFound
	case role != Owner:
		return ErrForbidden
	}
	return nil
}

// CreateAPIKey makes a key for a project, and returns it with its value, which is not
// kept. Scope is read or manage.
func (s *Store) CreateAPIKey(ctx context.Context, a Access, projectID, name, scope string, at time.Time) (APIKey, string, error) {
	if err := mayManageKeys(a, projectID); err != nil {
		return APIKey{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return APIKey{}, "", &InvalidError{"name", "must be 1 to 100 characters"}
	}
	if scope != ScopeRead && scope != ScopeManage {
		return APIKey{}, "", &InvalidError{"scope", "is read or manage"}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	apart, err := outsider(ctx, q, a.Actor, projectID)
	if err != nil {
		return APIKey{}, "", err
	}
	key, keyID := apiKeyPrefix+id.Key(), id.New(id.APIKey)
	err = q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: keyID, KeyHash: hash(key), WorkspaceID: a.WorkspaceID, ProjectID: projectID, Name: name, Scope: scope,
		MadeByKind: a.Actor.Kind, MadeByID: a.Actor.ID, MadeByName: a.Actor.Name, MadeByOutsider: boolInt(apart), MadeVia: a.Actor.Via,
		CreatedAt: at.UnixMilli(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: api_keys.project_id, api_keys.name") {
			return APIKey{}, "", &InvalidError{"name", fmt.Sprintf("this project already has a key named %q", name)}
		}
		return APIKey{}, "", err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: a.WorkspaceID, At: at, ProjectID: projectID, Kind: "key.made",
		AboutKind: "key", AboutID: keyID, AboutName: name, By: a.Actor, Details: map[string]any{"scope": scope},
	}); err != nil {
		return APIKey{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return APIKey{}, "", err
	}
	return APIKey{ID: keyID, ProjectID: projectID, Name: name, Scope: scope, MadeBy: LogParty{a.Actor.Kind, a.Actor.ID, a.Actor.Name}, Via: a.Actor.Via, Created: at.UTC()}, key, nil
}

// APIKeys lists a project's keys, or with "" those of every project the caller owns,
// which for an administrator is every project.
func (s *Store) APIKeys(ctx context.Context, a Access, projectID string) ([]APIKey, error) {
	var projects []string
	if projectID != "" {
		if err := mayManageKeys(a, projectID); err != nil {
			return nil, err
		}
		projects = []string{projectID}
	} else {
		for _, p := range a.Projects {
			if p.Role == Owner {
				projects = append(projects, p.ID)
			}
		}
	}
	q := db.New(s.r)
	keys := []APIKey{}
	for _, p := range projects {
		rows, err := q.APIKeysOfProject(ctx, db.APIKeysOfProjectParams{WorkspaceID: a.WorkspaceID, ProjectID: p})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			k := APIKey{
				ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Scope: r.Scope, Created: time.UnixMilli(r.CreatedAt).UTC(),
				MadeBy: LogParty{r.MadeByKind, r.MadeByID, r.MadeByName},
			}
			if r.UsedAt.Valid {
				k.LastUsed = time.UnixMilli(r.UsedAt.Int64).UTC()
			}
			own := a.PersonID != "" && r.MadeByID == a.PersonID
			// As the log reads: who an administrator is, is theirs to say, and the
			// sign-in is for an administrator or the one who made it (spec, Design).
			if r.MadeByOutsider == 1 && !own && !a.Admin {
				k.MadeBy = LogParty{Kind: "administrator"}
			}
			if a.Admin || own {
				k.Via = r.MadeVia
			}
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// RevokeAPIKey ends a key, which stops working on the next request. A key the caller
// may not see gives ErrNotFound.
func (s *Store) RevokeAPIKey(ctx context.Context, a Access, keyID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	k, err := q.APIKeyByID(ctx, db.APIKeyByIDParams{WorkspaceID: a.WorkspaceID, ID: keyID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := mayManageKeys(a, k.ProjectID); err != nil {
		return err
	}
	if err := writeLog(ctx, q, logEntry{
		WorkspaceID: a.WorkspaceID, At: at, ProjectID: k.ProjectID, Kind: "key.revoked",
		AboutKind: "key", AboutID: k.ID, AboutName: k.Name, By: a.Actor, Details: map[string]any{"scope": k.Scope},
	}); err != nil {
		return err
	}
	if err := q.DeleteAPIKey(ctx, db.DeleteAPIKeyParams{WorkspaceID: a.WorkspaceID, ID: keyID}); err != nil {
		return err
	}
	return tx.Commit()
}

// CurrentKey is the key a request came with.
type CurrentKey struct {
	ID, WorkspaceID, ProjectID, Name, Scope string
}

// AccessOfAPIKey returns what a key may do: its one project, as a viewer with the read
// scope and as an editor with manage. It gives ErrNotFound for a key that was revoked,
// or whose project is gone. Last use is written at most once per SessionTouch.
func (s *Store) AccessOfAPIKey(ctx context.Context, key string, at time.Time) (Access, CurrentKey, error) {
	r, err := db.New(s.r).APIKeyByHash(ctx, hash(key))
	if errors.Is(err, sql.ErrNoRows) {
		return Access{}, CurrentKey{}, ErrNotFound
	}
	if err != nil {
		return Access{}, CurrentKey{}, err
	}
	ms := at.UnixMilli()
	if !r.UsedAt.Valid || ms-r.UsedAt.Int64 >= SessionTouch.Milliseconds() {
		if err := db.New(s.w).TouchAPIKey(ctx, db.TouchAPIKeyParams{UsedAt: sql.NullInt64{Int64: ms, Valid: true}, ID: r.ID}); err != nil {
			return Access{}, CurrentKey{}, err
		}
	}
	role := Viewer
	if r.Scope == ScopeManage {
		role = Editor
	}
	return Access{
		WorkspaceID: r.WorkspaceID,
		Projects:    []ProjectAccess{{ID: r.ProjectID, Name: r.ProjectName, Role: role}},
		Actor:       Actor{Kind: "key", ID: r.ID, Name: r.Name},
	}, CurrentKey{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID, Name: r.Name, Scope: r.Scope,
	}, nil
}
