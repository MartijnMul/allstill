// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

// logRow is one line of the log as a sentence, per reader: what a reader may not see
// of an entry was dropped before it got here.
type logRow struct {
	Day     string // set on the first line of a day
	Time    string
	Class   string // the state shape it starts with
	Parts   []logPart
	Project string // shown unless the list is of one project
	Note    string
	Open    string // what the line stands for, when it stands for several
}

// logPart is a piece of a sentence: plain text, or a name, perhaps a link.
type logPart struct {
	Text string
	Href string
	Name bool
}

type logOption struct {
	Value, Label string
	Selected     bool
}

type logChip struct{ Label, Clear string }

type logData struct {
	Rows     []logRow
	Name     string
	Late     bool
	Projects []logOption
	Whats    []logOption
	Keep     []logOption // what the filter form keeps as it is
	Chips    []logChip
	Older    string
	About    string
	Nothing  string
}

// logHistory is the section on a check's page.
type logHistory struct {
	Rows []logRow
	All  string
}

// historyLines is how many lines a check's page shows.
const historyLines = 5

func (s *Server) history(ctx context.Context, v viewer, checkID string) (logHistory, error) {
	page, err := s.Store.ReadLog(ctx, v.Access, store.LogFilter{CheckID: checkID, Limit: historyLines})
	if err != nil {
		return logHistory{}, err
	}
	return logHistory{
		Rows: logRows(page.Entries, v, s.Now(), nil, false),
		All:  "/log?check=" + url.QueryEscape(checkID),
	}, nil
}

// GET /log: what happened, when and by what, newest first
// (intent/activity-log/spec.md).
func (s *Server) logPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	in := r.URL.Query()
	f := store.LogFilter{
		CheckID: in.Get("check"), ProjectID: in.Get("project"), PersonID: in.Get("person"),
		Name: strings.TrimSpace(in.Get("name")), What: in.Get("what"), Group: in.Get("group"),
	}
	f.Before, _ = strconv.ParseInt(in.Get("before"), 10, 64)
	f.Late = in.Get("late") == "1"
	switch f.What {
	case "happened", "done", "reached", "signins":
	default:
		f.What = ""
	}
	entries, err := s.Store.ReadLog(ctx, v.Access, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	checks, err := s.Store.Checks(ctx, v.Access)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	visible := make(map[string]bool, len(checks))
	for _, c := range checks {
		visible[c.ID] = true
	}
	now := s.Now()
	d := logData{Rows: logRows(entries.Entries, v, now, visible, f.ProjectID == ""), Name: f.Name, Late: f.Late}
	d.Projects = []logOption{{"", "All projects", f.ProjectID == ""}}
	for _, p := range v.Access.Projects {
		d.Projects = append(d.Projects, logOption{p.ID, p.Name, p.ID == f.ProjectID})
	}
	for _, o := range []logOption{
		{Value: "", Label: "Everything"}, {Value: "happened", Label: "What happened"}, {Value: "done", Label: "What was done"},
		{Value: "reached", Label: "Who was reached"}, {Value: "signins", Label: "Sign-ins"},
	} {
		o.Selected = o.Value == f.What
		d.Whats = append(d.Whats, o)
	}
	without := func(key string) string {
		q := r.URL.Query()
		q.Del(key)
		q.Del("before")
		if len(q) == 0 {
			return "/log"
		}
		return "/log?" + q.Encode()
	}
	if f.CheckID != "" {
		label := "One check"
		if len(entries.Entries) > 0 {
			label = entries.Entries[0].AboutName
		}
		d.Chips = append(d.Chips, logChip{label, without("check")})
		d.Keep = append(d.Keep, logOption{Value: "check", Label: f.CheckID})
	}
	if f.PersonID != "" {
		d.Chips = append(d.Chips, logChip{personNamed(entries.Entries, f.PersonID), without("person")})
		d.Keep = append(d.Keep, logOption{Value: "person", Label: f.PersonID})
	}
	if f.Group != "" {
		d.Chips = append(d.Chips, logChip{"What one line stands for", without("group")})
		d.Keep = append(d.Keep, logOption{Value: "group", Label: f.Group})
	}
	if entries.Older != 0 {
		q := r.URL.Query()
		q.Set("before", strconv.FormatInt(entries.Older, 10))
		d.Older = "/log?" + q.Encode()
	}
	if !entries.Started.IsZero() {
		d.About = fmt.Sprintf("Allstill started keeping this on %s, and nothing from before that is here. Lines are kept for %d days.",
			entries.Started.In(v.Loc).Format("2 January 2006"), int(entries.KeptFor/(24*time.Hour)))
	}
	if len(d.Rows) == 0 {
		switch {
		case d.Older != "":
			d.Nothing = "Nothing in this stretch; there is more further back."
		case len(in) > 0:
			d.Nothing = "Nothing matches this."
		default:
			d.Nothing = "Nothing here yet."
		}
		if !f.Late && (f.What == "" || f.What == "happened") && len(in) > 0 {
			d.Nothing += " Tick the box above to see checks that were briefly late."
		}
	}
	nav, err := s.nav(ctx, v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "log", page{Title: "Log", Person: v.Name, Crumbs: []crumb{{"Log", ""}}, Here: "log", Nav: nav, Data: d})
}

func personNamed(entries []store.LogEntry, id string) string {
	for _, e := range entries {
		if e.By.ID == id && e.By.Name != "" {
			return e.By.Name
		}
		if e.Person.ID == id && e.Person.Name != "" {
			return e.Person.Name
		}
	}
	return "One person"
}

// logRows writes entries as sentences. visible is which checks can be linked to, as
// one that went or moved away cannot; nil leaves them all unlinked, for a check's own
// page.
func logRows(entries []store.LogEntry, v viewer, now time.Time, visible map[string]bool, showProject bool) []logRow {
	rows := make([]logRow, 0, len(entries))
	day := ""
	for _, e := range entries {
		at := e.At.In(v.Loc)
		row := logRow{Time: at.Format("15:04"), Class: "quiet"}
		if d := at.Format("Monday 2 January 2006"); d != day {
			row.Day, day = d, d
		}
		if showProject {
			row.Project = e.ProjectName
		}
		if e.Recorded.Sub(e.At) > time.Minute {
			row.Note = "reported at " + clock(now, e.Recorded, v.Loc)
		}
		w := logWriter{e: e, loc: v.Loc, now: now, visible: visible, row: &row}
		w.write()
		rows = append(rows, row)
	}
	return rows
}

type logWriter struct {
	e       store.LogEntry
	loc     *time.Location
	now     time.Time
	visible map[string]bool
	row     *logRow
}

func (w logWriter) say(parts ...logPart) { w.row.Parts = append(w.row.Parts, parts...) }

func text(s string) logPart { return logPart{Text: s} }

// check names what an entry is about, linked when it is still there to see.
func (w logWriter) check() logPart {
	p := logPart{Text: w.e.AboutName, Name: true}
	if w.visible[w.e.AboutID] {
		p.Href = "/checks/" + url.PathEscape(w.e.AboutID)
	}
	return p
}

// signInWords says what a sign-in from the command line is, for its line.
func signInWords(e store.LogEntry) string {
	if detailString(e.Details, "scope") == store.ScopeRead {
		return e.AboutName + ", to read only"
	}
	return e.AboutName
}

// party names who acted or whom an entry concerns, at the start of a sentence.
func party(p store.LogParty) logPart {
	switch p.Kind {
	case "person":
		if p.Name == "" {
			return text("Someone")
		}
		return logPart{Text: p.Name, Name: true, Href: "/log?person=" + url.QueryEscape(p.ID)}
	case "team":
		return text("The team")
	case "administrator":
		return text("An administrator")
	case "outside":
		return text("Someone outside this project")
	case "job":
		return text("A job")
	case "allstill":
		return text("Allstill")
	case "probe":
		return text("The probe " + p.Name)
	case "key":
		return text("The key " + p.Name)
	case "command":
		return text("A command on the server (" + p.Name + ")")
	}
	return text("Someone")
}

func detailString(d map[string]any, key string) string {
	s, _ := d[key].(string)
	return s
}

func detailCount(d map[string]any, key string) int {
	n, _ := d[key].(float64)
	return int(n)
}

var stateWords = map[string]string{
	"up": "is up", "down": "went down", "late": "is late", "warning": "has a warning",
	"paused": "is paused", "unknown": "could not be judged", "new": "is new",
}

var stateClass = map[string]string{
	"up": "up", "down": "bad", "late": "warn", "warning": "warn", "paused": "paused", "unknown": "quiet", "new": "quiet",
}

var saidWords = map[string]string{
	"down": "is down", "recovered": "has recovered", "still_open": "has an incident still open",
	"resolved_unseen": "was resolved before they heard of it", "closed_deleted": "was closed by being deleted",
	"closed_paused": "was closed by being paused", "silent_check": "cannot alert",
	"paused_check": "is paused", "warning": "has a warning",
}

var channelWords = map[string]string{"webpush": "by push", "email": "by email"}

func howWords(d map[string]any) string {
	how := "at once"
	if detailString(d, "how") == "digest" {
		how = "in the digest"
	}
	if c := channelWords[detailString(d, "channel")]; c != "" {
		how += ", " + c
	}
	return how
}

func (w logWriter) write() {
	e, d := w.e, w.e.Details
	by := party(e.By)
	if via := detailString(d, "via"); via != "" {
		by.Text += ", with " + via
	}
	switch e.Kind {
	case "check.state", "check.late":
		to, from := detailString(d, "to"), detailString(d, "from")
		w.row.Class = stateClass[to]
		words := stateWords[to]
		if to == "up" && (from == "down" || from == "late" || from == "warning") {
			words = "came back up"
		}
		w.say(w.check(), text(" "+words))
		if r := detailString(d, "reason"); r != "" {
			w.say(text(": " + strings.TrimSuffix(lowerFirst(r), ".")))
		}
		w.say(text(". By "), lowerLabel(by), text("."))
	case "check.flapping":
		w.row.Class = "warn"
		w.say(w.check(), text(" keeps changing, so its changes are not written until it settles."))
	case "check.steady":
		w.say(w.check(), text(" has settled"))
		if s := detailString(d, "state"); s != "" {
			w.row.Class = stateClass[s]
			w.say(text(", " + strings.TrimPrefix(stateWords[s], "went ")))
		}
		if ms, ok := d["since"].(float64); ok {
			w.say(text(". Its changes since " + clock(w.now, time.UnixMilli(int64(ms)), w.loc) + " were not written"))
		}
		w.say(text("."))
	case "checks.unjudged":
		from, _ := d["from"].(float64)
		to, _ := d["to"].(float64)
		w.say(text("Nothing was judged between " + clock(w.now, time.UnixMilli(int64(from)), w.loc) + " and " +
			clock(w.now, time.UnixMilli(int64(to)), w.loc) + ": " + plural(detailCount(d, "count"), "check", "checks") + "."))
		w.row.Open = "/log?group=" + url.QueryEscape(e.Group)
	case "check.created":
		w.say(by, text(" added "), w.check(), text("."))
	case "check.changed":
		w.say(by, text(" changed "), w.check())
		if c := changeWords(d); c != "" {
			w.say(text(": " + c))
		}
		w.say(text("."))
		// One entry for both the change and what it did to the state (R6).
		if to := detailString(d, "to"); to != "" {
			w.row.Class = stateClass[to]
			w.say(text(" It is " + arrivedWords[to] + " now."))
		}
	case "check.paused":
		w.row.Class = "paused"
		w.say(by, text(" paused "), w.check(), text("."))
	case "check.resumed":
		w.say(by, text(" resumed "), w.check(), text("."))
	case "check.deleted":
		w.say(by, text(" deleted "), w.check(), text("."))
	case "check.ping_url_replaced":
		w.say(by, text(" replaced the ping URL of "), w.check(), text("."))
	case "check.moved_out":
		w.say(by, text(" moved "), w.check(), text(" to "+projectOr(d, "to_project")+"."))
	case "check.moved_in":
		w.say(by, text(" moved "), w.check(), text(" here from "+projectOr(d, "from_project")))
		if s := detailString(d, "state"); s != "" {
			w.row.Class = stateClass[s]
			w.say(text(". It arrived " + arrivedWords[s]))
			if d["incident_open"] == true {
				w.say(text(", with an incident open"))
			}
		}
		w.say(text("."))
	case "project.created":
		w.say(by, text(" made the project "), logPart{Text: e.AboutName, Name: true}, text("."))
	case "project.renamed":
		w.say(by, text(" renamed the project "+detailString(d, "from")+" to "+detailString(d, "to")+"."))
	case "person.added":
		w.say(by, text(" added "), party(e.Person), text("."))
	case "member.set":
		if from := detailString(d, "from"); from != "" {
			w.say(by, text(" made "), party(e.Person), text(" "+article(detailString(d, "to"))+" instead of "+article(from)+"."))
		} else {
			w.say(by, text(" made "), party(e.Person), text(" "+article(detailString(d, "to"))+" of this project."))
		}
	case "member.removed":
		w.say(by, text(" took "), party(e.Person), text(" out of this project."))
	case "passkey.link_made":
		w.say(by, text(" made a passkey link for "), party(e.Person), text("."))
	case "passkey.added":
		w.say(by, text(" added a passkey."))
	case "passkey.looks_copied":
		w.row.Class = "bad"
		w.say(text("A passkey of "), party(e.Person), text(" looks copied, so the sign-in was refused."))
	case "signin":
		w.say(by, text(" signed in."))
	case "token.made":
		w.say(by, text(" signed in from "+signInWords(e)+"."))
	case "token.ended":
		w.say(by, text(" ended the sign-in "+e.AboutName+" of "), party(e.Person), text("."))
	case "session.ended":
		w.say(by, text(" ended a browser session of "), party(e.Person), text("."))
	case "passkey.removed":
		w.say(by, text(" removed a passkey of "), party(e.Person), text("."))
	case "channel.confirmed":
		w.say(by, text(" confirmed "+channelNoun(d)+"."))
	case "channel.handed_on":
		w.say(by, text(" took over "+channelNoun(d)+" from someone else."))
	case "channel.handed_away":
		w.say(party(e.Person), text(" lost "+channelNoun(d)+" to someone else."))
	case "channel.removed":
		if e.By.Kind == "allstill" {
			w.say(text("Allstill removed "+channelNoun(d)+" of "), party(e.Person), text(", as it no longer subscribes."))
		} else {
			w.say(by, text(" removed "+channelNoun(d)+"."))
		}
	case "probe.created":
		w.say(by, text(" made the probe "), logPart{Text: e.AboutName, Name: true}, text("."))
	case "probe.rekeyed":
		w.say(by, text(" gave the probe "), logPart{Text: e.AboutName, Name: true}, text(" a new key."))
	case "alerts.mode":
		if detailString(d, "to") == "on" {
			w.say(by, text(" turned alerts on."))
		} else {
			w.say(by, text(" put alerts back in shadow mode."))
		}
	case "message.reached":
		if e.AboutKind != "check" {
			w.say(party(e.Person), text(" got a message about "+plural(detailCount(d, "count"), "thing", "things")+", "+howWords(d)+"."))
			return
		}
		w.say(party(e.Person), text(" was told "), w.check(), text(" "+saidWords[detailString(d, "what")]+", "+howWords(d)+"."))
	case "message.shadow":
		if e.AboutKind != "check" {
			w.say(party(e.Person), text(" would have got a message. Shadow mode: recorded, not sent."))
			return
		}
		w.say(party(e.Person), text(" would have been told "), w.check(), text(" "+saidWords[detailString(d, "what")]+". Shadow mode: recorded, not sent."))
	case "message.reached_several":
		w.say(party(e.Person), text(" got one message about "+plural(detailCount(d, "count"), "check", "checks")+", "+howWords(d)+"."))
		w.row.Open = "/log?group=" + url.QueryEscape(e.Group)
	case "message.shadow_several":
		w.say(party(e.Person), text(" would have got one message about "+plural(detailCount(d, "count"), "check", "checks")+". Shadow mode: recorded, not sent."))
		w.row.Open = "/log?group=" + url.QueryEscape(e.Group)
	case "message.unreached":
		w.row.Class = "warn"
		w.say(party(e.Person), text(" could not be reached about "), w.check())
		if why := detailString(d, "why"); why != "" {
			w.say(text(": " + why))
		}
		w.say(text("."))
	default:
		// A kind this version does not know yet shows what it has.
		w.say(by, text(": "+strings.ReplaceAll(e.Kind, ".", " ")))
		if e.AboutName != "" {
			w.say(text(", "), logPart{Text: e.AboutName, Name: true})
		}
		w.say(text("."))
	}
}

func lowerFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "Allstill") || strings.HasPrefix(s, "HTTP") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// lowerLabel is a party in the middle of a sentence.
func lowerLabel(p logPart) logPart {
	if !p.Name {
		p.Text = lowerFirst(p.Text)
	}
	return p
}

var arrivedWords = map[string]string{
	"up": "up", "down": "down", "late": "late", "warning": "with a warning", "paused": "paused", "unknown": "not judged", "new": "new",
}

func article(role string) string {
	if role == "owner" {
		return "an owner"
	}
	if role == "editor" {
		return "an editor"
	}
	return "a " + role
}

func projectOr(d map[string]any, key string) string {
	if p := detailString(d, key); p != "" {
		return p
	}
	return "another project"
}

func channelNoun(d map[string]any) string {
	if detailString(d, "channel") == "email" {
		return "an email address"
	}
	return "a device"
}

var fieldWords = map[string]string{
	"url": "the URL", "location": "the redirect it expects", "description": "the description", "urgency": "the urgency",
	"critical": "whether it is critical", "probe": "what runs it", "expected_status": "the status it expects",
	"json_field": "the JSON field it reads", "ca": "the certificate authority", "name": "the name", "slug": "the slug",
	"tags": "the tags", "every": "the interval", "cron": "the cron schedule", "time_zone": "the time zone",
	"grace": "the grace period", "fail_after": "how many failures make it down", "method": "the method",
	"follow_redirects": "whether it follows redirects", "timeout": "the timeout", "warn_days": "when the certificate warns",
	"fail_days": "when the certificate fails", "no_verify": "whether it verifies the certificate",
	"host": "the host", "port": "the port", "tls": "whether it uses TLS",
}

// changeWords says what a change changed, with values where the entry has them.
func changeWords(d map[string]any) string {
	changes, _ := d["changes"].([]any)
	var parts []string
	for _, c := range changes {
		m, _ := c.(map[string]any)
		field := detailString(m, "field")
		label := fieldWords[field]
		if label == "" {
			label = "the " + strings.ReplaceAll(field, "_", " ")
		}
		from, to := fieldValue(field, m["from"]), fieldValue(field, m["to"])
		switch {
		case from != "" && to != "":
			label += " from " + from + " to " + to
		case to != "":
			label += " to " + to
		case from != "":
			label += ", which was " + from + ", is gone"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func fieldValue(field string, v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == 0 {
			return ""
		}
		if field == "every" {
			return every(time.Duration(t) * time.Second)
		}
		if field == "grace" {
			return span(time.Duration(t) * time.Second)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ", ")
	}
	return ""
}
