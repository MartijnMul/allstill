// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/schedule"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// PingIn is a received ping. A check is named by its ping UUID, or by its
// project's ping key and its slug.
type PingIn struct {
	CheckUUID string
	PingKey   string
	Slug      string
	Create    bool // create a check for an unknown slug

	Kind          check.Kind
	ExitStatus    int // -1 when none
	RID           string
	Values        map[string]float64
	Method        string
	RemoteAddr    string
	UserAgent     string
	Body          []byte
	BodyTruncated bool
	At            time.Time
}

// PingOut is what became of a ping.
type PingOut struct {
	Created  bool      // the check was created by this ping
	Deadline time.Time // the check's new deadline, for the scheduler
}

// ErrAmbiguous means a ping names both a UUID and a slug, or neither.
var ErrAmbiguous = errors.New("a ping names a check by UUID or by ping key and slug")

// Ping records a ping and applies it to its check, in one transaction. A ping
// for a check that does not exist returns ErrNotFound.
func (s *Store) Ping(ctx context.Context, in PingIn) (PingOut, error) {
	if (in.CheckUUID == "") == (in.PingKey == "" || in.Slug == "") {
		return PingOut{}, ErrAmbiguous
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return PingOut{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)

	var out PingOut
	c, err := s.findCheck(ctx, q, in, &out)
	if err != nil {
		return PingOut{}, err
	}
	open, err := openRuns(ctx, q, c.ID)
	if err != nil {
		return PingOut{}, err
	}
	// A start can join a run whose end overtook it, within the grace period
	// a run has to end in: an older run with that rid is another run.
	var ended string
	if in.Kind == check.Start && in.RID != "" {
		since := in.At.Add(-time.Duration(c.GraceSeconds) * time.Second).UnixMilli()
		ended, err = q.EndedRunByRid(ctx, db.EndedRunByRidParams{CheckID: c.ID, Rid: nullString(in.RID), EndedAt: sql.NullInt64{Int64: since, Valid: true}})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PingOut{}, err
		}
	}
	before := statusOf(c)
	var after check.Status
	var act check.RunAction
	if cfg, err := configOf(c); err != nil {
		// The ping is kept, but without a schedule nothing can be judged.
		after = unreadable(before, err, in.At)
	} else {
		after, act, err = check.OnPing(cfg, before, open, check.Ping{Kind: in.Kind, ExitStatus: in.ExitStatus, RID: in.RID, At: in.At, Ended: ended})
		if err != nil {
			return PingOut{}, err
		}
	}

	at := in.At.UnixMilli()
	exit := sql.NullInt64{Int64: int64(in.ExitStatus), Valid: in.ExitStatus >= 0 && (in.Kind == check.Success || in.Kind == check.Fail)}
	runID := sql.NullString{String: act.RunID, Valid: act.RunID != ""}
	switch act.Op {
	case check.OpenNew:
		runID = sql.NullString{String: id.New(id.Run), Valid: true}
		err = q.CreateRun(ctx, db.CreateRunParams{
			ID: runID.String, WorkspaceID: c.WorkspaceID, CheckID: c.ID, Rid: nullString(in.RID),
			Outcome: "running", StartedAt: sql.NullInt64{Int64: at, Valid: true},
		})
	case check.Restart:
		err = q.RestartRun(ctx, db.RestartRunParams{StartedAt: sql.NullInt64{Int64: at, Valid: true}, ID: act.RunID})
	case check.Finish:
		err = q.FinishRun(ctx, db.FinishRunParams{
			Outcome: string(in.Kind), EndedAt: sql.NullInt64{Int64: endOf(act.RunID, open, at), Valid: true}, ExitStatus: exit, ID: act.RunID,
		})
	case check.EndOnly:
		runID = sql.NullString{String: id.New(id.Run), Valid: true}
		err = q.CreateRun(ctx, db.CreateRunParams{
			ID: runID.String, WorkspaceID: c.WorkspaceID, CheckID: c.ID, Rid: nullString(in.RID),
			Outcome: string(in.Kind), EndedAt: sql.NullInt64{Int64: at, Valid: true}, ExitStatus: exit,
		})
	}
	if err != nil {
		return PingOut{}, err
	}
	for _, letGo := range act.LetGo {
		if err := q.TimeOutRun(ctx, db.TimeOutRunParams{TimedOutAt: sql.NullInt64{Int64: at, Valid: true}, ID: letGo}); err != nil {
			return PingOut{}, err
		}
	}
	if len(in.Values) > 0 && runID.Valid {
		v, err := json.Marshal(in.Values)
		if err != nil {
			return PingOut{}, err
		}
		if err := q.MergeMeasured(ctx, db.MergeMeasuredParams{JsonPatch: string(v), ID: runID.String}); err != nil {
			return PingOut{}, err
		}
	}
	err = q.CreatePing(ctx, db.CreatePingParams{
		WorkspaceID: c.WorkspaceID, CheckID: c.ID, RunID: runID, Kind: string(in.Kind), ExitStatus: exit,
		ReceivedAt: at, Method: in.Method, RemoteAddr: in.RemoteAddr, UserAgent: in.UserAgent,
		BodyTruncated: boolInt(in.BodyTruncated), Body: in.Body,
	})
	if err != nil {
		return PingOut{}, err
	}
	if err := saveStatus(ctx, q, c, before, after); err != nil {
		return PingOut{}, err
	}
	out.Deadline = after.Deadline
	return out, tx.Commit()
}

// CreateLimit is how many checks a project may have for a ping with
// `create=1` to add another.
const CreateLimit = 1000

func (s *Store) findCheck(ctx context.Context, q *db.Queries, in PingIn, out *PingOut) (db.Check, error) {
	if in.CheckUUID != "" {
		c, err := q.CheckByPingUUID(ctx, in.CheckUUID)
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	h := sha256.Sum256([]byte(in.PingKey))
	p, err := q.ProjectByPingKeyHash(ctx, h[:])
	if errors.Is(err, sql.ErrNoRows) {
		return db.Check{}, ErrNotFound
	}
	if err != nil {
		return db.Check{}, err
	}
	c, err := q.CheckBySlug(ctx, db.CheckBySlugParams{ProjectID: p.ID, Slug: nullString(in.Slug)})
	if !errors.Is(err, sql.ErrNoRows) || !in.Create {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	// A leaked ping key must not fill the database with checks: as in
	// Healthchecks, pings create no more once a project is full.
	if n, err := q.CountChecksOfProject(ctx, p.ID); err != nil {
		return db.Check{}, err
	} else if n >= CreateLimit {
		return db.Check{}, ErrNotFound
	}
	// The project's defaults, as Healthchecks gives a check made by a ping.
	checkID, at := id.New(id.Check), in.At.UnixMilli()
	err = q.CreateCheck(ctx, db.CreateCheckParams{
		ID: checkID, WorkspaceID: p.WorkspaceID, ProjectID: p.ID, PingUuid: id.UUID(), Name: in.Slug,
		Slug: nullString(in.Slug), Tags: "[]", Kind: "ping", ScheduleKind: "interval",
		IntervalSeconds: sql.NullInt64{Int64: p.DefaultInterval, Valid: true}, GraceSeconds: p.DefaultGrace,
		Settings: "{}", State: string(check.New), StateSince: at, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		return db.Check{}, err
	}
	out.Created = true
	return q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: p.WorkspaceID, ID: checkID})
}

// ProcessDue applies every deadline that has passed at now, in small
// transactions, and returns the earliest deadline still ahead (zero when
// there is none).
func (s *Store) ProcessDue(ctx context.Context, now time.Time) (time.Time, error) {
	// A check can pass several deadlines at once, such as late and then down
	// after a pause of the server, so this runs until none is due. Every pass
	// moves each due check's deadline on, so it ends.
	const batch = 100
	for {
		n, err := s.processBatch(ctx, now, batch)
		if err != nil {
			return time.Time{}, err
		}
		if n == 0 {
			break
		}
	}
	next, err := db.New(s.r).EarliestDeadline(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return msTime(sql.NullInt64{Int64: next, Valid: next != 0}), nil
}

func (s *Store) processBatch(ctx context.Context, now time.Time, batch int64) (int, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	due, err := q.DueChecks(ctx, db.DueChecksParams{Deadline: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, Limit: batch})
	if err != nil {
		return 0, err
	}
	for _, c := range due {
		cfg, err := configOf(c)
		if err != nil {
			if err := saveStatus(ctx, q, c, statusOf(c), unreadable(statusOf(c), err, now)); err != nil {
				return 0, err
			}
			continue
		}
		open, err := openRuns(ctx, q, c.ID)
		if err != nil {
			return 0, err
		}
		before := statusOf(c)
		after, timedOut := check.OnDeadline(cfg, before, open, now)
		if after.Deadline.Equal(before.Deadline) {
			// Nothing moved it: never look at it again in this loop.
			after.Deadline = time.Time{}
		}
		for _, runID := range timedOut {
			if err := q.TimeOutRun(ctx, db.TimeOutRunParams{TimedOutAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, ID: runID}); err != nil {
				return 0, err
			}
		}
		if err := saveStatus(ctx, q, c, before, after); err != nil {
			return 0, err
		}
	}
	return len(due), tx.Commit()
}

// Alive records that the server runs at now, and removes sessions and links
// that have ended.
func (s *Store) Alive(ctx context.Context, now time.Time) error {
	q := db.New(s.w)
	err := q.SetInstallationValue(ctx, db.SetInstallationValueParams{
		Key: "alive_at", Value: strconv.FormatInt(now.UnixMilli(), 10),
	})
	if err != nil {
		return err
	}
	return prune(ctx, q, now)
}

// Setting returns an installation setting, or "" when it is not set.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	v, err := db.New(s.r).InstallationValue(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores an installation setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return db.New(s.w).SetInstallationValue(ctx, db.SetInstallationValueParams{Key: key, Value: value})
}

// Recover is called when the server starts: checks whose deadline passed
// while it was not running become unknown instead of late or down.
func (s *Store) Recover(ctx context.Context, now time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	v, err := q.InstallationValue(ctx, "alive_at")
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("alive_at %q: %w", v, err)
	}
	lastAlive := time.UnixMilli(ms)
	due, err := q.DueChecks(ctx, db.DueChecksParams{Deadline: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, Limit: -1})
	if err != nil {
		return err
	}
	for _, c := range due {
		before := statusOf(c)
		after := before
		if cfg, err := configOf(c); err != nil {
			after = unreadable(before, err, now)
		} else {
			after = check.OnRestart(cfg, before, lastAlive, now)
		}
		if err := saveStatus(ctx, q, c, before, after); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func configOf(c db.Check) (check.Config, error) {
	cfg := check.Config{Grace: time.Duration(c.GraceSeconds) * time.Second}
	switch c.ScheduleKind {
	case "interval":
		cfg.Schedule = check.Interval(time.Duration(c.IntervalSeconds.Int64) * time.Second)
	case "cron":
		cron, err := schedule.ParseCron(c.CronExpression.String, c.TimeZone.String)
		if err != nil {
			return cfg, fmt.Errorf("check %s: %w", c.ID, err)
		}
		cfg.Schedule = cron
	default:
		return cfg, fmt.Errorf("check %s: unknown schedule kind %q", c.ID, c.ScheduleKind)
	}
	return cfg, nil
}

// unreadable is the status of a check whose schedule cannot be read, such as
// a time zone this host does not know: unknown, with the reason, and no
// deadline, so it never holds up other checks.
func unreadable(s check.Status, err error, at time.Time) check.Status {
	if s.State != check.Unknown {
		s.Since = at
	}
	s.State, s.Reason, s.Deadline = check.Unknown, "Allstill cannot read its schedule: "+err.Error(), time.Time{}
	return s
}

func statusOf(c db.Check) check.Status {
	return check.Status{
		State:        check.State(c.State),
		Since:        time.UnixMilli(c.StateSince).UTC(),
		Reason:       c.StateReason,
		LastPing:     msTime(c.LastPingAt),
		NextExpected: msTime(c.NextExpectedAt),
		Deadline:     msTime(c.Deadline),
	}
}

// endOf is when a run ends: when its end arrived, but never before its start.
// A ping is timed when it arrives and written when it gets the lock, so the
// start of an overlapping run can be written first, a few milliseconds after
// the end that closes it.
func endOf(runID string, open []check.OpenRun, at int64) int64 {
	for _, r := range open {
		if r.ID == runID {
			return max(at, r.Started.UnixMilli())
		}
	}
	return at
}

func openRuns(ctx context.Context, q *db.Queries, checkID string) ([]check.OpenRun, error) {
	rows, err := q.OpenRuns(ctx, checkID)
	if err != nil {
		return nil, err
	}
	open := make([]check.OpenRun, len(rows))
	for i, r := range rows {
		open[i] = check.OpenRun{ID: r.ID, RID: r.Rid.String, Started: msTime(r.StartedAt), TimedOut: r.TimedOutAt.Valid}
	}
	return open, nil
}

// saveStatus writes a check's new status, and when its state changed, an
// event and the change to its incident.
func saveStatus(ctx context.Context, q *db.Queries, c db.Check, before, after check.Status) error {
	if after == before {
		return nil
	}
	err := q.UpdateCheckStatus(ctx, db.UpdateCheckStatusParams{
		State: string(after.State), StateSince: after.Since.UnixMilli(), StateReason: after.Reason,
		LastPingAt: nullMs(after.LastPing), NextExpectedAt: nullMs(after.NextExpected), Deadline: nullMs(after.Deadline),
		ID: c.ID,
	})
	if err != nil || after.State == before.State {
		return err
	}
	if err := incidentStep(ctx, q, c, after); err != nil {
		return err
	}
	return q.CreateEvent(ctx, db.CreateEventParams{
		WorkspaceID: c.WorkspaceID, ProjectID: sql.NullString{String: c.ProjectID, Valid: true},
		Type: "check.updated", SubjectID: c.ID, CreatedAt: now(),
	})
}

func msTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

func nullMs(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
