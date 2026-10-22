// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

type overviewData struct {
	Headline  string
	Summary   string
	Attention []card
	Views     []navItem
	Projects  []navItem
	Groups    []group
	Empty     string
}

type card struct {
	Href, Class, Title, Line, Note string
}

type navItem struct {
	Name, Href, Class string
	Count             int
	Current           bool
}

type group struct {
	Name   string
	Checks []row
}

type row struct {
	Href, Name, Sub, Schedule, Last, Value, Next string
	Look                                         look
	Bars                                         []look
	BarsLabel                                    string
}

// GET /: every check the person may see, the ones that need them first.
func (s *Server) overview(w http.ResponseWriter, r *http.Request, v viewer) {
	checks, err := s.Store.Checks(r.Context(), v.Access)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.Now()
	show, project := r.URL.Query().Get("show"), r.URL.Query().Get("project")
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	d := overviewData{}
	counts := map[check.State]int{}
	attention, paused := 0, 0
	perProject := map[string][]store.CheckView{}
	for _, c := range checks {
		counts[c.Status.State]++
		perProject[c.ProjectID] = append(perProject[c.ProjectID], c)
		if needsAttention(c.Status.State) {
			attention++
			d.Attention = append(d.Attention, attentionCard(c, now, v.Loc))
		}
		if c.Status.State == check.Paused {
			paused++
		}
	}
	// Down before late.
	slices.SortStableFunc(d.Attention, func(a, b card) int { return rank[b.Class] - rank[a.Class] })

	link := func(show, project string) string {
		q := []string{}
		if show != "" {
			q = append(q, "show="+show)
		}
		if project != "" {
			q = append(q, "project="+project)
		}
		if len(q) == 0 {
			return "/"
		}
		return "/?" + strings.Join(q, "&")
	}
	d.Views = []navItem{
		{Name: "All checks", Href: link("", ""), Count: len(checks), Current: show == "" && project == ""},
		{Name: "Needs attention", Href: link("attention", ""), Count: attention, Current: show == "attention"},
		{Name: "Paused", Href: link("paused", ""), Count: paused, Current: show == "paused"},
	}
	for _, p := range v.Access.Projects {
		worst := look{Class: "up"}
		for _, c := range perProject[p.ID] {
			worst = worse(worst, lookOf(c.Status.State))
		}
		if len(perProject[p.ID]) == 0 {
			worst.Class = "quiet"
		}
		d.Projects = append(d.Projects, navItem{
			Name: p.Name, Href: link("", p.ID), Class: worst.Class, Count: len(perProject[p.ID]), Current: project == p.ID,
		})
	}

	switch {
	case len(checks) == 0:
		d.Headline = "No checks yet"
	case len(d.Attention) == 1:
		d.Headline = "1 check needs you"
	case len(d.Attention) > 1:
		d.Headline = fmt.Sprintf("%d checks need you", len(d.Attention))
	// A check that never pinged cannot go down, so it is not known to be well.
	case counts[check.New] == 1:
		d.Headline = "1 check has not pinged yet"
	case counts[check.New] > 1:
		d.Headline = fmt.Sprintf("%d checks have not pinged yet", counts[check.New])
	default:
		d.Headline = "All is well"
	}
	parts := []string{plural(len(checks), "check", "checks")}
	for _, st := range []check.State{check.Up, check.Late, check.Warning, check.Down, check.Unknown, check.New, check.Paused} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], strings.ToLower(lookOf(st).Label)))
		}
	}
	d.Summary = strings.Join(parts, " · ")

	for _, p := range v.Access.Projects {
		if project != "" && project != p.ID {
			continue
		}
		g := group{Name: p.Name}
		for _, c := range perProject[p.ID] {
			if !matches(c, show, query) {
				continue
			}
			g.Checks = append(g.Checks, rowOf(c, now, v.Loc))
		}
		if len(g.Checks) > 0 {
			d.Groups = append(d.Groups, g)
		}
	}
	if len(d.Groups) == 0 {
		switch {
		case len(checks) == 0:
			d.Empty = "Import your checks from Healthchecks with allstill import healthchecks, or ping a new slug with ?create=1 to create it."
		case query != "":
			d.Empty = fmt.Sprintf("No check matches “%s”.", query)
		default:
			d.Empty = "No checks here."
		}
	}
	s.render(w, r, http.StatusOK, "overview", page{Title: "Checks", Person: v.Name, Search: query, Data: d})
}

// rank orders looks from calm to urgent.
var rank = map[string]int{"up": 0, "quiet": 1, "paused": 1, "warn": 2, "bad": 3}

func worse(a, b look) look {
	if rank[b.Class] > rank[a.Class] {
		return b
	}
	return a
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func matches(c store.CheckView, show, query string) bool {
	switch show {
	case "attention":
		if !needsAttention(c.Status.State) {
			return false
		}
	case "paused":
		if c.Status.State != check.Paused {
			return false
		}
	}
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	for _, f := range append([]string{c.Name, c.Slug, c.ProjectName, c.Description}, c.Tags...) {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func attentionCard(c store.CheckView, now time.Time, loc *time.Location) card {
	l := lookOf(c.Status.State)
	k := card{Href: "/checks/" + c.ID, Class: l.Class, Title: c.Name + " is " + strings.ToLower(l.Label)}
	k.Line = "Since " + clock(now, c.Status.Since, loc) + " · " + c.ProjectName
	if c.Status.Reason != "" {
		k.Note = c.Status.Reason
	}
	return k
}

func scheduleOf(c store.CheckView) string {
	if c.Cron != "" {
		return fmt.Sprintf("%s · %s · grace %s", c.Cron, c.TimeZone, span(c.Grace))
	}
	return fmt.Sprintf("%s · grace %s", every(c.Interval), span(c.Grace))
}

func rowOf(c store.CheckView, now time.Time, loc *time.Location) row {
	r := row{
		Href: "/checks/" + c.ID, Name: c.Name, Look: lookOf(c.Status.State),
		Schedule: scheduleOf(c), Last: ago(now, c.Status.LastPing), Value: "—",
	}
	r.Sub = "Cron"
	if c.Cron == "" {
		r.Sub = "Heartbeat"
	}
	if c.Slug != "" {
		r.Sub += " · " + c.Slug
	}
	switch c.Status.State {
	case check.Paused:
		r.Next = "—"
	case check.Down, check.Late:
		r.Next = "overdue"
	default:
		r.Next = until(now, c.Status.NextExpected, loc)
	}
	counts := map[string]int{}
	// Oldest on the left, as time runs.
	for i := len(c.Recent) - 1; i >= 0; i-- {
		l := outcomeLook(c.Recent[i].Outcome)
		r.Bars = append(r.Bars, l)
		counts[l.Class]++
	}
	r.BarsLabel = fmt.Sprintf("Last %d runs: %d ok, %d with a warning, %d failed", len(c.Recent), counts["up"], counts["warn"], counts["bad"])
	for _, run := range c.Recent {
		if len(run.Values) > 0 {
			r.Value = values(run.Values)
			break
		}
	}
	return r
}

// listedRuns is how many runs a check's page lists; its chart shows more.
const listedRuns = 10

type checkData struct {
	C        store.CheckView
	Look     look
	Since    string
	Incident string
	Tiles    []tile
	Schedule []pair
	Chart    *chart
	Runs     []runRow
	Log      *runLog
	NoOutput string
	PingURL  string
	SlugURL  string
	Example  string
}

type tile struct{ Label, Value, Note string }
type pair struct {
	K, V string
	Mono bool
}

type chart struct {
	Label    string
	Bars     []chartBar
	From, To string
	Width    int
	Height   int
}

type chartBar struct {
	X, Y, W, H int
	Class      string
	Title      string
}

type runRow struct {
	Href, Started, Took, Exit, Values string
	Look                              look
	Current                           bool
}

type runLog struct {
	Title     string
	Text      string
	Truncated bool
}

// GET /checks/{id}: one check, its recent runs, and the output of one run.
func (s *Server) checkPage(w http.ResponseWriter, r *http.Request, v viewer) {
	c, err := s.Store.Check(r.Context(), v.Access, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.Now()
	d := checkData{C: c, Look: lookOf(c.Status.State)}
	d.Since = "since " + clock(now, c.Status.Since, v.Loc)
	inc, err := s.Store.OpenIncident(r.Context(), c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if inc != nil {
		d.Incident = "An incident is open since " + clock(now, inc.OpenedAt, v.Loc) + "."
		if !inc.RecoveringSince.IsZero() {
			d.Incident += " It is back since " + clock(now, inc.RecoveringSince, v.Loc) +
				", and closes at " + clock(now, inc.RecoveringSince.Add(alert.Settle), v.Loc) + " if it stays so."
		}
	}
	d.PingURL = s.base.String() + "/ping/" + c.PingUUID
	if c.Slug != "" {
		d.SlugURL = s.base.String() + "/ping/<ping key>/" + c.Slug
	}
	d.Example = "ALLSTILL_URL=" + d.PingURL + " allstill run -- ./backup.sh"

	var last store.RunView
	for _, run := range c.Recent {
		if run.Outcome != "running" {
			last = run
			break
		}
	}
	lastTile := tile{Label: "Last ping", Value: clock(now, c.Status.LastPing, v.Loc), Note: ago(now, c.Status.LastPing)}
	d.Tiles = append(d.Tiles, lastTile)
	if last.ID != "" {
		t := tile{Label: "Last run", Value: outcomeLook(last.Outcome).Label}
		if took := last.Duration(); took > 0 {
			t.Note = "took " + span(took)
		}
		if last.ExitStatus >= 0 {
			t.Note = strings.TrimPrefix(t.Note+" · exit "+fmt.Sprint(last.ExitStatus), " · ")
		}
		d.Tiles = append(d.Tiles, t)
		if len(last.Values) > 0 {
			d.Tiles = append(d.Tiles, tile{Label: "Values", Value: values(last.Values)})
		}
	}
	next := tile{Label: "Next expected", Value: until(now, c.Status.NextExpected, v.Loc)}
	if c.Status.State == check.Paused {
		next.Value, next.Note = "—", "paused until the next ping"
	} else if !c.Status.Deadline.IsZero() {
		next.Note = "counts until " + clock(now, c.Status.Deadline, v.Loc)
	}
	d.Tiles = append(d.Tiles, next)

	if c.Cron != "" {
		d.Schedule = []pair{{"Cron", c.Cron, true}, {"Time zone", c.TimeZone, false}}
	} else {
		d.Schedule = []pair{{"Interval", every(c.Interval), false}}
	}
	d.Schedule = append(d.Schedule, pair{"Grace", span(c.Grace), false})
	if c.Slug != "" {
		d.Schedule = append(d.Schedule, pair{"Slug", c.Slug, true})
	}
	if len(c.Tags) > 0 {
		d.Schedule = append(d.Schedule, pair{"Tags", strings.Join(c.Tags, ", "), false})
	}
	d.Schedule = append(d.Schedule, pair{"Created", clock(now, c.Created, v.Loc), false})

	d.Chart = chartOf(c.Recent, now, v.Loc)

	selected := r.URL.Query().Get("run")
	if selected == "" && len(c.Recent) > 0 {
		selected = c.Recent[0].ID
	}
	for i, run := range c.Recent {
		if i == listedRuns {
			break
		}
		rr := runRow{
			Href: "/checks/" + c.ID + "?run=" + run.ID, Look: outcomeLook(run.Outcome),
			Started: clock(now, firstOf(run.Started, run.Ended), v.Loc), Took: "—", Exit: "—",
			Values: values(run.Values), Current: run.ID == selected,
		}
		if took := run.Duration(); took > 0 {
			rr.Took = span(took)
		} else if run.Outcome == "running" {
			rr.Took = span(now.Sub(run.Started)) + " so far"
		}
		if run.ExitStatus >= 0 {
			rr.Exit = fmt.Sprint(run.ExitStatus)
		}
		d.Runs = append(d.Runs, rr)
	}
	if selected != "" {
		pings, err := s.Store.RunLog(r.Context(), v.Access, c.ID, selected)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Log = logOf(pings, now, v.Loc)
		// Output is kept for the newest runs only.
		keep := s.Store.Keeps().Output
		d.NoOutput = fmt.Sprintf("No output for this run: none was sent, or the run is older than the last %d, whose output Allstill keeps.", keep)
		for i, run := range c.Recent {
			if run.ID == selected {
				if i < keep {
					d.NoOutput = "No output was sent with this run."
				} else {
					d.NoOutput = fmt.Sprintf("No output for this run: Allstill keeps the output of the last %d runs.", keep)
				}
			}
		}
	}
	crumbs := []crumb{{c.ProjectName, "/?project=" + c.ProjectID}, {c.Name, ""}}
	s.render(w, r, http.StatusOK, "check", page{Title: c.Name, Person: v.Name, Crumbs: crumbs, Data: d})
}

func firstOf(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// chartOf draws how long the recent runs took, oldest on the left. Runs
// without a start ping have no run time, so a heartbeat gets no chart.
func chartOf(runs []store.RunView, now time.Time, loc *time.Location) *chart {
	var timed []store.RunView
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Duration() > 0 {
			timed = append(timed, runs[i])
		}
	}
	if len(timed) < 2 {
		return nil
	}
	longest := time.Duration(0)
	for _, r := range timed {
		longest = max(longest, r.Duration())
	}
	const width, height, gap = 600, 140, 4
	w := (width - gap*(len(timed)-1)) / len(timed)
	ch := &chart{
		Width: width, Height: height,
		From: clock(now, timed[0].Started, loc), To: clock(now, timed[len(timed)-1].Started, loc),
		Label: fmt.Sprintf("Run time of the last %d runs, the longest %s", len(timed), span(longest)),
	}
	for i, r := range timed {
		h := max(2, int(float64(height)*float64(r.Duration())/float64(longest)))
		ch.Bars = append(ch.Bars, chartBar{
			X: i * (w + gap), Y: height - h, W: w, H: h, Class: outcomeLook(r.Outcome).Class,
			Title: clock(now, r.Started, loc) + ": " + span(r.Duration()),
		})
	}
	return ch
}

// logOf shows a run's pings, each with its time and kind, then its body.
func logOf(pings []store.PingView, now time.Time, loc *time.Location) *runLog {
	l := &runLog{Title: "Output"}
	var b strings.Builder
	for _, p := range pings {
		fmt.Fprintf(&b, "%s  %s", p.Received.In(loc).Format("15:04:05"), p.Kind)
		if p.ExitStatus >= 0 {
			fmt.Fprintf(&b, "  exit %d", p.ExitStatus)
		}
		b.WriteString("\n")
		if len(p.Body) > 0 {
			b.WriteString(strings.ToValidUTF8(string(p.Body), "�"))
			if !strings.HasSuffix(b.String(), "\n") {
				b.WriteString("\n")
			}
		}
		l.Truncated = l.Truncated || p.BodyTruncated
	}
	if len(pings) > 0 {
		l.Title = "Output of the run at " + clock(now, pings[0].Received, loc)
	}
	l.Text = strings.TrimSuffix(b.String(), "\n")
	return l
}
