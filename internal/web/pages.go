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
	Summary   string       // every state and its count, in words
	Health    []healthPart // the same, drawn as one bar
	Updated   string
	Attention []card
	List      string // what the list shows: all checks, a view, a project
	Shown     int    // how many checks the list shows
	Groups    []group
	Empty     string
	Calm      bool   // the list is empty because all is well, not for a filter
	Clear     string // the list without the search
	Needs     string // what the list of what needs someone is called
	Add       string // where to add a check, for someone who may
	AddFirst  bool   // the list is empty, and adding a check is the next step
	Import    bool   // the list is empty for an administrator, who can import checks
	Notice    *notice
}

// healthPart is one state's share of every check, in the bar across the top
// of the overview: X and W are percentages of its width.
type healthPart struct {
	Label, Class string
	N            int
	X, W         float64
}

type card struct {
	Href, Class, Title, Line, Note string
}

type group struct {
	Name   string
	Checks []row
}

type row struct {
	Href, Name, Sub, Last, Value, Next string
	Schedule, ScheduleNote             string // the time zone and grace go in the note
	Look                               look
	Bars                               []bar
	BarsLabel                          string
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

	// Someone who may only look cannot act and gets no alerts: nothing "needs" them.
	mayAct := len(editableProjects(v)) > 0
	d := overviewData{Updated: clock(now, now, v.Loc), Needs: "Needs attention", Add: addLink(v, project), Notice: s.noticeFor(r, "")}
	if mayAct {
		d.Needs = "Needs you"
	}
	counts := map[check.State]int{}
	waitingToRun := 0 // new checks that Allstill runs, rather than jobs ping
	for _, c := range checks {
		counts[c.Status.State]++
		if c.Status.State == check.New && store.Active(c.Kind) {
			waitingToRun++
		}
		if needsAttention(c.Status.State) {
			d.Attention = append(d.Attention, attentionCard(c, now, v.Loc))
		}
	}
	// Down before late.
	slices.SortStableFunc(d.Attention, func(a, b card) int { return rank[b.Class] - rank[a.Class] })
	// What may keep alerts from reaching the person comes first: it silences
	// everything else.
	alerts, err := s.alertCards(r.Context(), v, now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	checkCards := len(d.Attention)
	d.Attention = append(alerts, d.Attention...)
	// The server cannot judge its checks: they are unknown, not down
	// (intent/http-checks/spec.md, R7).
	if s.Offline != nil {
		if since, off := s.Offline(); off {
			d.Attention = append([]card{{Href: "/", Class: "bad", Title: "Allstill is offline for its checks",
				Line: "Since " + clock(now, since, v.Loc) + " · its own network does not answer, so the checks it runs are unknown"}}, d.Attention...)
		}
	}
	nav := navOf(checks, v, "overview", show, project)

	switch {
	case len(checks) == 0:
		d.Headline = "No checks yet"
	case checkCards == 1 && mayAct:
		d.Headline = "1 check needs you"
	case checkCards > 1 && mayAct:
		d.Headline = fmt.Sprintf("%d checks need you", checkCards)
	case checkCards == 1:
		d.Headline = "1 check needs attention"
	case checkCards > 1:
		d.Headline = fmt.Sprintf("%d checks need attention", checkCards)
	case len(alerts) > 0:
		d.Headline = "Alerts may not reach you"
	// A check that never pinged cannot go down, so it is not known to be well.
	case counts[check.New] == 1 && waitingToRun == 1:
		d.Headline = "1 check is not checked yet"
	case counts[check.New] == 1:
		d.Headline = "1 check has not pinged yet"
	case counts[check.New] > 1 && waitingToRun == counts[check.New]:
		d.Headline = fmt.Sprintf("%d checks are not checked yet", counts[check.New])
	case counts[check.New] > 1 && waitingToRun > 0:
		d.Headline = fmt.Sprintf("%d checks have not reported yet", counts[check.New])
	case counts[check.New] > 1:
		d.Headline = fmt.Sprintf("%d checks have not pinged yet", counts[check.New])
	default:
		d.Headline = "All is well"
	}
	parts := []string{plural(len(checks), "check", "checks")}
	x := 0.0
	// The ones that need someone first, so the bar reads from the left.
	for _, st := range []check.State{check.Down, check.Late, check.Warning, check.Up, check.Unknown, check.New, check.Paused} {
		if counts[st] == 0 {
			continue
		}
		l := lookOf(st)
		parts = append(parts, fmt.Sprintf("%d %s", counts[st], strings.ToLower(l.Label)))
		w := 100 * float64(counts[st]) / float64(len(checks))
		d.Health = append(d.Health, healthPart{Label: strings.ToLower(l.Label), Class: l.Class, N: counts[st], X: x, W: w})
		x += w
	}
	d.Summary = strings.Join(parts, ", ")

	d.Clear = overviewLink(show, project)
	d.List = "All checks"
	switch show {
	case "attention":
		d.List = "Needs attention"
	case "paused":
		d.List = "Paused"
	}
	for _, p := range v.Access.Projects {
		if project != "" && project != p.ID {
			continue
		}
		if project != "" {
			d.List = p.Name
		}
		g := group{Name: p.Name}
		for _, c := range checks {
			if c.ProjectID != p.ID || !matches(c, show, query) {
				continue
			}
			g.Checks = append(g.Checks, rowOf(c, now, v.Loc))
		}
		if len(g.Checks) > 0 {
			d.Groups = append(d.Groups, g)
			d.Shown += len(g.Checks)
		}
	}
	if len(d.Groups) == 0 {
		switch {
		// A new installation needs a shell once: projects are made there (C11).
		case len(v.Access.Projects) == 0 && v.Access.Admin:
			d.Empty = "There is no project yet. Make the first on the server, and add checks to it here: allstill admin create-project Backups"
		case len(checks) == 0 && !mayAct:
			d.Empty = "There are no checks here for you yet. Your administrator adds them, or gives you access to a project."
		case len(checks) == 0:
			d.Empty, d.AddFirst = "Nothing is watched yet. Add your first check: a job that reports in, a website or a port.", true
			d.Import = v.Access.Admin
		case query != "":
			d.Empty = fmt.Sprintf("No check matches “%s”.", query)
		// Unknown checks and what keeps alerts from reaching the person
		// need no one to act on a check, but all is not known to be well.
		case show == "attention" && len(d.Attention) == 0 && counts[check.Unknown] == 0:
			d.Empty, d.Calm = "Nothing "+strings.ToLower(d.Needs)+": every check is up, new or paused.", true
		case show == "attention":
			d.Empty = "No check is down, late or in warning."
		default:
			d.Empty = "No checks here."
			d.AddFirst = project != "" && show == "" && strings.Contains(d.Add, "project=")
		}
	}
	// The tab says how many need someone, so it shows among other tabs.
	title := "Checks"
	if checkCards > 0 {
		title = fmt.Sprintf("(%d) Checks", checkCards)
	}
	s.render(w, r, http.StatusOK, "overview", page{Title: title, Person: v.Name, Search: query, Nav: nav, Live: true, Data: d})
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

// scheduleOf says when a check is due, and below it its time zone and grace.
func scheduleOf(c store.CheckView) (schedule, note string) {
	if c.Cron != "" {
		return c.Cron, c.TimeZone + " · grace " + span(c.Grace)
	}
	return every(c.Interval), "grace " + span(c.Grace)
}

func rowOf(c store.CheckView, now time.Time, loc *time.Location) row {
	r := row{
		Href: "/checks/" + c.ID, Name: c.Name, Look: lookOf(c.Status.State),
		Last: ago(now, c.Status.LastPing), Value: "—",
	}
	r.Schedule, r.ScheduleNote = scheduleOf(c)
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
	r.Bars, r.BarsLabel = barsOf(c, now, loc)
	// Every row has as many slots, so the newest line up on the right.
	if n := store.RecentRuns - len(r.Bars); n > 0 {
		r.Bars = append(slices.Repeat([]bar{{Class: "none"}}, n), r.Bars...)
	}
	if store.Active(c.Kind) {
		return activeRow(r, c, now, loc)
	}
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
	C         store.CheckView
	Look      look
	Since     string
	Incident  string
	Kind      string // how it reports, or that Allstill runs it
	Updated   string
	HasValues bool // a listed run sent values, so the runs show a column for them
	Tiles     []tile
	Schedule  []pair
	Chart     *chart
	Runs      []runRow
	Log       *runLog
	NoOutput  string
	PingURL   string
	SlugURL   string
	Curl      string
	Example   string
	Connect   bool // new, and nothing has pinged it: how to connect a job comes first (R3)
	Notice    *notice
	Manage    *manageLinks
}

type tile struct{ Label, Value, Note string }
type pair struct {
	K, V string
	Mono bool
}

type chart struct {
	Title    string
	Label    string
	Bars     []chartBar
	From, To string
	Max, Mid string // the scale: the longest, and half of it
	Width    int
	Height   int
	longest  time.Duration
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
	d := checkData{C: c, Look: lookOf(c.Status.State), Updated: clock(now, now, v.Loc), Notice: s.noticeFor(r, c.ID)}
	if d.Manage, err = s.manageOf(r.Context(), v, c); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Since = "since " + clock(now, c.Status.Since, v.Loc)
	d.Kind = "Heartbeat"
	if c.Cron != "" {
		d.Kind = "Cron"
	}
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
	if store.Active(c.Kind) {
		s.activePage(w, r, v, c, d)
		return
	}
	// A ping URL is a secret: viewers, often outsiders who only need a
	// status, do not see it (intent/http-checks/spec.md, R12).
	if whole(v, c.ProjectID) {
		d.PingURL = s.base.String() + "/ping/" + c.PingUUID
		if c.Slug != "" {
			d.SlugURL = s.base.String() + "/ping/<ping key>/" + c.Slug
		}
		d.Curl = "curl -fsS -m 10 --retry 3 " + d.PingURL
		d.Example = "ALLSTILL_URL=" + d.PingURL + " allstill run -- ./backup.sh"
		d.Connect = c.Status.State == check.New && len(c.Recent) == 0 && !d.Manage.Own
	}

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
	} else if c.Status.State == check.New {
		next.Value, next.Note = "—", "waits for its first ping"
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
		d.HasValues = d.HasValues || len(run.Values) > 0
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
	nav, err := s.nav(r.Context(), v, c.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{c.ProjectName, overviewLink("", c.ProjectID)}, {c.Name, ""}}
	s.render(w, r, http.StatusOK, "check", page{Title: c.Name, Person: v.Name, Crumbs: crumbs, Nav: nav, Live: true, Data: d})
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
	// As many slots as a check view holds runs, so a bar is as wide on
	// every check, and the newest is on the right.
	const width, height, gap = 600, 140, 4
	slots := max(store.RecentRuns, len(timed))
	w := (width - gap*(slots-1)) / slots
	offset := (slots - len(timed)) * (w + gap)
	ch := &chart{
		Title: "Run time", Width: width, Height: height, longest: longest,
		From: clock(now, timed[0].Started, loc), To: clock(now, timed[len(timed)-1].Started, loc),
		Max: span(longest), Mid: span(longest / 2),
		Label: fmt.Sprintf("Run time of the last %d runs, the longest %s", len(timed), span(longest)),
	}
	for i, r := range timed {
		h := max(2, int(float64(height)*float64(r.Duration())/float64(longest)))
		ch.Bars = append(ch.Bars, chartBar{
			X: offset + i*(w+gap), Y: height - h, W: w, H: h, Class: outcomeLook(r.Outcome).Class,
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
