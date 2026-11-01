// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// What someone who may edit a project can do to its checks
// (intent/manage-checks/spec.md). Every operation here checks the caller's role,
// so the commands, the forms and the API keep to the same rules. A caller without
// a person is the administrator's command line.

// ErrChanged means a check changed since the caller read it.
var ErrChanged = errors.New("the check changed since it was read")

// What a project may hold when checks are added by people rather than by the
// administrator's commands or an import (R30). A check Allstill runs is a request
// from the server's address; the count is a coarse brake on that.
const (
	MaxActiveChecks = 200
	MaxPingChecks   = CreateLimit
)

// MaxDescription is how long a description added by a person may be.
const MaxDescription = 2000

// Change is what to change of a check. A nil field stays as it is.
type Change struct {
	// Version, when set, is the version the change was made from: if the check
	// has moved on, the change is refused with ErrChanged.
	Version     int64
	Name        *string
	Slug        *string
	Description *string
	Tags        *[]string
	// Schedule: an Interval, or a Cron with its TimeZone.
	Interval *time.Duration
	Cron     *string
	TimeZone *string
	Grace    *time.Duration
	// Settings replaces what a check Allstill runs fetches, as a whole.
	Settings map[string]any
	// Probe has that probe run it, or with "" the server.
	Probe *string
	// Urgency and Critical say how fast its problems reach people, set together.
	Urgency  *string
	Critical *bool
}

// viaPerson says the caller is a person, or a key, not the administrator's
// command line.
func viaPerson(a Access) bool { return a.PersonID != "" }

// editable reads a check for a caller who means to change it: not found when they
// may not see it, forbidden when they may only look.
func editable(ctx context.Context, q *db.Queries, a Access, checkID string) (db.Check, error) {
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: a.WorkspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	role, ok := a.Role(c.ProjectID)
	switch {
	case !ok:
		return c, ErrNotFound
	case role == Viewer:
		return c, ErrForbidden
	}
	return c, nil
}

// ownProbe says whether a check is a probe's own, which its sign of life depends
// on (R19).
func ownProbe(ctx context.Context, q *db.Queries, workspaceID, checkID string) (string, bool, error) {
	probeID, err := q.ProbeOfOwnCheck(ctx, db.ProbeOfOwnCheckParams{WorkspaceID: workspaceID, CheckID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return probeID, err == nil, err
}

const ownCheckProblem = "this is a probe's own check, which tells when the probe goes quiet"

// UpdateCheck changes a check, in one transaction. A change of schedule, grace or
// who runs it, and of what a check Allstill runs fetches, counts from at, so it
// never itself moves a check to late or down. A
// changed address, or another runner, starts a check's failures over. It returns
// the check's deadline, for the scheduler.
func (s *Store) UpdateCheck(ctx context.Context, a Access, checkID string, ch Change, at time.Time) (time.Time, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return time.Time{}, err
	}
	if ch.Version != 0 && ch.Version != c.Version {
		return time.Time{}, ErrChanged
	}
	if ch.Settings != nil && !Active(c.Kind) {
		return time.Time{}, &InvalidError{"kind", "only a check Allstill runs itself has these settings"}
	}
	updated, err := applyChange(c, ch)
	if err != nil {
		return time.Time{}, err
	}
	rescheduled := updated.ScheduleKind != c.ScheduleKind || updated.IntervalSeconds != c.IntervalSeconds ||
		updated.CronExpression != c.CronExpression || updated.TimeZone != c.TimeZone || updated.GraceSeconds != c.GraceSeconds
	if err := checkChange(ctx, q, a, c, updated, ch, rescheduled); err != nil {
		return time.Time{}, err
	}
	err = q.UpdateCheckSettings(ctx, db.UpdateCheckSettingsParams{
		Name: updated.Name, Slug: updated.Slug, Description: updated.Description, Tags: updated.Tags,
		ScheduleKind: updated.ScheduleKind, IntervalSeconds: updated.IntervalSeconds,
		CronExpression: updated.CronExpression, TimeZone: updated.TimeZone, GraceSeconds: updated.GraceSeconds,
		Settings: updated.Settings, ProbeID: updated.ProbeID, UpdatedAt: at.UnixMilli(), WorkspaceID: a.WorkspaceID, ID: checkID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: checks.project_id, checks.slug") {
			return time.Time{}, &InvalidError{"slug", fmt.Sprintf("%q is already used in this project", updated.Slug.String)}
		}
		return time.Time{}, err
	}
	before := statusOf(c)
	after := before
	moved := updated.ProbeID != c.ProbeID
	// New settings drop the attempt in flight, so it waits for the next one too.
	if rescheduled || moved || (Active(c.Kind) && updated.Settings != c.Settings) {
		cfg, err := configOf(updated)
		if err != nil {
			return time.Time{}, &InvalidError{"schedule", err.Error()}
		}
		open, err := openRuns(ctx, q, c.ID)
		if err != nil {
			return time.Time{}, err
		}
		_, err = q.OpenIncidentOfCheck(ctx, c.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, err
		}
		incident := err == nil
		var letGo []string
		if after, letGo, err = check.OnSchedule(cfg, before, open, incident, at); err != nil {
			return time.Time{}, &InvalidError{"schedule", err.Error()}
		}
		for _, runID := range letGo {
			if err := q.TimeOutRun(ctx, db.TimeOutRunParams{TimedOutAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, ID: runID}); err != nil {
				return time.Time{}, err
			}
		}
	}
	if moved || addressOf(updated.Settings) != addressOf(c.Settings) {
		after.FailStreak = 0
	}
	if err := saveStatus(ctx, q, updated, before, after); err != nil {
		return time.Time{}, err
	}
	if ch.Urgency != nil {
		if err := changeAlerting(ctx, q, a, c, *ch.Urgency, ch.Critical != nil && *ch.Critical, at); err != nil {
			return time.Time{}, err
		}
	}
	if err := q.CreateEvent(ctx, db.CreateEventParams{
		WorkspaceID: a.WorkspaceID, ProjectID: sql.NullString{String: c.ProjectID, Valid: true},
		Type: "check.updated", SubjectID: checkID, CreatedAt: at.UnixMilli(),
	}); err != nil {
		return time.Time{}, err
	}
	return after.Deadline, tx.Commit()
}

// applyChange returns the check as the change would leave it.
func applyChange(c db.Check, ch Change) (db.Check, error) {
	u := c
	if ch.Name != nil {
		u.Name = strings.TrimSpace(*ch.Name)
	}
	if ch.Slug != nil {
		u.Slug = nullString(*ch.Slug)
	}
	if ch.Description != nil {
		u.Description = *ch.Description
	}
	if ch.Tags != nil {
		tags, err := json.Marshal(append([]string{}, *ch.Tags...))
		if err != nil {
			return u, err
		}
		u.Tags = string(tags)
	}
	switch {
	case ch.Interval != nil && ch.Cron != nil:
		return u, &InvalidError{"schedule", "must be an interval or a cron expression"}
	case ch.Interval != nil:
		u.ScheduleKind = "interval"
		u.IntervalSeconds = sql.NullInt64{Int64: int64(*ch.Interval / time.Second), Valid: true}
		u.CronExpression, u.TimeZone = sql.NullString{}, sql.NullString{}
	case ch.Cron != nil:
		u.ScheduleKind = "cron"
		u.CronExpression = nullString(*ch.Cron)
		u.IntervalSeconds = sql.NullInt64{}
		if ch.TimeZone != nil {
			u.TimeZone = nullString(*ch.TimeZone)
		}
	case ch.TimeZone != nil && u.ScheduleKind == "cron":
		u.TimeZone = nullString(*ch.TimeZone)
	}
	if ch.Grace != nil {
		if Active(u.Kind) {
			return u, &InvalidError{"grace", "follows from the interval of a check Allstill runs itself"}
		}
		u.GraceSeconds = int64(*ch.Grace / time.Second)
	}
	if Active(u.Kind) && ch.Interval != nil {
		// An active check's grace follows from its interval.
		u.GraceSeconds = int64(activeGrace(*ch.Interval) / time.Second)
	}
	if ch.Settings != nil {
		raw, err := json.Marshal(ch.Settings)
		if err != nil {
			return u, err
		}
		u.Settings = string(raw)
	}
	if ch.Probe != nil {
		u.ProbeID = nullString(*ch.Probe)
	}
	return u, nil
}

// checkChange refuses what may not be: the same rules as a new check, and what
// keeps a probe to its projects and its own check working.
func checkChange(ctx context.Context, q *db.Queries, a Access, c, u db.Check, ch Change, rescheduled bool) error {
	var tags []string
	if err := json.Unmarshal([]byte(u.Tags), &tags); err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(u.Settings), &settings); err != nil {
		return err
	}
	n := NewCheck{
		Name: u.Name, Slug: u.Slug.String, Tags: tags, Kind: u.Kind, Grace: time.Duration(u.GraceSeconds) * time.Second,
		Cron: u.CronExpression.String, TimeZone: u.TimeZone.String, Settings: settings,
	}
	if u.ScheduleKind == "interval" {
		n.Interval = time.Duration(u.IntervalSeconds.Int64) * time.Second
	}
	if err := n.validate(); err != nil {
		return err
	}
	if viaPerson(a) && len(u.Description) > MaxDescription && u.Description != c.Description {
		return &InvalidError{"description", fmt.Sprintf("at most %d characters", MaxDescription)}
	}
	if _, own, err := ownProbe(ctx, q, a.WorkspaceID, c.ID); err != nil {
		return err
	} else if own && rescheduled {
		return &InvalidError{"schedule", ownCheckProblem + ", so its schedule stays"}
	}
	if ch.Probe != nil && *ch.Probe != c.ProbeID.String {
		if !Active(c.Kind) {
			return &InvalidError{"probe", "only a check Allstill runs itself runs on a probe"}
		}
		if *ch.Probe != "" {
			return probeServes(ctx, q, a.WorkspaceID, *ch.Probe, c.ProjectID)
		}
	}
	return nil
}

// addressOf is what a check's settings connect to.
func addressOf(settings string) string {
	var s struct {
		URL  string `json:"url"`
		Host string `json:"host"`
		Port int    `json:"port"`
		TLS  bool   `json:"tls"`
	}
	json.Unmarshal([]byte(settings), &s)
	return fmt.Sprintf("%s|%s|%d|%v", s.URL, s.Host, s.Port, s.TLS)
}

// PauseCheck pauses a check: nothing is expected of it, its open incident closes
// without a recovery message, and a check Allstill runs stops being run. A ping
// resumes a check that jobs ping. at is when it changes.
func (s *Store) PauseCheck(ctx context.Context, a Access, checkID string, at time.Time) error {
	return s.setState(ctx, a, checkID, at, check.OnPause)
}

// ResumeCheck resumes a paused check, which starts over as new.
func (s *Store) ResumeCheck(ctx context.Context, a Access, checkID string, at time.Time) error {
	return s.setState(ctx, a, checkID, at, check.OnResume)
}

func (s *Store) setState(ctx context.Context, a Access, checkID string, at time.Time, change func(check.Status, time.Time) check.Status) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return err
	}
	if _, own, err := ownProbe(ctx, q, a.WorkspaceID, checkID); err != nil {
		return err
	} else if own {
		return &InvalidError{"state", ownCheckProblem + ", so it cannot be paused"}
	}
	before := statusOf(c)
	if err := saveStatus(ctx, q, c, before, change(before, at)); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteCheck deletes a check: it is gone from every list at once, its ping URL
// stops working, and what it fetched, with any token in it, is cleared. Its open
// incident closes without a recovery message. Tidy removes its runs and pings, and
// then the check.
func (s *Store) DeleteCheck(ctx context.Context, a Access, checkID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return err
	}
	if _, own, err := ownProbe(ctx, q, a.WorkspaceID, checkID); err != nil {
		return err
	} else if own {
		return &InvalidError{"state", ownCheckProblem + ", so it cannot be deleted"}
	}
	ms := at.UnixMilli()
	if open, err := q.OpenIncidentOfCheck(ctx, checkID); err == nil {
		if err := resolve(ctx, q, open.ID, "deleted", ms); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := q.MarkCheckDeleted(ctx, db.MarkCheckDeletedParams{
		DeletedAt: sql.NullInt64{Int64: ms, Valid: true}, UpdatedAt: ms, WorkspaceID: a.WorkspaceID, ID: checkID,
	}); err != nil {
		return err
	}
	if err := q.CreateEvent(ctx, db.CreateEventParams{
		WorkspaceID: a.WorkspaceID, ProjectID: sql.NullString{String: c.ProjectID, Valid: true},
		Type: "check.removed", SubjectID: checkID, CreatedAt: ms,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// RotatePingURL gives a check a new ping URL and returns its UUID. The old one
// stops working at once.
func (s *Store) RotatePingURL(ctx context.Context, a Access, checkID string, at time.Time) (string, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return "", err
	}
	if Active(c.Kind) {
		return "", &InvalidError{"kind", "Allstill runs this check itself, so nothing pings it"}
	}
	uuid := id.UUID()
	if err := q.SetPingUUID(ctx, db.SetPingUUIDParams{PingUuid: uuid, UpdatedAt: at.UnixMilli(), WorkspaceID: a.WorkspaceID, ID: checkID}); err != nil {
		return "", err
	}
	return uuid, tx.Commit()
}

// MoveCheckAs is MoveCheck for a caller who may edit both projects. A probe's own
// check moves only for an administrator (R19).
func (s *Store) MoveCheckAs(ctx context.Context, a Access, checkID, toProjectID string, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return err
	}
	role, ok := a.Role(toProjectID)
	if !ok {
		return ErrNotFound
	}
	if role == Viewer {
		return ErrForbidden
	}
	if _, own, err := ownProbe(ctx, q, a.WorkspaceID, checkID); err != nil {
		return err
	} else if own && !a.Admin {
		return &InvalidError{"project", ownCheckProblem + ", so only an administrator can move it"}
	}
	if viaPerson(a) && toProjectID != c.ProjectID {
		if err := checkRoom(ctx, q, toProjectID, c.Kind); err != nil {
			return err
		}
	}
	if err := moveCheck(ctx, q, a.WorkspaceID, checkID, toProjectID, at); err != nil {
		return err
	}
	return tx.Commit()
}

// checkRoom refuses a check a project has no more room for (R30).
func checkRoom(ctx context.Context, q *db.Queries, projectID, kind string) error {
	if Active(kind) {
		n, err := q.CountActiveChecksOfProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= MaxActiveChecks {
			return &InvalidError{"project", fmt.Sprintf("holds the %d checks Allstill runs that a project may have", MaxActiveChecks)}
		}
		return nil
	}
	n, err := q.CountChecksOfProject(ctx, projectID)
	if err != nil {
		return err
	}
	if n >= MaxPingChecks {
		return &InvalidError{"project", fmt.Sprintf("holds the %d checks a project may have", MaxPingChecks)}
	}
	return nil
}

// SetAlertingAs is SetAlerting for a caller who may edit the check. Only an owner
// or administrator turns critical on, as it breaks through everyone's quiet hours
// (C4), and a probe's own check keeps alerting at once for anyone else (R19).
func (s *Store) SetAlertingAs(ctx context.Context, a Access, checkID, urgency string, critical bool, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := editable(ctx, q, a, checkID)
	if err != nil {
		return err
	}
	if err := changeAlerting(ctx, q, a, c, urgency, critical, at); err != nil {
		return err
	}
	return tx.Commit()
}

// changeAlerting holds SetAlertingAs's rules, within a caller's transaction.
func changeAlerting(ctx context.Context, q *db.Queries, a Access, c db.Check, urgency string, critical bool, at time.Time) error {
	role, _ := a.Role(c.ProjectID)
	if critical && c.Critical == 0 && role != Owner && !a.Admin {
		return &InvalidError{"critical", "only an owner or administrator can turn this on: it wakes people during their quiet hours"}
	}
	if _, own, err := ownProbe(ctx, q, a.WorkspaceID, c.ID); err != nil {
		return err
	} else if own && urgency != "now" && !a.Admin {
		return &InvalidError{"urgency", ownCheckProblem + ", so only an administrator can change this"}
	}
	return setAlerting(ctx, q, a.WorkspaceID, c.ID, urgency, critical, at)
}

// IsProbesOwnCheck says whether a check is a probe's own, which has its own rules
// (R19). The caller must be able to see it.
func (s *Store) IsProbesOwnCheck(ctx context.Context, a Access, checkID string) (bool, error) {
	q := db.New(s.r)
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: a.WorkspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if _, ok := a.Role(c.ProjectID); !ok {
		return false, ErrNotFound
	}
	_, own, err := ownProbe(ctx, q, a.WorkspaceID, checkID)
	return own, err
}

// RunsOfCheck says how many runs a check has, for the page that deletes it.
func (s *Store) RunsOfCheck(ctx context.Context, a Access, checkID string) (int64, error) {
	if _, err := s.Check(ctx, a, checkID); err != nil {
		return 0, err
	}
	return db.New(s.r).CountRunsOfCheck(ctx, checkID)
}

// ProbeChoice is a probe someone may give a check to.
type ProbeChoice struct {
	ID, Name, ProjectID string
}

// ProbesFor lists the probes serving the projects the caller may edit, per
// project. Someone who may only look, or has no part in a project, is not told of
// its probes (case 9).
func (s *Store) ProbesFor(ctx context.Context, a Access) ([]ProbeChoice, error) {
	q := db.New(s.r)
	var choices []ProbeChoice
	for _, p := range a.Projects {
		if p.Role == Viewer {
			continue
		}
		rows, err := q.ProbesOfProject(ctx, db.ProbesOfProjectParams{WorkspaceID: a.WorkspaceID, ProjectID: p.ID})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			choices = append(choices, ProbeChoice{ID: r.ID, Name: r.Name, ProjectID: p.ID})
		}
	}
	return choices, nil
}

// revisionOf names a check's settings, so an attempt made under other settings
// can be told apart. It is not a secret, and shows nothing of them.
func revisionOf(settings string) string {
	return hex.EncodeToString(hash(settings)[:8])
}
