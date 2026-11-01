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
`

// certificateSettings are the settings about a certificate, which a TCP check
// without TLS does not have.
var certificateSettings = []string{"warn_days", "fail_days", "ca", "no_verify"}

// checkFlags adds the flags of a check Allstill runs itself to fs. apply writes
// the ones given into settings for a check of this kind, refuses those of
// another kind, and returns the interval when -every was given.
func checkFlags(fs *flag.FlagSet) (apply func(kind string, settings map[string]any) (time.Duration, error)) {
	rawURL := fs.String("http", "", "the URL to fetch")
	address := fs.String("tcp", "", "the host and port to connect to, such as mail.example.com:993")
	useTLS := fs.Bool("tls", false, "finish a TLS handshake on the TCP port, and watch its certificate")
	method := fs.String("method", "GET", "GET or HEAD")
	expect := fs.String("expect", "", "the status codes that count as up, such as 200,301")
	location := fs.String("location", "", "where a redirect must lead")
	jsonField := fs.String("json", "", "a JSON field that must equal a value, such as /status=ok")
	follow := fs.Bool("follow", false, "follow up to 5 redirects")
	every := fs.Duration("every", time.Minute, "how often, from 1m to 24h")
	timeout := fs.Duration("timeout", active.DefaultTimeout, "how long an attempt may take, at most 60s")
	failAfter := fs.Int("fail-after", store.DefaultFailAfter, "how many failed attempts in a row make it down")
	warnDays := fs.Int("warn-days", active.DefaultWarnDays, "warn under this many days of certificate left; 0 turns it off")
	failDays := fs.Int("fail-days", active.DefaultFailDays, "fail under this many days of certificate left; 0 turns it off")
	ca := fs.String("ca", "", "a file of certificates to trust beyond the system's, in PEM")
	noVerify := fs.Bool("no-verify", false, "do not verify the certificate")
	flagKind := map[string]string{
		"http": "http", "method": "http", "expect": "http", "location": "http", "json": "http", "follow": "http",
		"tcp": "tcp", "tls": "tcp",
	}
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
			case "json":
				pointer, equals, ok := strings.Cut(*jsonField, "=")
				if *jsonField != "" && !ok {
					err = fmt.Errorf("-json %q: give a field and its value, such as /status=ok", *jsonField)
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
			for _, key := range certificateSettings {
				if !visited(fs, strings.ReplaceAll(key, "_", "-")) {
					delete(settings, key)
				}
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
	if kind == "http" {
		return "an HTTP"
	}
	return "a " + strings.ToUpper(kind)
}

// reachable refuses an address the server may not reach, saying what to do
// (ADR 0004). A host name that does not resolve now is only a warning: the
// check reports it.
func reachable(ctx context.Context, host string, allow []netip.Prefix, stdout io.Writer) error {
	if host == "" {
		return nil // the store says what is wrong with it
	}
	addrs := []netip.Addr{}
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, a)
	} else {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if addrs, err = net.DefaultResolver.LookupNetIP(lookup, "ip", host); err != nil {
			fmt.Fprintf(stdout, "Note: %s does not resolve now; the check will say so until it does.\n", host)
			return nil
		}
	}
	for _, a := range addrs {
		if outbound.Own(a) {
			return fmt.Errorf("%s is %s, this server's own address, which checks never reach", host, a)
		}
		if !outbound.Allowed(a, allow) {
			return fmt.Errorf("%s is %s, which is not on the public internet; to let the server reach it, allow its range when it starts, such as `allstill serve -allow-private %s` (ALLSTILL_ALLOW_PRIVATE). That opens the range to every project's checks", host, a, rangeOf(a))
		}
	}
	return nil
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
	var tags []string
	fs.Func("tag", "a tag; repeat it for several", func(v string) error {
		tags = append(tags, v)
		return nil
	})
	apply := checkFlags(fs)
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
	host := hostOf(rawURL)
	if kind == "tcp" {
		host, _ = settings["host"].(string)
	}
	if err := reachable(ctx, host, allow, stdout); err != nil {
		return err
	}
	admin, err := st.AdminAccess(ctx, ws)
	if err != nil {
		return err
	}
	created, err := st.CreateCheck(ctx, admin, projectID, store.NewCheck{
		Name: *name, Description: *description, Tags: tags, Kind: kind, Interval: interval, Settings: settings,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s to %s (%s), every %s.\n", *name, *project, created.ID, short(interval))
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
	host, port, watched := this.Certificate()
	if !watched {
		return nil
	}
	for _, c := range checks {
		h, p, on := c.Certificate()
		if c.ID != checkID && h == host && p == port && on {
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

Changes a check that Allstill runs itself: only the settings given change.
An empty -expect, -location, -json or -ca removes that setting; -follow=false
and -no-verify=false turn those off. The running server follows within a
minute.
`+checkFlagsHelp+`
Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project of the check")
	name := fs.String("check", "", "the check's name")
	apply := checkFlags(fs)
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
	admin, err := st.AdminAccess(ctx, ws)
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
	if !store.Active(c.Kind) {
		return fmt.Errorf("%s is pinged by a job; set-check changes checks Allstill runs itself", *name)
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
	if u, ok := settings["url"].(string); ok && fs.Lookup("http").Value.String() != "" {
		if err := reachable(ctx, hostOf(u), allow, stdout); err != nil {
			return err
		}
	}
	if h, ok := settings["host"].(string); ok && fs.Lookup("tcp").Value.String() != "" {
		if err := reachable(ctx, h, allow, stdout); err != nil {
			return err
		}
	}
	if err := st.SetActiveCheck(ctx, admin, c.ID, interval, settings, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Changed %s in %s, every %s.\n", *name, *project, short(interval))
	return nil
}
