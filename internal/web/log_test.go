// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

func logPageOf(t *testing.T, b *browser, query string) string {
	t.Helper()
	return string(b.ok("GET", "/log"+query, nil))
}

func TestTheLogPage(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	access, err := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.st.PauseCheck(ctx, access, f.checkID, now); err != nil {
		t.Fatal(err)
	}

	page := logPageOf(t, ada, "")
	for _, want := range []string{
		"<h1>Log</h1>", `href="/log" aria-current="page"`, "nightly-backup", "paused", "started keeping this on", "kept for 400 days",
		`action="/log"`, "Who was reached",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in\n%s", want, page)
		}
	}
	if strings.Contains(page, `id="live"`) {
		t.Error("the page refreshes itself, so a line can move while being read")
	}

	// A check's page has its history, with a way to all of it.
	checkPage := string(ada.ok("GET", "/checks/"+f.checkID, nil))
	for _, want := range []string{`id="history"`, "/log?check=" + f.checkID} {
		if !strings.Contains(checkPage, want) {
			t.Errorf("no %q on the check's page", want)
		}
	}
	// So does one Allstill runs itself.
	active := string(ada.ok("GET", "/checks/"+f.httpCheck(t), nil))
	if !strings.Contains(active, `id="history"`) {
		t.Error("no history on the page of a check Allstill runs")
	}

	// Narrowing keeps to what was asked, and can be undone.
	narrowed := logPageOf(t, ada, "?check="+f.checkID)
	if !strings.Contains(narrowed, "nightly-backup") || !strings.Contains(narrowed, "Stop narrowing") || strings.Contains(narrowed, "customer-sync") {
		t.Errorf("%s", narrowed)
	}

	// A deleted check is found by its name, and its name links nowhere.
	if err := f.st.DeleteCheck(ctx, access, f.checkID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	gone := logPageOf(t, ada, "?name=NIGHTLY-BACKUP")
	if !strings.Contains(gone, "deleted") || strings.Contains(gone, `href="/checks/`+f.checkID) {
		t.Errorf("%s", gone)
	}
}

// A viewer's page holds no name of anyone who acted, however it is asked (R21, R23).
func TestAViewersLogPageNamesNobody(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	editor, err := f.st.CreateMember(ctx, store.Command("test"), f.ws, store.NewPerson{Name: "Grace Hopper"}, []string{f.project}, store.Editor)
	if err != nil {
		t.Fatal(err)
	}
	access, err := f.st.AccessOfPerson(ctx, f.ws, editor)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.st.PauseCheck(ctx, access, f.checkID, now); err != nil {
		t.Fatal(err)
	}
	viewer := f.browser(t)
	f.register(t, viewer, f.bob)
	for _, query := range []string{"", "?check=" + f.checkID, "?person=" + editor, "?what=done", "?what=reached", "?project=" + f.project} {
		page := logPageOf(t, viewer, query)
		if strings.Contains(page, "Grace") || strings.Contains(page, "?person="+editor) {
			t.Errorf("%q: a name:\n%s", query, page)
		}
	}
	if page := logPageOf(t, viewer, ""); !strings.Contains(page, "The team") || !strings.Contains(page, "paused") {
		t.Errorf("%s", page)
	}
	if page := logPageOf(t, viewer, "?person="+editor); !strings.Contains(page, "Nothing matches this") {
		t.Errorf("%s", page)
	}
	// Another project looks the same as one that does not exist (R24).
	seen, unknown := logPageOf(t, viewer, "?project="+url.QueryEscape(f.hiddenProject(t))), logPageOf(t, viewer, "?project=prj_nothing")
	if strings.Contains(seen, "customer-sync") || !strings.Contains(seen, "Nothing matches this") || !strings.Contains(unknown, "Nothing matches this") {
		t.Errorf("%s", seen)
	}
}

func (f fixture) hiddenProject(t *testing.T) string {
	t.Helper()
	id, err := f.st.ProjectByName(t.Context(), f.ws, "Customer")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A long log has a way to older lines, which keeps what was asked (R26, R27).
func TestTheLogPageGoesBack(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	access, _ := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	now := time.Now()
	for i := range 60 {
		var err error
		if i%2 == 0 {
			err = f.st.PauseCheck(ctx, access, f.checkID, now)
		} else {
			err = f.st.ResumeCheck(ctx, access, f.checkID, now)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	page := logPageOf(t, ada, "?check="+f.checkID)
	i := strings.Index(page, ">Older<")
	if i < 0 {
		t.Fatalf("no way back in\n%s", page)
	}
	start := strings.LastIndex(page[:i], `href="`) + len(`href="`)
	href := strings.ReplaceAll(page[start:strings.Index(page[start:], `"`)+start], "&amp;", "&")
	if !strings.Contains(href, "check="+f.checkID) || !strings.Contains(href, "before=") {
		t.Errorf("%s", href)
	}
	if older := string(ada.ok("GET", href, nil)); !strings.Contains(older, "log-row") {
		t.Errorf("%s", older)
	}
}

// A name holding markup is only text.
func TestALogLineEscapes(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	access, _ := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	if _, err := f.st.CreateCheck(ctx, access, f.project, store.NewCheck{Name: "<script>alert(1)</script>", Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if page := logPageOf(t, ada, ""); strings.Contains(page, "<script>alert") {
		t.Errorf("%s", page)
	}
}

func sentenceOf(row logRow) string {
	var b strings.Builder
	for _, p := range row.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Every kind the log writes has a sentence of its own.
func TestEveryKindIsASentence(t *testing.T) {
	ada := store.LogParty{Kind: "person", ID: "per_1", Name: "Ada"}
	by := map[string]store.LogParty{
		"team": {Kind: "team"}, "administrator": {Kind: "administrator"}, "outside": {Kind: "outside"}, "job": {Kind: "job"},
		"allstill": {Kind: "allstill"}, "probe": {Kind: "probe", Name: "inside"}, "key": {Kind: "key", Name: "ci"},
		"command": {Kind: "command", Name: "delete-check"}, "person": ada,
	}
	for _, tc := range []struct {
		kind    string
		details map[string]any
		by      string
		about   string
		want    string
	}{
		{"check.state", map[string]any{"from": "up", "to": "down", "reason": "No ping within the grace period."}, "allstill", "check", "backup.sh went down: no ping within the grace period. By allstill."},
		{"check.state", map[string]any{"from": "down", "to": "up"}, "job", "check", "backup.sh came back up. By a job."},
		{"check.state", map[string]any{"from": "new", "to": "up"}, "probe", "check", "backup.sh is up. By the probe inside."},
		{"check.flapping", nil, "allstill", "check", "backup.sh keeps changing, so its changes are not written until it settles."},
		{"check.steady", map[string]any{"state": "up"}, "allstill", "check", "backup.sh has settled, is up."},
		{"checks.unjudged", map[string]any{"count": 12.0, "from": 0.0, "to": 0.0}, "allstill", "project", "12 checks"},
		{"check.created", nil, "person", "check", "Ada added backup.sh."},
		{"check.changed", map[string]any{"changes": []any{map[string]any{"field": "url"}}}, "team", "check", "The team changed backup.sh: the URL."},
		{"check.changed", map[string]any{"changes": []any{map[string]any{"field": "every", "from": 300.0, "to": 3600.0}}}, "person", "check", "Ada changed backup.sh: the interval from every 5 min to every hour."},
		{"check.changed", map[string]any{"from": "down", "to": "up", "changes": []any{map[string]any{"field": "cron", "to": "0 3 * * *"}}}, "person", "check", "Ada changed backup.sh: the cron schedule to 0 3 * * *. It is up now."},
		{"check.paused", nil, "administrator", "check", "An administrator paused backup.sh."},
		{"check.resumed", nil, "job", "check", "A job resumed backup.sh."},
		{"check.deleted", nil, "command", "check", "A command on the server (delete-check) deleted backup.sh."},
		{"check.ping_url_replaced", nil, "person", "check", "Ada replaced the ping URL of backup.sh."},
		{"check.moved_out", nil, "person", "check", "Ada moved backup.sh to another project."},
		{"check.moved_out", map[string]any{"to_project": "Other"}, "person", "check", "Ada moved backup.sh to Other."},
		{"check.moved_in", map[string]any{"state": "down", "incident_open": true}, "person", "check", "Ada moved backup.sh here from another project. It arrived down, with an incident open."},
		{"check.moved_in", map[string]any{"state": "warning"}, "person", "check", "It arrived with a warning."},
		{"message.reached", map[string]any{"what": "resolved_unseen", "how": "digest"}, "allstill", "check", "Bea was told backup.sh was resolved before they heard of it, in the digest."},
		{"message.reached", map[string]any{"what": "still_open", "how": "digest"}, "allstill", "check", "Bea was told backup.sh has an incident still open, in the digest."},
		{"project.created", nil, "command", "project", "made the project backup.sh."},
		{"project.renamed", map[string]any{"from": "A", "to": "B"}, "command", "project", "renamed the project A to B."},
		{"person.added", nil, "command", "person", "added Bea."},
		{"member.set", map[string]any{"to": "editor"}, "command", "person", "made Bea an editor of this project."},
		{"member.set", map[string]any{"from": "viewer", "to": "owner"}, "command", "person", "made Bea an owner instead of a viewer."},
		{"member.removed", nil, "command", "person", "took Bea out of this project."},
		{"passkey.link_made", nil, "command", "person", "made a passkey link for Bea."},
		{"passkey.added", nil, "person", "person", "Ada added a passkey."},
		{"passkey.looks_copied", nil, "person", "person", "A passkey of Bea looks copied, so the sign-in was refused."},
		{"signin", nil, "person", "person", "Ada signed in."},
		{"channel.confirmed", map[string]any{"channel": "email"}, "person", "channel", "Ada confirmed an email address."},
		{"channel.handed_on", map[string]any{"channel": "webpush"}, "person", "channel", "Ada took over a device from someone else."},
		{"channel.handed_away", map[string]any{"channel": "webpush"}, "allstill", "channel", "Bea lost a device to someone else."},
		{"channel.removed", map[string]any{"channel": "webpush"}, "allstill", "channel", "Allstill removed a device of Bea, as it no longer subscribes."},
		{"channel.removed", map[string]any{"channel": "email"}, "person", "channel", "Ada removed an email address."},
		{"probe.created", nil, "command", "probe", "made the probe backup.sh."},
		{"probe.rekeyed", nil, "command", "probe", "gave the probe backup.sh a new key."},
		{"alerts.mode", map[string]any{"to": "on"}, "command", "installation", "turned alerts on."},
		{"alerts.mode", map[string]any{"to": "shadow"}, "command", "installation", "put alerts back in shadow mode."},
		{"message.reached", map[string]any{"what": "down", "how": "now", "channel": "webpush"}, "allstill", "check", "Bea was told backup.sh is down, at once, by push."},
		{"message.reached", map[string]any{"count": 2.0, "how": "digest", "channel": "email"}, "allstill", "person", "Bea got a message about 2 things, in the digest, by email."},
		{"message.shadow", map[string]any{"what": "recovered", "how": "now"}, "allstill", "check", "Bea would have been told backup.sh has recovered. Shadow mode: recorded, not sent."},
		{"message.shadow", map[string]any{"count": 2.0, "how": "digest"}, "allstill", "person", "Bea would have got a message. Shadow mode: recorded, not sent."},
		{"message.reached_several", map[string]any{"count": 4.0, "how": "digest", "channel": "email"}, "allstill", "project", "Bea got one message about 4 checks, in the digest, by email."},
		{"message.shadow_several", map[string]any{"count": 4.0, "how": "digest"}, "allstill", "project", "Bea would have got one message about 4 checks."},
		{"message.unreached", map[string]any{"why": "the push service refused it"}, "allstill", "check", "Bea could not be reached about backup.sh: the push service refused it."},
		{"message.unreached", nil, "allstill", "check", "Bea could not be reached about backup.sh."},
	} {
		e := store.LogEntry{
			Kind: tc.kind, At: time.Unix(1000, 0), Recorded: time.Unix(1000, 0), AboutKind: tc.about, AboutName: "backup.sh", By: by[tc.by],
			Person: store.LogParty{Kind: "person", ID: "per_2", Name: "Bea"}, Details: tc.details, Group: "grp_1",
		}
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		rows := logRows([]store.LogEntry{e}, viewer{Loc: time.UTC}, time.Unix(2000, 0), nil, false)
		got := sentenceOf(rows[0])
		if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.want)) {
			t.Errorf("%s: %q, want %q", tc.kind, got, tc.want)
		}
	}
	// One this version does not know shows what it has.
	rows := logRows([]store.LogEntry{{Kind: "something.new", At: time.Unix(1, 0), Recorded: time.Unix(1, 0), By: by["allstill"], AboutName: "x"}}, viewer{Loc: time.UTC}, time.Unix(2, 0), nil, false)
	if got := sentenceOf(rows[0]); got != "Allstill: something new, x." {
		t.Errorf("%q", got)
	}
}

// A line that arrived late says so, and days are headed, in the reader's own zone.
func TestALineSaysWhenItArrived(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	happened := time.Date(2026, 11, 2, 22, 50, 0, 0, time.UTC)
	rows := logRows([]store.LogEntry{
		{Kind: "check.state", At: happened, Recorded: happened.Add(20 * time.Minute), Details: map[string]any{"to": "down"}, By: store.LogParty{Kind: "probe", Name: "p"}},
		{Kind: "check.state", At: happened.Add(-48 * time.Hour), Recorded: happened.Add(-48 * time.Hour), Details: map[string]any{"to": "up"}, By: store.LogParty{Kind: "job"}},
	}, viewer{Loc: ams}, happened.Add(time.Hour), nil, false)
	if rows[0].Time != "23:50" || rows[0].Day != "Monday 2 November 2026" || !strings.Contains(rows[0].Note, "reported at") {
		t.Errorf("%+v", rows[0])
	}
	if rows[1].Note != "" || rows[1].Day == "" {
		t.Errorf("%+v", rows[1])
	}
}

// A line says a state's shape only when it is about that state: being told a check is
// down is no green dot (R31), and an edit that changed the state shows it.
func TestAShapeBelongsToItsState(t *testing.T) {
	line := func(kind string, d map[string]any) string {
		rows := logRows([]store.LogEntry{{Kind: kind, At: time.Unix(1, 0), Recorded: time.Unix(1, 0), AboutKind: "check", AboutName: "x", Details: d}}, viewer{Loc: time.UTC}, time.Unix(2, 0), nil, false)
		return rows[0].Class
	}
	for _, c := range []struct {
		kind string
		d    map[string]any
		want string
	}{
		{"message.reached", map[string]any{"what": "down"}, "quiet"},
		{"message.reached_several", map[string]any{"count": 2.0}, "quiet"},
		{"check.changed", map[string]any{"from": "down", "to": "up"}, "up"},
		{"check.changed", map[string]any{}, "quiet"},
		{"check.state", map[string]any{"to": "down"}, "bad"},
	} {
		if got := line(c.kind, c.d); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.kind, c.d, got, c.want)
		}
	}
}

// An editor reads an administrator from outside the project as just that, on the page
// too (R22); the administrator reads their own name.
func TestAnEditorsLogPageNamesNoOutsider(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, err := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.PauseCheck(ctx, admin, f.checkID, time.Now()); err != nil {
		t.Fatal(err)
	}
	page := logPageOf(t, f.member(t, store.Editor), "?check="+f.checkID)
	if !strings.Contains(page, "An administrator") || strings.Contains(page, "?person="+f.ada) || strings.Contains(page, ">Ada<") {
		t.Errorf("%s", page)
	}
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	if page := logPageOf(t, ada, "?check="+f.checkID); !strings.Contains(page, ">Ada<") {
		t.Errorf("%s", page)
	}
}
