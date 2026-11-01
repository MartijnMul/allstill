// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A probe runs checks inside a network the server cannot reach
// (intent/http-checks/spec.md, R6, R7, R13): it fetches them and reports what it
// finds. It serves the projects an administrator gives it, and only checks of
// those can run on it. Every post pings a check of its own, so a probe that goes
// quiet alerts once, as that check going down, while the checks it runs become
// unknown at their deadlines.

// The probe's own check goes late after a minute and down after three: the
// 3 minutes of R7.
const (
	probeCheckInterval = time.Minute
	probeCheckGrace    = 2 * time.Minute
)

// What a batch of a probe's runs may hold: a run older than maxProbeRunAge is
// not applied, and its id is forgotten after keepProbeRunsSeen, which is longer.
const (
	maxProbeRunAge    = 24 * time.Hour
	keepProbeRunsSeen = 2 * maxProbeRunAge
	maxClockOff       = time.Minute
)

// ClockOffReason is why a run of a probe with a wrong clock is not judged:
// certificates are verified and measured against its clock.
const ClockOffReason = "not judged: the probe's clock is more than a minute off the server's"

// Probe is a probe, as its key identifies it.
type Probe struct {
	ID, WorkspaceID, Name string
	CheckID, PingUUID     string // its own check
}

// CreateProbe makes a probe that serves project and the projects in serves,
// with its own check in project. It returns its id and its key, which is shown
// once. The caller must be a workspace administrator.
func (s *Store) CreateProbe(ctx context.Context, a Access, name, project string, serves []string) (string, string, error) {
	if !a.Admin {
		return "", "", ErrForbidden
	}
	if name == "" || len(name) > 100 {
		return "", "", &InvalidError{"name", "must be 1 to 100 characters"}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := q.ProbeByName(ctx, db.ProbeByNameParams{WorkspaceID: a.WorkspaceID, Name: name}); err == nil {
		return "", "", &InvalidError{"name", "a probe has this name already"}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	projects := append([]string{project}, serves...)
	for _, p := range projects {
		if _, err := q.ProjectInWorkspace(ctx, db.ProjectInWorkspaceParams{WorkspaceID: a.WorkspaceID, ID: p}); errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		} else if err != nil {
			return "", "", err
		}
	}
	own := NewCheck{
		Name: "Probe " + name, Description: "Pinged by the probe " + name + " at least every 30 seconds.",
		Interval: probeCheckInterval, Grace: probeCheckGrace,
	}
	if err := own.validate(); err != nil {
		return "", "", err
	}
	c, err := insertCheck(ctx, q, a.WorkspaceID, project, own)
	if err != nil {
		return "", "", err
	}
	key, keyHash := newProbeKey()
	probeID := id.New(id.Probe)
	err = q.CreateProbe(ctx, db.CreateProbeParams{
		ID: probeID, WorkspaceID: a.WorkspaceID, Name: name, KeyHash: keyHash, CheckID: c.ID, CreatedAt: now(),
	})
	if err != nil {
		return "", "", err
	}
	for _, p := range projects {
		if err := q.AddProbeProject(ctx, db.AddProbeProjectParams{WorkspaceID: a.WorkspaceID, ProbeID: probeID, ProjectID: p}); err != nil {
			return "", "", err
		}
	}
	return probeID, key, tx.Commit()
}

// RekeyProbe gives a probe a new key, which is shown once; the old one stops
// working. The caller must be a workspace administrator.
func (s *Store) RekeyProbe(ctx context.Context, a Access, name string) (string, error) {
	if !a.Admin {
		return "", ErrForbidden
	}
	key, keyHash := newProbeKey()
	n, err := db.New(s.w).UpdateProbeKey(ctx, db.UpdateProbeKeyParams{KeyHash: keyHash, WorkspaceID: a.WorkspaceID, Name: name})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

func newProbeKey() (key string, keyHash []byte) {
	b := make([]byte, 32)
	rand.Read(b)
	key = "alp_" + base64.RawURLEncoding.EncodeToString(b)
	return key, hash(key)
}

// ProbeByKey finds the probe a key belongs to, or returns ErrNotFound.
func (s *Store) ProbeByKey(ctx context.Context, key string) (Probe, error) {
	r, err := db.New(s.r).ProbeByKeyHash(ctx, hash(key))
	if errors.Is(err, sql.ErrNoRows) {
		return Probe{}, ErrNotFound
	}
	if err != nil {
		return Probe{}, err
	}
	return Probe{ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, CheckID: r.CheckID, PingUUID: r.PingUuid}, nil
}

// ProbeIDByName finds a probe by its name, or returns ErrNotFound.
func (s *Store) ProbeIDByName(ctx context.Context, workspaceID, name string) (string, error) {
	r, err := db.New(s.r).ProbeByName(ctx, db.ProbeByNameParams{WorkspaceID: workspaceID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return r.ID, err
}

// ProbeName is the name of a probe, for showing where a check runs.
func (s *Store) ProbeName(ctx context.Context, workspaceID, probeID string) (string, error) {
	return db.New(s.r).ProbeNameByID(ctx, db.ProbeNameByIDParams{WorkspaceID: workspaceID, ID: probeID})
}

// probeServes refuses a probe that does not serve a project, so nobody can have
// a probe fetch from its network for a project it is not there for (C6).
func probeServes(ctx context.Context, q *db.Queries, workspaceID, probeID, projectID string) error {
	n, err := q.ProbeServes(ctx, db.ProbeServesParams{WorkspaceID: workspaceID, ProbeID: probeID, ProjectID: projectID})
	if err != nil {
		return err
	}
	if n == 0 {
		return &InvalidError{"probe", "does not serve this check's project"}
	}
	return nil
}

// ProbeCheck is a check for a probe to run.
type ProbeCheck struct {
	ID, Kind string
	Interval time.Duration
	Settings []byte
	Revision string
}

// ChecksOfProbe returns the checks a probe runs: not paused ones.
func (s *Store) ChecksOfProbe(ctx context.Context, probeID string) ([]ProbeCheck, error) {
	rows, err := db.New(s.r).ChecksOfProbe(ctx, sql.NullString{String: probeID, Valid: true})
	if err != nil {
		return nil, err
	}
	checks := make([]ProbeCheck, len(rows))
	for i, r := range rows {
		checks[i] = ProbeCheck{
			ID: r.ID, Kind: r.Kind, Interval: time.Duration(r.IntervalSeconds.Int64) * time.Second, Settings: []byte(r.Settings), Revision: revisionOf(r.Settings),
		}
	}
	return checks, nil
}

// ProbeRun is one attempt a probe reports. It says how long ago it ended, not
// when, so the probe's clock does not matter.
type ProbeRun struct {
	ID       string
	CheckID  string
	Revision string
	Outcome  check.Outcome
	Reason   string
	EndedAgo time.Duration
	Values   map[string]float64
}

// ProbeRuns is what became of a probe's post.
type ProbeRuns struct {
	Applied, Ignored int
	// ClockOff is how far the probe's clock is ahead of the server's, or
	// behind when negative, when that is more than a minute; else zero.
	ClockOff time.Duration
	// Deadline is the earliest new deadline, for the scheduler.
	Deadline time.Time
}

// ApplyProbeRuns takes a probe's post at the server's time at, in one
// transaction: it pings the probe's own check, and applies the runs, oldest
// first, stamped with the server's clock. It ignores a run it has seen, one
// older than a day or than the last it applied for that check, and one for a
// check that is not this probe's. With its clock more than a minute off, the
// probe's runs are not judged, so its checks become unknown.
func (s *Store) ApplyProbeRuns(ctx context.Context, p Probe, runs []ProbeRun, sentAt, at time.Time) (ProbeRuns, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return ProbeRuns{}, err
	}
	defer tx.Rollback()
	q := db.New(tx)

	var res ProbeRuns
	if off := sentAt.Sub(at); off > maxClockOff || off < -maxClockOff {
		res.ClockOff = off.Round(time.Second)
	}
	earliest := func(t time.Time) {
		if !t.IsZero() && (res.Deadline.IsZero() || t.Before(res.Deadline)) {
			res.Deadline = t
		}
	}
	runs = slices.Clone(runs)
	slices.SortStableFunc(runs, func(a, b ProbeRun) int { return cmp.Compare(b.EndedAgo, a.EndedAgo) })
	for _, r := range runs {
		ok, err := applyProbeRun(ctx, q, p, r, res.ClockOff != 0, at, earliest)
		if err != nil {
			return ProbeRuns{}, err
		}
		if ok {
			res.Applied++
		} else {
			res.Ignored++
		}
	}
	own, err := s.applyPing(ctx, q, PingIn{CheckUUID: p.PingUUID, Kind: check.Success, ExitStatus: -1, Method: "POST", UserAgent: "Allstill probe", At: at})
	if err != nil {
		return ProbeRuns{}, err
	}
	earliest(own.Deadline)
	return res, tx.Commit()
}

func applyProbeRun(ctx context.Context, q *db.Queries, p Probe, r ProbeRun, clockOff bool, at time.Time, earliest func(time.Time)) (bool, error) {
	if r.EndedAgo < 0 || r.EndedAgo > maxProbeRunAge {
		return false, nil
	}
	seen, err := q.MarkProbeRunSeen(ctx, db.MarkProbeRunSeenParams{WorkspaceID: p.WorkspaceID, ProbeID: p.ID, RunID: r.ID, SeenAt: at.UnixMilli()})
	if err != nil || seen == 0 {
		return false, err
	}
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: p.WorkspaceID, ID: r.CheckID})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ended := at.Add(-r.EndedAgo)
	if c.ProbeID.String != p.ID || !c.ProbeID.Valid || ended.Before(statusOf(c).LastPing) {
		return false, nil
	}
	if r.Revision != "" && r.Revision != revisionOf(c.Settings) {
		// Made under other settings, it says nothing of the check as it is now.
		return false, nil
	}
	a := Attempt{
		Attempt:  check.Attempt{Outcome: r.Outcome, Reason: r.Reason, At: ended},
		Started:  ended.Add(-time.Duration(r.Values["ms"]) * time.Millisecond),
		Measured: r.Values,
		ProbeID:  p.ID,
		Arrived:  at,
	}
	if clockOff {
		a.Outcome, a.Reason = check.Unjudged, ClockOffReason
	}
	after, err := recordAttempt(ctx, q, p.WorkspaceID, r.CheckID, a)
	if err != nil {
		return false, err
	}
	earliest(after.Deadline)
	return true, nil
}

// ProbeETag is what a probe's checks hash to, so it can ask whether they changed.
func ProbeETag(checks []ProbeCheck) string {
	raw, _ := json.Marshal(checks)
	return `"` + base64.RawURLEncoding.EncodeToString(hash(string(raw))[:16]) + `"`
}
