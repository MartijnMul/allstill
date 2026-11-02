// SPDX-License-Identifier: AGPL-3.0-only

// Package logtext says what the log's entries mean, in words, so that the dashboard
// and the command line word them alike (intent/cli-anywhere/spec.md). What a reader
// may not see of an entry was dropped before it got here.
package logtext

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Party is who acted or whom an entry concerns, as far as the reader may know.
type Party struct {
	Kind, ID, Name string
}

// Entry is what a sentence is made from.
type Entry struct {
	Kind      string
	AboutKind string
	AboutName string
	By        Party
	Person    Party
	Group     string
	Details   map[string]any
}

// Part is a piece of a sentence: plain text, or a name. A person's name carries their
// id, and what an entry is about says it is a check, for a page to link.
type Part struct {
	Text     string
	Name     bool
	Check    bool
	PersonID string
}

// Line is an entry as a sentence.
type Line struct {
	Class string // the state shape it starts with
	Parts []Part
	Group string // set when it stands for several, to ask for by
}

// String is the sentence as plain text.
func (l Line) String() string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Write says what an entry means, with times as now and loc make them clearest.
func Write(e Entry, now time.Time, loc *time.Location) Line {
	w := writer{e: e, now: now, loc: loc, line: Line{Class: "quiet"}}
	w.write()
	return w.line
}

type writer struct {
	e    Entry
	now  time.Time
	loc  *time.Location
	line Line
}

func (w *writer) say(parts ...Part) { w.line.Parts = append(w.line.Parts, parts...) }

func text(s string) Part { return Part{Text: s} }

// check names what an entry is about.
func (w *writer) check() Part { return Part{Text: w.e.AboutName, Name: true, Check: true} }

// signInWords says what a sign-in from the command line is, for its line.
func signInWords(e Entry) string {
	if detailString(e.Details, "scope") == "read" {
		return e.AboutName + ", to read only"
	}
	return e.AboutName
}

// party names who acted or whom an entry concerns, at the start of a sentence.
func party(p Party) Part {
	switch p.Kind {
	case "person":
		if p.Name == "" {
			return text("Someone")
		}
		return Part{Text: p.Name, Name: true, PersonID: p.ID}
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

func (w *writer) write() {
	e, d := w.e, w.e.Details
	by := party(e.By)
	if via := detailString(d, "via"); via != "" {
		by.Text += ", with " + via
	}
	switch e.Kind {
	case "check.state", "check.late":
		to, from := detailString(d, "to"), detailString(d, "from")
		w.line.Class = stateClass[to]
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
		w.line.Class = "warn"
		w.say(w.check(), text(" keeps changing, so its changes are not written until it settles."))
	case "check.steady":
		w.say(w.check(), text(" has settled"))
		if s := detailString(d, "state"); s != "" {
			w.line.Class = stateClass[s]
			w.say(text(", " + strings.TrimPrefix(stateWords[s], "went ")))
		}
		if ms, ok := d["since"].(float64); ok {
			w.say(text(". Its changes since " + Clock(w.now, time.UnixMilli(int64(ms)), w.loc) + " were not written"))
		}
		w.say(text("."))
	case "checks.unjudged":
		from, _ := d["from"].(float64)
		to, _ := d["to"].(float64)
		w.say(text("Nothing was judged between " + Clock(w.now, time.UnixMilli(int64(from)), w.loc) + " and " +
			Clock(w.now, time.UnixMilli(int64(to)), w.loc) + ": " + Plural(detailCount(d, "count"), "check", "checks") + "."))
		w.line.Group = e.Group
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
			w.line.Class = stateClass[to]
			w.say(text(" It is " + arrivedWords[to] + " now."))
		}
	case "check.paused":
		w.line.Class = "paused"
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
			w.line.Class = stateClass[s]
			w.say(text(". It arrived " + arrivedWords[s]))
			if d["incident_open"] == true {
				w.say(text(", with an incident open"))
			}
		}
		w.say(text("."))
	case "project.created":
		w.say(by, text(" made the project "), Part{Text: e.AboutName, Name: true}, text("."))
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
		w.line.Class = "bad"
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
		w.say(by, text(" made the probe "), Part{Text: e.AboutName, Name: true}, text("."))
	case "probe.rekeyed":
		w.say(by, text(" gave the probe "), Part{Text: e.AboutName, Name: true}, text(" a new key."))
	case "alerts.mode":
		if detailString(d, "to") == "on" {
			w.say(by, text(" turned alerts on."))
		} else {
			w.say(by, text(" put alerts back in shadow mode."))
		}
	case "message.reached":
		if e.AboutKind != "check" {
			w.say(party(e.Person), text(" got a message about "+Plural(detailCount(d, "count"), "thing", "things")+", "+howWords(d)+"."))
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
		w.say(party(e.Person), text(" got one message about "+Plural(detailCount(d, "count"), "check", "checks")+", "+howWords(d)+"."))
		w.line.Group = e.Group
	case "message.shadow_several":
		w.say(party(e.Person), text(" would have got one message about "+Plural(detailCount(d, "count"), "check", "checks")+". Shadow mode: recorded, not sent."))
		w.line.Group = e.Group
	case "message.unreached":
		w.line.Class = "warn"
		w.say(party(e.Person), text(" could not be reached about "), w.check())
		if why := detailString(d, "why"); why != "" {
			w.say(text(": " + why))
		}
		w.say(text("."))
	default:
		// A kind this version does not know yet shows what it has.
		w.say(by, text(": "+strings.ReplaceAll(e.Kind, ".", " ")))
		if e.AboutName != "" {
			w.say(text(", "), Part{Text: e.AboutName, Name: true})
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
func lowerLabel(p Part) Part {
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
			return Every(time.Duration(t) * time.Second)
		}
		if field == "grace" {
			return Span(time.Duration(t) * time.Second)
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

// Span says how long a duration is, to the precision a person needs.
func Span(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < 10*time.Minute:
		m, s := int(d.Minutes()), int(d.Seconds())%60
		if s == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, s)
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %d min", h, m)
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	if h == 0 {
		return fmt.Sprintf("%d days", days)
	}
	return fmt.Sprintf("%d days %d h", days, h)
}

// Every says how often an interval is.
func Every(d time.Duration) string {
	switch {
	case d == time.Hour:
		return "every hour"
	case d == 24*time.Hour:
		return "every day"
	case d == 7*24*time.Hour:
		return "every week"
	}
	return "every " + Span(d)
}

// Clock says when t is, as briefly as is clear from now: a time today, a
// weekday this week, a date otherwise.
func Clock(now, t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	t, now = t.In(loc), now.In(loc)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case t.After(now.AddDate(0, 0, -6)) && t.Before(now.AddDate(0, 0, 6)):
		return t.Format("Mon 15:04")
	case y1 == y2:
		return t.Format("Mon 2 Jan 15:04")
	}
	return t.Format("2 Jan 2006 15:04")
}

// Plural says how many there are of something.
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
