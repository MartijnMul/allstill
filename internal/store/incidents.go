// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// incidentStep keeps a check's incident in step with its state, in the
// transaction that saves the state (intent/alerts/spec.md):
//   - down opens an incident, or keeps the open one from recovering;
//   - up starts the settle period of its open incident;
//   - paused resolves it at once, without a recovery message.
//
// Other states change nothing. The sender is told to look at every recipient
// whose message may have changed.
func incidentStep(ctx context.Context, q *db.Queries, c db.Check, after check.Status) error {
	at := after.Since.UnixMilli()
	open, err := q.OpenIncidentOfCheck(ctx, c.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hasOpen := err == nil
	switch after.State {
	case check.Down:
		if hasOpen {
			if open.RecoveringSince.Valid {
				return q.SetRecovering(ctx, db.SetRecoveringParams{ID: open.ID})
			}
			return nil
		}
		incidentID := id.New(id.Incident)
		err := q.CreateIncident(ctx, db.CreateIncidentParams{
			ID: incidentID, WorkspaceID: c.WorkspaceID, Title: c.Name + " is down",
			Urgency: c.Urgency, Critical: c.Critical, CauseCheckID: sql.NullString{String: c.ID, Valid: true}, OpenedAt: at,
		})
		if err != nil {
			return err
		}
		err = q.AddIncidentCheck(ctx, db.AddIncidentCheckParams{
			WorkspaceID: c.WorkspaceID, IncidentID: incidentID, CheckID: c.ID, CheckName: c.Name, ProjectID: c.ProjectID, JoinedAt: at,
		})
		if err != nil {
			return err
		}
		// Followers of a `none` check are recorded too: its urgency may change
		// while the incident is open.
		return q.AddFollowers(ctx, db.AddFollowersParams{IncidentID: incidentID, DueAt: sql.NullInt64{Int64: at, Valid: true}, ProjectID: c.ProjectID, WorkspaceID: c.WorkspaceID})
	case check.Up:
		if hasOpen && !open.RecoveringSince.Valid {
			return q.SetRecovering(ctx, db.SetRecoveringParams{RecoveringSince: sql.NullInt64{Int64: at, Valid: true}, ID: open.ID})
		}
	case check.Paused:
		if hasOpen {
			return resolve(ctx, q, open.ID, "paused", at)
		}
	}
	return nil
}

// resolve closes an incident, and has the sender look at everyone who was
// told it was down.
func resolve(ctx context.Context, q *db.Queries, incidentID, reason string, at int64) error {
	err := q.ResolveIncident(ctx, db.ResolveIncidentParams{
		ResolvedAt: sql.NullInt64{Int64: at, Valid: true}, ResolveReason: sql.NullString{String: reason, Valid: true}, ID: incidentID,
	})
	if err != nil {
		return err
	}
	if err := q.CloseIncidentChecks(ctx, db.CloseIncidentChecksParams{LeftAt: sql.NullInt64{Int64: at, Valid: true}, IncidentID: incidentID}); err != nil {
		return err
	}
	return q.LookAgainAtTold(ctx, db.LookAgainAtToldParams{DueAt: sql.NullInt64{Int64: at, Valid: true}, IncidentID: incidentID})
}

// SettleIncidents resolves the incidents whose checks have been up for the
// settle period, a batch at a time, and returns how many it resolved. An
// incident resolves when its settle period ended, also when Allstill looks
// later, after it was down: so the time it came back up is kept, and the
// incident still falls after the last pass that saw it open.
func (s *Store) SettleIncidents(ctx context.Context, now time.Time, settle time.Duration) (int, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	rows, err := q.SettledIncidents(ctx, db.SettledIncidentsParams{
		RecoveringSince: sql.NullInt64{Int64: now.Add(-settle).UnixMilli(), Valid: true}, Limit: 100,
	})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := resolve(ctx, q, r.ID, "recovered", r.RecoveringSince.Int64+settle.Milliseconds()); err != nil {
			return 0, err
		}
	}
	return len(rows), tx.Commit()
}

// Due is one recipient the sender must look at: the incident and the
// person's settings as they are now, and what the person was told.
type Due struct {
	WorkspaceID      string
	IncidentID       string
	PersonID         string
	Told             string
	ToldHow          string
	ToldInShadow     bool
	DueAt            time.Time
	Open             bool
	Title            string
	Urgency          string
	Critical         bool
	Reason           string
	TimeZone         string
	QuietHours       []byte
	Follows          bool
	RecoveryMessages bool
}

// DueRecipients returns up to limit recipients due at now, grouped by person.
func (s *Store) DueRecipients(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	rows, err := db.New(s.r).DueRecipients(ctx, db.DueRecipientsParams{DueAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	due := make([]Due, len(rows))
	for i, r := range rows {
		due[i] = Due{
			WorkspaceID: r.WorkspaceID, IncidentID: r.IncidentID, PersonID: r.PersonID, Told: r.Told, ToldHow: r.ToldHow.String, ToldInShadow: r.ToldShadow == 1,
			DueAt: msTime(r.DueAt), Open: r.State != "resolved", Title: r.Title, Urgency: r.Urgency, Critical: r.Critical == 1,
			Reason: r.ResolveReason.String, TimeZone: r.TimeZone, QuietHours: []byte(r.QuietHours.String),
			Follows: r.Follows == 1 && r.Disabled == 0, RecoveryMessages: r.RecoveryMessages == 1,
		}
	}
	return due, nil
}

// Item is one line of a message: what it says about which incident.
type Item struct {
	IncidentID string `json:"incident_id,omitempty"`
	What       string `json:"what"` // down, recovered, still_open, resolved_unseen or silent_check
	Title      string `json:"title"`
}

// Delivery is one message to one person, as it is recorded: in shadow mode
// only recorded, otherwise sent or failed.
type Delivery struct {
	WorkspaceID string
	PersonID    string
	ChannelID   string
	Kind        string // alert, digest or test
	Items       []Item
	State       string // shadow, pending, sent or failed
	Error       string
	At          time.Time
}

// Told is what a recipient has now been told, or when to look again.
type Told struct {
	IncidentID string
	PersonID   string
	Told       string // empty when only the next look changes
	How        string
	Shadow     bool
	At         time.Time
	Again      time.Time // zero: nothing left to do
}

// Record writes a delivery, if there is one, and what its recipients were
// told, in one transaction.
func (s *Store) Record(ctx context.Context, d *Delivery, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := record(ctx, db.New(tx), d, told); err != nil {
		return err
	}
	return tx.Commit()
}

func record(ctx context.Context, q *db.Queries, d *Delivery, told []Told) error {
	if d != nil {
		if err := createDelivery(ctx, q, *d); err != nil {
			return err
		}
	}
	for _, t := range told {
		var err error
		if again := nullMs(t.Again); t.Told == "" {
			err = q.SetDue(ctx, db.SetDueParams{DueAt: again, IncidentID: t.IncidentID, PersonID: t.PersonID})
		} else {
			err = q.SetTold(ctx, db.SetToldParams{
				Told: t.Told, ToldAt: nullMs(t.At), ToldHow: nullString(t.How), ToldShadow: boolInt(t.Shadow),
				DueAt: again, IncidentID: t.IncidentID, PersonID: t.PersonID,
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DigestPerson is someone who may get a digest, and when the sender last
// looked whether one was due.
type DigestPerson struct {
	ID          string
	WorkspaceID string
	TimeZone    string
	DigestTime  string
	LookedAt    time.Time // the creation of the person until the first look
}

// DigestPeople returns everyone who may get a digest.
func (s *Store) DigestPeople(ctx context.Context) ([]DigestPerson, error) {
	rows, err := db.New(s.r).DigestPeople(ctx)
	if err != nil {
		return nil, err
	}
	people := make([]DigestPerson, len(rows))
	for i, r := range rows {
		looked := msTime(r.DigestAt)
		if looked.IsZero() {
			looked = time.UnixMilli(r.CreatedAt).UTC()
		}
		people[i] = DigestPerson{ID: r.ID, WorkspaceID: r.WorkspaceID, TimeZone: r.TimeZone, DigestTime: r.DigestTime, LookedAt: looked}
	}
	return people, nil
}

// DigestEntry is an incident in a person's digest: still open, or resolved
// since the last digest.
type DigestEntry struct {
	IncidentID       string
	Title            string
	Open             bool
	Urgency          string
	Reason           string
	Told             string
	ToldHow          string
	ToldInShadow     bool
	Follows          bool
	RecoveryMessages bool
}

// SilentCheck is a check that can never alert: never pinged, or with a
// schedule Allstill cannot read.
type SilentCheck struct {
	Name    string
	Project string
	State   string
}

// DigestOf returns what may go in a person's digest since a time.
func (s *Store) DigestOf(ctx context.Context, personID string, since time.Time) ([]DigestEntry, []SilentCheck, error) {
	q := db.New(s.r)
	rows, err := q.DigestIncidents(ctx, db.DigestIncidentsParams{PersonID: personID, Since: sql.NullInt64{Int64: since.UnixMilli(), Valid: true}})
	if err != nil {
		return nil, nil, err
	}
	entries := make([]DigestEntry, len(rows))
	for i, r := range rows {
		entries[i] = DigestEntry{
			IncidentID: r.IncidentID, Title: r.Title, Open: r.State != "resolved", Urgency: r.Urgency, Reason: r.ResolveReason.String,
			Told: r.Told, ToldHow: r.ToldHow.String, ToldInShadow: r.ToldShadow == 1, Follows: r.Follows == 1, RecoveryMessages: r.RecoveryMessages == 1,
		}
	}
	silent, err := q.SilentChecks(ctx, personID)
	if err != nil {
		return nil, nil, err
	}
	checks := make([]SilentCheck, len(silent))
	for i, c := range silent {
		checks[i] = SilentCheck{Name: c.Name, Project: c.ProjectName, State: c.State}
	}
	return entries, checks, nil
}

// RecordDigest writes a digest, if there is one, what it told, and that the
// sender looked at the person's digest at at.
func (s *Store) RecordDigest(ctx context.Context, personID string, at time.Time, d *Delivery, told []Told) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := record(ctx, q, d, told); err != nil {
		return err
	}
	if err := q.SetDigestAt(ctx, db.SetDigestAtParams{DigestAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, ID: personID}); err != nil {
		return err
	}
	return tx.Commit()
}

func createDelivery(ctx context.Context, q *db.Queries, d Delivery) error {
	items, err := json.Marshal(d.Items)
	if err != nil {
		return err
	}
	var sent sql.NullInt64
	if d.State == "sent" {
		sent = sql.NullInt64{Int64: d.At.UnixMilli(), Valid: true}
	}
	return q.CreateDelivery(ctx, db.CreateDeliveryParams{
		ID: id.New(id.Delivery), WorkspaceID: d.WorkspaceID, PersonID: d.PersonID, ChannelID: nullString(d.ChannelID),
		Kind: d.Kind, Items: string(items), State: d.State, LastError: nullString(d.Error), CreatedAt: d.At.UnixMilli(), SentAt: sent,
	})
}

// LastMessageAt is when the person was last sent an alert or a digest, or
// would have been.
func (s *Store) LastMessageAt(ctx context.Context, personID string) (time.Time, error) {
	ms, err := db.New(s.r).LastMessageAt(ctx, personID)
	if err != nil || ms == 0 {
		return time.Time{}, err
	}
	return time.UnixMilli(ms).UTC(), nil
}

// ShadowMode says whether messages are only recorded, not sent. It is the
// default until an administrator switches alerts on.
func (s *Store) ShadowMode(ctx context.Context) (bool, error) {
	v, err := s.Setting(ctx, "alerts")
	return v != "on", err
}

// Message is a delivery as a person sees it in their list of messages.
type Message struct {
	ID    string
	Kind  string
	State string
	Items []Item
	Error string
	At    time.Time
}

// Messages returns the newest messages to a person, sent or only recorded.
func (s *Store) Messages(ctx context.Context, personID string, limit int) ([]Message, error) {
	rows, err := db.New(s.r).DeliveriesOfPerson(ctx, db.DeliveriesOfPersonParams{PersonID: personID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, len(rows))
	for i, r := range rows {
		msgs[i] = Message{ID: r.ID, Kind: r.Kind, State: r.State, Error: r.LastError.String, At: time.UnixMilli(r.CreatedAt).UTC()}
		if err := json.Unmarshal([]byte(r.Items), &msgs[i].Items); err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

// NobodyFollows returns the projects where nobody gets alerts.
func (s *Store) NobodyFollows(ctx context.Context, workspaceID string) ([]ProjectAccess, error) {
	rows, err := db.New(s.r).ProjectsNobodyFollows(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectAccess, len(rows))
	for i, r := range rows {
		out[i] = ProjectAccess{ID: r.ID, Name: r.Name}
	}
	return out, nil
}

// IncidentView is a check's open incident, as the dashboard shows it.
type IncidentView struct {
	ID              string
	Title           string
	OpenedAt        time.Time
	RecoveringSince time.Time // zero unless its checks are up and settling
}

// OpenIncident returns the open incident of a check, or nil when there is
// none. The check is one read through the caller's access.
func (s *Store) OpenIncident(ctx context.Context, c CheckView) (*IncidentView, error) {
	i, err := db.New(s.r).OpenIncidentOfCheck(ctx, c.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &IncidentView{ID: i.ID, Title: i.Title, OpenedAt: time.UnixMilli(i.OpenedAt).UTC(), RecoveringSince: msTime(i.RecoveringSince)}, nil
}

// CheckIncident is one incident a check was in: when the check went down,
// since when it has been up while the incident settles, and when and why the
// incident resolved. The times are zero when they have not happened.
type CheckIncident struct {
	Down            time.Time
	RecoveringSince time.Time
	Resolved        time.Time
	Reason          string
}

// IncidentsOfCheck lists the incidents a check was in that were open at some
// time since then, oldest first.
func (s *Store) IncidentsOfCheck(ctx context.Context, checkID string, since time.Time) ([]CheckIncident, error) {
	rows, err := db.New(s.r).IncidentsOfCheck(ctx, db.IncidentsOfCheckParams{
		CheckID: checkID, ResolvedAt: sql.NullInt64{Int64: since.UnixMilli(), Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]CheckIncident, len(rows))
	for i, r := range rows {
		out[i] = CheckIncident{
			Down: time.UnixMilli(r.JoinedAt).UTC(), RecoveringSince: msTime(r.RecoveringSince),
			Resolved: msTime(r.ResolvedAt), Reason: r.ResolveReason.String,
		}
	}
	return out, nil
}

// SetAlerting sets how fast a problem with a check reaches people: its
// urgency (now, digest or none), and whether it breaks through quiet hours.
// It is for the administrator's command line: it checks no caller's access.
func (s *Store) SetAlerting(ctx context.Context, workspaceID, checkID, urgency string, critical bool) error {
	if urgency != "now" && urgency != "digest" && urgency != "none" {
		return &InvalidError{"urgency", "is now, digest or none"}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	at := now()
	n, err := q.SetAlerting(ctx, db.SetAlertingParams{
		Urgency: urgency, Critical: boolInt(critical), UpdatedAt: at, WorkspaceID: workspaceID, ID: checkID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := q.LookAgainAtUntold(ctx, checkID); err != nil {
		return err
	}
	return tx.Commit()
}
