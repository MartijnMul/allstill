// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// What the log records (intent/activity-log/spec.md). An entry is written in the
// transaction that makes its thing true, so an action is not done unless it is
// recorded. Nothing in the table can refuse a row and building one cannot fail,
// so a ping fails over its entry only when the database itself is failing (R33).

// Actor is who or what did something. Every writer of an action takes one, so a
// new action does not compile without saying who did it (R15).
type Actor struct {
	Kind  string // person, key, probe, job, command or allstill
	ID    string
	Name  string
	Admin bool   // a workspace administrator, who may not be a member of the project
	Via   string // the sign-in a person acted through, from the command line
}

// Command is the administrator's command line, by the command's name. A shell has
// no person in the dashboard's sense (C6).
func Command(name string) Actor { return Actor{Kind: "command", Name: name} }

var (
	byJob      = Actor{Kind: "job"}
	byAllstill = Actor{Kind: "allstill"}
)

func byProbe(id, name string) Actor { return Actor{Kind: "probe", ID: id, Name: name} }

// personActor is a person as they are now. A person who cannot be found still
// acted, so it is no error.
func personActor(ctx context.Context, q *db.Queries, workspaceID, personID string) (Actor, error) {
	p, err := q.PersonForLog(ctx, db.PersonForLogParams{WorkspaceID: workspaceID, ID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Actor{}, err
	}
	return Actor{Kind: "person", ID: personID, Name: p.Name, Admin: p.WorkspaceAdmin == 1}, nil
}

// logEntry is one line of the log, before what follows from the rest is filled in.
type logEntry struct {
	WorkspaceID string
	At          time.Time // when it happened
	Recorded    time.Time // when Allstill learned of it, if that is later
	ProjectID   string
	ProjectName string // looked up when empty; given when the name is about to change
	Kind        string
	AboutKind   string
	AboutID     string
	AboutName   string
	By          Actor
	Person      Actor // the person it concerns
	IncidentID  string
	Hidden      bool
	Group       string
	Details     map[string]any
}

// writeLog writes an entry within the caller's transaction.
func writeLog(ctx context.Context, q *db.Queries, e logEntry) error {
	if e.Recorded.IsZero() {
		e.Recorded = e.At
	}
	if e.ProjectID != "" && e.ProjectName == "" {
		name, err := q.NameOfProject(ctx, e.ProjectID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		e.ProjectName = name
	}
	byOutsider, err := outsider(ctx, q, e.By, e.ProjectID)
	if err != nil {
		return err
	}
	personOutsider, err := notInProject(ctx, q, e.Person, e.ProjectID)
	if err != nil {
		return err
	}
	if personOutsider && !e.Person.Admin {
		// Not an administrator: someone told of a check that has moved here since,
		// whose name this project's people do not get to read either (R25).
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		e.Details["person_elsewhere"] = true
	}
	if e.By.Via != "" {
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		e.Details["via"] = e.By.Via
	}
	return q.CreateLogEntry(ctx, db.CreateLogEntryParams{
		WorkspaceID: e.WorkspaceID, HappenedAt: e.At.UnixMilli(), RecordedAt: e.Recorded.UnixMilli(),
		ProjectID: e.ProjectID, ProjectName: e.ProjectName, Kind: e.Kind,
		AboutKind: e.AboutKind, AboutID: e.AboutID, AboutName: e.AboutName,
		ByKind: e.By.Kind, ByID: e.By.ID, ByName: e.By.Name, ByOutsider: boolInt(byOutsider),
		PersonID: e.Person.ID, PersonName: e.Person.Name, PersonOutsider: boolInt(personOutsider),
		IncidentID: e.IncidentID, Hidden: boolInt(e.Hidden), GroupID: e.Group, Details: detailsOf(e.Details),
	})
}

// outsider says a person is an administrator with no membership of the project,
// whose name editors and owners do not get to read (R22).
func outsider(ctx context.Context, q *db.Queries, a Actor, projectID string) (bool, error) {
	if !a.Admin || projectID == "" {
		return false, nil
	}
	_, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: a.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// notInProject says the person an entry concerns has no membership of its project.
func notInProject(ctx context.Context, q *db.Queries, a Actor, projectID string) (bool, error) {
	if a.ID == "" || projectID == "" {
		return false, nil
	}
	_, err := q.RoleInProject(ctx, db.RoleInProjectParams{ProjectID: projectID, PersonID: a.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// detailsOf encodes an entry's details. Those that cannot be encoded are dropped
// rather than refusing the entry, and with it what it records (R33).
func detailsOf(d map[string]any) string {
	if len(d) == 0 {
		return "{}"
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// logCheck writes an action on a check. incidentID is the incident it was open
// with, or that it closed.
func logCheck(ctx context.Context, q *db.Queries, by Actor, c db.Check, kind, incidentID string, at time.Time, details map[string]any) error {
	return writeLog(ctx, q, logEntry{
		WorkspaceID: c.WorkspaceID, At: at, ProjectID: c.ProjectID, Kind: kind,
		AboutKind: "check", AboutID: c.ID, AboutName: c.Name, By: by, IncidentID: incidentID, Details: details,
	})
}

// openIncidentID is the id of a check's open incident, or "".
func openIncidentID(ctx context.Context, q *db.Queries, checkID string) (string, error) {
	open, err := q.OpenIncidentOfCheck(ctx, checkID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return open.ID, err
}

// cause says what changed a check's state, for its entry.
type cause struct {
	by Actor
	// action says the action that changed it writes one entry for both (R6).
	action bool
	group  string
}

func causedBy(by Actor) cause { return cause{by: by} }

// byTheAction is the cause of a state change whose action writes the entry.
var byTheAction = cause{action: true}

const (
	// flappingChanges is how many changes of state a check may log in an hour.
	flappingChanges = 30
	// flappingOver is how long a check must hold its state to log again (R16).
	flappingOver = time.Hour
)

// logStateChange writes that a check's state changed, past its ceiling not at all.
func logStateChange(ctx context.Context, q *db.Queries, c db.Check, before, after check.Status, when time.Time, why cause) error {
	if why.action {
		return nil
	}
	open, err := q.OpenIncidentOfCheck(ctx, c.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	incident := err == nil
	// A ping that resumes a paused check is an action, which is always written. A job
	// that pings a moment after its time goes late and back on every run: that says
	// little, must not bury anything, and must not use up what its real changes may.
	resumed := before.State == check.Paused
	late := briefLate(before.State, after.State)
	if !resumed && !late {
		flapping, err := startsFlapping(ctx, q, c, when)
		if err != nil || flapping == skipEntry {
			return err
		}
		if flapping == firstOverCeiling {
			return writeLog(ctx, q, logEntry{
				WorkspaceID: c.WorkspaceID, At: after.Since, Recorded: when, ProjectID: c.ProjectID, Kind: "check.flapping",
				AboutKind: "check", AboutID: c.ID, AboutName: c.Name, By: byAllstill,
			})
		}
	}
	details := map[string]any{"from": before.State, "to": after.State}
	if reason := logReason(after.Reason); reason != "" {
		details["reason"] = reason
	}
	kind := "check.state"
	switch {
	case resumed:
		kind = "check.resumed"
	case late:
		kind = "check.late"
	}
	e := logEntry{
		WorkspaceID: c.WorkspaceID, At: after.Since, Recorded: when, ProjectID: c.ProjectID, Kind: kind,
		AboutKind: "check", AboutID: c.ID, AboutName: c.Name, By: why.by,
		Hidden: late || quietChange(before.State, after.State, incident), Group: why.group, Details: details,
	}
	if incident {
		e.IncidentID = open.ID
	}
	return writeLog(ctx, q, e)
}

// quietChange says a change of state shows only when narrowed to its check: not
// being able to judge is not news about the check, so a restart, a silent probe or
// a lost network does not bury the log. A real outage or recovery always shows (R17).
func quietChange(from, to check.State, incident bool) bool {
	if to == check.Unknown {
		return true
	}
	return from == check.Unknown && (to == check.Up || to == check.New) && !incident
}

// briefLate says a change is a check going late, or back to up from it. Going on to
// down is not this, and neither is a recovery: a down check never goes late, so its
// recovery is never from late.
func briefLate(from, to check.State) bool {
	return to == check.Late || (from == check.Late && to == check.Up)
}

type flappingState int

const (
	writeEntry flappingState = iota
	firstOverCeiling
	skipEntry
)

// startsFlapping says what a change of state does to a check's ceiling (R16).
func startsFlapping(ctx context.Context, q *db.Queries, c db.Check, when time.Time) (flappingState, error) {
	last, err := q.LastFlappingEntry(ctx, c.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return writeEntry, err
	}
	if last == "check.flapping" {
		return skipEntry, nil
	}
	ago, err := q.StateEntryAgo(ctx, db.StateEntryAgoParams{AboutID: c.ID, Offset: flappingChanges - 1})
	if errors.Is(err, sql.ErrNoRows) {
		return writeEntry, nil
	}
	if err != nil {
		return writeEntry, err
	}
	if when.Sub(time.UnixMilli(ago)) < flappingOver {
		return firstOverCeiling, nil
	}
	return writeEntry, nil
}

// closeFlapping says of each check that was changing too often and has held its
// state for an hour what it settled on. Nothing fires when a check stays steady, so
// the regular tidying calls this.
func (s *Store) closeFlapping(ctx context.Context, now time.Time) error {
	rows, err := db.New(s.r).FlappingChecks(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		st, err := db.New(s.r).StateSinceOfCheck(ctx, r.AboutID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		removed := err != nil
		if !removed && !st.DeletedAt.Valid && now.Sub(time.UnixMilli(st.StateSince)) < flappingOver {
			// A job going late and back on every run has no state that lasts an hour, yet
			// has settled if it has only done that for the hour since its last incident.
			settled, err := onlyBrieflyLate(ctx, db.New(s.r), r.AboutID, st.Kind, st.State, now)
			if err != nil {
				return err
			}
			if !settled {
				continue
			}
		}
		e := logEntry{
			WorkspaceID: r.WorkspaceID, At: now, ProjectID: r.ProjectID, ProjectName: r.ProjectName, Kind: "check.steady",
			AboutKind: "check", AboutID: r.AboutID, AboutName: r.AboutName, By: byAllstill,
			Details: map[string]any{"since": r.RecordedAt},
		}
		if !removed {
			// It may have moved since: its state is for the project it is in now (R25).
			e.ProjectID, e.ProjectName, e.AboutName = st.ProjectID, "", st.Name
		}
		if !removed && !st.DeletedAt.Valid {
			e.Details["state"] = st.State
		}
		if err := writeLog(ctx, db.New(s.w), e); err != nil {
			return err
		}
	}
	return nil
}

// Who was reached (R3 to R5). A message is written when it arrives, not when it is
// queued, as until then it can still be taken back, trimmed or turned to shadow.

// logLine is what a message said about one check, or about nothing the log can name.
type logLine struct {
	what, checkID, check, projectID, incidentID string
}

// linesOf names the checks a message's items are about. The title is not copied, as
// it can hold a reason (R1); an item with neither incident nor check, as in a message
// queued before the log knew of them, has no check.
func linesOf(ctx context.Context, q *db.Queries, items []Item) ([]logLine, error) {
	var lines []logLine
	for _, it := range items {
		if it.IncidentID == "" {
			lines = append(lines, logLine{what: it.What, checkID: it.CheckID, check: it.Check, projectID: it.ProjectID})
			continue
		}
		rows, err := q.ChecksOfIncident(ctx, it.IncidentID)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			lines = append(lines, logLine{what: it.What, incidentID: it.IncidentID})
		}
		for _, r := range rows {
			lines = append(lines, logLine{what: it.What, checkID: r.CheckID, check: r.Name, projectID: r.ProjectID, incidentID: it.IncidentID})
		}
	}
	return lines, nil
}

// logMessage writes that a message reached a person, or was only recorded in shadow
// mode: a line per check, and for a project with several, one line that opens to them
// (R19). A test message is no part of the log.
func logMessage(ctx context.Context, q *db.Queries, workspaceID, personID, kind, messageKind, channel string, items []Item, at time.Time) error {
	if messageKind != "alert" && messageKind != "digest" {
		return nil
	}
	person, err := personActor(ctx, q, workspaceID, personID)
	if err != nil {
		return err
	}
	lines, err := linesOf(ctx, q, items)
	if err != nil {
		return err
	}
	how := "now"
	if messageKind == "digest" {
		how = "digest"
	}
	// What cannot be tied to a check is one line, for the person.
	withoutCheck := 0
	// Checks, not lines: one digest can say two things of one check, such as that its
	// incident was closed by pausing and that it is paused.
	checks, groups := map[string][]string{}, map[string]string{}
	var projects []string
	for _, l := range lines {
		if l.checkID == "" {
			withoutCheck++
			continue
		}
		if len(checks[l.projectID]) == 0 {
			projects = append(projects, l.projectID)
		}
		if !slices.Contains(checks[l.projectID], l.checkID) {
			checks[l.projectID] = append(checks[l.projectID], l.checkID)
		}
	}
	for _, p := range projects {
		if len(checks[p]) > 1 {
			groups[p] = id.New(id.LogGroup)
		}
	}
	base := func(what string) map[string]any {
		d := map[string]any{"how": how}
		if what != "" {
			d["what"] = what
		}
		if channel != "" {
			d["channel"] = channel
		}
		return d
	}
	for _, l := range lines {
		if l.checkID == "" {
			continue
		}
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: workspaceID, At: at, ProjectID: l.projectID, Kind: kind, AboutKind: "check", AboutID: l.checkID, AboutName: l.check,
			By: byAllstill, Person: person, IncidentID: l.incidentID, Hidden: groups[l.projectID] != "", Group: groups[l.projectID], Details: base(l.what),
		}); err != nil {
			return err
		}
	}
	for _, p := range projects {
		if groups[p] == "" {
			continue
		}
		d := base("")
		d["count"] = len(checks[p])
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: workspaceID, At: at, ProjectID: p, Kind: kind + "_several", AboutKind: "project", AboutID: p,
			By: byAllstill, Person: person, Group: groups[p], Details: d,
		}); err != nil {
			return err
		}
	}
	if withoutCheck > 0 {
		d := base("")
		d["count"] = withoutCheck
		return writeLog(ctx, q, logEntry{WorkspaceID: workspaceID, At: at, Kind: kind, AboutKind: "person", AboutID: personID, AboutName: person.Name, By: byAllstill, Person: person, Details: d})
	}
	return nil
}

// logUnreachedItems writes that a message reached a person by no way at all, for each
// incident it was about. A line with no incident is not chased: there is nothing that
// waits on it.
func logUnreachedItems(ctx context.Context, q *db.Queries, personID string, items []Item, why string, at time.Time) error {
	for _, it := range items {
		if it.IncidentID != "" {
			if err := logUnreached(ctx, q, it.IncidentID, personID, why, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// logUnreached writes once that a person cannot be reached about an incident, until
// a message does reach them. The sender tries again every ten minutes, which must not
// write six lines an hour (R5).
func logUnreached(ctx context.Context, q *db.Queries, incidentID, personID, why string, at time.Time) error {
	last, err := q.LastReachOfIncident(ctx, db.LastReachOfIncidentParams{IncidentID: incidentID, PersonID: personID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last == "message.unreached" {
		return nil
	}
	rows, err := q.ChecksOfIncident(ctx, incidentID)
	if err != nil {
		return err
	}
	for _, r := range rows {
		person, err := personActor(ctx, q, r.WorkspaceID, personID)
		if err != nil {
			return err
		}
		if err := writeLog(ctx, q, logEntry{
			WorkspaceID: r.WorkspaceID, At: at, ProjectID: r.ProjectID, Kind: "message.unreached", AboutKind: "check", AboutID: r.CheckID, AboutName: r.Name,
			By: byAllstill, Person: person, IncidentID: incidentID, Details: map[string]any{"why": logFailure(why)},
		}); err != nil {
			return err
		}
	}
	return nil
}

// logFailure is why a message failed as the log keeps it: an error from sending can
// hold a device's push address, and what the server said can hold anything.
func logFailure(reason string) string {
	switch {
	case reason == NoChannel:
		return "the person has no way to be reached"
	case strings.HasPrefix(reason, "the push service answered"):
		return "the push service refused it"
	case reason == "the device no longer subscribes; it was removed":
		return "the device no longer subscribes"
	case strings.HasPrefix(reason, "the mail server"):
		return "the mail server did not take it"
	case strings.HasPrefix(reason, "email is not set up"), reason == "push is not set up on this server":
		return "this server is not set up to send it"
	case reason == "the channel was removed", strings.HasPrefix(reason, "the channel changed hands"):
		return "the device or address changed"
	case strings.HasPrefix(reason, "the address was refused"):
		return "the address was refused"
	case strings.HasPrefix(reason, "the device subscribed for another key"):
		return "the device must subscribe again"
	}
	return "it could not be sent"
}

// onlyBrieflyLate says a check that jobs ping, up or late, has had no incident for as
// long as it takes to count as steady. An incident stays open while a check keeps
// failing, until it has held for a while, so this tells a job that is a little late from
// a flapping one. A check Allstill runs never goes late, and can change without an
// incident, between up and a warning, so it must hold its state.
func onlyBrieflyLate(ctx context.Context, q *db.Queries, checkID, kind, state string, now time.Time) (bool, error) {
	if Active(kind) || (state != string(check.Up) && state != string(check.Late)) {
		return false, nil
	}
	if _, err := q.OpenIncidentOfCheck(ctx, checkID); err == nil {
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	closed, err := q.LastIncidentClosedOfCheck(ctx, checkID)
	if err != nil {
		return false, err
	}
	return now.Sub(time.UnixMilli(closed)) >= flappingOver, nil
}

// A reason on a check's page can hold what a JSON field said and what was expected,
// and a probe's can hold anything. The log keeps for a long time, past a delete, so
// it keeps only Allstill's own wording: logReason returns only text from here (C4).
var (
	reasonsAsTheyAre = []string{
		"The job reported a failure.",
		"The ping is overdue.",
		"No ping since Allstill restarted.",
		"No attempt in time: the server or probe that runs it is behind or offline.",
		"Allstill was not running when an attempt was due.",
		"Allstill was not running when a ping was due.",
		check.ScheduleChanged,
		check.StillDown,
		"blocked by a bot challenge (Cloudflare)",
		"rate limited (HTTP 429)",
		"no Location in the redirect",
		"the redirect leads elsewhere than expected",
		"the answer is not JSON",
		"the address is not on the public internet, and this server may not reach it",
		"redirected to an address that is not http or https",
		"certificate expired",
		"certificate not valid yet",
		"certificate is for another host name",
		"certificate not trusted: its issuer is unknown",
		"certificate not valid",
		"the server does not speak TLS",
		"the TLS handshake failed",
		"the host name does not exist",
		"the host name could not be looked up",
		"connection refused",
		"connection reset",
		"no route to the address",
		"the network is unreachable",
		"the connection closed without an answer",
		"the request failed",
		"the connection failed",
		"the URL cannot be requested",
		"not judged: the server's network was down",
		"not judged: the probe's network was down",
		ClockOffReason,
	}
	// Those with a value in them, by how they start.
	reasonsByStart = []struct{ start, reason string }{
		{"The job failed with exit status ", "The job failed with an exit status."},
		{"A run started but did not end within", "A run started but did not end in time."},
		{"No ping within the grace period", "No ping within the grace period."},
		{"Allstill cannot read its schedule or settings", "Allstill cannot read its schedule or settings."},
		{"HTTP ", "The server answered with another HTTP status."},
		{"more than ", "too many redirects"},
		{"certificate expires in ", "the certificate is running out"},
		{"the answer is larger than ", "the answer is too large"},
		{"no answer within ", "no answer in time"},
		{"no ", "a JSON field was not as expected"},
	}
)

// logReason is a reason as the log keeps it, "" when it says nothing Allstill knows
// to be safe. What a JSON field said is written as its kind.
func logReason(reason string) string {
	if slices.Contains(reasonsAsTheyAre, reason) {
		return reason
	}
	if strings.Contains(reason, " instead of ") && !strings.HasPrefix(reason, "HTTP ") {
		return "a JSON field was not as expected"
	}
	for _, r := range reasonsByStart {
		if strings.HasPrefix(reason, r.start) {
			return r.reason
		}
	}
	return ""
}

// Fields whose values a change may show, as a check's page shows them to anyone who
// can see it. A field not listed is named without its value, so a new setting starts
// out hidden (R12).
var loggedValues = []string{"name", "slug", "tags", "every", "cron", "time_zone", "grace", "fail_after"}

// fieldChange is one changed field of a check.
type fieldChange struct {
	Field string `json:"field"`
	From  any    `json:"from,omitempty"`
	To    any    `json:"to,omitempty"`
}

// changesOf says what differs between a check and the same check changed.
func changesOf(c, u db.Check) []fieldChange {
	var out []fieldChange
	add := func(field string, from, to any) {
		if reflect.DeepEqual(from, to) {
			return
		}
		fc := fieldChange{Field: field}
		if slices.Contains(loggedValues, field) {
			fc.From, fc.To = from, to
		}
		out = append(out, fc)
	}
	add("name", c.Name, u.Name)
	add("slug", c.Slug.String, u.Slug.String)
	add("description", c.Description, u.Description)
	add("tags", decoded(c.Tags), decoded(u.Tags))
	add("every", c.IntervalSeconds.Int64, u.IntervalSeconds.Int64)
	add("cron", c.CronExpression.String, u.CronExpression.String)
	add("time_zone", c.TimeZone.String, u.TimeZone.String)
	add("grace", c.GraceSeconds, u.GraceSeconds)
	add("probe", c.ProbeID.String, u.ProbeID.String)
	if c.Settings != u.Settings {
		from, _ := decoded(c.Settings).(map[string]any)
		to, _ := decoded(u.Settings).(map[string]any)
		var keys []string
		for k := range from {
			keys = append(keys, k)
		}
		for k := range to {
			if _, ok := from[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			add(k, from[k], to[k])
		}
	}
	return out
}

func decoded(raw string) any {
	var v any
	json.Unmarshal([]byte(raw), &v)
	return v
}

// alertingChanges says what a change of alerting changes.
func alertingChanges(c db.Check, urgency string, critical bool) []fieldChange {
	var out []fieldChange
	if c.Urgency != urgency {
		out = append(out, fieldChange{Field: "urgency"})
	}
	if (c.Critical == 1) != critical {
		out = append(out, fieldChange{Field: "critical"})
	}
	return out
}
