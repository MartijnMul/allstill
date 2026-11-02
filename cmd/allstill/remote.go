// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/client"
	"github.com/MartijnMul/allstill/internal/store"
)

// Commands that work from anywhere, over the API, with a sign-in of a person
// (intent/cli-anywhere/spec.md). What they do and print is in internal/client; they
// only wire it up.

// parseAround reads flags before and after a command's arguments, as in
// `allstill login URL -read-only`, and returns the arguments.
func parseAround(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

// credentials reads the file where sign-ins are kept.
func credentials() (string, client.Credentials, error) {
	path, err := client.CredentialsPath()
	if err != nil {
		return "", client.Credentials{}, err
	}
	c, err := client.LoadCredentials(path)
	return path, c, err
}

// serverOf says which installation is meant: the one named, or the last signed in to.
func serverOf(named string, c client.Credentials) (string, error) {
	if named == "" {
		named = os.Getenv("ALLSTILL_SERVER")
	}
	if named == "" {
		named = c.Current
	}
	if named == "" {
		return "", errors.New("you are not signed in here. Sign in with allstill login, and the address of your Allstill")
	}
	return client.ParseServer(named)
}

// signedIn returns a client for the installation meant, with its sign-in.
func signedIn(named string) (*client.Client, string, client.Credentials, error) {
	path, c, err := credentials()
	if err != nil {
		return nil, "", c, err
	}
	server, err := serverOf(named, c)
	if err != nil {
		return nil, "", c, err
	}
	saved, ok := c.Servers[server]
	if !ok {
		return nil, "", c, notSignedIn(server)
	}
	api, err := client.New(server, saved.Token, version(), nil)
	return api, path, c, err
}

func notSignedIn(server string) error {
	return fmt.Errorf("you are not signed in on %s. Sign in with allstill login %s", server, server)
}

func serverFlag(fs *flag.FlagSet) *string {
	return fs.String("server", "", "the installation, if not the last you signed in to (ALLSTILL_SERVER)")
}

func loginCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill login URL [-read-only] [-name NAME]

Signs this machine in to an installation. It prints a short code, which you approve
with your passkey in a browser on any device, also your phone, so this machine needs
no browser. From then on, commands work here with your role as it is at that moment.
You see and end sign-ins under Devices in the dashboard; allstill logout ends this one.

Flags:
`)
		fs.PrintDefaults()
	}
	readOnly := fs.Bool("read-only", false, "only read, as a viewer in every project you can see, for an AI assistant for example")
	name := fs.String("name", "", "what to call this sign-in where you see it (default: this machine's name)")
	rest, err := parseAround(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		fs.Usage()
		return errors.New("give one address")
	}
	_, c, err := credentials()
	if err != nil {
		return err
	}
	named := ""
	if len(rest) == 1 {
		named = rest[0]
	}
	server, err := serverOf(named, c)
	if err != nil {
		return err
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	scope := store.ScopeManage
	if *readOnly {
		scope = store.ScopeRead
	}
	api, err := client.New(server, "", version(), nil)
	if err != nil {
		return err
	}
	code, err := api.RequestDeviceCode(ctx, *name, scope)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "To sign in, open this address in a browser on any device:\n\n  %s\n\nand enter this code:\n\n  %s\n\nOnly enter it if you started this yourself. Waiting...\n", code.VerificationURI, code.UserCode)
	token, err := api.WaitForToken(ctx, code)
	if errors.Is(err, context.Canceled) {
		return errors.New("stopped, so this machine is not signed in")
	}
	if err != nil {
		return err
	}
	// Read again: the wait can be minutes, with another login or logout meanwhile.
	path, c, err := credentials()
	if err != nil {
		if now, newErr := client.New(server, token.AccessToken, version(), nil); newErr == nil {
			now.EndThisToken(ctx)
		}
		return err
	}
	old, had := c.Servers[server]
	c.Servers[server], c.Current = client.Credential{Token: token.AccessToken}, server
	if err := c.Save(path); err != nil {
		// The sign-in cannot be kept, so it must not stay open.
		if signedIn, newErr := client.New(server, token.AccessToken, version(), nil); newErr == nil {
			signedIn.EndThisToken(ctx)
		}
		return fmt.Errorf("signed in, but it cannot be kept in %s: %w", path, err)
	}
	if had {
		// This machine no longer holds the one it had, so nothing could end it later.
		if previous, err := client.New(server, old.Token, version(), nil); err == nil {
			previous.EndThisToken(ctx)
		}
	}
	signedInAs := "Signed in"
	if now, err := client.New(server, token.AccessToken, version(), nil); err == nil {
		if me, err := now.Me(ctx); err == nil && me.Person != nil {
			signedInAs += " as " + client.Clean(me.Person.Name)
		}
	}
	if token.Scope == store.ScopeRead {
		signedInAs += ", to read only,"
	}
	fmt.Fprintf(stdout, "%s on %s.\n", signedInAs, server)
	return nil
}

func logoutCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill logout [-server URL]

Ends the sign-in on this machine, which stops working at once, and forgets it. Other
sign-ins stay: see them under Devices in the dashboard.

Flags:
`)
		fs.PrintDefaults()
	}
	server := serverFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("logout takes no arguments")
	}
	api, path, c, err := signedIn(*server)
	if err != nil {
		return err
	}
	err = api.EndThisToken(ctx)
	if err != nil && !errors.Is(err, client.ErrSignedOut) {
		// Forgetting it now would leave it open, with nothing here to end it.
		return fmt.Errorf("you are still signed in: %w. Try again, or end it under Devices", err)
	}
	delete(c.Servers, api.Server)
	moved := c.Current == api.Server
	if moved {
		// Any that is left, as nothing says which was used before.
		left := slices.Sorted(maps.Keys(c.Servers))
		c.Current = ""
		if len(left) > 0 {
			c.Current = left[0]
		}
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("the sign-in has ended, but it could not be removed from %s: %w", path, err)
	}
	fmt.Fprintf(stdout, "Signed out of %s.\n", api.Server)
	if moved && c.Current != "" {
		fmt.Fprintf(stdout, "From now on, commands use %s.\n", c.Current)
	}
	return nil
}

func whoamiCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill whoami [-server URL]

Says who you are signed in as, on which installation, and what you may do there.

Flags:
`)
		fs.PrintDefaults()
	}
	server := serverFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("whoami takes no arguments")
	}
	api, _, _, err := signedIn(*server)
	if err != nil {
		return err
	}
	me, err := api.Me(ctx)
	if errors.Is(err, client.ErrSignedOut) {
		return fmt.Errorf("your sign-in on %s has ended. Sign in again with allstill login %s", api.Server, api.Server)
	}
	if err != nil {
		return err
	}
	who := "Someone"
	if me.Person != nil {
		who = client.Clean(me.Person.Name)
		if me.Person.Email != nil {
			who += " (" + client.Clean(*me.Person.Email) + ")"
		}
	}
	fmt.Fprintf(stdout, "%s on %s", who, api.Server)
	if me.SignIn != nil {
		fmt.Fprintf(stdout, ", signed in as %s", client.Clean(me.SignIn.Name))
	}
	fmt.Fprintln(stdout, ".")
	manage := false
	for _, s := range me.Scopes {
		manage = manage || string(s) == store.ScopeManage
	}
	switch {
	case !manage:
		fmt.Fprintln(stdout, "This sign-in can only read, as a viewer in every project you can see.")
	case me.Person != nil && me.Person.WorkspaceAdmin != nil && *me.Person.WorkspaceAdmin:
		fmt.Fprintln(stdout, "This sign-in can do everything you can, and you are an administrator, who can see and change every project.")
	default:
		fmt.Fprintln(stdout, "This sign-in can do everything your roles allow.")
	}
	if len(me.Projects) == 0 {
		fmt.Fprintln(stdout, "You are in no project yet.")
	} else {
		var lines strings.Builder
		for _, p := range me.Projects {
			fmt.Fprintf(&lines, "  %s: %s\n", client.Clean(p.Name), client.Clean(string(p.Role)))
		}
		fmt.Fprintf(stdout, "Projects:\n%s", lines.String())
	}
	if !client.SameVersion(me.ServerVersion, version()) {
		fmt.Fprintf(stdout, "The server runs version %s, and this command line is %s.\n", client.Clean(me.ServerVersion), version())
	}
	return nil
}

// reading is what the commands that read have in common: where, and how to answer.
type reading struct {
	server *string
	json   *bool
}

func readingFlags(fs *flag.FlagSet) reading {
	return reading{serverFlag(fs), fs.Bool("json", false, "print the API's own answer, for scripts")}
}

// client returns the sign-in to read with.
func (r reading) client() (*client.Client, error) {
	api, _, _, err := signedIn(*r.server)
	return api, err
}

// said turns what a server refused into what to do about it.
func said(api *client.Client, err error) error {
	if errors.Is(err, client.ErrSignedOut) {
		return fmt.Errorf("your sign-in on %s has ended. Sign in again with allstill login %s", api.Server, api.Server)
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("stopped")
	}
	return err
}

func statusCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill status [-json] [-server URL]

Says how many checks are in each state, per project you can see, and every check
that needs attention, with why. Its exit status is 0 however bad things are: it reports
state, not health.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("status takes no arguments")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	status, raw, err := api.Status(ctx)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	client.PrintStatus(stdout, api.Server, status, time.Now(), time.Local)
	return nil
}

func projectsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill projects [-json] [-server URL]

Lists the projects you can see, with what you are in each and how their checks stand.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("projects takes no arguments")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	projects, err := api.Projects(ctx)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintItems(stdout, projects.Raw)
	}
	client.PrintProjects(stdout, projects.Items)
	return nil
}

func checksCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			return checksShow(ctx, args[1:], stdout, stderr)
		case "runs":
			return checksRuns(ctx, args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("checks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill checks [-project NAME] [-json] [-server URL]
       allstill checks show -check NAME [flags]
       allstill checks runs -check NAME [flags]

Lists the checks you can see, in one project or in all. An address shows as you may
see it: the scheme and host alone if you may only look, or with a read-only sign-in.
No list shows a ping URL.

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
		// `allstill checks -project P show -check C` says the same as with flags after it.
		rest := fs.Args()
		flags := slices.Clone(args[:len(args)-len(rest)])
		switch rest[0] {
		case "show":
			return checksShow(ctx, append(flags, rest[1:]...), stdout, stderr)
		case "runs":
			return checksRuns(ctx, append(flags, rest[1:]...), stdout, stderr)
		}
		fs.Usage()
		return fmt.Errorf("unknown checks command %q", client.Clean(rest[0]))
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	var projects []apiclient.Project
	if *project != "" {
		p, err := api.FindProject(ctx, *project)
		if err != nil {
			return said(api, err)
		}
		projects = []apiclient.Project{p}
	} else {
		all, err := api.Projects(ctx)
		if err != nil {
			return said(api, err)
		}
		projects = all.Items
	}
	var checks []client.CheckOf
	var raw []json.RawMessage
	for _, p := range projects {
		page, err := api.Checks(ctx, p.Id)
		if err != nil {
			return said(api, err)
		}
		for _, c := range page.Items {
			checks = append(checks, client.CheckOf{Check: c, Project: p.Name})
		}
		raw = append(raw, page.Raw...)
	}
	if *r.json {
		return client.PrintItems(stdout, raw)
	}
	client.PrintChecks(stdout, checks, time.Now(), time.Local)
	return nil
}

func checksShow(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checks show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill checks show -check NAME [-project NAME] [-json] [-server URL]

Shows one check, by name or id. Two with one name list both, with their ids.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	name := fs.String("check", "", "the check, by name or id")
	project := fs.String("project", "", "the project it is in, by name or id, if two share its name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -check")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	check, raw, err := api.FindCheck(ctx, *project, *name)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	projectName := ""
	if projects, err := api.Projects(ctx); err == nil {
		for _, p := range projects.Items {
			if p.Id == check.ProjectId {
				projectName = p.Name
			}
		}
	}
	client.PrintCheck(stdout, check, projectName, time.Now(), time.Local)
	return nil
}

func checksRuns(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checks runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill checks runs -check NAME [-project NAME] [-limit N] [-json] [-server URL]

Lists a check's latest runs, newest first.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	name := fs.String("check", "", "the check, by name or id")
	project := fs.String("project", "", "the project it is in, by name or id, if two share its name")
	limit := fs.Int("limit", 20, "how many, up to 200")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -check")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	check, _, err := api.FindCheck(ctx, *project, *name)
	if err != nil {
		return said(api, err)
	}
	runs, raw, err := api.Runs(ctx, check.Id, *limit)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	client.PrintRuns(stdout, runs.Items, time.Now(), time.Local)
	return nil
}

func logCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill log [flags]

Shows what happened, when and by whom, newest first, in the words and under the rules
for each reader of Log in the dashboard.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	project := fs.String("project", "", "only this project, by name or id")
	check := fs.String("check", "", "only this check, by name or id")
	name := fs.String("name", "", "only what is about this exact name, also a check that was deleted")
	person := fs.String("person", "", "only what this person did or what concerns them, by id")
	what := fs.String("what", "", "only happened, done, reached or signins")
	late := fs.Bool("late", false, "also show checks that were briefly late")
	limit := fs.Int("limit", 50, "how many lines, up to 200")
	before := fs.String("before", "", "read on from where a list said there is more")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("log takes no arguments")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	params := &apiclient.ListLogParams{Limit: limit, Late: late}
	if *project != "" {
		p, err := api.FindProject(ctx, *project)
		if err != nil {
			return said(api, err)
		}
		params.ProjectId = &p.Id
	}
	if *check != "" {
		c, _, err := api.FindCheck(ctx, *project, *check)
		if errors.Is(err, client.ErrNoSuchThing) {
			return fmt.Errorf("%w. For one that was deleted, use -name", err)
		}
		if err != nil {
			return said(api, err)
		}
		params.CheckId = &c.Id
	}
	if *name != "" {
		params.Name = name
	}
	if *person != "" {
		params.PersonId = person
	}
	if *what != "" {
		kind := apiclient.ListLogParamsWhat(*what)
		if !kind.Valid() {
			return errors.New("-what takes happened, done, reached or signins")
		}
		params.What = &kind
	}
	if *before != "" {
		params.Cursor = before
	}
	page, raw, err := api.Log(ctx, params)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	client.PrintLog(stdout, page, time.Now(), time.Local)
	return nil
}

func probesCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("probes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill probes [-json] [-server URL]

Lists the probes that serve projects you may edit, to choose one for a check. Making
a probe stays on the server.

Flags:
`)
		fs.PrintDefaults()
	}
	r := readingFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("probes takes no arguments")
	}
	api, err := r.client()
	if err != nil {
		return err
	}
	probes, raw, err := api.Probes(ctx)
	if err != nil {
		return said(api, err)
	}
	if *r.json {
		return client.PrintJSON(stdout, raw)
	}
	projects, err := api.Projects(ctx)
	if err != nil {
		return said(api, err)
	}
	client.PrintProbes(stdout, probes.Items, projects.Items)
	return nil
}
