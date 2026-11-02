// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// logWorld is two projects and everyone who reads them differently.
type logWorld struct {
	fixture
	other                                      string // a second project
	at                                         time.Time
	owner, editor, viewer, both, dan, stranger Access
	checkID, uuid, otherCheck                  string
}

func newLogWorld(t *testing.T) logWorld {
	t.Helper()
	f := setup(t)
	ctx := t.Context()
	w := logWorld{fixture: f, at: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	var err error
	w.other, _, err = f.s.CreateProject(ctx, Command("test"), f.ws, "Elsewhere")
	must(t, err)
	member := func(name string, roles map[string]Role) Access {
		id, err := f.s.CreatePerson(ctx, Command("test"), f.ws, NewPerson{Name: name})
		must(t, err)
		for p, r := range roles {
			must(t, f.s.SetMember(ctx, Command("test"), f.ws, p, id, r))
		}
		a, err := f.s.AccessOfPerson(ctx, f.ws, id)
		must(t, err)
		return a
	}
	w.owner = member("Olive", map[string]Role{f.project: Owner})
	w.editor = member("Eddie", map[string]Role{f.project: Editor})
	w.viewer = member("Carol", map[string]Role{f.project: Viewer})
	w.both = member("Eve", map[string]Role{f.project: Viewer, w.other: Editor})
	w.stranger = member("Nobody", map[string]Role{})
	dan, err := f.s.CreatePerson(ctx, Command("test"), f.ws, NewPerson{Name: "Dan", WorkspaceAdmin: true})
	must(t, err)
	w.dan, err = f.s.AccessOfPerson(ctx, f.ws, dan)
	must(t, err)
	c, err := f.s.CreateCheck(ctx, w.editor, f.project, NewCheck{Name: "backup.sh", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	w.checkID, w.uuid = c.ID, c.PingUUID
	o, err := f.s.CreateCheck(ctx, w.both, w.other, NewCheck{Name: "elsewhere.sh", Interval: time.Hour, Grace: time.Hour})
	must(t, err)
	w.otherCheck = o.ID
	return w
}

func (w logWorld) read(t *testing.T, a Access, f LogFilter) []LogEntry {
	t.Helper()
	p, err := w.s.ReadLog(t.Context(), a, f)
	must(t, err)
	return p.Entries
}

func kindsSeen(es []LogEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Kind
	}
	return out
}

// Each row of the spec's table, for each kind of reader (R20).
func TestWhoSeesWhat(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	// A state change, a message, a role, and what is the installation's.
	_, err := w.s.Ping(ctx, ping(w.uuid, check.Fail, "", w.at))
	must(t, err)
	incident := w.incidents(t)[0].ID
	must(t, w.s.Sent(ctx, Pending{ID: "x", WorkspaceID: w.ws, PersonID: w.editor.PersonID, Kind: "alert", ChannelKind: "webpush", Items: []Item{{IncidentID: incident, What: "down"}}}, w.at))
	must(t, w.s.Failed(ctx, Pending{ID: "x", WorkspaceID: w.ws, PersonID: w.owner.PersonID, Kind: "alert", ChannelKind: "email", Items: []Item{{IncidentID: incident, What: "down"}}}, "the push service answered 503", false, false, w.at, Ways{}))
	_, err = w.s.UpdateCheck(ctx, w.editor, w.checkID, Change{Grace: ptr(2 * time.Hour)}, w.at)
	must(t, err)
	must(t, w.s.RenameProject(ctx, Command("rename-project"), w.ws, w.project, "Backups two"))
	_, err = w.s.SetAlerts(ctx, Command("set-alerts"), true)
	must(t, err)
	admin, err := w.s.AdminAccess(ctx, w.ws, "create-probe")
	must(t, err)
	_, _, err = w.s.CreateProbe(ctx, admin, "inside", w.project, nil)
	must(t, err)
	link, err := w.s.CreatePasskeyLink(ctx, Command("create-user"), w.ws, w.viewer.PersonID, w.at)
	must(t, err)
	_, err = w.s.AddPasskey(ctx, link, key("carol"), "a browser", w.at)
	must(t, err)

	seen := func(a Access, kind string) bool {
		t.Helper()
		page, err := w.s.ReadLog(ctx, a, LogFilter{Limit: 200})
		must(t, err)
		return slices.ContainsFunc(page.Entries, func(e LogEntry) bool { return e.Kind == kind })
	}
	for _, tc := range []struct {
		kind                                 string
		viewer, editor, owner, dan, stranger bool
	}{
		{"check.state", true, true, true, true, false},
		{"check.changed", true, true, true, true, false},
		{"project.renamed", true, true, true, true, false},
		{"message.reached", false, true, true, true, false},
		{"message.unreached", false, true, true, true, false},
		{"member.set", false, false, true, true, false},
		{"project.created", false, false, false, true, false},
		{"person.added", false, false, false, true, false},
		{"probe.created", false, false, false, true, false},
		{"alerts.mode", false, false, false, true, false},
		{"passkey.added", true, false, false, true, false}, // the viewer's own
	} {
		for who, c := range map[string]struct {
			a    Access
			want bool
		}{"viewer": {w.viewer, tc.viewer}, "editor": {w.editor, tc.editor}, "owner": {w.owner, tc.owner}, "administrator": {w.dan, tc.dan}, "stranger": {w.stranger, tc.stranger}} {
			if got := seen(c.a, tc.kind); got != c.want {
				t.Errorf("%s reading %s: %v, want %v", who, tc.kind, got, c.want)
			}
		}
	}
	// Why a message failed is for administrators.
	reason := func(a Access) any {
		for _, e := range w.read(t, a, LogFilter{Limit: 200}) {
			if e.Kind == "message.unreached" {
				return e.Details["why"]
			}
		}
		return nil
	}
	if reason(w.dan) != "the push service refused it" || reason(w.editor) != nil {
		t.Errorf("administrator %v, editor %v", reason(w.dan), reason(w.editor))
	}
}

// A viewer reads what was done, by "the team", and never a name (R21).
func TestAViewerReadsTheTeam(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	must(t, w.s.PauseCheck(ctx, w.editor, w.checkID, w.at))
	must(t, w.s.ResumeCheck(ctx, w.dan, w.checkID, w.at))
	byCommand, err := w.s.AdminAccess(ctx, w.ws, "pause-check")
	must(t, err)
	must(t, w.s.PauseCheck(ctx, byCommand, w.checkID, w.at))
	_, err = w.s.Ping(ctx, ping(w.uuid, check.Success, "", w.at))
	must(t, err)
	_, err = w.s.UpdateCheck(ctx, w.editor, w.checkID, Change{Grace: ptr(2 * time.Hour)}, w.at)
	must(t, err)
	es := w.read(t, w.viewer, LogFilter{CheckID: w.checkID})
	byKind := map[string]string{}
	for _, e := range es {
		byKind[e.Kind+":"+e.By.Kind] = e.By.Name
		if text := fmt.Sprintf("%+v", e); strings.Contains(text, "Eddie") || strings.Contains(text, "Dan") || strings.Contains(text, "pause-check") {
			t.Errorf("a name in %s", text)
		}
	}
	for _, want := range []string{"check.paused:team", "check.resumed:team", "check.resumed:job"} {
		if _, ok := byKind[want]; !ok {
			t.Errorf("no %s in %v", want, byKind)
		}
	}
	// Values on the list still show: the check's page shows them to a viewer too.
	i := slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == "check.changed" })
	if !strings.Contains(fmt.Sprint(es[i].Details), "7200") {
		t.Errorf("%v", es[i].Details)
	}
}

// Editors and owners read an administrator from outside the project as just that;
// administrators read the name; someone who is a member is named (R22).
func TestAnOutsiderReadsAsAnAdministrator(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	must(t, w.s.PauseCheck(ctx, w.dan, w.checkID, w.at))
	must(t, w.s.Sent(ctx, Pending{ID: "x", WorkspaceID: w.ws, PersonID: w.dan.PersonID, Kind: "digest", ChannelKind: "email", Items: []Item{{CheckID: w.checkID, Check: "backup.sh", ProjectID: w.project, What: "paused_check"}}}, w.at))
	lineIn := func(a Access, kind string) LogEntry {
		t.Helper()
		es := w.read(t, a, LogFilter{CheckID: w.checkID})
		return es[slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == kind })]
	}
	if e := lineIn(w.editor, "check.paused"); e.By.Kind != "administrator" || e.By.Name != "" || e.By.ID != "" {
		t.Errorf("%+v", e.By)
	}
	if e := lineIn(w.owner, "message.reached"); e.Person.Kind != "administrator" || e.Person.Name != "" {
		t.Errorf("%+v", e.Person)
	}
	if e := lineIn(w.dan, "check.paused"); e.By.Name != "Dan" {
		t.Errorf("%+v", e.By)
	}
	// And someone told of a check that has moved since is named to nobody there.
	must(t, w.s.MoveCheck(ctx, Command("test"), w.ws, w.checkID, w.other, w.at))
	must(t, w.s.Sent(ctx, Pending{ID: "x", WorkspaceID: w.ws, PersonID: w.owner.PersonID, Kind: "digest", ChannelKind: "email", Items: []Item{{CheckID: w.checkID, Check: "backup.sh", ProjectID: w.other, What: "paused_check"}}}, w.at))
	es := w.read(t, w.both, LogFilter{CheckID: w.checkID})
	i := slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == "message.reached" && e.ProjectID == w.other })
	if es[i].Person.Kind != "outside" || es[i].Person.Name != "" {
		t.Errorf("%+v", es[i].Person)
	}
}

// Narrowing to a person leaves out where the reader only reads "the team", so comparing
// cannot pin those lines on them (R23).
func TestNarrowingToAPersonFindsOnlyWhereTheyAreNamed(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	ada, err := w.s.CreateMember(ctx, Command("test"), w.ws, NewPerson{Name: "Ada"}, []string{w.project, w.other}, Editor)
	must(t, err)
	access, err := w.s.AccessOfPerson(ctx, w.ws, ada)
	must(t, err)
	must(t, w.s.PauseCheck(ctx, access, w.checkID, w.at))
	must(t, w.s.PauseCheck(ctx, access, w.otherCheck, w.at))
	es := w.read(t, w.both, LogFilter{PersonID: ada})
	if len(es) == 0 {
		t.Fatal("found nothing where Eve may read her")
	}
	for _, e := range es {
		if e.ProjectID != w.other {
			t.Errorf("found in a project Eve only views: %+v", e)
		}
	}
	if es := w.read(t, w.viewer, LogFilter{PersonID: ada}); len(es) != 0 {
		t.Errorf("a viewer narrowed to a person: %+v", es)
	}
}

// What someone cannot see gives the same page as what does not exist (R24).
func TestWhatCannotBeSeenIsAsIfNotThere(t *testing.T) {
	w := newLogWorld(t)
	for name, f := range map[string]LogFilter{
		"a project":         {ProjectID: w.other},
		"a check":           {CheckID: w.otherCheck},
		"a name":            {Name: "elsewhere.sh"},
		"a project unknown": {ProjectID: "prj_nothing"},
		"a check unknown":   {CheckID: "chk_nothing"},
	} {
		if es := w.read(t, w.editor, f); len(es) != 0 {
			t.Errorf("%s: %+v", name, es)
		}
	}
	if es := w.read(t, w.dan, LogFilter{CheckID: w.otherCheck}); len(es) == 0 {
		t.Error("an administrator sees nothing of it")
	}
}

// A move names the other project only to someone who can see it (R25).
func TestAMoveNamesTheOtherProjectOnlyToWhoCanSeeIt(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	must(t, w.s.MoveCheck(ctx, Command("test"), w.ws, w.checkID, w.other, w.at))
	out := func(a Access) map[string]any {
		t.Helper()
		es := w.read(t, a, LogFilter{CheckID: w.checkID})
		return es[slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == "check.moved_out" })].Details
	}
	if d := out(w.editor); d["to_project"] != nil || d["to_project_id"] != nil {
		t.Errorf("%v", d)
	}
	if d := out(w.both); d["to_project"] != "Elsewhere" {
		t.Errorf("%v", d)
	}
	// What happened before stays with the old project.
	if es := w.read(t, w.editor, LogFilter{CheckID: w.checkID}); !slices.Contains(kindsSeen(es), "check.created") {
		t.Errorf("%v", kindsSeen(es))
	}
}

// Access is judged as it is today (R20).
func TestAccessIsJudgedToday(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	must(t, w.s.PauseCheck(ctx, w.editor, w.checkID, w.at))
	if es := w.read(t, w.stranger, LogFilter{CheckID: w.checkID}); len(es) != 0 {
		t.Fatalf("%+v", es)
	}
	must(t, w.s.SetMember(ctx, Command("test"), w.ws, w.project, w.stranger.PersonID, Viewer))
	joined, err := w.s.AccessOfPerson(ctx, w.ws, w.stranger.PersonID)
	must(t, err)
	if es := w.read(t, joined, LogFilter{CheckID: w.checkID}); len(es) == 0 {
		t.Error("added now, they cannot read back")
	}
	must(t, w.s.RemoveMember(ctx, Command("test"), w.ws, w.project, w.stranger.PersonID))
	left, err := w.s.AccessOfPerson(ctx, w.ws, w.stranger.PersonID)
	must(t, err)
	if es := w.read(t, left, LogFilter{CheckID: w.checkID}); len(es) != 0 {
		t.Errorf("removed, they still read %+v", es)
	}
}

// Pages go through everything once, newest first, across the sources of a reader.
func TestPagingGoesThroughEverythingOnce(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	for i := range 60 {
		c := w.checkID
		a := w.editor
		if i%2 == 1 {
			c, a = w.otherCheck, w.both
		}
		if i%4 < 2 {
			must(t, w.s.PauseCheck(ctx, a, c, w.at))
		} else {
			must(t, w.s.ResumeCheck(ctx, a, c, w.at))
		}
	}
	var seen []int64
	var before int64
	for pages := 0; ; pages++ {
		p, err := w.s.ReadLog(ctx, w.both, LogFilter{Before: before, Limit: 7})
		must(t, err)
		if len(p.Entries) > 7 || (p.Older != 0 && len(p.Entries) != 7) {
			t.Fatalf("%d entries, older %d", len(p.Entries), p.Older)
		}
		for _, e := range p.Entries {
			seen = append(seen, e.ID)
		}
		if p.Older == 0 {
			break
		}
		before = p.Older
	}
	sorted := slices.Clone(seen)
	slices.Sort(sorted)
	slices.Reverse(sorted)
	if !slices.Equal(seen, sorted) || len(slices.Compact(sorted)) != len(seen) {
		t.Errorf("out of order or twice: %v", seen)
	}
	if len(seen) < 60 {
		t.Errorf("%d entries", len(seen))
	}
}

// What the default list leaves out shows when asked for by its check or its line, and
// a deleted check is found by its name, however typed (R17, R19, R28).
func TestWhatTheListLeavesOut(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	_, err := w.s.Ping(ctx, ping(w.uuid, check.Success, "", w.at))
	must(t, err)
	must(t, w.s.Alive(ctx, w.at.Add(time.Minute)))
	must(t, w.s.Recover(ctx, w.at.Add(5*time.Hour)))
	isUnknown := func(e LogEntry) bool { return e.Kind == "check.state" && e.Details["to"] == "unknown" }
	if es := w.read(t, w.editor, LogFilter{Limit: 200}); slices.ContainsFunc(es, isUnknown) {
		t.Errorf("in the list: %v", kindsSeen(es))
	}
	es := w.read(t, w.editor, LogFilter{CheckID: w.checkID})
	if !slices.ContainsFunc(es, isUnknown) {
		t.Errorf("not on its check: %v", kindsSeen(es))
	}
	list := w.read(t, w.editor, LogFilter{Limit: 200})
	head := list[slices.IndexFunc(list, func(e LogEntry) bool { return e.Kind == "checks.unjudged" })]
	if opened := w.read(t, w.editor, LogFilter{Group: head.Group}); !slices.ContainsFunc(opened, isUnknown) {
		t.Errorf("not behind its line: %v", kindsSeen(opened))
	}

	must(t, w.s.DeleteCheck(ctx, w.editor, w.checkID, w.at.Add(6*time.Hour)))
	for _, typed := range []string{"backup.sh", "BACKUP.SH"} {
		if es := w.read(t, w.editor, LogFilter{Name: typed}); !slices.Contains(kindsSeen(es), "check.deleted") {
			t.Errorf("%q: %v", typed, kindsSeen(es))
		}
	}
	if es := w.read(t, w.editor, LogFilter{What: "done", Limit: 200}); !slices.Contains(kindsSeen(es), "check.deleted") {
		t.Errorf("%v", kindsSeen(es))
	}
	if es := w.read(t, w.editor, LogFilter{What: "happened", Limit: 200}); slices.Contains(kindsSeen(es), "check.deleted") {
		t.Errorf("%v", kindsSeen(es))
	}
}

// A kind this version does not list is for administrators, so a new kind starts out
// hidden.
func TestAnUnknownKindIsForAdministrators(t *testing.T) {
	w := newLogWorld(t)
	_, err := w.s.w.ExecContext(t.Context(), `INSERT INTO log_entries (workspace_id, happened_at, recorded_at, project_id, kind, by_kind) VALUES (?, 1, 1, ?, 'something.new', 'allstill')`, w.ws, w.project)
	must(t, err)
	if es := w.read(t, w.owner, LogFilter{ProjectID: w.project, Limit: 200}); slices.Contains(kindsSeen(es), "something.new") {
		t.Error("an owner reads it")
	}
	if es := w.read(t, w.dan, LogFilter{ProjectID: w.project, Limit: 200}); !slices.Contains(kindsSeen(es), "something.new") {
		t.Error("an administrator does not")
	}
}

// Every way of reading the log uses its index, without sorting what it found (R32).
func TestTheLogsQueriesUseTheirIndexes(t *testing.T) {
	s := open(t)
	raw, err := os.ReadFile("queries/log.sql")
	must(t, err)
	queries := regexp.MustCompile(`(?s)-- name: (LogEntries\w+) :many\n(.*?);`).FindAllStringSubmatch(string(raw), -1)
	if len(queries) < 8 {
		t.Fatalf("%d queries found", len(queries))
	}
	args := regexp.MustCompile(`sqlc\.arg\((\w+)\)`)
	for _, q := range queries {
		sql := args.ReplaceAllStringFunc(q[2], func(m string) string {
			switch m {
			case "sqlc.arg(before)":
				return "1000"
			case "sqlc.arg(batch)":
				return "200"
			}
			return "'x'"
		})
		rows, err := s.r.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+sql)
		must(t, err)
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			must(t, rows.Scan(&id, &parent, &unused, &detail))
			plan = append(plan, detail)
		}
		rows.Close()
		text := strings.Join(plan, "; ")
		if strings.Contains(text, "TEMP B-TREE") {
			t.Errorf("%s sorts: %s", q[1], text)
		}
		if q[1] != "LogEntriesAll" && !strings.Contains(text, "USING INDEX") && !strings.Contains(text, "USING COVERING INDEX") {
			t.Errorf("%s uses no index: %s", q[1], text)
		}
		if q[1] == "LogEntriesAll" && !strings.Contains(text, "INTEGER PRIMARY KEY") {
			t.Errorf("%s: %s", q[1], text)
		}
	}
}

// A viewer does not read a command's name on a line about them either (R21).
func TestAViewersOwnLineNamesNoCommand(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	_, err := w.s.CreatePasskeyLink(ctx, Command("passkey-link"), w.ws, w.viewer.PersonID, w.at)
	must(t, err)
	es := w.read(t, w.viewer, LogFilter{Limit: 200})
	i := slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == "passkey.link_made" })
	if i < 0 || es[i].By.Kind != "team" || es[i].By.Name != "" {
		t.Errorf("%+v", es)
	}
	es = w.read(t, w.dan, LogFilter{Limit: 200})
	if i := slices.IndexFunc(es, func(e LogEntry) bool { return e.Kind == "passkey.link_made" }); es[i].By.Name != "passkey-link" {
		t.Errorf("%+v", es[i])
	}
}

// A page that reads its limit of lines stops, and goes on from there, unless it was
// asked for something that would be given away by a way on (R24).
func TestAPageCutShort(t *testing.T) {
	w := newLogWorld(t)
	ctx := t.Context()
	was := logScanLimit
	logScanLimit = 20
	t.Cleanup(func() { logScanLimit = was })
	for range 60 {
		must(t, w.s.PauseCheck(ctx, w.both, w.otherCheck, w.at))
		must(t, w.s.ResumeCheck(ctx, w.both, w.otherCheck, w.at))
	}
	// A list narrowed to a kind only some lines have is cut short and goes on, always
	// further back.
	var before int64
	for range 100 {
		p, err := w.s.ReadLog(ctx, w.both, LogFilter{Before: before, What: "happened"})
		must(t, err)
		if p.Older == 0 {
			break
		}
		if before != 0 && p.Older >= before {
			t.Fatalf("it went back: %d after %d", p.Older, before)
		}
		before = p.Older
	}
	// Asked for a name that holds only what they may not see: the same as no such name.
	p, err := w.s.ReadLog(ctx, w.editor, LogFilter{Name: "elsewhere.sh"})
	must(t, err)
	if len(p.Entries) != 0 || p.Older != 0 {
		t.Errorf("%+v", p)
	}
	// And a list cut short with nothing to show still goes on.
	viewerOfOther := w.both
	p, err = w.s.ReadLog(ctx, viewerOfOther, LogFilter{What: "signins"})
	must(t, err)
	if p.Older == 0 || len(p.Entries) != 0 {
		t.Errorf("%+v", p)
	}
}
