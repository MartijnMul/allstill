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
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/web"
)

// Commands run on the server itself, against its database file. They act as
// the workspace administrator.

const adminUsage = `Usage:
  allstill admin create-user -name NAME [-email EMAIL] [flags]
  allstill admin passkey-link (-email EMAIL | -person ID) [flags]
  allstill admin create-project [-db file] NAME
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
	case "passkey-link":
		return passkeyLink(ctx, args[1:], stdout, stderr)
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

func createUser(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin create-user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin create-user -name NAME [-email EMAIL] [flags]

Adds a person and prints a link, valid for 24 hours, where they make a
passkey to sign in with. For now everyone added here is a workspace
administrator, who sees every project; narrower roles come with invitations.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	name := fs.String("name", "", "the person's name")
	email := fs.String("email", "", "where their email alerts and digest go")
	tz := fs.String("time-zone", "UTC", "the time zone the dashboard shows them, such as Europe/Amsterdam")
	baseURL := fs.String("base-url", os.Getenv("ALLSTILL_BASE_URL"), "the dashboard's address (ALLSTILL_BASE_URL; default: the one the server last ran with)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -name")
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
	personID, err := st.CreatePerson(ctx, ws, store.NewPerson{Name: strings.TrimSpace(*name), Email: *email, TimeZone: *tz, WorkspaceAdmin: true})
	if err != nil {
		return err
	}
	token, err := st.CreatePasskeyLink(ctx, ws, personID, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s (%s), a workspace administrator.\nThey make a passkey with this link, which works once, for 24 hours:\n\n  %s/setup#%s\n", *name, personID, base, token)
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
creating it when needed. Each check keeps its UUID, so jobs keep their ping
URLs apart from the host. Checks already in Allstill are skipped, so it can
run again. The project's read-write API key is read from HEALTHCHECKS_API_KEY;
the import only reads from Healthchecks.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	base := fs.String("url", "https://healthchecks.io", "the Healthchecks site")
	project := fs.String("project", "", "the Allstill project to import into")
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
