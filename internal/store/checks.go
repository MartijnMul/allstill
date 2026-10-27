// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/schedule"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// ErrForbidden means the caller may see the project but not do this in it.
var ErrForbidden = errors.New("forbidden")

// ErrExists means a check with this ping UUID exists already.
var ErrExists = errors.New("a check with this ping UUID exists")

// InvalidError says which setting is wrong and why.
type InvalidError struct{ Field, Problem string }

func (e *InvalidError) Error() string { return e.Field + ": " + e.Problem }

// NewCheck is a ping check to create. Its schedule is an interval, or a cron
// expression with a time zone. A check moved from elsewhere keeps its ping
// UUID, so jobs only change the host they ping; otherwise a new one is made.
// Settings holds what later features read, keyed by feature.
type NewCheck struct {
	Name        string
	Slug        string
	Description string
	Tags        []string
	Interval    time.Duration
	Cron        string
	TimeZone    string
	Grace       time.Duration
	PingUUID    string
	Paused      bool
	Settings    map[string]any
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)

func (n NewCheck) validate() error {
	switch {
	case strings.TrimSpace(n.Name) == "" || len(n.Name) > 200:
		return &InvalidError{"name", "must be 1 to 200 characters"}
	case n.Slug != "" && (!slugPattern.MatchString(n.Slug) || n.Slug == "start" || n.Slug == "fail" || n.Slug == "log"):
		return &InvalidError{"slug", "must be lower-case letters, digits, - and _, and not start, fail or log"}
	case (n.Interval != 0) == (n.Cron != ""):
		return &InvalidError{"schedule", "must be an interval or a cron expression"}
	case n.Cron == "" && n.Interval < time.Minute:
		return &InvalidError{"schedule.every", "must be at least 60 seconds"}
	case n.Grace < time.Minute:
		return &InvalidError{"grace", "must be at least 60 seconds"}
	case len(n.Tags) > 20:
		return &InvalidError{"tags", "at most 20"}
	}
	for _, t := range n.Tags {
		if t == "" || len(t) > 50 {
			return &InvalidError{"tags", "each must be 1 to 50 characters"}
		}
	}
	if n.PingUUID != "" {
		// The UUID is the ping URL's secret: only a random one will do.
		if u, ok := id.ParseUUID(n.PingUUID); !ok || u != n.PingUUID || u[14] != '4' || !strings.ContainsRune("89ab", rune(u[19])) {
			return &InvalidError{"ping_uuid", "must be a random (version 4) UUID in lower case"}
		}
	}
	if n.Cron != "" {
		if _, err := schedule.ParseCron(n.Cron, n.TimeZone); err != nil {
			return &InvalidError{"schedule", err.Error()}
		}
	}
	return nil
}

// CreatedCheck is what a client needs of a new check.
type CreatedCheck struct {
	ID       string
	PingUUID string
}

// CreateCheck adds a ping check to a project; the caller must be an editor or
// owner there.
func (s *Store) CreateCheck(ctx context.Context, a Access, projectID string, n NewCheck) (CreatedCheck, error) {
	role, ok := a.Role(projectID)
	if !ok {
		return CreatedCheck{}, ErrNotFound
	}
	if role == Viewer {
		return CreatedCheck{}, ErrForbidden
	}
	if err := n.validate(); err != nil {
		return CreatedCheck{}, err
	}
	c := CreatedCheck{ID: id.New(id.Check), PingUUID: n.PingUUID}
	if c.PingUUID == "" {
		c.PingUUID = id.UUID()
	}
	tags, err := json.Marshal(append([]string{}, n.Tags...))
	if err != nil {
		return CreatedCheck{}, err
	}
	settings := []byte("{}")
	if n.Settings != nil {
		if settings, err = json.Marshal(n.Settings); err != nil {
			return CreatedCheck{}, err
		}
	}
	at := now()
	state := check.New
	if n.Paused {
		state = check.Paused
	}
	p := db.CreateCheckParams{
		ID: c.ID, WorkspaceID: a.WorkspaceID, ProjectID: projectID, PingUuid: c.PingUUID, Name: n.Name,
		Slug: nullString(n.Slug), Description: n.Description, Tags: string(tags), Kind: "ping",
		GraceSeconds: int64(n.Grace / time.Second), Settings: string(settings),
		State: string(state), StateSince: at, CreatedAt: at, UpdatedAt: at,
	}
	if n.Cron != "" {
		p.ScheduleKind = "cron"
		p.CronExpression, p.TimeZone = nullString(n.Cron), nullString(n.TimeZone)
	} else {
		p.ScheduleKind = "interval"
		p.IntervalSeconds = sql.NullInt64{Int64: int64(n.Interval / time.Second), Valid: true}
	}
	if err := db.New(s.w).CreateCheck(ctx, p); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: checks.project_id, checks.slug") {
			return CreatedCheck{}, &InvalidError{"slug", fmt.Sprintf("%q is already used in this project", n.Slug)}
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed: checks.ping_uuid") {
			return CreatedCheck{}, ErrExists
		}
		return CreatedCheck{}, err
	}
	return c, nil
}

// MoveCheck moves a check to another project. Its ping UUID, runs and pings go
// with it; a ping by slug needs the new project's ping key. A slug the new
// project already uses is refused. It is for the administrator's command line:
// it checks no caller's access. at is when it moves.
func (s *Store) MoveCheck(ctx context.Context, workspaceID, checkID, toProjectID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	from, err := q.ProjectOfCheck(ctx, db.ProjectOfCheckParams{WorkspaceID: workspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil || from == toProjectID {
		return err
	}
	if _, err := q.ProjectInWorkspace(ctx, db.ProjectInWorkspaceParams{WorkspaceID: workspaceID, ID: toProjectID}); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	ms := at.UnixMilli()
	if err := q.MoveCheck(ctx, db.MoveCheckParams{ProjectID: toProjectID, UpdatedAt: ms, WorkspaceID: workspaceID, ID: checkID}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: checks.project_id, checks.slug") {
			return &InvalidError{"slug", "the other project has a check with this slug"}
		}
		return err
	}
	// An open incident goes along: the new project's people follow it from now.
	if open, err := q.OpenIncidentOfCheck(ctx, checkID); err == nil {
		if err := q.MoveIncidentCheck(ctx, db.MoveIncidentCheckParams{ProjectID: toProjectID, CheckID: checkID}); err != nil {
			return err
		}
		// They should have known since it opened, so the sender looks at once:
		// due now, not since it opened, which would read as a sender behind.
		err := q.AddFollowers(ctx, db.AddFollowersParams{IncidentID: open.ID, DueAt: sql.NullInt64{Int64: ms, Valid: true}, ProjectID: toProjectID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// It leaves the one project and arrives in the other, in that order, so
	// who sees both ends up with the check.
	for _, e := range []struct{ project, kind string }{{from, "check.removed"}, {toProjectID, "check.updated"}} {
		if err := q.CreateEvent(ctx, db.CreateEventParams{
			WorkspaceID: workspaceID, ProjectID: sql.NullString{String: e.project, Valid: true},
			Type: e.kind, SubjectID: checkID, CreatedAt: ms,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
