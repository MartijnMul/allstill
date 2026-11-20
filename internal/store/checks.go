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
	// Urgency, when set, says how fast its problems reach people, with Critical.
	Urgency  string
	Critical bool
	Settings map[string]any
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
		return &InvalidError{"kind", "must be ping, http or tcp"}
	case Active(n.Kind) && n.Cron != "":
		return &InvalidError{"schedule", "a check Allstill runs itself runs on an interval"}
	case Active(n.Kind) && n.Slug != "":
		return &InvalidError{"slug", "a slug is only for a check that jobs ping"}
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
		n.Grace = activeGrace(n.Interval)
	}
	if err := n.validate(); err != nil {
		return CreatedCheck{}, err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return CreatedCheck{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := liveProject(ctx, q, a.WorkspaceID, projectID); err != nil {
		return CreatedCheck{}, err
	}
	if viaPerson(a) {
		if len(n.Description) > MaxDescription {
			return CreatedCheck{}, &InvalidError{"description", fmt.Sprintf("at most %d characters", MaxDescription)}
		}
		if err := teamRoom(ctx, q, projectID, cmp.Or(n.Kind, "ping")); err != nil {
			return CreatedCheck{}, err
		}
		if err := checkRoom(ctx, q, projectID, n.Kind); err != nil {
			return CreatedCheck{}, err
		}
	}
	if n.ProbeID != "" {
		if !Active(n.Kind) {
			return CreatedCheck{}, &InvalidError{"probe", "only a check Allstill runs itself runs on a probe"}
		}
		if err := probeServes(ctx, q, a.WorkspaceID, n.ProbeID, projectID); err != nil {
			return CreatedCheck{}, err
		}
	}
	created, err := insertCheck(ctx, q, a.WorkspaceID, projectID, n)
	if err != nil {
		return CreatedCheck{}, err
	}
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: a.WorkspaceID, ID: created.ID})
	if err != nil {
		return CreatedCheck{}, err
	}
	at := time.UnixMilli(c.CreatedAt)
	if n.Urgency != "" {
		if err := changeAlerting(ctx, q, a, c, n.Urgency, n.Critical, at); err != nil {
			return CreatedCheck{}, err
		}
	}
	// One form is one entry, also when it sets how the check alerts (R6).
	if err := logCheck(ctx, q, a.Actor, c, "check.created", "", at, map[string]any{"kind": c.Kind}); err != nil {
		return CreatedCheck{}, err
	}
	return created, tx.Commit()
}

// insertCheck writes a check that has been validated, into a project read as
// live in the same transaction: a project deleted a moment ago takes no check.
func insertCheck(ctx context.Context, q *db.Queries, workspaceID, projectID string, n NewCheck) (CreatedCheck, error) {
	if _, err := liveProject(ctx, q, workspaceID, projectID); err != nil {
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
func (s *Store) MoveCheck(ctx context.Context, by Actor, workspaceID, checkID, toProjectID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := moveCheck(ctx, db.New(tx), by, workspaceID, checkID, toProjectID, at); err != nil {
		return err
	}
	return tx.Commit()
}

func moveCheck(ctx context.Context, q *db.Queries, by Actor, workspaceID, checkID, toProjectID string, at time.Time) error {
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
	// A check stays in its team: it carries its history and its ping URL, which a
	// project of another team must not come to hold (teams spec, R24).
	fromTeam, err := q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: from})
	if err != nil {
		return err
	}
	toTeam, err := q.TeamOfProject(ctx, db.TeamOfProjectParams{WorkspaceID: workspaceID, ID: toProjectID})
	if err != nil {
		return err
	}
	if fromTeam != toTeam {
		return &InvalidError{"project", "belongs to another team, and a check moves only between projects of one team"}
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
	open, err := q.OpenIncidentOfCheck(ctx, checkID)
	incident := err == nil
	incidentID := open.ID
	if err == nil {
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
	// Each project keeps what happened in it, and reads of the other only when it
	// can see it. Who arrives down should not read a recovery from nowhere (R25).
	fromName, err := q.NameOfProject(ctx, from)
	if err != nil {
		return err
	}
	toName, err := q.NameOfProject(ctx, toProjectID)
	if err != nil {
		return err
	}
	if err := logCheck(ctx, q, by, db.Check{WorkspaceID: workspaceID, ProjectID: from, ID: checkID, Name: moved.Name}, "check.moved_out", incidentID, at, map[string]any{
		"to_project_id": toProjectID, "to_project": toName,
	}); err != nil {
		return err
	}
	return logCheck(ctx, q, by, db.Check{WorkspaceID: workspaceID, ProjectID: toProjectID, ID: checkID, Name: moved.Name}, "check.moved_in", incidentID, at, map[string]any{
		"from_project_id": from, "from_project": fromName, "state": moved.State, "incident_open": incident,
	})
}

// Attempt is one attempt of an active check, as its runner saw it.
type Attempt struct {
	check.Attempt
	Started  time.Time
	Measured map[string]float64 // such as status, ms and cert_days
	// ProbeID is the probe that made it: only who runs the check can judge it.
	ProbeID   string
	ProbeName string
	// Arrived is when Allstill learned of it, when that is later than it ended.
	Arrived time.Time
	// Revision, when set, names the settings it was made under: under others, it
	// says nothing about the check as it is now.
	Revision string
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
	if a.Revision != "" && a.Revision != revisionOf(c.Settings) {
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
	by := byAllstill
	if a.ProbeID != "" {
		by = byProbe(a.ProbeID, a.ProbeName)
	}
	if err := saveStatusAt(ctx, q, c, before, after, cmp.Or(a.Arrived, after.Since), causedBy(by)); err != nil {
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
		c := active.Check{ID: r.ID, WorkspaceID: r.WorkspaceID, ProjectID: r.ProjectID, TeamID: r.TeamID, Private: r.PrivateRanges != "", Revision: revisionOf(r.Settings), Every: time.Duration(r.IntervalSeconds.Int64) * time.Second}
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
	_, err := s.UpdateCheck(ctx, a, checkID, Change{Interval: &interval, Settings: settings, Probe: probeID}, at)
	return err
}
