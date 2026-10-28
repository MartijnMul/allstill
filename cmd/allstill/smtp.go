// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/email"
)

// smtpFlags adds the settings for sending email to fs, named as other
// self-hosted services name them (intent/alerts/plan.md, step 4). Once fs is
// parsed, the function it returns reads them: nil when no host is set. The
// password is never a flag, which anyone who can list processes would see.
func smtpFlags(fs *flag.FlagSet) func(hello string) (*email.Config, error) {
	host := fs.String("smtp-host", os.Getenv("ALLSTILL_SMTP_HOST"), "the SMTP server email is sent through, such as smtp.example.com (ALLSTILL_SMTP_HOST; no email without it)")
	port := fs.String("smtp-port", os.Getenv("ALLSTILL_SMTP_PORT"), "its port (ALLSTILL_SMTP_PORT; default 587)")
	security := fs.String("smtp-security", os.Getenv("ALLSTILL_SMTP_SECURITY"), "starttls, tls or none (ALLSTILL_SMTP_SECURITY; default tls on ports 465 and 2465, starttls otherwise)")
	username := fs.String("smtp-username", os.Getenv("ALLSTILL_SMTP_USERNAME"), "the user name to sign in with (ALLSTILL_SMTP_USERNAME)")
	passwordFile := fs.String("smtp-password-file", os.Getenv("ALLSTILL_SMTP_PASSWORD_FILE"), "a file holding the password, such as a container secret (ALLSTILL_SMTP_PASSWORD_FILE; or the password itself in ALLSTILL_SMTP_PASSWORD)")
	from := fs.String("smtp-from", os.Getenv("ALLSTILL_SMTP_FROM"), `the sender people see, on a domain the server sends for, such as "Allstill <allstill@example.com>" (ALLSTILL_SMTP_FROM)`)
	caFile := fs.String("smtp-ca-file", os.Getenv("ALLSTILL_SMTP_CA_FILE"), "certificates to trust instead of the system's, for a relay with its own (ALLSTILL_SMTP_CA_FILE)")
	return func(hello string) (*email.Config, error) {
		if *host == "" {
			return nil, nil
		}
		c := &email.Config{
			Host: *host, Security: email.Security(strings.ToLower(*security)), Username: *username,
			Password: os.Getenv("ALLSTILL_SMTP_PASSWORD"), From: *from, Hello: hello,
		}
		if *port != "" {
			n, err := strconv.Atoi(*port)
			if err != nil {
				return nil, fmt.Errorf("the SMTP port %q is not a number", *port)
			}
			c.Port = n
		}
		if *passwordFile != "" {
			if c.Password != "" {
				return nil, errors.New("set the SMTP password or a file holding it, not both")
			}
			b, err := os.ReadFile(*passwordFile)
			if err != nil {
				return nil, fmt.Errorf("reading the SMTP password: %w", err)
			}
			c.Password = strings.TrimRight(string(b), "\r\n")
		}
		if *caFile != "" {
			b, err := os.ReadFile(*caFile)
			if err != nil {
				return nil, fmt.Errorf("reading the SMTP certificates: %w", err)
			}
			c.RootCAs = x509.NewCertPool()
			if !c.RootCAs.AppendCertsFromPEM(b) {
				return nil, fmt.Errorf("%s holds no PEM certificate", *caFile)
			}
		}
		if err := c.Check(); err != nil {
			return nil, fmt.Errorf("email: %w", err)
		}
		return c, nil
	}
}

// testEmail sends one message with the server's settings and says how far
// it got, to get them right before anything depends on them.
func testEmail(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	// The settings may sit in a file only root reads, as the Linux package
	// keeps them: read it before the flags take their defaults from the
	// environment.
	if file := envFileArg(args); file != "" {
		if err := loadEnvFile(file); err != nil {
			return err
		}
	}
	fs := flag.NewFlagSet("admin test-email", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "the address to send the test to")
	fs.String("env-file", "", "read the settings from this file first, as systemd does, such as /etc/allstill/allstill.env")
	baseURL := fs.String("base-url", os.Getenv("ALLSTILL_BASE_URL"), "the dashboard's address, whose host name Allstill introduces itself with (ALLSTILL_BASE_URL)")
	settings := smtpFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	address, err := email.ParseAddress(*to)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}
	cfg, err := settings(helloName(*baseURL))
	if err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("no SMTP host is set: set ALLSTILL_SMTP_HOST and the rest, as for allstill serve")
	}
	if cfg.Plain() {
		fmt.Fprintf(stdout, "Warning: messages to %s travel unencrypted.\n", cfg.Host)
	}
	cfg.Step = func(s string) { fmt.Fprintln(stdout, "ok:", s) }
	out, err := cfg.Send(ctx, email.Message{
		To: address, Subject: "Allstill test message",
		Body: "This is a test message from Allstill. Email works with these settings.\n",
	}, time.Now())
	switch out {
	case email.Sent:
		fmt.Fprintf(stdout, "Sent to %s. If it does not arrive, look in the spam folder, and check that %s may send for the From address's domain (SPF, DKIM).\n", address, cfg.Host)
		return nil
	case email.Retry:
		return fmt.Errorf("not sent, but it may work later: %w", err)
	case email.Rejected:
		return fmt.Errorf("the server refused %s: %w", address, err)
	}
	return fmt.Errorf("not sent; the settings need a change: %w", err)
}

// helloName is the host name of the dashboard's address, an IP address as
// SMTP writes one, or localhost.
func helloName(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return "localhost"
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if ip.Is6() {
			return "[IPv6:" + ip.String() + "]"
		}
		return "[" + ip.String() + "]"
	}
	return u.Hostname()
}

// envFileArg finds -env-file in args.
func envFileArg(args []string) string {
	for i, a := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "env-file" {
			continue
		}
		if hasValue {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// loadEnvFile sets the variables of a file written for systemd's
// EnvironmentFile, in its common form: NAME=value lines, comments starting
// with # or ;, and values that may be quoted. Escapes and continued lines
// are not read.
func loadEnvFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading the settings: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		os.Setenv(strings.TrimSpace(name), value)
	}
	return nil
}

// probeEmail signs in once at start, without sending, and logs whether
// that worked; a mail server that is down does not stop Allstill.
func probeEmail(ctx context.Context, cfg *email.Config, log interface {
	Info(string, ...any)
	Warn(string, ...any)
}) {
	if err := cfg.Probe(ctx); err != nil {
		log.Warn("email: the SMTP server cannot be used; email will fail until it can", "host", cfg.Host, "error", err)
		return
	}
	if cfg.Username == "" {
		log.Info("email: the SMTP server answers", "host", cfg.Host)
		return
	}
	log.Info("email: the SMTP server takes Allstill's sign-in", "host", cfg.Host)
}
