// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/outbound"
	"github.com/MartijnMul/allstill/internal/store"
)

// A check Allstill runs itself (intent/http-checks/spec.md, R8): its page and
// row show its attempts, their duration and reason, and the certificate's
// days left. Its URL, and where it must redirect, are secrets: whole for the
// project's editors and owners and for administrators, the scheme and host
// for everyone else (R12).

// whole says whether the viewer may see a check's secrets whole: its ping
// URL, the URL it fetches, and where that must redirect.
func whole(v viewer, projectID string) bool {
	role, _ := v.Access.Role(projectID)
	return role == store.Editor || role == store.Owner
}

type activeData struct {
	C        store.CheckView
	Look     look
	Since    string
	Incident string
	Kind     string
	Updated  string
	Tiles    []tile
	What     []pair
	Chart    *chart
	Runs     []attemptRow
	Secret   bool // the viewer sees only the scheme and host
}

type attemptRow struct {
	Started, Took, Status, Certificate, Reason string
	Look                                       look
}

// settingsOf reads an HTTP check's settings; the store checked them when
// they were saved.
func settingsOf(c store.CheckView) active.HTTP {
	h, _ := active.ParseHTTP(c.Settings)
	return h
}

func (s *Server) activePage(w http.ResponseWriter, r *http.Request, v viewer, c store.CheckView, d checkData) {
	now := s.Now()
	h := settingsOf(c)
	a := activeData{C: c, Look: d.Look, Since: d.Since, Incident: d.Incident, Updated: d.Updated, Kind: strings.ToUpper(c.Kind), Secret: !whole(v, c.ProjectID)}

	var last store.RunView
	if len(c.Recent) > 0 {
		last = c.Recent[0]
	}
	a.Tiles = append(a.Tiles, tile{Label: "Last attempt", Value: clock(now, c.Status.LastPing, v.Loc), Note: ago(now, c.Status.LastPing)})
	if last.ID != "" {
		t := tile{Label: "Last result", Value: outcomeLook(last.Outcome).Label, Note: last.Reason}
		if code, ok := last.Values["status"]; ok {
			t.Note = strings.TrimPrefix(t.Note+" · HTTP "+strconv.Itoa(int(code)), " · ")
		}
		if took := last.Duration(); took > 0 {
			t.Note += " · took " + millis(took)
		}
		a.Tiles = append(a.Tiles, t)
		if days, ok := last.Values["cert_days"]; ok {
			a.Tiles = append(a.Tiles, tile{Label: "Certificate", Value: daysLeft(days), Note: "until it expires"})
		}
	}
	next := tile{Label: "Next attempt", Value: until(now, c.Status.NextExpected, v.Loc)}
	switch {
	case c.Status.State == check.Paused:
		next.Value, next.Note = "—", "paused"
	case !c.Status.Deadline.IsZero():
		next.Note = "unknown if none by " + clock(now, c.Status.Deadline, v.Loc)
	}
	a.Tiles = append(a.Tiles, next)

	url, location := h.URL, h.Location
	if a.Secret {
		url, location = active.Shown(url), active.Shown(location)
	}
	method := h.Method
	if method == "" {
		method = "GET"
	}
	a.What = []pair{{"URL", url, true}, {"Request", method + " · timeout " + span(timeoutOf(h)), false}, {"Up when", expectsOf(h), false}}
	if h.Location != "" {
		a.What = append(a.What, pair{"Redirects to", location, true})
	}
	if h.Follow {
		a.What = append(a.What, pair{"Redirects", fmt.Sprintf("followed, at most %d", outbound.MaxRedirects), false})
	}
	a.What = append(a.What,
		pair{"Interval", every(c.Interval), false},
		pair{"Down after", plural(failAfter(h), "failed attempt", "failed attempts in a row"), false})
	if strings.HasPrefix(h.URL, "https:") {
		a.What = append(a.What, pair{"Certificate", certificateOf(h), false})
	}
	if len(c.Tags) > 0 {
		a.What = append(a.What, pair{"Tags", strings.Join(c.Tags, ", "), false})
	}
	a.What = append(a.What, pair{"Created", clock(now, c.Created, v.Loc), false})

	if ch := chartOf(c.Recent, now, v.Loc); ch != nil {
		ch.Label = strings.Replace(ch.Label, "Run time of the last", "Response time of the last", 1)
		ch.Label = strings.Replace(ch.Label, " runs, ", " attempts, ", 1)
		ch.Title, ch.Max, ch.Mid = "Response time", millis(ch.longest), millis(ch.longest/2)
		a.Chart = ch
	}
	for i, run := range c.Recent {
		if i == listedRuns {
			break
		}
		row := attemptRow{Look: outcomeLook(run.Outcome), Started: clock(now, run.At(), v.Loc), Took: "—", Status: "—", Certificate: "—", Reason: run.Reason}
		if took := run.Duration(); took > 0 {
			row.Took = millis(took)
		}
		if code, ok := run.Values["status"]; ok {
			row.Status = strconv.Itoa(int(code))
		}
		if days, ok := run.Values["cert_days"]; ok {
			row.Certificate = daysLeft(days)
		}
		a.Runs = append(a.Runs, row)
	}
	nav, err := s.nav(r.Context(), v, c.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{c.ProjectName, overviewLink("", c.ProjectID)}, {c.Name, ""}}
	s.render(w, r, http.StatusOK, "active", page{Title: c.Name, Person: v.Name, Crumbs: crumbs, Nav: nav, Live: true, Data: a})
}

// activeRow fills in an overview row for a check Allstill runs itself.
func activeRow(r row, c store.CheckView, now time.Time, loc *time.Location) row {
	h := settingsOf(c)
	host, _ := h.Host()
	r.Sub = strings.ToUpper(c.Kind) + " · " + host
	// Its grace follows from its interval, so it is not shown.
	r.Schedule, r.ScheduleNote = every(c.Interval), ""
	var parts []string
	if len(c.Recent) > 0 {
		last := c.Recent[0]
		if code, ok := last.Values["status"]; ok {
			parts = append(parts, strconv.Itoa(int(code)))
		}
		if took := last.Duration(); took > 0 {
			parts = append(parts, millis(took))
		}
		if days, ok := last.Values["cert_days"]; ok {
			parts = append(parts, "cert "+daysLeft(days))
		}
	}
	if len(parts) > 0 {
		r.Value = strings.Join(parts, " · ")
	}
	if c.Status.State == check.Down {
		// Down, it is still tried in its time.
		r.Next = until(now, c.Status.NextExpected, loc)
	}
	return r
}

// millis says how long an attempt took: in milliseconds under ten seconds.
func millis(d time.Duration) string {
	if d < 10*time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
	}
	return span(d)
}

// daysLeft says how long a certificate has: in hours under two days.
func daysLeft(days float64) string {
	switch {
	case days <= 0:
		return "expired"
	case days < 2:
		return plural(int(days*24), "hour", "hours")
	}
	return plural(int(days), "day", "days")
}

func timeoutOf(h active.HTTP) time.Duration {
	if h.Timeout == 0 {
		return active.DefaultTimeout
	}
	return time.Duration(h.Timeout) * time.Second
}

func failAfter(h active.HTTP) int {
	if h.FailAfter == 0 {
		return store.DefaultFailAfter
	}
	return h.FailAfter
}

func expectsOf(h active.HTTP) string {
	var s string
	switch {
	case len(h.Expect) > 0:
		codes := make([]string, len(h.Expect))
		for i, c := range h.Expect {
			codes[i] = strconv.Itoa(c)
		}
		s = "HTTP " + strings.Join(codes, ", ")
	case h.Location != "":
		s = "a redirect (3xx)"
	default:
		s = "HTTP 2xx"
	}
	if h.JSON != nil {
		s += fmt.Sprintf(" and %s is %s", strings.TrimPrefix(h.JSON.Pointer, "/"), h.JSON.Equals)
	}
	return s
}

func certificateOf(h active.HTTP) string {
	days := func(p *int, def int) int {
		if p == nil {
			return def
		}
		return *p
	}
	warn, fail := days(h.WarnDays, active.DefaultWarnDays), days(h.FailDays, active.DefaultFailDays)
	var parts []string
	if h.NoVerify {
		parts = append(parts, "not verified")
	} else if h.CA != "" {
		parts = append(parts, "verified with its own CA")
	}
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("warns under %d days", warn))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("fails under %d days", fail))
	}
	if warn == 0 && fail == 0 {
		parts = append(parts, "its expiry is not watched")
	}
	return strings.Join(parts, " · ")
}
