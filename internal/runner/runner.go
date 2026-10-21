// SPDX-License-Identifier: AGPL-3.0-only

// Package runner runs a job and reports it: a start ping before, and after it
// an end ping with its exit status, the tail of its output and the values it
// wrote. It never changes the job's exit status, and an unreachable server
// never holds the job up: each ping gives up after a timeout.
//
// The ping URLs follow Healthchecks, so the same run can report to Allstill and
// to Healthchecks while checks move over.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
)

// Options says what to run and where to report.
type Options struct {
	URLs    []string      // ping URLs: a check's, or a project ping key's with a slug
	Command []string      // the job and its arguments
	Timeout time.Duration // per ping
	Tail    int           // bytes of output to send with the end ping
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
	Client  *http.Client
	Warn    func(format string, args ...any) // problems reaching the server
}

// ValuesEnv names the file a job may write values to, one `name=number` per
// line; they are sent with the end ping.
const ValuesEnv = "ALLSTILL_VALUES_FILE"

// waitDelay is how long the end waits for the job's output to close after
// the job itself has exited, such as when it left a process running in the
// background that still holds its output.
const waitDelay = 2 * time.Second

// Run runs the job and returns its exit status.
func Run(ctx context.Context, o Options) int {
	// Signals are caught from the start, so one that arrives during the start
	// ping is passed on to the job as soon as it runs.
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, append([]os.Signal{os.Interrupt}, forwarded...)...)
	defer signal.Stop(signals)

	rid := id.UUID()
	o.pingAll(ctx, "/start", rid, nil, "")

	values, err := os.CreateTemp("", "allstill-values-*")
	if err != nil {
		o.Warn("cannot create the values file: %v", err)
	} else {
		values.Close()
		defer os.Remove(values.Name())
	}

	tail := &tailBuffer{max: o.Tail}
	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = io.MultiWriter(o.Stdout, tail)
	cmd.Stderr = io.MultiWriter(o.Stderr, tail)
	cmd.Env = os.Environ()
	cmd.WaitDelay = waitDelay
	if values != nil {
		cmd.Env = append(cmd.Env, ValuesEnv+"="+values.Name())
	}

	status := 0
	started := time.Now()
	if err := cmd.Start(); err != nil {
		// As a shell would: 127 when there is no such command, else 126.
		status = 126
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			status = 127
		}
		fmt.Fprintf(o.Stderr, "allstill run: %v\n", err)
		fmt.Fprintf(tail, "allstill run: %v\n", err)
	} else {
		stop := forwardSignals(signals, cmd.Process)
		err := cmd.Wait()
		stop()
		status = exitStatus(cmd.ProcessState, err)
	}

	header := ""
	if values != nil {
		header = readValues(values.Name(), o.Warn)
	}
	body := tail.String()
	if body == "" {
		body = fmt.Sprintf("Exit status %d after %s.\n", status, time.Since(started).Round(time.Second))
	}
	o.pingAll(ctx, fmt.Sprintf("/%d", min(status, 255)), rid, []byte(body), header)
	return status
}

// forwardSignals passes signals sent to this process on to the job. An
// interrupt from a terminal already reaches the job through its process group,
// so it is not sent again; it is only kept from stopping this process. (An
// interrupt sent to this process alone is therefore not passed on.)
func forwardSignals(ch <-chan os.Signal, p *os.Process) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case sig := <-ch:
				if sig != os.Interrupt {
					p.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func exitStatus(ps *os.ProcessState, err error) int {
	if ps == nil {
		return 1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

// pingAll sends one ping to every URL at once and waits for all of them, each
// for at most the timeout.
func (o Options) pingAll(ctx context.Context, suffix, rid string, body []byte, values string) {
	var wg sync.WaitGroup
	for _, u := range o.URLs {
		wg.Go(func() {
			if err := o.ping(ctx, u, suffix, rid, body, values); err != nil {
				o.Warn("ping %s to %s: %v", strings.TrimPrefix(suffix, "/"), redact(u), err)
			}
		})
	}
	wg.Wait()
}

func (o Options) ping(ctx context.Context, base, suffix, rid string, body []byte, values string) error {
	u, err := url.Parse(base)
	if err != nil {
		return errors.New("the ping URL cannot be read")
	}
	// The suffix goes on the path: a URL may carry a query, such as create=1.
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	u.RawPath = ""
	q := u.Query()
	q.Set("rid", rid)
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	// Try again on a network error or a server error, until the timeout.
	for attempt := 0; ; attempt++ {
		method, rd := http.MethodGet, io.Reader(nil)
		if body != nil {
			method, rd = http.MethodPost, bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "allstill-run")
		if values != "" {
			req.Header.Set("Allstill-Values", values)
		}
		resp, err := o.Client.Do(req)
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL, which holds the secret
		}
		if err == nil {
			answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			if resp.StatusCode < 500 {
				if resp.StatusCode >= 300 {
					return fmt.Errorf("the server answered %s", resp.Status)
				}
				// Answers that are 200 but did not count, from Allstill and
				// from hc-ping.com.
				switch strings.TrimSpace(string(answer)) {
				case "OK (rate limited)":
					return errors.New("the ping was dropped: this check is pinged too often")
				case "OK (not found)":
					return errors.New("no check has this ping URL")
				}
				return nil
			}
			err = fmt.Errorf("the server answered %s", resp.Status)
		}
		wait := time.Duration(attempt+1) * 500 * time.Millisecond
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

// redact keeps the host of a ping URL and hides the path, which is a secret.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "a ping URL"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

var (
	valueName   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	valueNumber = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,6})?$`)
)

// readValues turns the job's `name=number` lines into an Allstill-Values
// header. Lines the server would refuse are reported and skipped, so they
// never cost the end ping.
func readValues(path string, warn func(string, ...any)) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	var members []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, num, ok := strings.Cut(line, "=")
		name, num = strings.TrimSpace(name), strings.TrimSpace(num)
		if !ok || !valueName.MatchString(name) || !valueNumber.MatchString(num) {
			warn("skipped a line in the values file, not name=number: %q", line)
			continue
		}
		if len(members) == 32 {
			warn("skipped values after the first 32")
			break
		}
		members = append(members, name+"="+num)
	}
	return strings.Join(members, ", ")
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.buf), "")
}
