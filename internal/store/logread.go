// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// Reading the log (intent/activity-log/spec.md, R20 to R25). What a reader may see
// is judged here, on each entry by itself, so narrowing can never reach past it.

// LogFilter narrows the log. What is "", happened, done, reached or signins.
type LogFilter struct {
	CheckID   string
	ProjectID string
	PersonID  string
	Name      string
	What      string
	Group     string
	// Late also shows a check going late and back, which a job that pings a moment
	// after its time does on every run.
	Late   bool
	Before int64 // only entries older than this number
	Limit  int
}

// LogParty is who acted or whom an entry concerns, as far as the reader may know.
// Kind is person, key, probe, job, command or allstill; or, for someone the reader
// may not know by name, team, administrator or outside. Only a person has an ID.
type LogParty struct {
	Kind, ID, Name string
}

// LogEntry is one line of the log, as one reader may read it.
type LogEntry struct {
	ID          int64
	At          time.Time
	Recorded    time.Time
	ProjectID   string
	ProjectName string
	Kind        string
	AboutKind   string
	AboutID     string
	AboutName   string
	By          LogParty
	Person      LogParty
	Group       string
	Hidden      bool
	Details     map[string]any
	// stripped says an administrator reads the line without what it was about.
	// One such line is shown per person and incident, which incidentID is for.
	stripped   bool
	incidentID string
}

// LogPage is one page of the log.
type LogPage struct {
	Entries []LogEntry
	Older   int64 // pass as Before for the next page; zero when there is none
	Started time.Time
	KeptFor time.Duration
}

const logBatch = 200

// logScanLimit is how many entries one page reads to find what the reader may see,
// which keeps a page quick. A page cut short still links to the next.
var logScanLimit = 50_000

// logClass says which of the filters a kind belongs to.
func logClass(kind string) string {
	switch {
	case kind == "check.state", kind == "check.late", kind == "check.flapping", kind == "check.steady", kind == "checks.unjudged":
		return "happened"
	case strings.HasPrefix(kind, "message."):
		return "reached"
	case slices.Contains(logKindsOf["signins"], kind):
		return "signins"
	}
	return "done"
}

// logKindsOf lists the kinds of a filter.
var logKindsOf = map[string][]string{
	"happened": {"check.state", "check.late", "check.flapping", "check.steady", "checks.unjudged"},
	"reached":  {"message.reached", "message.reached_several", "message.shadow", "message.shadow_several", "message.unreached"},
	"signins":  {"signin", "passkey.added", "passkey.looks_copied", "passkey.removed", "token.made", "token.ended", "session.ended"},
}

// Who may read a kind of entry in a project they are in. A kind not listed is read
// by nobody, so a new kind starts out hidden, and a test names every kind that is
// written in a project.
var logMinRole = map[string]Role{
	"check.state": Viewer, "check.late": Viewer, "check.flapping": Viewer, "check.steady": Viewer, "checks.unjudged": Viewer,
	"check.created": Viewer, "check.changed": Viewer, "check.paused": Viewer, "check.resumed": Viewer,
	"check.moved_out": Viewer, "check.moved_in": Viewer, "check.deleted": Viewer, "check.ping_url_replaced": Viewer,
	"project.renamed": Viewer, "project.created": Owner, "command.read": Owner, "alerts.chosen": Owner,
	"message.reached": Editor, "message.reached_several": Editor, "message.shadow": Editor, "message.shadow_several": Editor, "message.unreached": Editor,
	"member.set": Owner, "member.removed": Owner, "key.made": Owner, "key.revoked": Owner,
	"invite.made": Owner, "invite.withdrawn": Owner, "invite.accepted": Owner,
}

// What concerns only the person it is about, outside any project.
var logOwnKinds = map[string]bool{
	"signin": true, "passkey.added": true, "passkey.looks_copied": true, "passkey.link_made": true, "passkey.removed": true,
	"token.made": true, "token.ended": true, "session.ended": true,
	"channel.confirmed": true, "channel.handed_on": true, "channel.handed_away": true, "channel.removed": true,
	"message.reached": true, "message.shadow": true,
}

// RoleAtLeast says a role gives at least what another does.
func RoleAtLeast(have, want Role) bool { return roleAtLeast(have, want) }

func roleAtLeast(have, want Role) bool {
	rank := map[Role]int{Viewer: 1, Editor: 2, Owner: 3}
	return rank[have] >= rank[want]
}

// What an administrator reads of a team they are not in: what concerns the
// installation, which made the team and keeps its probes (teams spec, R40).
var logInstallationKinds = map[string]bool{
	"team.created": true, "team.changed": true, "team.removed": true, "probe.created": true, "probe.rekeyed": true,
}

// unreached is the one kind an administrator reads of a project they are not in,
// stripped of what it was about: mail and push are theirs to keep working (R11).
const unreached = "message.unreached"

// readLogEntry returns an entry as a reader may read it, or false when they may not.
// Access is judged as it is today: someone added now can read back, and someone
// removed no longer can (R20).
//
// A line in a project is read by the reader's role in it, a team's own line by
// the team's owners, and one of neither by an administrator; the person a line
// concerns reads what is their own. Being an administrator gives no project.
func readLogEntry(r db.LogEntry, a Access) (LogEntry, bool) {
	own := a.PersonID != "" && r.PersonID == a.PersonID
	// As the reader's role in the line's project, or the owner's that whoever may
	// read a line outside any project has of it.
	role := Owner
	// The reader is one of the people who run what the line is in, to whom its
	// people are named.
	among := false
	// The reader is only the person the line concerns.
	ownOnly := false
	switch {
	case r.ProjectID != "":
		have, ok := a.Role(r.ProjectID)
		want, listed := logMinRole[r.Kind]
		switch {
		case ok && listed && roleAtLeast(have, want):
			role, among = have, a.knowsTeamOf(r.ProjectID)
		case a.Admin && r.Kind == unreached:
			return strippedUnreached(r), true
		default:
			return LogEntry{}, false
		}
	case r.TeamID != "":
		switch teamRole, in := a.TeamRole(r.TeamID); {
		case in && teamRole == Owner:
			among = true
		case a.Admin && logInstallationKinds[r.Kind]:
			among = true
		case own && logOwnKinds[r.Kind]:
			ownOnly = true
		default:
			return LogEntry{}, false
		}
	case a.Admin:
		among = true
	case own && logOwnKinds[r.Kind]:
		ownOnly = true
	default:
		return LogEntry{}, false
	}
	e := LogEntry{
		ID: r.ID, At: time.UnixMilli(r.HappenedAt).UTC(), Recorded: time.UnixMilli(r.RecordedAt).UTC(),
		ProjectID: r.ProjectID, ProjectName: r.ProjectName, Kind: r.Kind,
		AboutKind: r.AboutKind, AboutID: r.AboutID, AboutName: r.AboutName,
		By:     LogParty{r.ByKind, r.ByID, r.ByName},
		Person: LogParty{"person", r.PersonID, r.PersonName},
		Group:  r.GroupID, Hidden: r.Hidden == 1,
	}
	if r.PersonID == "" {
		e.Person = LogParty{}
	}
	var details map[string]any
	if json.Unmarshal([]byte(r.Details), &details) != nil || details == nil {
		details = map[string]any{}
	}
	elsewhere := details["person_elsewhere"] == true
	delete(details, "person_elsewhere")
	if !a.Admin {
		delete(details, "why") // why a message failed is for administrators
	}
	// Another project is named only to someone who can see it (R25).
	for _, key := range []string{"to_project", "from_project"} {
		idKey := key + "_id"
		p, _ := details[idKey].(string)
		_, can := a.Role(p)
		if !can {
			delete(details, key)
		}
		if !can || !a.Admin {
			delete(details, idKey)
		}
	}
	// The projects a probe serves are for an administrator, as far as they can
	// see them.
	if serves, ok := details["serves"].([]any); ok && a.Admin {
		seen := []any{}
		for _, p := range serves {
			id, _ := p.(string)
			if _, can := a.Role(id); can {
				seen = append(seen, p)
			}
		}
		details["serves"] = seen
	} else {
		delete(details, "serves")
	}
	switch {
	case ownOnly:
		// A line about them, outside any project, such as a passkey link made for
		// them: how the team works is no more theirs to read here (R21).
		if k := e.By.Kind; r.ByID != a.PersonID && (k == "person" || k == "key" || k == "command") {
			e.By = LogParty{Kind: "team"}
		}
	case r.ProjectID == "":
	case role == Viewer:
		// Outsiders do not learn the team, by name or by how it works (R21).
		if k := e.By.Kind; k == "person" || k == "key" || k == "command" {
			e.By = LogParty{Kind: "team"}
		}
		e.Person = LogParty{}
	case !among:
		// An editor or owner who is not in the project's team: who is in the team
		// is the team's to say (R22; teams spec, R40).
		if r.ByID != a.PersonID {
			e.By = apart(e.By, r.ByOutsider, false)
		}
		if !own {
			e.Person = apart(e.Person, r.PersonOutsider, elsewhere)
		}
	}
	// A line about a person is about the person it concerns: where they are not
	// named, neither is what it is about, which a filter by name would find.
	if r.AboutKind == "person" && r.AboutID == r.PersonID && e.Person.ID != r.PersonID {
		e.AboutID, e.AboutName = "", ""
	}
	// Which sign-in it came through is for those who may read who it was (R39).
	if e.By.Kind != "person" {
		delete(details, "via")
	}
	e.Details = details
	return e, true
}

// apart is a person as someone outside the project's team reads them.
func apart(p LogParty, how int64, elsewhere bool) LogParty {
	switch {
	case how == asAdministrator && elsewhere:
		return LogParty{Kind: "outside"}
	case how == asAdministrator:
		return LogParty{Kind: "administrator"}
	case how == throughTeam:
		return LogParty{Kind: "team"}
	}
	return p
}

// strippedUnreached is a message that reached someone by no way, as an
// administrator reads it who cannot see what it was about: who, when and why.
// The rest is left out before anything is narrowed, so no filter confirms it.
func strippedUnreached(r db.LogEntry) LogEntry {
	var details map[string]any
	json.Unmarshal([]byte(r.Details), &details)
	why, _ := details["why"].(string)
	return LogEntry{
		ID: r.ID, At: time.UnixMilli(r.HappenedAt).UTC(), Recorded: time.UnixMilli(r.RecordedAt).UTC(),
		Kind: r.Kind, By: LogParty{Kind: r.ByKind}, Person: LogParty{"person", r.PersonID, r.PersonName},
		Details: map[string]any{"why": why}, stripped: true, incidentID: r.IncidentID,
	}
}

// isLate says an entry is a check going late or back from it. Those written before
// they had a kind of their own are told by what they say.
func (e LogEntry) isLate() bool {
	if e.Kind == "check.late" {
		return true
	}
	if e.Kind != "check.state" {
		return false
	}
	return e.Details["to"] == "late" || (e.Details["from"] == "late" && e.Details["to"] == "up")
}

// matches says whether an entry the reader may see is what they narrowed to.
func (f LogFilter) matches(e LogEntry) bool {
	if e.isLate() && !f.Late {
		return false
	}
	switch {
	case f.CheckID != "" && e.AboutID != f.CheckID:
		return false
	case f.ProjectID != "" && e.ProjectID != f.ProjectID:
		return false
	case f.What != "" && logClass(e.Kind) != f.What:
		return false
	case f.Group != "" && e.Group != f.Group:
		return false
	case f.Name != "" && !strings.EqualFold(e.AboutName, f.Name):
		return false
	// After masking: someone the reader sees only as "the team" is never found by
	// their name (R23).
	case f.PersonID != "" && e.By.ID != f.PersonID && e.Person.ID != f.PersonID:
		return false
	}
	// A check's own going into unknown, or what a line stands for, shows when asked
	// for by that check or that line (R17, R19).
	return !e.Hidden || e.isLate() || f.CheckID != "" || f.Name != "" || f.Group != ""
}

type logSource func(ctx context.Context, q *db.Queries, before, batch int64) ([]db.LogEntry, error)

// logFrom is a query by one value, as a source.
func logFrom[P any](of string, params func(of string, before, batch int64) P, query func(context.Context, P) ([]db.LogEntry, error)) logSource {
	return func(ctx context.Context, _ *db.Queries, before, batch int64) ([]db.LogEntry, error) {
		return query(ctx, params(of, before, batch))
	}
}

// logSources picks the indexes that serve a request; whatever else it asks is
// checked on what they return. But for a check or a line that stands for
// several, it reads only where the reader may read: narrowing by a name or a
// person matches there, so that another team's project is never read through,
// which a reader could feel as pages cut short (teams spec, R43). An
// administrator reads through what is outside any project, where a team's own
// lines are few.
func logSources(q *db.Queries, f LogFilter, a Access) []logSource {
	withLate := func(calm, late logSource) []logSource {
		// Late lines only belong to what happened, so nothing else reads through them.
		if f.Late && (f.What == "" || f.What == "happened") {
			return []logSource{calm, late}
		}
		return []logSource{calm}
	}
	project := func(of string) []logSource {
		return withLate(
			logFrom(of, func(of string, before, batch int64) db.LogEntriesOfProjectParams {
				return db.LogEntriesOfProjectParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesOfProject),
			logFrom(of, func(of string, before, batch int64) db.LogEntriesLateOfProjectParams {
				return db.LogEntriesLateOfProjectParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesLateOfProject))
	}
	switch {
	case f.Group != "":
		return []logSource{logFrom(f.Group, func(of string, before, batch int64) db.LogEntriesOfGroupParams {
			return db.LogEntriesOfGroupParams{Of: of, Before: before, Batch: batch}
		}, q.LogEntriesOfGroup)}
	case f.CheckID != "":
		return withLate(
			logFrom(f.CheckID, func(of string, before, batch int64) db.LogEntriesAboutParams {
				return db.LogEntriesAboutParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesAbout),
			logFrom(f.CheckID, func(of string, before, batch int64) db.LogEntriesLateAboutParams {
				return db.LogEntriesLateAboutParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesLateAbout))
	case f.ProjectID != "":
		return project(f.ProjectID)
	}
	var sources []logSource
	for _, p := range a.Projects {
		// What a role cannot read in a project is not worth reading through it for.
		if f.What == "signins" || (f.What == "reached" && p.Role == Viewer) {
			continue
		}
		sources = append(sources, project(p.ID)...)
	}
	for _, t := range a.Teams {
		if t.Role == Owner {
			sources = append(sources, logFrom(t.ID, func(of string, before, batch int64) db.LogEntriesOfTeamParams {
				return db.LogEntriesOfTeamParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesOfTeam))
		}
	}
	switch {
	case a.Admin:
		// What concerns the installation is every line outside a project, their own
		// among them, and a message that reached nobody, wherever it was about.
		sources = append(sources, logFrom("", func(of string, before, batch int64) db.LogEntriesOfProjectParams {
			return db.LogEntriesOfProjectParams{Of: of, Before: before, Batch: batch}
		}, q.LogEntriesOfProject))
		if f.What == "" || f.What == "reached" {
			sources = append(sources, logFrom(unreached, func(of string, before, batch int64) db.LogEntriesOfKindParams {
				return db.LogEntriesOfKindParams{Of: of, Before: before, Batch: batch}
			}, q.LogEntriesOfKind))
		}
	case a.PersonID != "":
		sources = append(sources, logFrom(a.PersonID, func(of string, before, batch int64) db.LogEntriesOwnParams {
			return db.LogEntriesOwnParams{Of: of, Before: before, Batch: batch}
		}, q.LogEntriesOwn))
	}
	return sources
}

// ReadLog returns a page of the log, newest first, as far as the reader may see it.
func (s *Store) ReadLog(ctx context.Context, a Access, f LogFilter) (LogPage, error) {
	q := db.New(s.r)
	page := LogPage{KeptFor: s.Keeps().logAge()}
	if v, err := s.Setting(ctx, "log_started_at"); err != nil {
		return page, err
	} else if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		page.Started = time.UnixMilli(ms).UTC()
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	// A project they cannot see gives the same page as one that does not exist (R24),
	// and someone who only views reads no names, so finds no one but themselves (R23).
	if _, ok := a.Role(f.ProjectID); f.ProjectID != "" && !ok {
		return page, nil
	}
	if f.PersonID != "" && f.PersonID != a.PersonID && !a.Admin && !slices.ContainsFunc(a.Projects, func(p ProjectAccess) bool { return p.Role != Viewer }) {
		return page, nil
	}
	before := f.Before
	if before <= 0 {
		before = math.MaxInt64
	}
	sources := logSources(q, f, a)
	buffers := make([][]db.LogEntry, len(sources))
	cursors := make([]int64, len(sources))
	finished := make([]bool, len(sources))
	for i := range cursors {
		cursors[i] = before
	}
	var last int64
	scanned := 0
	for len(page.Entries) <= f.Limit {
		// The source whose next entry is newest, reading on where one has run out.
		best := -1
		for i := range sources {
			if len(buffers[i]) == 0 && !finished[i] {
				rows, err := sources[i](ctx, q, cursors[i], logBatch)
				if err != nil {
					return page, err
				}
				buffers[i] = rows
				if len(rows) < logBatch {
					finished[i] = true
				}
				if len(rows) > 0 {
					cursors[i] = rows[len(rows)-1].ID
				}
			}
			if len(buffers[i]) > 0 && (best < 0 || buffers[i][0].ID > buffers[best][0].ID) {
				best = i
			}
		}
		if best < 0 {
			return page, nil
		}
		r := buffers[best][0]
		buffers[best] = buffers[best][1:]
		if r.ID == last {
			continue // by this person, and about them
		}
		last = r.ID
		if scanned++; scanned > logScanLimit {
			// Asked for something that holds nothing they may see, a way on would say
			// it exists, as nothing else does (R24). Past this many lines, what they
			// may see of a name or a check is rarer still than that is worth.
			if len(page.Entries) > 0 || (f.CheckID == "" && f.Name == "" && f.PersonID == "" && f.Group == "") {
				page.Older = r.ID + 1
			}
			return page, nil
		}
		// The kind first: decoding is most of what reading an entry costs.
		if f.What != "" && logClass(r.Kind) != f.What {
			continue
		}
		if e, ok := readLogEntry(r, a); ok && f.matches(e) {
			// One line was written per check of the incident, which says how many
			// went down: an administrator who cannot see them reads the last, and
			// which that is does not depend on where a page begins. The numbers of
			// the lines would still say it, where an incident holds several checks.
			// None does yet: when incidents come to join checks, one line is to be
			// written per person and incident (intent/teams/plan-2.md).
			if e.stripped {
				later, err := q.LaterUnreached(ctx, db.LaterUnreachedParams{
					IncidentID: e.incidentID, PersonID: e.Person.ID, HappenedAt: e.At.UnixMilli(), ID: e.ID,
				})
				if err != nil {
					return page, err
				}
				if later > 0 {
					continue
				}
			}
			page.Entries = append(page.Entries, e)
		}
	}
	page.Entries = page.Entries[:f.Limit]
	page.Older = page.Entries[f.Limit-1].ID
	return page, nil
}
