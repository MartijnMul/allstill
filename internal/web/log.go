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

	"github.com/MartijnMul/allstill/internal/logtext"
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
		row := logRow{Time: at.Format("15:04")}
		if d := at.Format("Monday 2 January 2006"); d != day {
			row.Day, day = d, d
		}
		if showProject {
			row.Project = e.ProjectName
		}
		if e.Recorded.Sub(e.At) > time.Minute {
			row.Note = "reported at " + clock(now, e.Recorded, v.Loc)
		}
		line := logtext.Write(textOf(e), now, v.Loc)
		row.Class = line.Class
		if line.Group != "" {
			row.Open = "/log?group=" + url.QueryEscape(line.Group)
		}
		for _, p := range line.Parts {
			part := logPart{Text: p.Text, Name: p.Name}
			switch {
			case p.Check && visible[e.AboutID]:
				part.Href = "/checks/" + url.PathEscape(e.AboutID)
			case p.PersonID != "":
				part.Href = "/log?person=" + url.QueryEscape(p.PersonID)
			}
			row.Parts = append(row.Parts, part)
		}
		rows = append(rows, row)
	}
	return rows
}

// textOf is an entry as the log's wording takes it.
func textOf(e store.LogEntry) logtext.Entry {
	return logtext.Entry{
		Kind: e.Kind, AboutKind: e.AboutKind, AboutName: e.AboutName, Group: e.Group, Details: e.Details,
		By:     logtext.Party{Kind: e.By.Kind, ID: e.By.ID, Name: e.By.Name},
		Person: logtext.Party{Kind: e.Person.Kind, ID: e.Person.ID, Name: e.Person.Name},
	}
}
