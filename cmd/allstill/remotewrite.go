// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/client"
)

// Commands that change things from anywhere (intent/cli-anywhere/spec.md, R11, R20 to
// R22). They take the flags of `admin add-check` and `admin set-check`, parsed by the
// same code, so the two cannot disagree. The rules are the server's.

var remoteCheckHelp = `
For all:
  -slug NAME         for a check jobs ping, a name to ping by with the project's key
  -description TEXT  what it watches, for people
  -tags a,b          tags, separated by commas
  -urgency now       how fast its problems reach people: now, digest or none
  -critical          breaks through quiet hours; an owner or administrator turns it on

For a job that reports in:
  -every 1h          how often it is expected
  -cron "0 3 * * *"  or when, with -time-zone Europe/Amsterdam, by default this
                     machine's
  -grace 30m         how long after that before it is down
` + strings.NewReplacer("-json /status=ok  ", "-json-field /status=ok\n                    ", "set-check -probe", "checks change -probe").Replace(checkFlagsHelp)

// As sent, a check's settings are what a request says as a whole, with its probe.
func settingsAs[T any](settings map[string]any, probeID string) (*T, error) {
	with := map[string]any{}
	for k, v := range settings {
		with[k] = v
	}
	if probeID != "" {
		with["probe_id"] = probeID
	}
	raw, err := json.Marshal(with)
	if err != nil {
		return nil, err
	}
	var out T
	return &out, json.Unmarshal(raw, &out)
}

// localTimeZone is this machine's time zone by name, as a cron schedule needs one.
func localTimeZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(link, "zoneinfo/"); ok {
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

func splitTags(text string) []string {
	tags := []string{}
	for _, t := range strings.Split(text, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// commonCheckFlags are what adding and changing a check share, beyond checkFlags.
type commonCheckFlags struct {
	fs                      *flag.FlagSet
	slug, description, tags *string
	urgency, probe          *string
	cron, timeZone          *string
	grace                   *time.Duration
	critical                *bool
	apply                   func(string, map[string]any) (time.Duration, error)
	r                       reading
}

func commonFlags(fs *flag.FlagSet) *commonCheckFlags {
	return &commonCheckFlags{
		fs: fs, r: readingFlags(fs),
		slug:        fs.String("slug", "", "for a check jobs ping, a name to ping by with the project's key"),
		description: fs.String("description", "", "what it watches, for people"),
		tags:        fs.String("tags", "", "tags, separated by commas"),
		urgency:     fs.String("urgency", "", "how fast its problems reach people: now, digest or none"),
		critical:    fs.Bool("critical", false, "break through quiet hours; an owner or administrator turns it on"),
		probe:       fs.String("probe", "", `the probe to run it on, by name or id; "" for the server`),
		cron:        fs.String("cron", "", "for a job that reports in: when it is expected, as a cron expression"),
		timeZone:    fs.String("time-zone", "", "for a job that reports in: the time zone of -cron"),
		grace:       fs.Duration("grace", 0, "for a job that reports in: how long after the expected time before it is down"),
		apply:       checkFlags(fs, "json-field"),
	}
}

// alerting is how fast its problems reach people, when a flag said so.
func (f *commonCheckFlags) alerting() *apiclient.Alerting {
	if !visited(f.fs, "urgency") && !visited(f.fs, "critical") {
		return nil
	}
	out := &apiclient.Alerting{}
	if visited(f.fs, "urgency") {
		urgency := apiclient.AlertingUrgency(*f.urgency)
		out.Urgency = &urgency
	}
	if visited(f.fs, "critical") {
		out.Critical = f.critical
	}
	return out
}

// probeID finds a probe that serves a project, by name or id.
func probeID(ctx context.Context, api *client.Client, name, projectID string) (string, error) {
	probes, _, err := api.Probes(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range probes.Items {
		if p.ProjectId == projectID && (p.Name == name || p.Id == name) {
			return p.Id, nil
		}
	}
	return "", fmt.Errorf("no probe named %q serves this project. allstill probes lists those that do", client.Clean(name))
}

// schedule reads what the flags say of when a job is expected. current is its cron
// schedule's time zone and expression, if it has one.
func (f *commonCheckFlags) schedule(interval time.Duration, currentZone, currentCron string) (*apiclient.Schedule, error) {
	if visited(f.fs, "every") && interval < time.Minute {
		return nil, errors.New("-every must be at least 1m")
	}
	if interval != 0 && *f.cron != "" {
		return nil, errors.New("give -every or -cron, not both")
	}
	if interval != 0 && *f.timeZone != "" {
		return nil, errors.New("-time-zone goes with -cron, not -every")
	}
	var s apiclient.Schedule
	switch {
	case interval != 0:
		if err := s.FromIntervalSchedule(apiclient.IntervalSchedule{Kind: "interval", Every: int64(interval / time.Second)}); err != nil {
			return nil, err
		}
	case *f.cron != "" || *f.timeZone != "":
		cron := *f.cron
		if cron == "" {
			if cron = currentCron; cron == "" {
				return nil, errors.New("-time-zone goes with a cron schedule, and this check runs on an interval")
			}
		}
		zone := *f.timeZone
		if zone == "" {
			if zone = currentZone; zone == "" {
				zone = localTimeZone()
			}
		}
		if err := s.FromCronSchedule(apiclient.CronSchedule{Kind: "cron", Expression: cron, TimeZone: zone}); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	return &s, nil
}

func printRunLine(w io.Writer, pingURL string) {
	url := client.Clean(pingURL)
	// In the environment, as a flag shows the secret to anyone who can list processes.
	fmt.Fprintf(w, "Its ping URL is a secret: anyone who has it can ping this check.\n\n  %s\n\nTo report a job with it:\n\n  ALLSTILL_URL=%s allstill run -- ./backup.sh\n\n", url, url)
}

func checksAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checks add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill checks add -project NAME -name NAME [-http URL | -tcp HOST:PORT] [flags]

Adds a check to a project you may edit, as the form in the dashboard does and under the
same rules. Without -http or -tcp it is a job that reports in, by default once a day with
an hour's grace, and this prints its ping URL, once. With either, Allstill runs it, by
default each minute, and tries the address once to show what it finds.
`+remoteCheckHelp+`
Flags:
`)
		fs.PrintDefaults()
	}
	project := fs.String("project", "", "the project, by name or id; left out when you have just one")
	name := fs.String("name", "", "the check's name")
	f := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -name")
	}
	kind := "ping"
	switch {
	case visited(fs, "http") && visited(fs, "tcp"):
		return errors.New("give one of -http and -tcp")
	case visited(fs, "http"):
		kind = "http"
	case visited(fs, "tcp"):
		kind = "tcp"
	}
	settings := map[string]any{}
	interval, err := f.apply(kind, settings)
	if err != nil {
		return err
	}
	if visited(fs, "grace") && kind != "ping" {
		return errors.New("-grace is for a job that reports in; this one runs on an interval, and its grace follows from that")
	}
	in := apiclient.CheckInput{Name: *name, Kind: apiclient.CheckKind(kind), Alerting: f.alerting()}
	if visited(fs, "slug") {
		in.Slug = f.slug
	}
	if visited(fs, "description") {
		in.Description = f.description
	}
	if visited(fs, "tags") {
		tags := splitTags(*f.tags)
		in.Tags = &tags
	}
	if visited(fs, "grace") {
		grace := int64(*f.grace / time.Second)
		in.Grace = &grace
	}
	if in.Schedule, err = f.schedule(interval, "", ""); err != nil {
		return err
	}
	api, err := f.r.client()
	if err != nil {
		return err
	}
	p, err := api.OneProject(ctx, *project)
	if err != nil {
		return said(api, err)
	}
	onProbe := ""
	if *f.probe != "" {
		if onProbe, err = probeID(ctx, api, *f.probe, p.Id); err != nil {
			return said(api, err)
		}
	}
	switch kind {
	case "http":
		if in.Http, err = settingsAs[apiclient.HttpCheck](settings, onProbe); err != nil {
			return err
		}
	case "tcp":
		if in.Tcp, err = settingsAs[apiclient.TcpCheck](settings, onProbe); err != nil {
			return err
		}
	}
	made, raw, err := api.AddCheck(ctx, p.Id, in)
	if err != nil {
		return said(api, err)
	}
	if *f.r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "Added %s to %s (%s).\n", client.Clean(made.Name), client.Clean(p.Name), client.Clean(made.Id))
	if made.PingUrl != nil {
		fmt.Fprintln(stdout)
		printRunLine(stdout, *made.PingUrl)
		fmt.Fprintln(stdout, "It waits for its first ping.")
	}
	if made.Try != nil {
		client.PrintTry(stdout, made.Try, time.Now(), time.Local)
	}
	if made.SameName != nil && *made.SameName {
		fmt.Fprintln(stdout, "Another check in this project has the same name.")
	}
	return nil
}

// settingsFlags are those that change what a check Allstill runs fetches.
var settingsFlags = []string{"http", "tcp", "tls", "method", "expect", "location", "json-field", "follow", "timeout", "fail-after", "warn-days", "fail-days", "ca", "no-verify"}

func checksChange(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checks change", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill checks change -check NAME [-project NAME] [flags]

Changes a check you may edit, as the form in the dashboard does and under the same rules:
only what you give changes. A new schedule counts from now. A new address must be one
the server may reach, and is tried once. If someone else changed it at the same moment,
nothing is saved, and it says so. An empty -expect, -location, -json-field, -ca, -slug,
-description or -tags removes that; -follow=false and -no-verify=false turn those off.
`+remoteCheckHelp+`
Flags:
`)
		fs.PrintDefaults()
	}
	name := fs.String("check", "", "the check, by name or id")
	project := fs.String("project", "", "the project it is in, by name or id, if two share its name")
	newName := fs.String("new-name", "", "a new name for it")
	f := commonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -check")
	}
	api, err := f.r.client()
	if err != nil {
		return err
	}
	found, _, err := api.FindCheck(ctx, *project, *name)
	if err != nil {
		return said(api, err)
	}
	c, etag, err := api.CheckToChange(ctx, found.Id)
	if err != nil {
		return said(api, err)
	}
	kind := string(c.Kind)
	settings := map[string]any{}
	current := ""
	switch {
	case c.Http != nil:
		if current, err = fillSettings(settings, c.Http); err != nil {
			return err
		}
	case c.Tcp != nil:
		if current, err = fillSettings(settings, c.Tcp); err != nil {
			return err
		}
	}
	interval, err := f.apply(kind, settings)
	if err != nil {
		return err
	}
	if kind != "ping" && (visited(fs, "cron") || visited(fs, "time-zone") || visited(fs, "grace")) {
		return errors.New("-cron, -time-zone and -grace are for a job that reports in; this one runs on an interval, and its grace follows from that")
	}
	var patch apiclient.CheckPatch
	changed := false
	if visited(fs, "new-name") {
		patch.Name, changed = newName, true
	}
	if visited(fs, "slug") {
		patch.Slug, changed = f.slug, true
	}
	if visited(fs, "description") {
		patch.Description, changed = f.description, true
	}
	if visited(fs, "tags") {
		tags := splitTags(*f.tags)
		patch.Tags, changed = &tags, true
	}
	if visited(fs, "grace") {
		grace := int64(*f.grace / time.Second)
		patch.Grace, changed = &grace, true
	}
	cron, zone := cronOf(c)
	if patch.Schedule, err = f.schedule(interval, zone, cron); err != nil {
		return err
	}
	changed = changed || patch.Schedule != nil
	if a := f.alerting(); a != nil {
		patch.Alerting, changed = a, true
	}
	moving := visited(fs, "probe")
	if moving || slices.ContainsFunc(settingsFlags, func(n string) bool { return visited(fs, n) }) {
		if moving {
			current = ""
			if *f.probe != "" {
				if current, err = probeID(ctx, api, *f.probe, c.ProjectId); err != nil {
					return said(api, err)
				}
			}
		}
		switch kind {
		case "http":
			if patch.Http, err = settingsAs[apiclient.HttpCheck](settings, current); err != nil {
				return err
			}
		case "tcp":
			if patch.Tcp, err = settingsAs[apiclient.TcpCheck](settings, current); err != nil {
				return err
			}
		default:
			return errors.New("-probe is for a check Allstill runs itself, and jobs ping this one")
		}
		changed = true
	}
	if !changed {
		fs.Usage()
		return errors.New("give what to change")
	}
	after, raw, err := api.ChangeCheck(ctx, c.Id, etag, patch)
	if err != nil {
		return said(api, err)
	}
	if *f.r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "Changed %s.\n", client.Clean(after.Name))
	if after.Try != nil {
		client.PrintTry(stdout, after.Try, time.Now(), time.Local)
	}
	if patch.Schedule != nil || patch.Grace != nil {
		fmt.Fprintln(stdout, "It counts from now.")
	}
	if after.SameName != nil && *after.SameName {
		fmt.Fprintln(stdout, "Another check in this project has the same name.")
	}
	return nil
}

// fillSettings reads what a check runs into settings, as flags change it, and returns
// its probe apart.
func fillSettings(settings map[string]any, from any) (string, error) {
	raw, err := json.Marshal(from)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return "", err
	}
	probe, _ := settings["probe_id"].(string)
	delete(settings, "probe_id")
	return probe, nil
}

// cronOf is a check's cron expression and time zone, if it has them.
func cronOf(c *apiclient.Check) (string, string) {
	if c.Schedule == nil {
		return "", ""
	}
	s, err := c.Schedule.AsCronSchedule()
	if err != nil || s.Kind != "cron" {
		return "", ""
	}
	return s.Expression, s.TimeZone
}

// oneCheckCommand is a command that does one thing to one check.
func oneCheckCommand(ctx context.Context, name, usageLine, help string, args []string, stdout, stderr io.Writer, more func(*flag.FlagSet), do func(*client.Client, *apiclient.Check, reading) error) error {
	fs := flag.NewFlagSet("checks "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: allstill checks %s\n\n%s\n\nFlags:\n", usageLine, help)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	check := fs.String("check", "", "the check, by name or id")
	project := fs.String("project", "", "the project it is in, by name or id, if two share its name")
	if more != nil {
		more(fs)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -check")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	c, _, err := api.FindCheck(ctx, *project, *check)
	if err != nil {
		return said(api, err)
	}
	return said(api, do(api, c, r))
}

func checksPause(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return oneCheckCommand(ctx, "pause", "pause -check NAME [-project NAME] [-json] [-server URL]",
		"Pauses a check: nothing is expected of it, and an open incident closes without a\nrecovery message. A job's next ping resumes it, and a failure then alerts as usual.",
		args, stdout, stderr, nil, func(api *client.Client, c *apiclient.Check, r reading) error {
			raw, err := api.PauseCheck(ctx, c.Id)
			if err != nil {
				return err
			}
			if *r.json {
				return client.PrintJSON(stdout, raw)
			}
			line := "A job's next ping resumes it."
			if c.Kind != "ping" {
				line = "Allstill does not try it until you resume it."
			}
			fmt.Fprintf(stdout, "Paused %s. %s\n", client.Clean(c.Name), line)
			return nil
		})
}

func checksResume(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return oneCheckCommand(ctx, "resume", "resume -check NAME [-project NAME] [-json] [-server URL]",
		"Resumes a paused check, which starts over as new.",
		args, stdout, stderr, nil, func(api *client.Client, c *apiclient.Check, r reading) error {
			raw, err := api.ResumeCheck(ctx, c.Id)
			if err != nil {
				return err
			}
			if *r.json {
				return client.PrintJSON(stdout, raw)
			}
			if c.State != apiclient.CheckStatePaused {
				fmt.Fprintf(stdout, "%s is not paused.\n", client.Clean(c.Name))
				return nil
			}
			fmt.Fprintf(stdout, "Resumed %s. It starts over, as new.\n", client.Clean(c.Name))
			return nil
		})
}

func checksDelete(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return oneCheckCommand(ctx, "delete", "delete -check NAME [-project NAME] [-json] [-server URL]",
		"Deletes a check with its runs, pings and output, for good. Its ping URL stops working,\nand a job that pings its slug with create=1 makes a new check. To stop it and keep its\nhistory, pause it instead.",
		args, stdout, stderr, nil, func(api *client.Client, c *apiclient.Check, r reading) error {
			if err := api.DeleteCheck(ctx, c.Id); err != nil {
				return err
			}
			// The API answers with nothing, which is what -json then prints.
			if !*r.json {
				fmt.Fprintf(stdout, "Deleted %s. Its history goes with the next tidy.\n", client.Clean(c.Name))
			}
			return nil
		})
}

func checksMove(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var to *string
	return oneCheckCommand(ctx, "move", "move -check NAME -to PROJECT [-project NAME] [-json] [-server URL]",
		"Moves a check to another project you may edit, with its runs and output, which that\nproject's viewers can then read. Its ping URL keeps working. A ping by slug needs the new\nproject's ping key, and a job that pings with create=1 makes a new check in the old one.",
		args, stdout, stderr, func(fs *flag.FlagSet) {
			to = fs.String("to", "", "the project to move it to, by name or id")
		}, func(api *client.Client, c *apiclient.Check, r reading) error {
			if *to == "" {
				return errors.New("give -to, the project to move it to")
			}
			p, err := api.FindProject(ctx, *to)
			if err != nil {
				return err
			}
			if p.Id == c.ProjectId && !*r.json {
				fmt.Fprintf(stdout, "%s is in %s already.\n", client.Clean(c.Name), client.Clean(p.Name))
				return nil
			}
			raw, err := api.MoveCheck(ctx, c.Id, p.Id)
			if err != nil {
				return err
			}
			if *r.json {
				return client.PrintJSON(stdout, raw)
			}
			fmt.Fprintf(stdout, "Moved %s to %s.\n", client.Clean(c.Name), client.Clean(p.Name))
			return nil
		})
}

func checksPingURL(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var replace *bool
	return oneCheckCommand(ctx, "ping-url", "ping-url -check NAME [-replace] [-project NAME] [-json] [-server URL]",
		"Shows a job's ping URL, for those who may see it, which takes the manage scope and an\neditor's role. -replace gives it a new one, and the old one stops working at once, so do\nthat when it has leaked; jobs stop reporting until they have the new one.",
		args, stdout, stderr, func(fs *flag.FlagSet) {
			replace = fs.Bool("replace", false, "give it a new ping URL; the old one stops working at once")
		}, func(api *client.Client, c *apiclient.Check, r reading) error {
			fetch := api.PingURL
			if *replace {
				fetch = api.ReplacePingURL
			}
			url, raw, err := fetch(ctx, c.Id)
			if err != nil {
				return err
			}
			if *r.json {
				return client.PrintJSON(stdout, raw)
			}
			if *replace {
				fmt.Fprintln(stdout, "Replaced. The old one no longer works, so give your jobs this one.")
				fmt.Fprintln(stdout)
			}
			printRunLine(stdout, url)
			return nil
		})
}

func keysCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "make":
			return keysMake(ctx, args[1:], stdout, stderr)
		case "revoke":
			return keysRevoke(ctx, args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill keys [-project NAME] [-json] [-server URL]
       allstill keys make NAME -project NAME -scope read|manage
       allstill keys revoke NAME [-project NAME]

Lists the keys of a project, or of every project you own, with who made each and when it
was last used. A key is for a script: with ALLSTILL_SERVER and ALLSTILL_TOKEN, a command
uses it without signing in. It reaches its own project alone, and keeps working when who
made it has gone. Managing keys takes an owner of the project or an administrator, with
their own sign-in.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	project := fs.String("project", "", "only this project, by name or id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("unknown keys command %q", client.Clean(fs.Arg(0)))
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	projects, err := api.Projects(ctx)
	if err != nil {
		return said(api, err)
	}
	projectID := ""
	if *project != "" {
		p, err := api.FindProject(ctx, *project)
		if err != nil {
			return said(api, err)
		}
		projectID = p.Id
	}
	keys, raw, err := api.Keys(ctx, projectID)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	printKeys(stdout, keys.Items, projects.Items, time.Now(), time.Local)
	return nil
}

func printKeys(w io.Writer, keys []apiclient.ApiKey, projects []apiclient.Project, now time.Time, loc *time.Location) {
	if len(keys) == 0 {
		fmt.Fprintln(w, "No keys.")
		return
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.Id] = p.Name
	}
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "KEY\tSCOPE\tPROJECT\tMADE BY\tMADE\tLAST USED")
	for _, k := range keys {
		used := "never"
		if k.LastUsedAt != nil {
			used = client.Clock(now, *k.LastUsedAt, loc)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", client.Clean(k.Name), client.Clean(string(k.Scope)), client.Clean(names[k.ProjectId]), madeBy(k), client.Clock(now, k.CreatedAt, loc), used)
	}
	t.Flush()
}

// madeBy says who made a key, as far as this reader may read it.
func madeBy(k apiclient.ApiKey) string {
	who := "someone"
	switch k.MadeBy.Kind {
	case "administrator":
		who = "an administrator"
	case "command":
		who = "a command on the server"
	case "person":
		if k.MadeBy.Name != nil {
			who = client.Clean(*k.MadeBy.Name)
		}
	}
	if k.MadeVia != nil {
		who += ", with " + client.Clean(*k.MadeVia)
	}
	return who
}

func keysMake(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("keys make", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill keys make NAME [-project NAME] [-scope read|manage] [-json] [-server URL]

Makes a key for a project you own, and prints it once: only its hash is kept. With read
it acts as a viewer of this project, and with manage as an editor, and it reaches nothing
else.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	project := fs.String("project", "", "the project, by name or id; left out when you have just one")
	scope := fs.String("scope", "read", "read, or manage to change things too")
	rest, err := parseAround(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		fs.Usage()
		return errors.New("give the key one name")
	}
	if *scope != "read" && *scope != "manage" {
		return errors.New("-scope takes read or manage")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	p, err := api.OneProject(ctx, *project)
	if err != nil {
		return said(api, err)
	}
	made, raw, err := api.MakeKey(ctx, p.Id, rest[0], *scope)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "Made the %s key %s for %s. This is the only time it is shown:\n\n  %s\n\nKeep it in the secrets of what will use it, and give it as ALLSTILL_TOKEN, with ALLSTILL_SERVER=%s.\n",
		client.Clean(string(made.Scope)), client.Clean(made.Name), client.Clean(p.Name), client.Clean(made.Key), api.Server)
	return nil
}

func keysRevoke(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill keys revoke NAME [-project NAME] [-server URL]

Revokes a key, by name or id, which stops working on its next request. What it did stays
in the log. Two with one name list both, with their ids.

Flags:
`)
		fs.PrintDefaults()
	}
	server := serverFlag(fs)
	project := fs.String("project", "", "the project it is for, by name or id, if two share its name")
	rest, err := parseAround(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		fs.Usage()
		return errors.New("give one key")
	}
	api, _, _, err := signedIn(*server)
	if err != nil {
		return err
	}
	projectID := ""
	if *project != "" {
		p, err := api.FindProject(ctx, *project)
		if err != nil {
			return said(api, err)
		}
		projectID = p.Id
	}
	keys, _, err := api.Keys(ctx, projectID)
	if err != nil {
		return said(api, err)
	}
	var found []apiclient.ApiKey
	for _, k := range keys.Items {
		if k.Name == rest[0] || k.Id == rest[0] {
			found = append(found, k)
		}
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("you have no key named %q. allstill keys lists those you do", client.Clean(rest[0]))
	case 1:
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%d keys are named %q. Use -project, or one of these ids:", len(found), client.Clean(rest[0]))
		for _, k := range found {
			fmt.Fprintf(&b, "\n  %s", client.Clean(k.Id))
		}
		return errors.New(b.String())
	}
	if err := api.RevokeKey(ctx, found[0].Id); err != nil {
		return said(api, err)
	}
	fmt.Fprintf(stdout, "Revoked %s. It stops working on its next request.\n", client.Clean(found[0].Name))
	return nil
}
