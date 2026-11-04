// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"cmp"
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
//   - up, or warning, starts the settle period of its open incident;
//   - unknown stops it for a check Allstill runs itself, which may still be
//     down (intent/http-checks/spec.md);
//   - paused resolves it at once, without a recovery message.
//
// Other states change nothing. The sender is told to look at every recipient
// whose message may have changed.
//
// It dates the incident at the time given: when Allstill learned of the change,
// which for a probe's run can be long after the run ended. Dated then, an outage
// it hears of late is still told, and settles from now. When the check went down
// stays its own history.
func incidentStep(ctx context.Context, q *db.Queries, c db.Check, after check.Status, when time.Time) error {
	at := when.UnixMilli()
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
			WorkspaceID: c.WorkspaceID, IncidentID: incidentID, CheckID: c.ID, CheckName: c.Name, ProjectID: c.ProjectID, JoinedAt: after.Since.UnixMilli(),
		})
		if err != nil {
			return err
		}
		// Followers of a `none` check are recorded too: its urgency may change
		// while the incident is open.
		return q.AddFollowers(ctx, db.AddFollowersParams{IncidentID: incidentID, DueAt: sql.NullInt64{Int64: at, Valid: true}, ProjectID: c.ProjectID, WorkspaceID: c.WorkspaceID})
	case check.Up, check.Warning:
		if hasOpen && !open.RecoveringSince.Valid {
			return q.SetRecovering(ctx, db.SetRecoveringParams{RecoveringSince: sql.NullInt64{Int64: at, Valid: true}, ID: open.ID})
		}
	case check.Unknown:
		// An active check that cannot be judged may still be down: its
		// incident stays open, and settles only after it is seen up again
		// (intent/http-checks/spec.md).
		if hasOpen && Active(c.Kind) && open.RecoveringSince.Valid {
			return q.SetRecovering(ctx, db.SetRecoveringParams{ID: open.ID})
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
	OverWhenHeard    bool // its check came back before Allstill heard it went down
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
			OverWhenHeard: r.OverWhenHeard == 1,
		}
	}
	return due, nil
}

// Item is one line of a message: what it says about which incident.
type Item struct {
	IncidentID string `json:"incident_id,omitempty"`
	What       string `json:"what"` // down, recovered, still_open, resolved_unseen, closed_deleted, closed_paused, silent_check, paused_check or warning
	Title      string `json:"title"`
	// A line about a check with no incident says which, for the log, as its title
	// is free text that can hold a reason.
	CheckID   string `json:"check_id,omitempty"`
	Check     string `json:"check,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
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
	// Unreached says the person has no way to be told about the incident now, for
	// the log, with At.
	Unreached bool
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
		switch d.State {
		case "shadow":
			if err := logMessage(ctx, q, d.WorkspaceID, d.PersonID, "message.shadow", d.Kind, "", d.Items, d.At); err != nil {
				return err
			}
		case "failed":
			if err := logUnreachedItems(ctx, q, d.PersonID, d.Items, d.Error, d.At); err != nil {
				return err
			}
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
		if t.Unreached {
			if err := logUnreached(ctx, q, t.IncidentID, t.PersonID, NoChannel, t.At); err != nil {
				return err
			}
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

// ListedCheck is a check the digest lists for itself: one that cannot alert
// (never pinged or never worked, or with a schedule Allstill cannot read), in state
// new or unknown; one that is paused; or one in warning, with its reason
// (intent/http-checks/spec.md, R9).
type ListedCheck struct {
	ID        string
	ProjectID string
	Name      string
	Project   string
	State     string
	Reason    string
}

// DigestOf returns what may go in a person's digest since a time.
func (s *Store) DigestOf(ctx context.Context, personID string, since time.Time) ([]DigestEntry, []ListedCheck, error) {
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
	var checks []ListedCheck
	for _, c := range silent {
		checks = append(checks, ListedCheck{ID: c.ID, ProjectID: c.ProjectID, Name: c.Name, Project: c.ProjectName, State: c.State, Reason: c.StateReason})
	}
	warning, err := q.WarningChecks(ctx, personID)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range warning {
		checks = append(checks, ListedCheck{ID: c.ID, ProjectID: c.ProjectID, Name: c.Name, Project: c.ProjectName, State: string(check.Warning), Reason: c.StateReason})
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
	delivery := id.New(id.Delivery)
	return q.CreateDelivery(ctx, db.CreateDeliveryParams{
		ID: delivery, MessageID: nullString(delivery), WorkspaceID: d.WorkspaceID, PersonID: d.PersonID, ChannelID: nullString(d.ChannelID),
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

// SetAlerts switches alerts on, or back to shadow mode. Switching them on
// marks every open incident as from shadow mode, in the same transaction:
// nobody is alerted for it, and the digest lists it (R10). It returns how
// many open incidents now stay quiet.
func (s *Store) SetAlerts(ctx context.Context, by Actor, on bool) (quiet int, err error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	was, err := q.InstallationValue(ctx, "alerts")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	value := "shadow"
	if on && was != "on" {
		value = "on"
		if _, err := q.MarkOpenIncidentsShadow(ctx); err != nil {
			return 0, err
		}
	} else if on {
		value = "on" // on already: nothing began in shadow mode since
	}
	if err := q.SetInstallationValue(ctx, db.SetInstallationValueParams{Key: "alerts", Value: value}); err != nil {
		return 0, err
	}
	if from := cmp.Or(was, "shadow"); from != value {
		ws, err := q.Workspace(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: ws.ID, At: time.Now(), Kind: "alerts.mode", AboutKind: "installation", By: by,
			Details: map[string]any{"from": from, "to": value},
		}); err != nil {
			return 0, err
		}
	}
	if on {
		n, err := q.QuietIncidents(ctx)
		if err != nil {
			return 0, err
		}
		quiet = int(n)
	}
	return quiet, tx.Commit()
}

// Unreachable returns the names of people who get alerts but have no
// confirmed device made for key, nor a confirmed address the server can
// mail (mail): alerts cannot reach them.
func (s *Store) Unreachable(ctx context.Context, key string, mail bool) ([]string, error) {
	people, err := db.New(s.r).PeopleWhoGetAlerts(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range people {
		push, email, err := s.Targets(ctx, p.ID, key)
		if err != nil {
			return nil, err
		}
		if len(push) == 0 && (len(email) == 0 || !mail) {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

// NoChannel is why a message failed when the person has no confirmed device
// or address.
const NoChannel = "no confirmed device or email address to reach this person"

// FailedLately says whether a message to the person failed for reason since
// then, so the same failure is recorded once, not at every retry.
func (s *Store) FailedLately(ctx context.Context, personID, reason string, since time.Time) (bool, error) {
	n, err := db.New(s.r).RecentFailures(ctx, db.RecentFailuresParams{PersonID: personID, LastError: nullString(reason), CreatedAt: since.UnixMilli()})
	return n > 0, err
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
func (s *Store) SetAlerting(ctx context.Context, by Actor, workspaceID, checkID, urgency string, critical bool, at time.Time) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	c, err := q.CheckByID(ctx, db.CheckByIDParams{WorkspaceID: workspaceID, ID: checkID})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := setAlerting(ctx, q, workspaceID, checkID, urgency, critical, at); err != nil {
		return err
	}
	if err := logAlerting(ctx, q, by, c, urgency, critical, at); err != nil {
		return err
	}
	return tx.Commit()
}

func setAlerting(ctx context.Context, q *db.Queries, workspaceID, checkID, urgency string, critical bool, at time.Time) error {
	if urgency != "now" && urgency != "digest" && urgency != "none" {
		return &InvalidError{"urgency", "must be now, digest or none"}
	}
	ms := at.UnixMilli()
	n, err := q.SetAlerting(ctx, db.SetAlertingParams{
		Urgency: urgency, Critical: boolInt(critical), UpdatedAt: ms, WorkspaceID: workspaceID, ID: checkID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return q.LookAgainAtUntold(ctx, db.LookAgainAtUntoldParams{DueAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, CheckID: checkID})
}
