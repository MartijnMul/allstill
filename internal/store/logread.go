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
	Before    int64 // only entries older than this number
	Limit     int
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
	case kind == "check.state", kind == "check.flapping", kind == "check.steady", kind == "checks.unjudged":
		return "happened"
	case strings.HasPrefix(kind, "message."):
		return "reached"
	case kind == "signin", kind == "passkey.added", kind == "passkey.looks_copied":
		return "signins"
	}
	return "done"
}

// logKindsOf lists the kinds of a filter, for an administrator's query by kind.
var logKindsOf = map[string][]string{
	"happened": {"check.state", "check.flapping", "check.steady", "checks.unjudged"},
	"reached":  {"message.reached", "message.reached_several", "message.shadow", "message.shadow_several", "message.unreached"},
	"signins":  {"signin", "passkey.added", "passkey.looks_copied"},
}

// Who may read a kind of entry in a project they are in; an administrator reads
// everything. A kind not listed is for administrators, so a new kind starts out hidden.
var logMinRole = map[string]Role{
	"check.state": Viewer, "check.flapping": Viewer, "check.steady": Viewer, "checks.unjudged": Viewer,
	"check.created": Viewer, "check.changed": Viewer, "check.paused": Viewer, "check.resumed": Viewer,
	"check.moved_out": Viewer, "check.moved_in": Viewer, "check.deleted": Viewer, "check.ping_url_replaced": Viewer,
	"project.renamed": Viewer,
	"message.reached": Editor, "message.reached_several": Editor, "message.shadow": Editor, "message.shadow_several": Editor, "message.unreached": Editor,
	"member.set": Owner, "member.removed": Owner,
}

// What concerns only the person it is about, outside any project.
var logOwnKinds = map[string]bool{
	"signin": true, "passkey.added": true, "passkey.looks_copied": true, "passkey.link_made": true,
	"channel.confirmed": true, "channel.handed_on": true, "channel.handed_away": true, "channel.removed": true,
	"message.reached": true, "message.shadow": true,
}

func roleAtLeast(have, want Role) bool {
	rank := map[Role]int{Viewer: 1, Editor: 2, Owner: 3}
	return rank[have] >= rank[want]
}

// readLogEntry returns an entry as a reader may read it, or false when they may not.
// Access is judged as it is today: someone added now can read back, and someone
// removed no longer can (R20).
func readLogEntry(r db.LogEntry, a Access) (LogEntry, bool) {
	own := a.PersonID != "" && r.PersonID == a.PersonID
	role := Owner
	if !a.Admin {
		if r.ProjectID == "" {
			if !own || !logOwnKinds[r.Kind] {
				return LogEntry{}, false
			}
		} else {
			have, ok := a.Role(r.ProjectID)
			want, listed := logMinRole[r.Kind]
			if !ok || !listed || !roleAtLeast(have, want) {
				return LogEntry{}, false
			}
			role = have
		}
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
		// Another project is named only to someone who can see it (R25).
		for _, key := range []string{"to_project", "from_project"} {
			idKey := key + "_id"
			if p, ok := details[idKey].(string); ok {
				if _, can := a.Role(p); !can {
					delete(details, key)
				}
			}
			delete(details, idKey)
		}
		delete(details, "serves")
		switch {
		case r.ProjectID == "":
			// A line about them, outside any project, such as a passkey link made for
			// them: how the team works is no more theirs to read here (R21).
			if k := e.By.Kind; r.ByID != a.PersonID && (k == "person" || k == "key" || k == "command") {
				e.By = LogParty{Kind: "team"}
			}
		case role == Viewer:
			// Outsiders do not learn the team, by name or by how it works (R21).
			if k := e.By.Kind; k == "person" || k == "key" || k == "command" {
				e.By = LogParty{Kind: "team"}
			}
			e.Person = LogParty{}
		default:
			if r.ByOutsider == 1 && r.ByID != a.PersonID {
				e.By = LogParty{Kind: "administrator"}
			}
			if r.PersonOutsider == 1 && !own {
				if elsewhere {
					e.Person = LogParty{Kind: "outside"}
				} else {
					e.Person = LogParty{Kind: "administrator"}
				}
			}
		}
	}
	e.Details = details
	return e, true
}

// matches says whether an entry the reader may see is what they narrowed to.
func (f LogFilter) matches(e LogEntry) bool {
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
	return !e.Hidden || f.CheckID != "" || f.Name != "" || f.Group != ""
}

type logSource func(ctx context.Context, q *db.Queries, before, batch int64) ([]db.LogEntry, error)

func logOf(query func(context.Context, *db.Queries, string, int64, int64) ([]db.LogEntry, error), of string) logSource {
	return func(ctx context.Context, q *db.Queries, before, batch int64) ([]db.LogEntry, error) {
		return query(ctx, q, of, before, batch)
	}
}

func logProject(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesOfProject(ctx, db.LogEntriesOfProjectParams{Of: of, Before: before, Batch: batch})
}

func logAbout(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesAbout(ctx, db.LogEntriesAboutParams{Of: of, Before: before, Batch: batch})
}

func logNamed(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesNamed(ctx, db.LogEntriesNamedParams{Of: of, Before: before, Batch: batch})
}

func logBy(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesBy(ctx, db.LogEntriesByParams{Of: of, Before: before, Batch: batch})
}

func logConcerning(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesConcerning(ctx, db.LogEntriesConcerningParams{Of: of, Before: before, Batch: batch})
}

func logOwn(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesOwn(ctx, db.LogEntriesOwnParams{Of: of, Before: before, Batch: batch})
}

func logOfKind(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesOfKind(ctx, db.LogEntriesOfKindParams{Of: of, Before: before, Batch: batch})
}

func logOfGroup(ctx context.Context, q *db.Queries, of string, before, batch int64) ([]db.LogEntry, error) {
	return q.LogEntriesOfGroup(ctx, db.LogEntriesOfGroupParams{Of: of, Before: before, Batch: batch})
}

// logSources picks the indexes that serve a request; whatever else it asks is
// checked on what they return.
func logSources(f LogFilter, a Access) []logSource {
	switch {
	case f.Group != "":
		return []logSource{logOf(logOfGroup, f.Group)}
	case f.CheckID != "":
		return []logSource{logOf(logAbout, f.CheckID)}
	case f.Name != "":
		return []logSource{logOf(logNamed, f.Name)}
	case f.PersonID != "":
		return []logSource{logOf(logBy, f.PersonID), logOf(logConcerning, f.PersonID)}
	case f.ProjectID != "":
		return []logSource{logOf(logProject, f.ProjectID)}
	}
	var sources []logSource
	if a.Admin {
		kinds, narrow := logKindsOf[f.What]
		if !narrow {
			// "done" is most of what is not the others, so it reads everything.
			return []logSource{func(ctx context.Context, q *db.Queries, before, batch int64) ([]db.LogEntry, error) {
				return q.LogEntriesAll(ctx, db.LogEntriesAllParams{Before: before, Batch: batch})
			}}
		}
		for _, k := range kinds {
			sources = append(sources, logOf(logOfKind, k))
		}
		return sources
	}
	for _, p := range a.Projects {
		sources = append(sources, logOf(logProject, p.ID))
	}
	if a.PersonID != "" {
		sources = append(sources, logOf(logOwn, a.PersonID))
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
	if _, ok := a.Role(f.ProjectID); f.ProjectID != "" && !ok && !a.Admin {
		return page, nil
	}
	if f.PersonID != "" && f.PersonID != a.PersonID && !a.Admin && !slices.ContainsFunc(a.Projects, func(p ProjectAccess) bool { return p.Role != Viewer }) {
		return page, nil
	}
	before := f.Before
	if before <= 0 {
		before = math.MaxInt64
	}
	sources := logSources(f, a)
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
			page.Entries = append(page.Entries, e)
		}
	}
	page.Entries = page.Entries[:f.Limit]
	page.Older = page.Entries[f.Limit-1].ID
	return page, nil
}
