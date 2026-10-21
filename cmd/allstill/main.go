// SPDX-License-Identifier: AGPL-3.0-only

// Command allstill is the Allstill server and its command-line tools in one
// binary. This file only wires the parts in internal/ together.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // every time zone, also on hosts without a zone database

	"github.com/MartijnMul/allstill/internal/ingest"
	"github.com/MartijnMul/allstill/internal/runner"
	"github.com/MartijnMul/allstill/internal/scheduler"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/web"
)

const usage = `Allstill watches your jobs, backups and sites.

Usage:
  allstill serve [flags]                 run the server
  allstill run [flags] -- command [args] run a job and report it
  allstill import healthchecks [flags]   copy checks from Healthchecks
  allstill admin create-user [flags]     add a person, with a link to make a passkey
  allstill admin passkey-link [flags]    a new passkey link for someone
  allstill admin create-project NAME     create a project
  allstill admin rename-project NAME NEW rename a project
  allstill admin move-checks [flags]     move checks to another project
  allstill backup [flags] FILE           copy the database while it runs
  allstill version                       print the version

Run 'allstill <command> -h' for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	var exit exitStatus
	if errors.As(err, &exit) {
		os.Exit(int(exit))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "allstill:", err)
		os.Exit(1)
	}
}

// exitStatus ends the process with a job's own exit status.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "run":
		return runJob(ctx, args[1:], stdout, stderr)
	case "import":
		return importCommand(ctx, args[1:], stdout, stderr)
	case "admin":
		return adminCommand(ctx, args[1:], stdout, stderr)
	case "backup":
		return backup(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "allstill", version())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func serve(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	listen := fs.String("listen", envOr("ALLSTILL_LISTEN", "127.0.0.1:8000"), "the address to listen on (ALLSTILL_LISTEN)")
	baseURL := fs.String("base-url", os.Getenv("ALLSTILL_BASE_URL"), "the address people use to reach Allstill, such as https://allstill.example.com; passkeys belong to its host name (ALLSTILL_BASE_URL; default http://localhost and the port of -listen)")
	runs, err := envInt("ALLSTILL_KEEP_RUNS", store.DefaultRetention.Runs)
	if err != nil {
		return err
	}
	output, err := envInt("ALLSTILL_KEEP_OUTPUT", store.DefaultRetention.Output)
	if err != nil {
		return err
	}
	keepRuns := fs.Int("keep-runs", runs, "how many of its newest runs each check keeps, with their pings (ALLSTILL_KEEP_RUNS)")
	keepOutput := fs.Int("keep-output", output, "of how many of its newest runs each check keeps the output (ALLSTILL_KEEP_OUTPUT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	retention := store.Retention{Runs: *keepRuns, Output: *keepOutput}
	if err := retention.Check(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if *baseURL == "" {
		_, port, err := net.SplitHostPort(*listen)
		if err != nil {
			return fmt.Errorf("-listen %q: %w", *listen, err)
		}
		*baseURL = "http://localhost:" + port
	}
	base, err := web.ParseBaseURL(*baseURL)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	st.Retention = retention
	ws, err := st.Workspace(ctx)
	if err != nil {
		return err
	}
	// The admin commands print links with it.
	if err := st.SetSetting(ctx, "base_url", base.String()); err != nil {
		return err
	}
	if n, err := st.PasskeysOfOtherHosts(ctx, base.Hostname()); err != nil {
		return err
	} else if n > 0 {
		log.Warn("some passkeys were made for another host name and do not work here; make new ones with 'allstill admin passkey-link'",
			"passkeys", n, "host", base.Hostname())
	}

	sched := scheduler.New(st, log)
	mux := http.NewServeMux()
	(&ingest.Handler{Store: st, Nudge: sched.Nudge, Log: log, Now: time.Now}).Register(mux)
	dashboard, err := web.New(st, ws, base, log)
	if err != nil {
		return err
	}
	dashboard.Register(mux)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errs := make(chan error, 2)
	go func() { errs <- sched.Run(ctx) }()
	go func() {
		log.Info("listening", "address", *listen, "dashboard", base.String(), "version", version())
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(err, srv.Shutdown(shutdown))
}

func runJob(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill run [flags] -- command [args]

Runs the command and reports it: a start ping before, an end ping after with
its exit status and the tail of its output. The exit status is the command's.
The command may write values, one name=number per line, to the file named in
$ALLSTILL_VALUES_FILE; they are sent with the end ping.

The end ping waits at most two seconds for the command's output to close after
it exits. A process it leaves running in the background should send its output
elsewhere, or its next write to it fails.

Flags:
`)
		fs.PrintDefaults()
	}
	var urls []string
	fs.Func("url", "a ping URL to report to; repeat it to report to several, such as Allstill and Healthchecks while moving. Prefer ALLSTILL_URL (space-separated): a flag shows the secret URL to anyone who can list processes", func(v string) error {
		urls = append(urls, v)
		return nil
	})
	timeout := fs.Duration("timeout", 10*time.Second, "how long each ping may take, retries included")
	tail := fs.Int("tail", 10_000, "bytes of output to send with the end ping")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(urls) == 0 {
		urls = strings.Fields(os.Getenv("ALLSTILL_URL"))
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("no command to run")
	}
	warn := func(format string, a ...any) { fmt.Fprintf(stderr, "allstill run: "+format+"\n", a...) }
	if len(urls) == 0 {
		warn("no ping URL (--url or ALLSTILL_URL); running without reporting")
	}
	// A signal that stops the job must not also stop its end ping.
	code := runner.Run(context.WithoutCancel(ctx), runner.Options{
		URLs: urls, Command: fs.Args(), Timeout: *timeout, Tail: *tail,
		Stdout: stdout, Stderr: stderr, Stdin: os.Stdin, Client: http.DefaultClient, Warn: warn,
	})
	if code != 0 {
		return exitStatus(code)
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// envInt reads a whole number from the environment, or gives fallback when
// the variable is not set.
func envInt(name string, fallback int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a whole number", name, v)
	}
	return n, nil
}

// buildVersion is set by the release build, as `-X main.buildVersion=1.2.3`.
var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
