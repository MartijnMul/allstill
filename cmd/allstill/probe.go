// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/probe"
	"github.com/MartijnMul/allstill/internal/store"
)

// probeCommand is `allstill probe`.
func probeCommand(ctx context.Context, args []string, _, stderr io.Writer) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: ALLSTILL_PROBE_KEY=... allstill probe -server URL [flags]

Runs checks inside a network the server cannot reach: it connects out to the
server, fetches the checks assigned to it, runs them and sends the runs back.
It opens no port. It may only reach public addresses and the ranges in -allow,
and never its own machine's. Make a probe and its key with
allstill admin create-probe, on the server.

Flags:
`)
		fs.PrintDefaults()
	}
	server := fs.String("server", os.Getenv("ALLSTILL_SERVER"), "the server's address, such as https://allstill.example.com (ALLSTILL_SERVER)")
	keyFile := fs.String("key-file", "", "a file holding the probe's key, instead of ALLSTILL_PROBE_KEY")
	allowFlag := fs.String("allow", os.Getenv("ALLSTILL_PROBE_ALLOW"), "the private ranges it may reach, such as 10.0.0.0/8 (ALLSTILL_PROBE_ALLOW)")
	caFile := fs.String("ca", "", "a file of certificates to trust for every check, beyond the system's, in PEM")
	contact := fs.String("contact", os.Getenv("ALLSTILL_CONTACT"), "an address for the owners of what it checks, sent with each request (ALLSTILL_CONTACT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -server")
	}
	base, err := url.Parse(*server)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return fmt.Errorf("-server %q: give an address such as https://allstill.example.com", *server)
	}
	key := strings.TrimSpace(os.Getenv("ALLSTILL_PROBE_KEY"))
	if *keyFile != "" {
		raw, err := os.ReadFile(*keyFile)
		if err != nil {
			return fmt.Errorf("-key-file: %w", err)
		}
		key = strings.TrimSpace(string(raw))
	}
	if key == "" {
		return errors.New("give the probe's key in ALLSTILL_PROBE_KEY or -key-file; allstill admin create-probe makes one")
	}
	allow, err := parseRanges(*allowFlag)
	if err != nil {
		return fmt.Errorf("-allow: %w", err)
	}
	if err := active.CheckContact(*contact); err != nil {
		return err
	}
	runner := &active.Runner{Allow: allow, UserAgent: active.UserAgent(version(), *contact)}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			return fmt.Errorf("-ca: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return fmt.Errorf("-ca %s: it holds no certificate in PEM", *caFile)
		}
		runner.CA = string(pem)
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		log.Warn("the server's address is not https, so this probe's key and its checks' URLs travel unencrypted")
	}
	log.Info("probe starting", "server", base.Redacted(), "version", version(), "allow", *allowFlag)
	return (&probe.Probe{Server: base, Key: key, Runner: runner, Log: log}).Run(ctx)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func createProbe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin create-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin create-probe NAME -project PROJECT [-serves Q,R] [flags]

Makes a probe, which runs checks inside a network the server cannot reach, and
prints its key once. It has a check of its own in PROJECT, whose people are
alerted when it goes quiet for 3 minutes. It serves PROJECT and the projects in
-serves: only their checks can run on it, as anyone who can edit them can make
it fetch from its network.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	project := fs.String("project", "", "the project of its own check")
	serves := fs.String("serves", "", "other projects it serves, separated by commas")
	name, rest := nameFirst(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	if name == "" || *project == "" {
		fs.Usage()
		return errors.New("give a name and -project")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projectOf := func(n string) (string, error) {
		id, err := st.ProjectByName(ctx, ws, n)
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("no project is named %q", n)
		}
		return id, err
	}
	projectID, err := projectOf(*project)
	if err != nil {
		return err
	}
	var others []string
	for _, n := range strings.Split(*serves, ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		id, err := projectOf(n)
		if err != nil {
			return err
		}
		others = append(others, id)
	}
	admin, err := st.AdminAccess(ctx, ws)
	if err != nil {
		return err
	}
	_, key, err := st.CreateProbe(ctx, admin, name, projectID, others)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Made the probe %s. Its key, shown once:\n\n  %s\n\nRun it inside its network, with a range it may reach:\n\n  ALLSTILL_PROBE_KEY=... allstill probe -server https://allstill.example.com -allow 10.0.0.0/8\n\nThen give it checks: allstill admin add-check ... -probe %q\n\nEditors of the projects it serves can have it fetch anything it reaches, also another project's network, and whoever runs it receives the addresses of its checks. One probe per party is the safe setup.\n", name, key, name)
	return nil
}

func rekeyProbe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin rekey-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	name, rest := nameFirst(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	if name == "" {
		return errors.New("usage: allstill admin rekey-probe NAME [-db file]")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	admin, err := st.AdminAccess(ctx, ws)
	if err != nil {
		return err
	}
	key, err := st.RekeyProbe(ctx, admin, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no probe is named %q", name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "The probe %s has a new key, shown once; the old one no longer works:\n\n  %s\n", name, key)
	return nil
}

// nameFirst takes a name given before the flags.
func nameFirst(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
