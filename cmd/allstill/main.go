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

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/guide"
	"github.com/MartijnMul/allstill/internal/ingest"
	"github.com/MartijnMul/allstill/internal/outbound"
	"github.com/MartijnMul/allstill/internal/runner"
	"github.com/MartijnMul/allstill/internal/scheduler"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/web"
)

const usage = `Allstill watches your jobs, backups and sites.

Usage:
  allstill serve [flags]                 run the server
  allstill run [flags] -- command [args] run a job and report it
  allstill probe [flags]                 run checks inside a network the server cannot reach
  allstill import healthchecks [flags]   copy checks from Healthchecks
  allstill compare healthchecks [flags]  where Allstill saw checks differently
  allstill login URL [flags]             sign in from this machine, with a code you approve anywhere
  allstill logout                        end the sign-in on this machine
  allstill whoami                        who you are signed in as, and what you may do
  allstill status [flags]                what is up, and what needs attention
  allstill projects [flags]              the projects you can see
  allstill checks [flags]                the checks you can see
  allstill checks show [flags]           one check
  allstill checks runs [flags]           a check's latest runs
  allstill log [flags]                   what happened, when and by whom
  allstill probes [flags]                the probes that serve projects you may edit
  allstill admin create-user [flags]     add a person, with a link to make a passkey
  allstill admin set-member [flags]      what someone may do in a project
  allstill admin passkey-link [flags]    a new passkey link for someone
  allstill admin end-sessions [flags]    end all of someone's sign-ins and browser sessions
  allstill admin remove-passkey [flags]  take away a passkey, with what it approved
  allstill admin create-project NAME     create a project
  allstill admin rename-project NAME NEW rename a project
  allstill admin move-checks [flags]     move checks to another project
  allstill admin set-alerting [flags]    how fast a check's problems reach people
  allstill admin add-check [flags]       add a check Allstill runs itself, such as a site
  allstill admin set-check [flags]       change a check
  allstill admin pause-check [flags]     pause a check; resume-check starts it again
  allstill admin delete-check [flags]    delete a check with its history
  allstill admin create-probe NAME       make a probe, and print its key once
  allstill admin rekey-probe NAME        give a probe a new key
  allstill admin test-email -to ADDRESS  send a test email with the SMTP settings
  allstill admin set-alerts on|shadow    switch alerts on, or back to shadow mode
  allstill backup [flags] FILE           copy the database while it runs
  allstill guide [-skill]                how to use Allstill, for people and AI assistants
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
	case "probe":
		return probeCommand(ctx, args[1:], stdout, stderr)
	case "run":
		return runJob(ctx, args[1:], stdout, stderr)
	case "import":
		return importCommand(ctx, args[1:], stdout, stderr)
	case "compare":
		return compareCommand(ctx, args[1:], stdout, stderr)
	case "admin":
		return adminCommand(ctx, args[1:], stdout, stderr)
	case "login":
		return loginCommand(ctx, args[1:], stdout, stderr)
	case "logout":
		return logoutCommand(ctx, args[1:], stdout, stderr)
	case "whoami":
		return whoamiCommand(ctx, args[1:], stdout, stderr)
	case "status":
		return statusCommand(ctx, args[1:], stdout, stderr)
	case "projects":
		return projectsCommand(ctx, args[1:], stdout, stderr)
	case "checks":
		return checksCommand(ctx, args[1:], stdout, stderr)
	case "log":
		return logCommand(ctx, args[1:], stdout, stderr)
	case "probes":
		return probesCommand(ctx, args[1:], stdout, stderr)
	case "backup":
		return backup(ctx, args[1:], stdout, stderr)
	case "guide":
		return guideCommand(args[1:], stdout, stderr)
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
	logDays, err := envInt("ALLSTILL_KEEP_LOG", int(store.DefaultLogAge/(24*time.Hour)))
	if err != nil {
		return err
	}
	keepRuns := fs.Int("keep-runs", runs, "how many of its newest runs each check keeps, with their pings (ALLSTILL_KEEP_RUNS)")
	keepOutput := fs.Int("keep-output", output, "of how many of its newest runs each check keeps the output (ALLSTILL_KEEP_OUTPUT)")
	keepLog := fs.Int("keep-log", logDays, "how many days the log is kept (ALLSTILL_KEEP_LOG)")
	mailSettings := smtpFlags(fs)
	shadow := fs.Bool("shadow", os.Getenv("ALLSTILL_SHADOW") == "1", "record alerts without sending them, whatever the setting in the database, such as to serve a copy of it (ALLSTILL_SHADOW=1)")
	allowPrivate := fs.String("allow-private", os.Getenv("ALLSTILL_ALLOW_PRIVATE"), "private ranges the checks Allstill runs may reach, such as 10.0.0.0/8, comma-separated; they are open to every project's checks (ALLSTILL_ALLOW_PRIVATE)")
	canaryAddrs := fs.String("canary", os.Getenv("ALLSTILL_CANARY"), "host:port addresses independent of the sites checked, such as a public DNS server's 9.9.9.9:53, comma-separated: when none answers, the server's own network is down and its checks are unknown instead of down (ALLSTILL_CANARY)")
	contact := fs.String("contact", os.Getenv("ALLSTILL_CONTACT"), "an address for site owners, sent in the User-Agent of every check's request (ALLSTILL_CONTACT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	allow, err := parseRanges(*allowPrivate)
	if err != nil {
		return fmt.Errorf("-allow-private: %w", err)
	}
	canary, err := parseCanary(*canaryAddrs)
	if err != nil {
		return err
	}
	if err := active.CheckContact(*contact); err != nil {
		return fmt.Errorf("-contact: %w", err)
	}
	if *keepLog < 1 {
		// To Retention, zero means the default: not what someone typing 0 asks for.
		return fmt.Errorf("-keep-log: keep the log for at least %d days", int(store.MinLogAge/(24*time.Hour)))
	}
	retention := store.Retention{Runs: *keepRuns, Output: *keepOutput, Log: time.Duration(*keepLog) * 24 * time.Hour}
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
	// The admin commands print links with it, and check new checks' addresses
	// against what the server may reach.
	for k, v := range map[string]string{"base_url": base.String(), "allow_private": *allowPrivate, "canary": strings.Join(canary, ",")} {
		if err := st.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	if n, err := st.PasskeysOfOtherHosts(ctx, base.Hostname()); err != nil {
		return err
	} else if n > 0 {
		log.Warn("some passkeys were made for another host name and do not work here; make new ones with 'allstill admin passkey-link'",
			"passkeys", n, "host", base.Hostname())
	}

	mail, err := mailSettings(helloName(base.String()))
	if err != nil {
		return err
	}
	if mail == nil {
		log.Info("email: no SMTP host is set, so alerts go by push only")
	} else {
		if mail.Plain() {
			log.Warn("email: messages to the SMTP server travel unencrypted", "host", mail.Host)
		}
		go probeEmail(ctx, mail, log)
	}

	key, err := st.VAPIDKey(ctx, ws)
	if err != nil {
		return err
	}
	if base.Scheme != "https" {
		log.Warn("phones only get alerts when Allstill is reached over https: Apple's push service refuses the rest", "base_url", base.String())
	}

	sched := scheduler.New(st, log)
	mux := http.NewServeMux()
	(&ingest.Handler{Store: st, Nudge: sched.Nudge, Log: log, Now: time.Now}).Register(mux)
	wake := make(chan struct{}, 1)
	dashboard, err := web.New(st, ws, base, log)
	if err != nil {
		return err
	}
	dashboard.PushKey = key.PublicKey()
	dashboard.Version = version()
	dashboard.Wake = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
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
	// It records what it would send until an administrator switches alerts
	// on (intent/alerts/spec.md); test messages always go.
	sender := &alert.Sender{
		Store: st, Log: log, Now: time.Now, Shadow: *shadow, Wake: wake,
		Push: &alert.Push{Key: key, Base: base.String(), Client: outbound.Client(30 * time.Second)},
		Base: base.String(),
	}
	if mail != nil {
		sender.Mail = mail
	}
	dashboard.MailOn, dashboard.MailProblem = mail != nil, sender.MailProblem
	dashboard.Behind = sender.Behind

	// The checks Allstill runs itself (intent/http-checks/spec.md).
	runner := &active.Runner{Allow: allow, UserAgent: active.UserAgent(version(), *contact)}
	checks := &active.Loop{
		Checks:  st.ActiveChecks,
		Attempt: runner.Attempt,
		Record: func(ctx context.Context, done []active.Done) error {
			attempts := make([]store.AttemptOf, len(done))
			for i, d := range done {
				attempts[i] = store.AttemptOf{WorkspaceID: d.Check.WorkspaceID, CheckID: d.Check.ID, Attempt: store.Attempt{
					Attempt:  check.Attempt{Outcome: d.Result.Outcome, Reason: d.Result.Reason, At: d.Result.Ended},
					Started:  d.Result.Started,
					Measured: d.Result.Measured,
					Revision: d.Check.Revision,
				}}
			}
			deadline, err := st.RecordAttempts(ctx, attempts)
			if !deadline.IsZero() {
				sched.Nudge(deadline)
			}
			return err
		},
		Log: log,
	}
	if len(canary) > 0 {
		checks.Canary = active.Canary(canary, 5*time.Second)
	} else {
		log.Info("checks: no canary is set, so if the server's own network goes down, the checks it runs go down and alert")
	}
	if len(allow) > 0 {
		log.Warn("checks may reach these private ranges, for every project", "allow_private", *allowPrivate)
	}
	dashboard.Offline = checks.Offline
	dashboard.Nudge = sched.Nudge
	dashboard.Allow, dashboard.Try = allow, runner.Attempt

	errs := make(chan error, 4)
	go func() { errs <- sched.Run(ctx) }()
	go func() { errs <- checks.Run(ctx) }()
	go func() { errs <- sender.Run(ctx) }()
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

func guideCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	skill := fs.Bool("skill", false, "print the skill that points AI assistants to this guide, to save as SKILL.md")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("guide takes no arguments")
	}
	if *skill {
		fmt.Fprint(stdout, guide.Skill)
		return nil
	}
	fmt.Fprintf(stdout, "This guide describes allstill %s.\n\n%s", version(), guide.Manual())
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
