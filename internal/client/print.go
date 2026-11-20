// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf16"

	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/logtext"
)

// What a person reads. Everything from a server goes through Clean on its way out, so
// a monitored site or a typed name cannot steer the terminal (R19).

// PrintJSON writes what a server said as JSON again, indented: whatever came, what
// could steer a terminal stays escaped, as the encoder alone leaves some of that as
// it is. A script still reads the same text.
func PrintJSON(w io.Writer, raw []byte) error {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return fmt.Errorf("the server's answer is not JSON")
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		return err
	}
	var safe strings.Builder
	for _, c := range out.String() {
		// A line break here is the indenting's: one in a string is escaped already.
		if c == '\n' || !steers(c) {
			safe.WriteRune(c)
			continue
		}
		for _, unit := range utf16.Encode([]rune{c}) {
			fmt.Fprintf(&safe, "\\u%04x", unit)
		}
	}
	_, err := io.WriteString(w, safe.String())
	return err
}

// PrintItems writes a list gathered over several pages as the API's own shape.
func PrintItems(w io.Writer, items []json.RawMessage) error {
	if items == nil {
		items = []json.RawMessage{}
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return err
	}
	return PrintJSON(w, raw)
}

// countOrder says what is shown first, as what most needs someone.
var countOrder = []string{"down", "late", "warning", "unknown", "new", "up", "paused"}

// countWords says how many checks are in each state, in that order, and any state
// this version does not know after them.
func countWords(counts apiclient.StateCounts) string {
	var parts []string
	for _, state := range countOrder {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, state))
		}
	}
	var others []string
	for state, n := range counts {
		if n > 0 && !slices.Contains(countOrder, state) {
			others = append(others, fmt.Sprintf("%d %s", n, Clean(state)))
		}
	}
	sort.Strings(others)
	parts = append(parts, others...)
	if len(parts) == 0 {
		return "no checks"
	}
	return strings.Join(parts, ", ")
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

// PrintStatus says how things stand, what needs attention first.
func PrintStatus(w io.Writer, server string, s *apiclient.Status, now time.Time, loc *time.Location) {
	total := 0
	for _, n := range s.Counts {
		total += n
	}
	checks := logtext.Plural(total, "check", "checks")
	switch {
	case len(s.Attention) > 0:
		verb := "need"
		if len(s.Attention) == 1 {
			verb = "needs"
		}
		fmt.Fprintf(w, "%d of %s %s attention on %s.\n", len(s.Attention), checks, verb, server)
	case s.Counts["unknown"] > 0:
		fmt.Fprintf(w, "Nothing needs attention on %s, but %d of %s cannot be judged.\n", server, s.Counts["unknown"], checks)
	default:
		fmt.Fprintf(w, "All still on %s: %s, and nothing needs attention.\n", server, checks)
	}
	if len(s.Attention) > 0 {
		fmt.Fprintln(w)
		t := table(w)
		for _, c := range s.Attention {
			line := fmt.Sprintf("  %s\t%s\t%s\tsince %s", Clean(string(c.State)), Clean(c.Name), Clean(c.ProjectName), logtext.Clock(now, c.StateSince, loc))
			if c.StateReason != nil {
				line += "\t" + Clean(*c.StateReason)
			}
			fmt.Fprintln(t, line)
		}
		t.Flush()
	}
	if len(s.Projects) > 0 {
		fmt.Fprintln(w)
		t := table(w)
		for _, p := range s.Projects {
			fmt.Fprintf(t, "%s\t%s\n", Clean(p.Name), countWords(p.Counts))
		}
		t.Flush()
	}
}

// PrintProjects lists projects, with what the caller is in each.
func PrintProjects(w io.Writer, projects []apiclient.Project) {
	if len(projects) == 0 {
		fmt.Fprintln(w, "You are in no project yet. An administrator can add you to one.")
		return
	}
	t := table(w)
	fmt.Fprintln(t, "PROJECT\tYOU ARE\tTOLD\tCHECKS\tID")
	for _, p := range projects {
		told := "-"
		if p.Alerts != nil && *p.Alerts {
			told = "yes"
		} else if p.Alerts != nil {
			told = "no"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", Clean(ProjectLabel(p, projects)), Clean(string(p.Role)), told, countWords(p.Counts), Clean(p.Id))
	}
	t.Flush()
}

// ProjectLabel is a project's name in a list: where another project in it has the
// same name, the team comes before it, for a team the caller is in.
func ProjectLabel(p apiclient.Project, among []apiclient.Project) string {
	shared := 0
	for _, o := range among {
		if o.Name == p.Name && (o.Team == nil || p.Team == nil || o.Team.Id != p.Team.Id) {
			shared++
		}
	}
	if shared > 0 && p.Team != nil {
		return p.Team.Name + " / " + p.Name
	}
	return p.Name
}

// scheduleWords says when a check is due or tried.
func scheduleWords(c apiclient.Check) string {
	if c.Schedule == nil {
		return ""
	}
	if cron, err := c.Schedule.AsCronSchedule(); err == nil && cron.Expression != "" {
		return Clean(cron.Expression) + " (" + Clean(cron.TimeZone) + ")"
	}
	if interval, err := c.Schedule.AsIntervalSchedule(); err == nil && interval.Every > 0 {
		return logtext.Every(time.Duration(interval.Every) * time.Second)
	}
	return ""
}

// addressWords says what a check looks at, as far as the caller may know.
func addressWords(c apiclient.Check) string {
	switch {
	case c.Http != nil:
		return Clean(c.Http.Url)
	case c.Tcp != nil:
		return Clean(c.Tcp.Host) + ":" + strconv.Itoa(c.Tcp.Port)
	}
	return ""
}

// CheckOf is a check with the name of its project, for a list across projects.
type CheckOf struct {
	apiclient.Check
	Project string
}

// PrintChecks lists checks.
func PrintChecks(w io.Writer, checks []CheckOf, now time.Time, loc *time.Location) {
	if len(checks) == 0 {
		fmt.Fprintln(w, "No checks here that you can see.")
		return
	}
	t := table(w)
	fmt.Fprintln(t, "STATE\tCHECK\tPROJECT\tSINCE\tWHAT")
	for _, c := range checks {
		what := addressWords(c.Check)
		if what == "" {
			what = scheduleWords(c.Check)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", Clean(string(c.State)), Clean(c.Name), Clean(c.Project), logtext.Clock(now, c.StateSince, loc), what)
	}
	t.Flush()
}

// PrintCheck shows one check.
func PrintCheck(w io.Writer, c *apiclient.Check, project string, now time.Time, loc *time.Location) {
	t := table(w)
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(t, "%s\t%s\n", label, value)
		}
	}
	optional := func(s *string) string {
		if s == nil {
			return ""
		}
		return Clean(*s)
	}
	when := func(at *time.Time) string {
		if at == nil {
			return ""
		}
		return logtext.Clock(now, *at, loc)
	}
	line("Check", Clean(c.Name))
	line("Id", Clean(c.Id))
	line("Project", Clean(project))
	line("Kind", Clean(string(c.Kind)))
	line("State", Clean(string(c.State))+" since "+logtext.Clock(now, c.StateSince, loc))
	line("Why", optional(c.StateReason))
	line("Address", addressWords(*c))
	line("Schedule", scheduleWords(*c))
	if c.Grace != nil && c.Http == nil && c.Tcp == nil {
		line("Grace", logtext.Span(time.Duration(*c.Grace)*time.Second))
	}
	line("Runs on", optional(c.ProbeName))
	line("Last ping", when(c.LastPingAt))
	line("Next due", when(c.NextExpectedAt))
	if c.Alerting != nil && c.Alerting.Urgency != nil {
		urgency := Clean(string(*c.Alerting.Urgency))
		if c.Alerting.Critical != nil && *c.Alerting.Critical {
			urgency += ", critical"
		}
		line("Alerts", urgency)
	}
	if c.Tags != nil {
		tags := make([]string, len(*c.Tags))
		for i, tag := range *c.Tags {
			tags[i] = Clean(tag)
		}
		line("Tags", strings.Join(tags, ", "))
	}
	line("Description", optional(c.Description))
	line("Slug", optional(c.Slug))
	line("Created", logtext.Clock(now, c.CreatedAt, loc))
	t.Flush()
}

// PrintRuns lists a check's runs.
func PrintRuns(w io.Writer, runs []apiclient.Run, now time.Time, loc *time.Location) {
	if len(runs) == 0 {
		fmt.Fprintln(w, "No runs yet.")
		return
	}
	t := table(w)
	fmt.Fprintln(t, "WHEN\tRESULT\tTOOK\tDETAILS")
	for _, r := range runs {
		at := r.StartedAt
		if at == nil {
			at = r.EndedAt
		}
		when := ""
		if at != nil {
			when = logtext.Clock(now, *at, loc)
		}
		took := ""
		if r.RunTime != nil {
			took = logtext.Span(time.Duration(*r.RunTime) * time.Second)
		}
		var details []string
		if r.ExitStatus != nil {
			details = append(details, "exit "+strconv.Itoa(*r.ExitStatus))
		}
		if r.Values != nil {
			names := make([]string, 0, len(*r.Values))
			for name := range *r.Values {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				details = append(details, Clean(name)+" "+strconv.FormatFloat((*r.Values)[name], 'f', -1, 64))
			}
		}
		if r.Reason != nil {
			details = append(details, Clean(*r.Reason))
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", when, Clean(string(r.Outcome)), took, strings.Join(details, " · "))
	}
	t.Flush()
}

func partyOf(p *apiclient.LogParty) logtext.Party {
	if p == nil {
		return logtext.Party{}
	}
	out := logtext.Party{Kind: p.Kind}
	if p.Id != nil {
		out.ID = *p.Id
	}
	if p.Name != nil {
		out.Name = *p.Name
	}
	return out
}

// LogLine is an entry in the words the dashboard uses. A kind this version does not
// know says only what kind it is.
func LogLine(e apiclient.LogEntry, now time.Time, loc *time.Location) string {
	in := logtext.Entry{Kind: e.Kind, By: partyOf(&e.By), Person: partyOf(e.Person), Details: map[string]any{}}
	if e.AboutKind != nil {
		in.AboutKind = *e.AboutKind
	}
	if e.AboutName != nil {
		in.AboutName = *e.AboutName
	}
	if e.Group != nil {
		in.Group = *e.Group
	}
	if e.Details != nil {
		in.Details = *e.Details
	}
	return Clean(logtext.Write(in, now, loc).String())
}

// PrintLog lists a page of the log, newest first, as the dashboard words it.
func PrintLog(w io.Writer, page *apiclient.LogPage, now time.Time, loc *time.Location) {
	if len(page.Items) == 0 {
		fmt.Fprintln(w, "Nothing matches this.")
		if page.StartedAt != nil {
			fmt.Fprintf(w, "Allstill began keeping this on %s, and nothing from before that is here.\n", page.StartedAt.In(loc).Format("2 January 2006"))
		}
	} else {
		t := table(w)
		for _, e := range page.Items {
			project := ""
			if e.ProjectName != nil {
				project = Clean(*e.ProjectName)
			}
			fmt.Fprintf(t, "%s\t%s\t%s\n", logtext.Clock(now, e.At, loc), project, LogLine(e, now, loc))
		}
		t.Flush()
	}
	if page.NextCursor != nil {
		fmt.Fprintf(w, "\nThere is more. Add -before %s to read on.\n", Clean(*page.NextCursor))
	}
}

// PrintProbes lists probes, with the projects they serve by name.
func PrintProbes(w io.Writer, probes []apiclient.Probe, projects []apiclient.Project) {
	if len(probes) == 0 {
		fmt.Fprintln(w, "No probes serve a project you may edit.")
		return
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.Id] = p.Name
	}
	t := table(w)
	fmt.Fprintln(t, "PROBE\tSERVES\tID")
	for _, p := range probes {
		fmt.Fprintf(t, "%s\t%s\t%s\n", Clean(p.Name), Clean(names[p.ProjectId]), Clean(p.Id))
	}
	t.Flush()
}

// SameVersion says two versions are one, as a release's tag starts with a v and its
// image's name does not.
func SameVersion(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}
