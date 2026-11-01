// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
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
	ProbeID     string // the probe that runs it; by default the server
	Settings    map[string]any
	// Kind is ping (the default), or http or tcp for a check Allstill runs
	// itself, on Interval, with a grace of at least the interval.
	Kind string
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
	case n.Kind != "" && n.Kind != "ping" && !Active(n.Kind):
		return &InvalidError{"kind", "is ping, http or tcp"}
	case Active(n.Kind) && n.Cron != "":
		return &InvalidError{"schedule", "a check Allstill runs itself runs on an interval"}
	}
	for _, t := range n.Tags {
		if t == "" || len(t) > 50 {
			return &InvalidError{"tags", "each must be 1 to 50 characters"}
		}
	}
	if Active(n.Kind) {
		if err := validateActive(n.Kind, n.Interval, n.Settings); err != nil {
			return err
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

// MaxActiveInterval is the longest interval of a check Allstill runs itself:
// at least once a day (intent/http-checks/spec.md, R2).
const MaxActiveInterval = 24 * time.Hour

// validateActive checks what an active check runs on, so a check is never
// made or changed into one Allstill cannot read or run.
func validateActive(kind string, interval time.Duration, settings map[string]any) error {
	if interval > MaxActiveInterval {
		return &InvalidError{"schedule.every", "must be at most a day"}
	}
	if v, ok := settings["fail_after"]; ok {
		// Read as configOf reads it.
		raw, _ := json.Marshal(v)
		var after int
		if json.Unmarshal(raw, &after) != nil || after < 1 || after > 100 {
			return &InvalidError{"fail_after", "must be a whole number from 1 to 100"}
		}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if kind == "http" {
		_, err = active.ParseHTTP(raw)
	} else {
		_, err = active.ParseTCP(raw)
	}
	if err != nil {
		if se, ok := errors.AsType[*active.SettingError](err); ok {
			return &InvalidError{se.Field, se.Problem}
		}
		return err
	}
	return nil
}

// activeGrace is an active check's grace: its deadline is its next attempt
// plus the interval or 2 minutes, whichever is longer.
func activeGrace(interval time.Duration) time.Duration { return max(interval, 2*time.Minute) }

// CreatedCheck is what a client needs of a new check.
type CreatedCheck struct {
	ID       string
	PingUUID string
}

// CreateCheck adds a check to a project; the caller must be an editor or
// owner there. An active check's grace is at least its interval: its deadline
// is its next attempt plus the grace (intent/http-checks/spec.md).
func (s *Store) CreateCheck(ctx context.Context, a Access, projectID string, n NewCheck) (CreatedCheck, error) {
	role, ok := a.Role(projectID)
	if !ok {
		return CreatedCheck{}, ErrNotFound
	}
	if role == Viewer {
		return CreatedCheck{}, ErrForbidden
	}
	if Active(n.Kind) {
		n.Grace = max(n.Grace, activeGrace(n.Interval))
	}
	if err := n.validate(); err != nil {
		return CreatedCheck{}, err
	}
	q := db.New(s.w)
	if n.ProbeID != "" {
		if !Active(n.Kind) {
			return CreatedCheck{}, &InvalidError{"probe", "only a check Allstill runs itself runs on a probe"}
		}
		if err := probeServes(ctx, q, a.WorkspaceID, n.ProbeID, projectID); err != nil {
			return CreatedCheck{}, err
		}
	}
	return insertCheck(ctx, q, a.WorkspaceID, projectID, n)
}

// insertCheck writes a check that has been validated.
func insertCheck(ctx context.Context, q *db.Queries, workspaceID, projectID string, n NewCheck) (CreatedCheck, error) {
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
		ID: c.ID, WorkspaceID: workspaceID, ProjectID: projectID, PingUuid: c.PingUUID, Name: n.Name,
		Slug: nullString(n.Slug), Description: n.Description, Tags: string(tags), Kind: cmp.Or(n.Kind, "ping"),
		GraceSeconds: int64(n.Grace / time.Second), Settings: string(settings),
		State: string(state), StateSince: at, CreatedAt: at, UpdatedAt: at, ProbeID: nullString(n.ProbeID),
	}
	if n.Cron != "" {
		p.ScheduleKind = "cron"
		p.CronExpression, p.TimeZone = nullString(n.Cron), nullString(n.TimeZone)
	} else {
		p.ScheduleKind = "interval"
		p.IntervalSeconds = sql.NullInt64{Int64: int64(n.Interval / time.Second), Valid: true}
	}
	if err := q.CreateCheck(ctx, p); err != nil {
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
	// A probe keeps to the projects it serves, also when a check moves (C6).
	moved, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: workspaceID, ID: checkID})
	if err != nil {
		return err
	}
	if moved.ProbeID.Valid {
		if err := probeServes(ctx, q, workspaceID, moved.ProbeID.String, toProjectID); err != nil {
			return err
		}
	}
	if own, err := q.ProbeOfOwnCheck(ctx, db.ProbeOfOwnCheckParams{WorkspaceID: workspaceID, CheckID: checkID}); err == nil {
		if err := probeServes(ctx, q, workspaceID, own, toProjectID); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
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

// Attempt is one attempt of an active check, as its runner saw it.
type Attempt struct {
	check.Attempt
	Started  time.Time
	Measured map[string]float64 // such as status, ms and cert_days
	// ProbeID is the probe that made it: only who runs the check can judge it.
	ProbeID string
	// Arrived is when Allstill learned of it, when that is later than it ended.
	Arrived time.Time
}

// RecordAttempt keeps an attempt of an active check as a run, and applies it
// to the check's state, in one transaction. An attempt that could not judge
// the check is no run: it only makes the check unknown.
func (s *Store) RecordAttempt(ctx context.Context, workspaceID, checkID string, a Attempt) (check.Status, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return check.Status{}, err
	}
	defer tx.Rollback()
	after, err := recordAttempt(ctx, db.New(tx), workspaceID, checkID, a)
	if err != nil {
		return check.Status{}, err
	}
	return after, tx.Commit()
}

// AttemptOf is an attempt of one check, for RecordAttempts.
type AttemptOf struct {
	WorkspaceID, CheckID string
	Attempt
}

// RecordAttempts keeps the attempts of one tick of the loop in one
// transaction, so ping ingest waits for the write lock once, not for each.
// An attempt for a check that is gone is left out. It returns the earliest
// deadline the checks now have, for the scheduler.
func (s *Store) RecordAttempts(ctx context.Context, attempts []AttemptOf) (time.Time, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	var earliest time.Time
	for _, a := range attempts {
		after, err := recordAttempt(ctx, q, a.WorkspaceID, a.CheckID, a.Attempt)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return time.Time{}, err
		}
		if d := after.Deadline; !d.IsZero() && (earliest.IsZero() || d.Before(earliest)) {
			earliest = d
		}
	}
	return earliest, tx.Commit()
}

func recordAttempt(ctx context.Context, q *db.Queries, workspaceID, checkID string, a Attempt) (check.Status, error) {
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: workspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return check.Status{}, ErrNotFound
	}
	if err != nil {
		return check.Status{}, err
	}
	cfg, err := configOf(c)
	if err != nil {
		return check.Status{}, err
	}
	before := statusOf(c)
	if before.State == check.Paused {
		// Paused while the attempt ran: it is not judged, nor kept.
		return before, nil
	}
	if c.ProbeID.String != a.ProbeID {
		// Given to someone else while the attempt ran, or before the loop
		// heard: a check is run by the server, or by one probe.
		return before, nil
	}
	after, err := check.OnAttempt(cfg, before, a.Attempt)
	if err != nil {
		return check.Status{}, err
	}
	if a.Outcome != check.Unjudged {
		outcome := map[check.Outcome]string{check.Passed: "success", check.Warned: "warning", check.Failed: "fail"}[a.Outcome]
		if a.Measured == nil {
			a.Measured = map[string]float64{}
		}
		measured, err := json.Marshal(a.Measured)
		if err != nil {
			return check.Status{}, err
		}
		if err := q.CreateAttemptRun(ctx, db.CreateAttemptRunParams{
			ID: id.New(id.Run), WorkspaceID: c.WorkspaceID, CheckID: c.ID, Outcome: outcome,
			StartedAt: nullMs(a.Started), EndedAt: nullMs(a.At), Measured: string(measured), Reason: a.Reason,
		}); err != nil {
			return check.Status{}, err
		}
	}
	if err := saveStatusAt(ctx, q, c, before, after, cmp.Or(a.Arrived, after.Since)); err != nil {
		return check.Status{}, err
	}
	return after, nil
}

// ActiveChecks returns the checks the server's loop runs: those Allstill
// runs itself that are not paused, of the kinds it knows. One whose settings
// it cannot read is left out; it never had a run, or becomes unknown at its
// deadline.
func (s *Store) ActiveChecks(ctx context.Context) ([]active.Check, error) {
	rows, err := db.New(s.r).ActiveChecks(ctx)
	if err != nil {
		return nil, err
	}
	var checks []active.Check
	for _, r := range rows {
		c := active.Check{ID: r.ID, WorkspaceID: r.WorkspaceID, Every: time.Duration(r.IntervalSeconds.Int64) * time.Second}
		switch r.Kind {
		case "http":
			if c.HTTP, err = active.ParseHTTP([]byte(r.Settings)); err != nil {
				continue
			}
		case "tcp":
			t, err := active.ParseTCP([]byte(r.Settings))
			if err != nil {
				continue
			}
			c.TCP = &t
		default:
			continue
		}
		checks = append(checks, c)
	}
	return checks, nil
}

// SetActiveCheck changes what a check Allstill runs itself fetches and how
// often: its interval, and its settings as a whole. The caller must be an
// editor or owner of its project. A check waiting for an attempt waits at
// most one new interval for it, so a longer one does not make it unknown.
// at is when it changes.
func (s *Store) SetActiveCheck(ctx context.Context, a Access, checkID string, interval time.Duration, settings map[string]any, at time.Time) error {
	return s.setActiveCheck(ctx, a, checkID, interval, settings, nil, at)
}

// SetActiveCheckOn is SetActiveCheck that also has a probe run it, or with "" the
// server, as one change: the probe must serve its project (C6). Failures from one
// runner do not count towards the next.
func (s *Store) SetActiveCheckOn(ctx context.Context, a Access, checkID string, interval time.Duration, settings map[string]any, probeID string, at time.Time) error {
	return s.setActiveCheck(ctx, a, checkID, interval, settings, &probeID, at)
}

func (s *Store) setActiveCheck(ctx context.Context, a Access, checkID string, interval time.Duration, settings map[string]any, probeID *string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: a.WorkspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	role, ok := a.Role(c.ProjectID)
	switch {
	case !ok:
		return ErrNotFound
	case role == Viewer:
		return ErrForbidden
	case !Active(c.Kind):
		return &InvalidError{"kind", "only a check Allstill runs itself has these settings"}
	case interval < time.Minute:
		return &InvalidError{"schedule.every", "must be at least 60 seconds"}
	}
	if err := validateActive(c.Kind, interval, settings); err != nil {
		return err
	}
	if probeID != nil && *probeID != "" {
		if err := probeServes(ctx, q, a.WorkspaceID, *probeID, c.ProjectID); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	grace := activeGrace(interval)
	deadline := c.Deadline
	if deadline.Valid {
		deadline.Int64 = at.Add(interval + grace).UnixMilli()
	}
	n, err := q.UpdateActiveCheck(ctx, db.UpdateActiveCheckParams{
		IntervalSeconds: sql.NullInt64{Int64: int64(interval / time.Second), Valid: true},
		GraceSeconds:    int64(grace / time.Second), Settings: string(raw), Deadline: deadline,
		UpdatedAt: at.UnixMilli(), WorkspaceID: a.WorkspaceID, ID: checkID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if probeID != nil {
		if err := q.SetCheckProbe(ctx, db.SetCheckProbeParams{
			ProbeID: nullString(*probeID), Deadline: deadline, UpdatedAt: at.UnixMilli(), WorkspaceID: a.WorkspaceID, ID: checkID,
		}); err != nil {
			return err
		}
	}
	if err := q.CreateEvent(ctx, db.CreateEventParams{
		WorkspaceID: a.WorkspaceID, ProjectID: sql.NullString{String: c.ProjectID, Valid: true},
		Type: "check.updated", SubjectID: checkID, CreatedAt: at.UnixMilli(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}
