// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// CheckView is a check as people see it.
type CheckView struct {
	ID          string
	ProjectID   string
	ProjectName string
	Name        string
	Slug        string
	Description string
	Tags        []string
	PingUUID    string
	ProbeID     string // the probe that runs it, if any
	// Kind is ping, or http or tcp for a check Allstill runs itself, whose
	// Settings say what it fetches. They hold its URL, a secret (R12).
	Kind     string
	Settings []byte
	Interval time.Duration // zero for a cron schedule
	Cron     string
	TimeZone string
	Grace    time.Duration
	Status   check.Status
	Created  time.Time
	// Recent holds the latest runs, newest first: up to RecentRuns of them.
	Recent []RunView
	// Downs holds when the check went down, newest first: when it joined an
	// incident, back to the oldest run in Recent when that holds RecentRuns.
	// A check that goes down again while its incident is still open joins no
	// new one, so that time is not here.
	Downs []Down
}

// Down is a time a check went down, and when its incident resolved: zero
// while it is open.
type Down struct {
	At, Resolved time.Time
}

// RecentRuns is how many of a check's latest runs a CheckView holds.
const RecentRuns = 30

// RunView is one run of a job.
type RunView struct {
	ID         string
	Outcome    string // running, success, warning, fail or timeout
	Started    time.Time
	Ended      time.Time
	TimedOut   time.Time
	ExitStatus int // -1 when not reported
	Values     map[string]float64
	Reason     string // why an attempt of an active check failed or warned
}

// At is when the run started, or when it ended for a run whose start is not
// known.
func (r RunView) At() time.Time {
	if r.Started.IsZero() {
		return r.Ended
	}
	return r.Started
}

// Duration is how long the run took, or zero when its start or end is not
// known.
func (r RunView) Duration() time.Duration {
	if r.Started.IsZero() || r.Ended.IsZero() {
		return 0
	}
	return r.Ended.Sub(r.Started)
}

// PingView is one ping of a run, with its body.
type PingView struct {
	Kind          string
	ExitStatus    int // -1 when not reported
	Received      time.Time
	Body          []byte
	BodyTruncated bool
}

// Checks returns every check the caller may see, by project and then by name.
func (s *Store) Checks(ctx context.Context, a Access) ([]CheckView, error) {
	q := db.New(s.r)
	var views []CheckView
	for _, p := range a.Projects {
		rows, err := q.ChecksOfProject(ctx, db.ChecksOfProjectParams{WorkspaceID: a.WorkspaceID, ProjectID: p.ID})
		if err != nil {
			return nil, err
		}
		for _, c := range rows {
			v, err := viewOf(ctx, q, c, p.Name)
			if err != nil {
				return nil, err
			}
			views = append(views, v)
		}
	}
	return views, nil
}

// Check returns one check, or ErrNotFound when the caller may not see it.
func (s *Store) Check(ctx context.Context, a Access, checkID string) (CheckView, error) {
	q := db.New(s.r)
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: a.WorkspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return CheckView{}, ErrNotFound
	}
	if err != nil {
		return CheckView{}, err
	}
	for _, p := range a.Projects {
		if p.ID == c.ProjectID {
			return viewOf(ctx, q, c, p.Name)
		}
	}
	return CheckView{}, ErrNotFound
}

// RunLog returns the pings of one of a check's runs, in order, or ErrNotFound
// when the caller may not see the check or the run is not its.
func (s *Store) RunLog(ctx context.Context, a Access, checkID, runID string) ([]PingView, error) {
	if _, err := s.Check(ctx, a, checkID); err != nil {
		return nil, err
	}
	q := db.New(s.r)
	run, err := q.RunOfCheck(ctx, db.RunOfCheckParams{CheckID: checkID, ID: runID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.PingsOfRun(ctx, sql.NullString{String: run, Valid: true})
	if err != nil {
		return nil, err
	}
	pings := make([]PingView, len(rows))
	for i, p := range rows {
		pings[i] = PingView{
			Kind: p.Kind, ExitStatus: exitOf(p.ExitStatus), Received: time.UnixMilli(p.ReceivedAt).UTC(),
			Body: p.Body, BodyTruncated: p.BodyTruncated == 1,
		}
	}
	return pings, nil
}

func viewOf(ctx context.Context, q *db.Queries, c db.Check, projectName string) (CheckView, error) {
	v := CheckView{
		ID: c.ID, ProjectID: c.ProjectID, ProjectName: projectName, Name: c.Name, Slug: c.Slug.String,
		Description: c.Description, PingUUID: c.PingUuid, ProbeID: c.ProbeID.String, Kind: c.Kind, Settings: []byte(c.Settings),
		Interval: time.Duration(c.IntervalSeconds.Int64) * time.Second,
		Cron:     c.CronExpression.String, TimeZone: c.TimeZone.String,
		Grace:  time.Duration(c.GraceSeconds) * time.Second,
		Status: statusOf(c), Created: time.UnixMilli(c.CreatedAt).UTC(),
	}
	if err := json.Unmarshal([]byte(c.Tags), &v.Tags); err != nil {
		return v, err
	}
	// The newest id sorts after every real one.
	runs, err := q.RunsOfCheck(ctx, db.RunsOfCheckParams{CheckID: c.ID, ID: "~", Limit: RecentRuns})
	if err != nil {
		return v, err
	}
	for _, r := range runs {
		rv := RunView{
			ID: r.ID, Outcome: r.Outcome, Started: msTime(r.StartedAt), Ended: msTime(r.EndedAt),
			TimedOut: msTime(r.TimedOutAt), ExitStatus: exitOf(r.ExitStatus), Reason: r.Reason,
		}
		if r.Measured != "{}" {
			if err := json.Unmarshal([]byte(r.Measured), &rv.Values); err != nil {
				return v, err
			}
		}
		v.Recent = append(v.Recent, rv)
	}
	from := int64(0)
	if len(v.Recent) == RecentRuns {
		from = v.Recent[len(v.Recent)-1].At().UnixMilli()
	}
	downs, err := q.DownsOfCheck(ctx, db.DownsOfCheckParams{CheckID: c.ID, JoinedAt: from, Limit: RecentRuns})
	if err != nil {
		return v, err
	}
	for _, d := range downs {
		v.Downs = append(v.Downs, Down{At: time.UnixMilli(d.JoinedAt).UTC(), Resolved: msTime(d.ResolvedAt)})
	}
	return v, nil
}

func exitOf(v sql.NullInt64) int {
	if !v.Valid {
		return -1
	}
	return int(v.Int64)
}
