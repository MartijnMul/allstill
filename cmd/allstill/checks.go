// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/outbound"
	"github.com/MartijnMul/allstill/internal/store"
)

// Checks Allstill runs itself are added and changed from the command line
// for now (intent/http-checks/spec.md, C10).

const checkFlagsHelp = `
What an HTTP check fetches and how it is judged:
  -http URL          the address; a token in its path or query is a secret, as
                     a ping URL is: only the project's editors see it whole
  -method GET|HEAD   GET by default
  -expect 200,301    the status codes that count as up (default any 2xx, or any
                     3xx with -location)
  -location URL      where a redirect must lead
  -json /status=ok   a field of a JSON answer that must equal a value
  -follow            follow up to 5 redirects, instead of judging the first answer

What a TCP check connects to:
  -tcp HOST:PORT     a port that should accept a connection
  -tls               also finish a TLS handshake and watch its certificate; not
                     for STARTTLS, as on mail ports 25 and 587

For both:
  -every 1m          how often, from 1m to 24h
  -timeout 10s       how long an attempt may take, at most 60s
  -fail-after 2      how many failed attempts in a row make it down
  -warn-days 14      warn when its certificate has fewer days left (or a third
                     of its life, when that is shorter); 0 turns it off
  -fail-days 3       fail under this many days (or a tenth of its life); 0 off
  -ca FILE           certificates to trust beyond the system's, in PEM
  -no-verify         do not verify the certificate; its expiry is still read
  -probe NAME        run it on this probe, inside its network, instead of on
                     the server; set-check -probe "" brings it back
`

// checkFlags adds the flags of a check Allstill runs itself to fs. apply writes
// the ones given into settings for a check of this kind, refuses those of
// another kind, and returns the interval when -every was given. jsonField names the
// flag for a JSON field, as commands that work from anywhere keep -json for how they
// answer.
func checkFlags(fs *flag.FlagSet, jsonFlag string) (apply func(kind string, settings map[string]any) (time.Duration, error)) {
	rawURL := fs.String("http", "", "the URL to fetch")
	address := fs.String("tcp", "", "the host and port to connect to, such as mail.example.com:993")
	useTLS := fs.Bool("tls", false, "finish a TLS handshake on the TCP port, and watch its certificate")
	method := fs.String("method", "GET", "GET or HEAD")
	expect := fs.String("expect", "", "the status codes that count as up, such as 200,301")
	location := fs.String("location", "", "where a redirect must lead")
	jsonField := fs.String(jsonFlag, "", "a JSON field that must equal a value, such as /status=ok")
	follow := fs.Bool("follow", false, "follow up to 5 redirects")
	every := fs.Duration("every", time.Minute, "how often, from 1m to 24h")
	timeout := fs.Duration("timeout", active.DefaultTimeout, "how long an attempt may take, at most 60s")
	failAfter := fs.Int("fail-after", store.DefaultFailAfter, "how many failed attempts in a row make it down")
	warnDays := fs.Int("warn-days", active.DefaultWarnDays, "warn under this many days of certificate left; 0 turns it off")
	failDays := fs.Int("fail-days", active.DefaultFailDays, "fail under this many days of certificate left; 0 turns it off")
	ca := fs.String("ca", "", "a file of certificates to trust beyond the system's, in PEM")
	noVerify := fs.Bool("no-verify", false, "do not verify the certificate")
	flagKind := map[string]string{
		"http": "http", "method": "http", "expect": "http", "location": "http", jsonFlag: "http", "follow": "http",
		"tcp": "tcp", "tls": "tcp",
	}
	// What all checks Allstill runs itself have, and a check that jobs ping has not.
	notForPing := []string{"timeout", "fail-after", "warn-days", "fail-days", "ca", "no-verify", "probe"}
	return func(kind string, settings map[string]any) (time.Duration, error) {
		var interval time.Duration
		var err error
		set := func(key string, v any, empty bool) {
			if empty {
				delete(settings, key)
			} else {
				settings[key] = v
			}
		}
		fs.Visit(func(f *flag.Flag) {
			if err != nil {
				return
			}
			if k, ok := flagKind[f.Name]; ok && k != kind {
				err = fmt.Errorf("-%s is for %s checks, and this is %s check", f.Name, strings.ToUpper(k), an(kind))
				return
			}
			if kind == "ping" && slices.Contains(notForPing, f.Name) {
				err = fmt.Errorf("-%s is for checks Allstill runs itself, and jobs ping this one", f.Name)
				return
			}
			switch f.Name {
			case "http":
				set("url", *rawURL, false)
			case "tcp":
				host, port, e := net.SplitHostPort(*address)
				n, e2 := strconv.Atoi(port)
				if e != nil || e2 != nil {
					err = fmt.Errorf("-tcp %q: give a host and a port, such as mail.example.com:993", *address)
					return
				}
				set("host", host, false)
				set("port", n, false)
			case "tls":
				set("tls", true, !*useTLS)
			case "method":
				set("method", strings.ToUpper(*method), strings.EqualFold(*method, "GET"))
			case "expect":
				var codes []int
				for _, c := range strings.FieldsFunc(*expect, func(r rune) bool { return r == ',' || r == ' ' }) {
					n, e := strconv.Atoi(c)
					if e != nil {
						err = fmt.Errorf("-expect %q: give status codes such as 200,301", *expect)
						return
					}
					codes = append(codes, n)
				}
				set("expected_status", codes, len(codes) == 0)
			case "location":
				set("location", *location, *location == "")
			case jsonFlag:
				pointer, equals, ok := strings.Cut(*jsonField, "=")
				if *jsonField != "" && !ok {
					err = fmt.Errorf("-%s %q: give a field and its value, such as /status=ok", jsonFlag, *jsonField)
					return
				}
				set("json_field", active.JSONField{Pointer: pointer, Equals: equals}, *jsonField == "")
			case "follow":
				set("follow_redirects", true, !*follow)
			case "every":
				interval = *every
			case "timeout":
				if *timeout%time.Second != 0 || *timeout < time.Second {
					err = fmt.Errorf("-timeout %s: give whole seconds, such as 10s", *timeout)
					return
				}
				set("timeout", int(*timeout/time.Second), *timeout == active.DefaultTimeout)
			case "fail-after":
				set("fail_after", *failAfter, false)
			case "warn-days":
				set("warn_days", *warnDays, false)
			case "fail-days":
				set("fail_days", *failDays, false)
			case "ca":
				if *ca == "" {
					delete(settings, "ca")
					return
				}
				pem, e := os.ReadFile(*ca)
				if e != nil {
					err = fmt.Errorf("-ca: %w", e)
					return
				}
				set("ca", string(pem), false)
			case "no-verify":
				set("no_verify", true, !*noVerify)
			}
		})
		// Without TLS there is no certificate, so what is known of one goes,
		// unless it was just given: then the store says it needs TLS.
		if err == nil && kind == "tcp" && !*useTLS && visited(fs, "tls") {
			given := map[string]any{}
			for _, key := range active.CertificateSettings {
				if v, ok := settings[key]; ok && visited(fs, strings.ReplaceAll(key, "_", "-")) {
					given[key] = v
				}
			}
			active.WithoutTLS(settings)
			for key, v := range given {
				settings[key] = v
			}
		}
		return interval, err
	}
}

// visited says whether a flag was given.
func visited(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

// an names a kind of check as a sentence does.
func an(kind string) string {
	switch kind {
	case "http":
		return "an HTTP"
	case "ping":
		return "a ping"
	}
	return "a " + strings.ToUpper(kind)
}

// reachable refuses an address the server may not reach, saying what to do: this
// is the server's own shell, so it names what the address resolved to (ADR 0004). A
// host name that does not resolve now is only a warning: the check reports it.
func reachable(ctx context.Context, host string, allow []netip.Prefix, stdout io.Writer) error {
	err := outbound.CheckHost(ctx, host, allow)
	if errors.Is(err, outbound.ErrNoSuchHost) {
		fmt.Fprintf(stdout, "Note: %s does not resolve now; the check will say so until it does.\n", host)
		return nil
	}
	if u, ok := errors.AsType[*outbound.Unreachable](err); ok {
		if u.Own {
			return fmt.Errorf("%s is %s, this server's own address, which checks never reach", u.Host, u.Addr)
		}
		return fmt.Errorf("%s is %s, which is not on the public internet; to let the server reach it, allow its range when it starts, such as `allstill serve -allow-private %s` (ALLSTILL_ALLOW_PRIVATE). That opens the range to every project's checks", u.Host, u.Addr, rangeOf(u.Addr))
	}
	return err
}

// hostIn is the host a check's settings connect to.
func hostIn(kind string, settings map[string]any) string {
	if kind == "tcp" {
		host, _ := settings["host"].(string)
		return host
	}
	u, _ := settings["url"].(string)
	return hostOf(u)
}

// hostOf is the host of a URL, or "" when it cannot be read.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// rangeOf suggests the private range an address is in.
func rangeOf(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	for _, s := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fd00::/8"} {
		if p := netip.MustParsePrefix(s); p.Contains(a) {
			return p
		}
	}
	return netip.PrefixFrom(a, a.BitLen())
}

// serverSettings are what `serve` last ran with that the admin commands
// need: the private ranges it may reach, and its canary.
func serverSettings(ctx context.Context, st *store.Store) (allow []netip.Prefix, canary string, err error) {
	v, err := st.Setting(ctx, "allow_private")
	if err != nil {
		return nil, "", err
	}
	if allow, err = parseRanges(v); err != nil {
		return nil, "", err
	}
	canary, err = st.Setting(ctx, "canary")
	return allow, canary, err
}

// parseRanges reads ranges such as 10.0.0.0/8, separated by commas or spaces.
func parseRanges(v string) ([]netip.Prefix, error) {
	var ps []netip.Prefix
	for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a range such as 10.0.0.0/8", s)
		}
		ps = append(ps, p.Masked())
	}
	return ps, nil
}

// parseCanary reads host:port addresses, separated by commas or spaces.
func parseCanary(v string) ([]string, error) {
	addrs := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	for _, a := range addrs {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return nil, fmt.Errorf("-canary %q: give host:port, such as 9.9.9.9:53", a)
		}
	}
	return addrs, nil
}

func addCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin add-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin add-check -project PROJECT -name NAME (-http URL | -tcp HOST:PORT) [flags]

Adds a check that Allstill runs itself, from the server, on its interval: an
HTTP request, judged by its status code, a JSON field or where it redirects,
or a connection to a TCP port, optionally with TLS. Over TLS, also by the
certificate's days left. It tries the address once, to show what the check
will see. The running server starts it within a minute.
`+checkFlagsHelp+`
Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project to add it to")
	name := fs.String("name", "", "the check's name")
	description := fs.String("description", "", "what it watches, for people")
	probeName := fs.String("probe", "", "the probe to run it on, instead of the server")
	var tags []string
	fs.Func("tag", "a tag; repeat it for several", func(v string) error {
		tags = append(tags, v)
		return nil
	})
	apply := checkFlags(fs, "json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rawURL, address := fs.Lookup("http").Value.String(), fs.Lookup("tcp").Value.String()
	if *project == "" || *name == "" || (rawURL == "") == (address == "") || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -project, -name and one of -http and -tcp")
	}
	kind := "http"
	if address != "" {
		kind = "tcp"
	}
	settings := map[string]any{}
	interval, err := apply(kind, settings)
	if err != nil {
		return err
	}
	if interval == 0 {
		interval = time.Minute
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projectID, err := st.ProjectByName(ctx, ws, *project)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no project is named %q", *project)
	} else if err != nil {
		return err
	}
	allow, canary, err := serverSettings(ctx, st)
	if err != nil {
		return err
	}
	admin, err := st.AdminAccess(ctx, ws, "add-check")
	if err != nil {
		return err
	}
	var probeID string
	if *probeName != "" {
		// What the server may reach says nothing of a probe's network.
		if probeID, err = st.ProbeIDByName(ctx, ws, *probeName); errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no probe is named %q", *probeName)
		} else if err != nil {
			return err
		}
	} else if err := reachable(ctx, hostIn(kind, settings), allow, stdout); err != nil {
		return err
	}
	created, err := st.CreateCheck(ctx, admin, projectID, store.NewCheck{
		Name: *name, Description: *description, Tags: tags, Kind: kind, Interval: interval, Settings: settings, ProbeID: probeID,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s to %s (%s), every %s.\n", *name, *project, created.ID, short(interval))
	if probeID != "" {
		fmt.Fprintf(stdout, "The probe %s starts it within a minute; only it can try the address.\n", *probeName)
		return nil
	}
	if err := sameCertificate(ctx, st, created.ID, *project, *name, stdout); err != nil {
		return err
	}
	if canary == "" {
		fmt.Fprintln(stdout, "This server has no canary (-canary): if its own network goes down, this check goes down and alerts with it.")
	}
	return tryOnce(ctx, &active.Runner{Allow: allow, UserAgent: active.UserAgent(version(), "")}, kind, settings, stdout)
}

// sameCertificate says when another check reads the certificate this one
// reads, and how to have it warn once (C3).
func sameCertificate(ctx context.Context, st *store.Store, checkID, project, name string, stdout io.Writer) error {
	checks, err := st.ActiveChecks(ctx)
	if err != nil {
		return err
	}
	var this active.Check
	for _, c := range checks {
		if c.ID == checkID {
			this = c
		}
	}
	if this.ID == "" {
		return nil
	}
	host, port, watched := this.Certificate()
	if !watched {
		return nil
	}
	for _, c := range checks {
		h, p, on := c.Certificate()
		if c.ID != checkID && c.ProjectID == this.ProjectID && h == host && p == port && on {
			fmt.Fprintf(stdout, "Another check already watches the certificate of %s:%s. To have it warn once, turn its warnings off on one of them: allstill admin set-check -project %q -check %q -warn-days 0\n", host, port, project, name)
			return nil
		}
	}
	return nil
}

// tryOnce makes one attempt and says how it went, and warns when HEAD is
// refused.
func tryOnce(ctx context.Context, runner *active.Runner, kind string, settings map[string]any, stdout io.Writer) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	var c active.Check
	if kind == "tcp" {
		t, err := active.ParseTCP(raw)
		if err != nil {
			return err
		}
		c.TCP = &t
	} else if c.HTTP, err = active.ParseHTTP(raw); err != nil {
		return err
	}
	r := runner.Attempt(ctx, c)
	line := "Tried it now: " + map[string]string{"passed": "up", "warned": "up, with a warning", "failed": "it fails"}[string(r.Outcome)]
	if r.Reason != "" {
		line += ", " + r.Reason
	}
	if code, ok := r.Measured["status"]; ok {
		line += fmt.Sprintf(" (HTTP %d in %d ms)", int(code), int(r.Measured["ms"]))
	} else if c.TCP != nil {
		line += fmt.Sprintf(" (in %d ms)", int(r.Measured["ms"]))
	}
	if days, ok := r.Measured["cert_days"]; ok {
		line += fmt.Sprintf("; its certificate has %.0f days left", days)
	}
	fmt.Fprintln(stdout, line+".")
	if c.HTTP.Method == "HEAD" && r.Measured["status"] == 405 {
		fmt.Fprintln(stdout, "The site answers HEAD with 405: it does not take HEAD requests. Leave out -method HEAD, or set it with set-check -method GET.")
	}
	return nil
}

func setCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-check -project PROJECT -check NAME [flags]

Changes a check: only the settings given change. A new schedule counts from now.
An empty -expect, -location, -json or -ca removes that setting; -follow=false
and -no-verify=false turn those off. The running server follows within a
minute.

For a check that jobs ping:
  -every 1h          how often it is expected
  -cron "0 3 * * *"  or when, with -time-zone Europe/Amsterdam
  -grace 30m         how long after that before it is down
`+checkFlagsHelp+`
Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project of the check")
	name := fs.String("check", "", "the check's name")
	probeName := fs.String("probe", "", `the probe to run it on; "" for the server`)
	cron := fs.String("cron", "", "for a check that jobs ping: when it is expected, as a cron expression")
	timeZone := fs.String("time-zone", "", "for a check that jobs ping: the time zone of -cron")
	grace := fs.Duration("grace", 0, "for a check that jobs ping: how long after the expected time before it is down")
	apply := checkFlags(fs, "json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *name == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -project and -check")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projectID, err := st.ProjectByName(ctx, ws, *project)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no project is named %q", *project)
	} else if err != nil {
		return err
	}
	admin, err := st.AdminAccess(ctx, ws, "set-check")
	if err != nil {
		return err
	}
	checks, err := st.Checks(ctx, admin)
	if err != nil {
		return err
	}
	var found []store.CheckView
	for _, c := range checks {
		if c.ProjectID == projectID && c.Name == *name {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("%s has no check named %q", *project, *name)
	case 1:
	default:
		return fmt.Errorf("%s has %d checks named %q", *project, len(found), *name)
	}
	c := found[0]
	if c.Kind == "ping" {
		return setPingCheck(ctx, fs, st, admin, c, apply, *project, *cron, *timeZone, *grace, stdout)
	}
	if visited(fs, "cron") || visited(fs, "time-zone") || visited(fs, "grace") {
		return errors.New("-cron, -time-zone and -grace are for checks that jobs ping; this one runs on an interval, and its grace follows from that")
	}
	settings := map[string]any{}
	if err := json.Unmarshal(c.Settings, &settings); err != nil {
		return fmt.Errorf("its settings cannot be read: %w", err)
	}
	interval, err := apply(c.Kind, settings)
	if err != nil {
		return err
	}
	if interval == 0 {
		interval = c.Interval
	}
	allow, _, err := serverSettings(ctx, st)
	if err != nil {
		return err
	}
	// Where it will run decides what it may reach: only the server's ranges are
	// the server's to check, whenever it comes to the server or its address
	// changes there.
	probeID, moving := c.ProbeID, visited(fs, "probe")
	if moving {
		probeID = ""
		if *probeName != "" {
			if probeID, err = st.ProbeIDByName(ctx, ws, *probeName); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no probe is named %q", *probeName)
			} else if err != nil {
				return err
			}
		}
	}
	if probeID == "" && (moving || visited(fs, "http") || visited(fs, "tcp")) {
		if err := reachable(ctx, hostIn(c.Kind, settings), allow, stdout); err != nil {
			return err
		}
	}
	if moving {
		err = st.SetActiveCheckOn(ctx, admin, c.ID, interval, settings, probeID, time.Now())
	} else {
		err = st.SetActiveCheck(ctx, admin, c.ID, interval, settings, time.Now())
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Changed %s in %s, every %s.\n", *name, *project, short(interval))
	if moving && *probeName != "" {
		fmt.Fprintf(stdout, "The probe %s runs it from now on.\n", *probeName)
	} else if moving {
		fmt.Fprintln(stdout, "The server runs it from now on.")
	}
	return nil
}

// setPingCheck changes the schedule of a check that jobs ping.
func setPingCheck(ctx context.Context, fs *flag.FlagSet, st *store.Store, admin store.Access, c store.CheckView, apply func(string, map[string]any) (time.Duration, error), project, cron, timeZone string, grace time.Duration, stdout io.Writer) error {
	interval, err := apply("ping", map[string]any{})
	if err != nil {
		return err
	}
	var ch store.Change
	switch {
	case interval != 0 && cron != "":
		return errors.New("give -every or -cron, not both")
	case interval != 0:
		ch.Interval = &interval
	case cron != "":
		if timeZone == "" {
			return errors.New("give -time-zone with -cron, such as Europe/Amsterdam")
		}
		ch.Cron, ch.TimeZone = &cron, &timeZone
	case timeZone != "":
		if c.Cron == "" {
			return errors.New("-time-zone goes with a cron schedule, and this check runs on an interval")
		}
		ch.TimeZone = &timeZone
	}
	if interval != 0 && timeZone != "" {
		return errors.New("-time-zone goes with -cron, not -every")
	}
	if visited(fs, "grace") {
		ch.Grace = &grace
	}
	if ch.Interval == nil && ch.Cron == nil && ch.TimeZone == nil && ch.Grace == nil {
		return errors.New("give -every, -cron or -grace")
	}
	if _, err := st.UpdateCheck(ctx, admin, c.ID, ch, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Changed %s in %s. It counts from now.\n", c.Name, project)
	return nil
}

// oneCheck finds a check by project and name, for the commands.
func oneCheck(ctx context.Context, st *store.Store, command, ws, project, name string) (store.Access, store.CheckView, error) {
	projectID, err := st.ProjectByName(ctx, ws, project)
	if errors.Is(err, store.ErrNotFound) {
		return store.Access{}, store.CheckView{}, fmt.Errorf("no project is named %q", project)
	} else if err != nil {
		return store.Access{}, store.CheckView{}, err
	}
	admin, err := st.AdminAccess(ctx, ws, command)
	if err != nil {
		return store.Access{}, store.CheckView{}, err
	}
	checks, err := st.Checks(ctx, admin)
	if err != nil {
		return store.Access{}, store.CheckView{}, err
	}
	var found []store.CheckView
	for _, c := range checks {
		if c.ProjectID == projectID && c.Name == name {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return store.Access{}, store.CheckView{}, fmt.Errorf("%s has no check named %q", project, name)
	case 1:
		return admin, found[0], nil
	}
	return store.Access{}, store.CheckView{}, fmt.Errorf("%s has %d checks named %q", project, len(found), name)
}

// checkCommand is a command that does one thing to one check.
func checkCommand(ctx context.Context, name, help string, args []string, stdout, stderr io.Writer, do func(*store.Store, store.Access, store.CheckView) (string, error)) error {
	fs := flag.NewFlagSet("admin "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: allstill admin %s -project PROJECT -check NAME [-db file]\n\n%s\n\nFlags:\n", name, help)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project of the check")
	check := fs.String("check", "", "the check's name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *check == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -project and -check")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	admin, c, err := oneCheck(ctx, st, name, ws, *project, *check)
	if err != nil {
		return err
	}
	said, err := do(st, admin, c)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, said)
	return nil
}

func pauseCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return checkCommand(ctx, "pause-check", "Pauses a check: nothing is expected of it, and an open incident closes without\na recovery message. A job's next ping resumes it, and a failure then alerts.", args, stdout, stderr,
		func(st *store.Store, admin store.Access, c store.CheckView) (string, error) {
			if err := st.PauseCheck(ctx, admin, c.ID, time.Now()); err != nil {
				return "", err
			}
			return "Paused " + c.Name + ".", nil
		})
}

func resumeCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return checkCommand(ctx, "resume-check", "Resumes a paused check, which starts over as new.", args, stdout, stderr,
		func(st *store.Store, admin store.Access, c store.CheckView) (string, error) {
			if c.Status.State != "paused" {
				return c.Name + " is not paused.", nil
			}
			if err := st.ResumeCheck(ctx, admin, c.ID, time.Now()); err != nil {
				return "", err
			}
			return "Resumed " + c.Name + ".", nil
		})
}

func deleteCheck(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return checkCommand(ctx, "delete-check", "Deletes a check with its runs, pings and output, for good. Its ping URL stops\nworking. A job that pings its slug with create=1 makes a new check.", args, stdout, stderr,
		func(st *store.Store, admin store.Access, c store.CheckView) (string, error) {
			if err := st.DeleteCheck(ctx, admin, c.ID, time.Now()); err != nil {
				return "", err
			}
			return "Deleted " + c.Name + ". Its history goes with the next tidy.", nil
		})
}
