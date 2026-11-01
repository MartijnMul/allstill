// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/web"
)

// Commands run on the server itself, against its database file. They act as
// the workspace administrator.

const adminUsage = `Usage:
  allstill admin create-user -name NAME [-email EMAIL] [-project PROJECT -role ROLE] [flags]
  allstill admin set-member (-email EMAIL | -person ID) -project PROJECT -role ROLE [flags]
  allstill admin passkey-link (-email EMAIL | -person ID) [flags]
  allstill admin create-project [-db file] NAME
  allstill admin rename-project [-db file] NAME NEW-NAME
  allstill admin move-checks -from PROJECT -to PROJECT (-tag TAG | -check NAME)... [-db file]
  allstill admin set-alerting -project PROJECT -check NAME -urgency now|digest|none [-critical] [-db file]
  allstill admin add-check -project PROJECT -name NAME (-http URL | -tcp HOST:PORT) [flags]
  allstill admin set-check -project PROJECT -check NAME [flags]
  allstill admin pause-check -project PROJECT -check NAME [-db file]
  allstill admin resume-check -project PROJECT -check NAME [-db file]
  allstill admin delete-check -project PROJECT -check NAME [-db file]
  allstill admin create-probe NAME -project PROJECT [-serves Q,R] [-db file]
  allstill admin rekey-probe NAME [-db file]
  allstill admin test-email -to ADDRESS [smtp flags]
  allstill admin set-alerts [-db file] on|shadow
`

func adminCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return errors.New("no admin command given")
	}
	switch args[0] {
	case "create-project":
		return createProject(ctx, args[1:], stdout, stderr)
	case "create-user":
		return createUser(ctx, args[1:], stdout, stderr)
	case "set-member":
		return setMember(ctx, args[1:], stdout, stderr)
	case "passkey-link":
		return passkeyLink(ctx, args[1:], stdout, stderr)
	case "rename-project":
		return renameProject(ctx, args[1:], stdout, stderr)
	case "move-checks":
		return moveChecks(ctx, args[1:], stdout, stderr)
	case "set-alerting":
		return setAlerting(ctx, args[1:], stdout, stderr)
	case "add-check":
		return addCheck(ctx, args[1:], stdout, stderr)
	case "set-check":
		return setCheck(ctx, args[1:], stdout, stderr)
	case "pause-check":
		return pauseCheck(ctx, args[1:], stdout, stderr)
	case "resume-check":
		return resumeCheck(ctx, args[1:], stdout, stderr)
	case "delete-check":
		return deleteCheck(ctx, args[1:], stdout, stderr)
	case "create-probe":
		return createProbe(ctx, args[1:], stdout, stderr)
	case "rekey-probe":
		return rekeyProbe(ctx, args[1:], stdout, stderr)
	case "test-email":
		return testEmail(ctx, args[1:], stdout, stderr)
	case "set-alerts":
		return setAlerts(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, adminUsage)
	return fmt.Errorf("unknown admin command %q", args[0])
}

func createProject(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin create-project", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("give the project's name")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.ProjectByName(ctx, ws, fs.Arg(0)); err == nil {
		return fmt.Errorf("a project named %q exists already", fs.Arg(0))
	}
	projectID, key, err := st.CreateProject(ctx, ws, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created project %s (%s).\nIts ping key, for pings by slug, is shown only now:\n\n  %s\n", fs.Arg(0), projectID, key)
	return nil
}

func renameProject(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin rename-project", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("give the project's name and its new name")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projectID, err := st.ProjectByName(ctx, ws, fs.Arg(0))
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no project is named %q", fs.Arg(0))
	} else if err != nil {
		return err
	}
	if err := st.RenameProject(ctx, ws, projectID, fs.Arg(1)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Renamed %s to %s.\n", fs.Arg(0), fs.Arg(1))
	return nil
}

func moveChecks(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin move-checks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin move-checks -from PROJECT -to PROJECT (-tag TAG | -check NAME)... [-db file]

Moves the checks of one project that have one of the tags, or one of the names,
to another project, creating it when needed. Their ping URLs, runs and pings
stay the same; a job that pings by slug needs the new project's ping key.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	from := fs.String("from", "", "the project the checks are in")
	to := fs.String("to", "", "the project to move them to")
	tags, names := map[string]bool{}, map[string]bool{}
	fs.Func("tag", "move the checks with this tag; repeat it for several", func(v string) error {
		tags[v] = true
		return nil
	})
	fs.Func("check", "move the check with this name; repeat it for several", func(v string) error {
		names[v] = true
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" || len(tags)+len(names) == 0 || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -from, -to, and at least one -tag or -check")
	}
	if *from == *to {
		return errors.New("-from and -to name the same project")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	fromID, err := st.ProjectByName(ctx, ws, *from)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no project is named %q", *from)
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
	var moving []store.CheckView
	found := map[string]bool{}
	for _, c := range checks {
		if c.ProjectID != fromID {
			continue
		}
		if names[c.Name] || slices.ContainsFunc(c.Tags, func(t string) bool { return tags[t] }) {
			moving = append(moving, c)
			found[c.Name] = true
		}
	}
	for name := range names {
		if !found[name] {
			fmt.Fprintf(stderr, "%s has no check named %q\n", *from, name)
		}
	}
	if len(moving) == 0 {
		return fmt.Errorf("no check in %s matches", *from)
	}
	toID, err := st.ProjectByName(ctx, ws, *to)
	if errors.Is(err, store.ErrNotFound) {
		var pingKey string
		if toID, pingKey, err = st.CreateProject(ctx, ws, *to); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created project %s. Its ping key, shown only now: %s\n\n", *to, pingKey)
	} else if err != nil {
		return err
	}
	moved := 0
	for _, c := range moving {
		if err := st.MoveCheck(ctx, ws, c.ID, toID, time.Now()); err != nil {
			fmt.Fprintf(stdout, "not moved  %s: %v\n", c.Name, err)
			continue
		}
		line := "moved      " + c.Name
		if c.Slug != "" {
			line += " (pinged by slug: use the new project's ping key)"
		}
		fmt.Fprintln(stdout, line)
		moved++
	}
	fmt.Fprintf(stdout, "\n%d of %d checks moved from %s to %s.\n", moved, len(moving), *from, *to)
	if moved < len(moving) {
		return errors.New("some checks were not moved")
	}
	return nil
}

func setAlerts(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-alerts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-alerts [-db file] on|shadow

Switches alerts on, so people are really told, or back to shadow mode, where
Allstill records what it would have told them under Messages and sends only
test messages. Incidents open when alerts go on stay quiet: the digest lists
them. The running server follows within seconds.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || (fs.Arg(0) != "on" && fs.Arg(0) != "shadow") {
		fs.Usage()
		return errors.New("give on or shadow")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	on := fs.Arg(0) == "on"
	quiet, err := st.SetAlerts(ctx, on)
	if err != nil {
		return err
	}
	if !on {
		fmt.Fprintln(stdout, "Shadow mode: alerts are recorded under Messages and not sent; test messages still go.")
		return nil
	}
	fmt.Fprintln(stdout, "Alerts are on: people are told when a check goes down (unless the server runs with -shadow).")
	if quiet > 0 {
		fmt.Fprintf(stdout, "Open incidents that began in shadow mode stay quiet: %d. The digest lists them.\n", quiet)
	}
	key, err := st.VAPIDKey(ctx, ws)
	if err != nil {
		return err
	}
	// Confirmed addresses always count as reachable: this command may not
	// see the server's SMTP settings (with the Linux package only root reads
	// them), and the overview shows when email does not work. It says so
	// when it cannot see them.
	mail := os.Getenv("ALLSTILL_SMTP_HOST") != ""
	names, err := st.Unreachable(ctx, key.PublicKey(), true) // the server checks email itself, on the overview
	if err != nil {
		return err
	}
	if !mail {
		fmt.Fprintln(stdout, "This command does not see the SMTP settings, so email addresses count as reachable; check email with 'allstill admin test-email'.")
	}
	if len(names) > 0 {
		fmt.Fprintf(stdout, "No confirmed device or address, so alerts cannot reach them: %s. They add one under Devices.\n", strings.Join(names, ", "))
	}
	return nil
}

func setAlerting(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-alerting", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-alerting -project PROJECT -check NAME -urgency now|digest|none [-critical] [-db file]

Sets how fast a problem with a check reaches people: now (their phone), digest
(the daily digest) or none (shown, never sent). A critical check breaks
through quiet hours.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project of the check")
	name := fs.String("check", "", "the check's name")
	urgency := fs.String("urgency", "now", "now, digest or none")
	critical := fs.Bool("critical", false, "break through quiet hours")
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
	var ids []string
	for _, c := range checks {
		if c.ProjectID == projectID && c.Name == *name {
			ids = append(ids, c.ID)
		}
	}
	switch len(ids) {
	case 0:
		return fmt.Errorf("%s has no check named %q", *project, *name)
	case 1:
	default:
		return fmt.Errorf("%s has %d checks named %q", *project, len(ids), *name)
	}
	if err := st.SetAlerting(ctx, ws, ids[0], *urgency, *critical, time.Now()); err != nil {
		return err
	}
	what := *urgency
	if *critical {
		what += ", critical"
	}
	fmt.Fprintf(stdout, "%s in %s: %s.\n", *name, *project, what)
	return nil
}

func backup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill backup [-db file] FILE

Writes a consistent copy of the database to FILE, which must not exist yet,
while Allstill runs. The copy is readable by its owner only.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("give the file to write the copy to; it must not exist yet")
	}
	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(ctx, fs.Arg(0)); err != nil {
		return fmt.Errorf("writing %s: %w", fs.Arg(0), err)
	}
	fmt.Fprintf(stdout, "Copied %s to %s.\n", *dbPath, fs.Arg(0))
	return nil
}

func createUser(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin create-user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin create-user -name NAME [-email EMAIL] [-project PROJECT -role ROLE] [flags]

Adds a person and prints a link, valid for 24 hours, where they make a
passkey to sign in with.

With -project, they see only those projects, with the role you give:
  viewer   sees the checks, their hosts and ports, last runs and what jobs
           printed; not full URLs or ping URLs; gets no alerts
  editor   also sees full URLs and ping URLs, and gets alerts for the project
  owner    the same as an editor, for now

Without -project, they are a workspace administrator, who sees every project
and gets alerts for all of them, so do not add an outside party that way.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	name := fs.String("name", "", "the person's name")
	email := fs.String("email", "", "where their email alerts and digest go")
	tz := fs.String("time-zone", "UTC", "the time zone the dashboard shows them, such as Europe/Amsterdam")
	var projects []string
	fs.Func("project", "a project they may see; repeat it for several. Without it they are an administrator", func(v string) error {
		projects = append(projects, v)
		return nil
	})
	roleName := fs.String("role", "", "their role in those projects: viewer, editor or owner")
	baseURL := fs.String("base-url", os.Getenv("ALLSTILL_BASE_URL"), "the dashboard's address (ALLSTILL_BASE_URL; default: the one the server last ran with)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -name")
	}
	if (len(projects) == 0) != (*roleName == "") {
		return errors.New("give -project and -role together, or neither for a workspace administrator")
	}
	var role store.Role
	if *roleName != "" {
		var err error
		if role, err = store.ParseRole(*roleName); err != nil {
			return fmt.Errorf("-role: %w", err)
		}
	}
	if _, err := time.LoadLocation(*tz); err != nil || *tz == "" || *tz == "Local" {
		return fmt.Errorf("-time-zone %q is not a time zone such as Europe/Amsterdam", *tz)
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	base, err := baseOf(ctx, st, *baseURL)
	if err != nil {
		return err
	}
	if *email != "" {
		if _, err := st.PersonByEmail(ctx, ws, *email); err == nil {
			return fmt.Errorf("someone with the email address %s exists already; use 'allstill admin passkey-link -email %s' for a new link", *email, *email)
		}
	}
	person := store.NewPerson{Name: strings.TrimSpace(*name), Email: *email, TimeZone: *tz}
	var personID, what string
	if len(projects) == 0 {
		person.WorkspaceAdmin = true
		personID, err = st.CreatePerson(ctx, ws, person)
		what = "a workspace administrator"
	} else {
		ids := make([]string, len(projects))
		for i, project := range projects {
			if ids[i], err = st.ProjectByName(ctx, ws, project); errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no project is named %q", project)
			} else if err != nil {
				return err
			}
		}
		personID, err = st.CreateMember(ctx, ws, person, ids, role)
		what = fmt.Sprintf("%s of %s", role, strings.Join(projects, ", "))
	}
	if err != nil {
		return err
	}
	token, err := st.CreatePasskeyLink(ctx, ws, personID, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s (%s), %s.\nThey make a passkey with this link, which works once, for 24 hours:\n\n  %s/setup#%s\n", *name, personID, what, base, token)
	return nil
}

func setMember(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-member", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-member (-email EMAIL | -person ID) -project PROJECT -role ROLE [flags]

Gives someone a role in a project: viewer, editor or owner, or none to take
them out of it. It applies from their next click. A workspace administrator
sees every project whatever their roles say, but as a viewer of one gets no
alerts for it; -role none gives those back.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	email := fs.String("email", "", "the person's email address")
	personID := fs.String("person", "", "the person's id, per_…")
	project := fs.String("project", "", "the project")
	roleName := fs.String("role", "", "viewer, editor, owner, or none")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*email == "") == (*personID == "") || *project == "" || *roleName == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -email or -person, and -project and -role")
	}
	var role store.Role
	if *roleName != "none" {
		var err error
		if role, err = store.ParseRole(*roleName); err != nil {
			return fmt.Errorf("-role: %w", err)
		}
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	var p store.Person
	if *email != "" {
		p, err = st.PersonByEmail(ctx, ws, *email)
	} else {
		p, err = st.Person(ctx, ws, *personID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("there is no such person, or they are disabled")
	}
	if err != nil {
		return err
	}
	projectID, err := st.ProjectByName(ctx, ws, *project)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no project is named %q", *project)
	} else if err != nil {
		return err
	}
	if *roleName == "none" {
		if err := st.RemoveMember(ctx, ws, projectID, p.ID); errors.Is(err, store.ErrNotFound) {
			if p.Admin {
				return fmt.Errorf("%s has no role in %s, and sees it as a workspace administrator", p.Name, *project)
			}
			return fmt.Errorf("%s is not in %s", p.Name, *project)
		} else if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s is no longer in %s.\n", p.Name, *project)
	} else {
		if err := st.SetMember(ctx, ws, projectID, p.ID, role); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s is now %s of %s.\n", p.Name, role, *project)
	}
	switch {
	case p.Admin && role == store.Viewer:
		fmt.Fprintf(stdout, "They are a workspace administrator, so they still see every project, but they no longer get alerts for %s. -role none gives those back.\n", *project)
	case p.Admin:
		fmt.Fprintln(stdout, "They are a workspace administrator, so they still see every project.")
	}
	return nil
}

func passkeyLink(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin passkey-link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin passkey-link (-email EMAIL | -person ID) [flags]

Prints a new link, valid for 24 hours, where someone makes a passkey: for a
new device, or after losing theirs. Their passkeys and sessions stay as they
are; an earlier link they did not use stops working.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	email := fs.String("email", "", "the person's email address")
	personID := fs.String("person", "", "the person's id, per_…")
	baseURL := fs.String("base-url", os.Getenv("ALLSTILL_BASE_URL"), "the dashboard's address (ALLSTILL_BASE_URL; default: the one the server last ran with)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*email == "") == (*personID == "") || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -email or -person")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	base, err := baseOf(ctx, st, *baseURL)
	if err != nil {
		return err
	}
	var p store.Person
	if *email != "" {
		p, err = st.PersonByEmail(ctx, ws, *email)
	} else {
		p, err = st.Person(ctx, ws, *personID)
	}
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("there is no such person, or they are disabled")
	}
	if err != nil {
		return err
	}
	token, err := st.CreatePasskeyLink(ctx, ws, p.ID, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "A passkey link for %s, which works once, for 24 hours:\n\n  %s/setup#%s\n", p.Name, base, token)
	return nil
}

// baseOf is the dashboard's address: the one given, or the one the server
// last ran with.
func baseOf(ctx context.Context, st *store.Store, given string) (*url.URL, error) {
	if given == "" {
		v, err := st.Setting(ctx, "base_url")
		if err != nil {
			return nil, err
		}
		if v == "" {
			return nil, errors.New("give -base-url or ALLSTILL_BASE_URL: the server has not run with this database yet")
		}
		given = v
	}
	return web.ParseBaseURL(given)
}

func importCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "healthchecks" {
		fmt.Fprintln(stderr, "Usage: allstill import healthchecks [flags]")
		return errors.New("unknown import source")
	}
	fs := flag.NewFlagSet("import healthchecks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill import healthchecks -project NAME [flags]

Copies the checks of one Healthchecks project into an Allstill project,
creating it when needed. Each check keeps its UUID, so a job only changes the
start of its ping URL. Checks already in Allstill are skipped, so it can
run again; with -tag, only the checks with one of the tags are copied, to split
one Healthchecks project over several. The project's read-write API key is
read from HEALTHCHECKS_API_KEY; the import only reads from Healthchecks.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	base := fs.String("url", "https://healthchecks.io", "the Healthchecks site")
	project := fs.String("project", "", "the Allstill project to import into")
	var tags []string
	fs.Func("tag", "copy only the checks with this Healthchecks tag; repeat it for several", func(v string) error {
		tags = append(tags, v)
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	key := os.Getenv("HEALTHCHECKS_API_KEY")
	if *project == "" || key == "" {
		fs.Usage()
		return errors.New("give -project and set HEALTHCHECKS_API_KEY")
	}

	checks, err := importer.Fetch(ctx, http.DefaultClient, *base, key)
	if err != nil {
		return err
	}
	if len(tags) > 0 {
		if checks = importer.WithTags(checks, tags...); len(checks) == 0 {
			return errors.New("no check at Healthchecks has one of these tags")
		}
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projectID, err := st.ProjectByName(ctx, ws, *project)
	if errors.Is(err, store.ErrNotFound) {
		var pingKey string
		if projectID, pingKey, err = st.CreateProject(ctx, ws, *project); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created project %s. Its ping key, shown only now: %s\n\n", *project, pingKey)
	} else if err != nil {
		return err
	}
	admin, err := st.AdminAccess(ctx, ws)
	if err != nil {
		return err
	}
	results, err := importer.Import(ctx, st, admin, projectID, checks)
	imported := 0
	for _, r := range results {
		mark := "skipped "
		if r.CheckID != "" {
			mark, imported = "imported", imported+1
		}
		line := fmt.Sprintf("%s  %s", mark, r.Name)
		if r.Note != "" {
			line += ": " + r.Note
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "\n%d of %d checks imported into %s.\n", imported, len(checks), *project)
	return err
}

func openStore(ctx context.Context, path string) (*store.Store, string, error) {
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, "", err
	}
	ws, err := st.Workspace(ctx)
	if err != nil {
		st.Close()
		return nil, "", err
	}
	return st, ws, nil
}
