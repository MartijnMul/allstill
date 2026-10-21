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
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // every time zone, also on hosts without a zone database

	"github.com/MartijnMul/allstill/internal/ingest"
	"github.com/MartijnMul/allstill/internal/runner"
	"github.com/MartijnMul/allstill/internal/scheduler"
	"github.com/MartijnMul/allstill/internal/store"
)

const usage = `Allstill watches your jobs, backups and sites.

Usage:
  allstill serve [flags]                 run the server
  allstill run [flags] -- command [args] run a job and report it
  allstill import healthchecks [flags]   copy checks from Healthchecks
  allstill admin create-project NAME     create a project
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Workspace(ctx); err != nil {
		return err
	}

	sched := scheduler.New(st, log)
	mux := http.NewServeMux()
	(&ingest.Handler{Store: st, Nudge: sched.Nudge, Log: log, Now: time.Now}).Register(mux)

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
		log.Info("listening", "address", *listen, "version", version())
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

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
