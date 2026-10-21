// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/MartijnMul/allstill/internal/importer"
	"github.com/MartijnMul/allstill/internal/store"
)

// Commands run on the server itself, against its database file. They act as
// the workspace administrator.

func adminCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "create-project" {
		fmt.Fprintln(stderr, "Usage: allstill admin create-project [-db file] NAME")
		return errors.New("unknown admin command")
	}
	fs := flag.NewFlagSet("admin create-project", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	if err := fs.Parse(args[1:]); err != nil {
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
