// SPDX-License-Identifier: AGPL-3.0-only

// Package probe is `allstill probe`, which runs checks inside a network the
// server cannot reach (intent/http-checks/spec.md): it connects out to the
// server, fetches its checks, runs them with the server's own loop, and sends
// the runs back. It opens no port.
//
// A post is also its sign of life, and its canary: when an attempt times out and
// the server cannot be reached either, its own network is down, so the attempt
// judges nothing. Runs wait until the server can be reached, and say how long
// ago they ended, so a retry or a wrong clock cannot move a check.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/id"
)

const (
	// sendEvery is how often it looks for runs to post.
	sendEvery = 5 * time.Second
	// aliveEvery is how long it goes without a post at most (C7).
	aliveEvery = 30 * time.Second
	// keepRuns is how many runs wait while the server cannot be reached; the
	// oldest go first. At a check a minute, that is hours for many checks.
	keepRuns = 5000
	// batch is how many runs one post holds at most, and maxReason how long a
	// reason is (the API's limits).
	batch     = 500
	maxReason = 500

	notJudged = "not judged: the probe's network was down"
)

// Probe connects a loop of checks to the server.
type Probe struct {
	Server *url.URL
	Key    string
	Runner *active.Runner
	Log    *slog.Logger
	// HTTP is the client for the server, which is the administrator's choice
	// of address, so not held to the checks' address rules.
	HTTP *http.Client

	mu       sync.Mutex
	waiting  []waitingRun
	lastPost time.Time
	etag     string
	checks   []active.Check
	unreach  bool
	refusal  int // the status of the last post, when the server refused it
	clockOff int64
}

type waitingRun struct {
	run   api.ProbeRun
	ended time.Time
}

// Refused is an answer from the server other than yes. Being an answer, it
// proves the network works.
type Refused struct {
	Status int
	Detail string
}

func (r *Refused) Error() string { return r.Detail }

// fatal says only a person can solve it: a wrong key, or another version.
func (r *Refused) fatal() bool {
	return r.Status == http.StatusUnauthorized || r.Status == http.StatusUpgradeRequired
}

// rejected says the post itself is what the server will not take, so sending it
// again cannot help.
func (r *Refused) rejected() bool {
	return r.Status == http.StatusBadRequest || r.Status == http.StatusRequestEntityTooLarge
}

// Run runs the probe until ctx ends, or the server refuses it for good.
func (p *Probe) Run(ctx context.Context) error {
	if p.HTTP == nil {
		p.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	// A redirect would be an answer to something else; the key stays here.
	p.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopOn := func(err error) {
		if r, ok := errors.AsType[*Refused](err); ok && r.fatal() {
			cancel(r)
		}
	}

	// Refused at once, rather than at the first post.
	if _, err := p.post(ctx, nil); err != nil {
		if r, ok := errors.AsType[*Refused](err); ok && r.fatal() {
			return r
		}
	}
	refresh := func() {
		if err := p.fetch(ctx); err != nil && ctx.Err() == nil {
			stopOn(err)
			p.reached(err)
		}
	}
	refresh()

	loop := &active.Loop{
		// What it last fetched: the loop must not wait for the server, which
		// may hang, as its attempts start from the loop's own goroutine.
		Checks:    func(context.Context) ([]active.Check, error) { return p.current(), nil },
		Attempt:   p.Runner.Attempt,
		Record:    p.record,
		NotJudged: notJudged,
		Canary: func(ctx context.Context) bool {
			ctx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			_, err := p.post(ctx, nil)
			return err == nil || errors.As(err, new(*Refused))
		},
		Log: p.Log,
	}
	var wg sync.WaitGroup
	wg.Go(func() { loop.Run(ctx) })
	wg.Go(func() {
		tick := time.NewTicker(active.Reload)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				refresh()
			}
		}
	})
	tick := time.NewTicker(sendEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			if r, ok := errors.AsType[*Refused](context.Cause(ctx)); ok {
				return r
			}
			// What finished before the end goes along, if the server answers.
			last, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stop()
			p.send(last)
			return nil
		case <-tick.C:
			stopOn(p.send(ctx))
		}
	}
}

// record keeps finished attempts until a post takes them.
func (p *Probe) record(_ context.Context, done []active.Done) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range done {
		run := api.ProbeRun{Id: id.UUID(), CheckId: d.Check.ID, Outcome: api.ProbeRunOutcome(d.Result.Outcome)}
		if d.Result.Reason != "" {
			reason := limited(d.Result.Reason)
			run.Reason = &reason
		}
		if len(d.Result.Measured) > 0 {
			values := d.Result.Measured
			run.Values = &values
		}
		p.waiting = append(p.waiting, waitingRun{run, d.Result.Ended})
	}
	if over := len(p.waiting) - keepRuns; over > 0 {
		p.Log.Warn("the server has been out of reach for long: dropping the oldest runs", "runs", over)
		p.waiting = append(p.waiting[:0], p.waiting[over:]...)
	}
	return nil
}

// send posts what waits, or when nothing has been posted for a while nothing,
// as its sign of life. Runs stay until the server has them; sent again after a
// lost answer, their ids keep them from counting twice. A post the server will
// not take goes, so it cannot hold up the others or the sign of life.
func (p *Probe) send(ctx context.Context) error {
	for {
		p.mu.Lock()
		n := min(len(p.waiting), batch)
		first := append([]waitingRun(nil), p.waiting[:n]...)
		due := time.Since(p.lastPost) >= aliveEvery
		p.mu.Unlock()
		if n == 0 && !due {
			return nil
		}
		_, err := p.post(ctx, first)
		if r, ok := errors.AsType[*Refused](err); ok && r.rejected() {
			p.Log.Error("the server would not take these runs, so they are dropped", "runs", n, "error", r)
		} else if err != nil {
			return err
		}
		p.mu.Lock()
		// By id: while the post was out, the oldest may have been dropped.
		sent := make(map[string]bool, n)
		for _, r := range first {
			sent[r.run.Id] = true
		}
		p.waiting = slices.DeleteFunc(p.waiting, func(w waitingRun) bool { return sent[w.run.Id] })
		more := len(p.waiting) > 0
		p.mu.Unlock()
		if !more {
			return nil
		}
	}
}

// post sends runs, with how long ago each ended as of now.
func (p *Probe) post(ctx context.Context, runs []waitingRun) (api.ProbeRunResult, error) {
	var res api.ProbeRunResult
	body := api.ProbeRunBatch{SentAt: time.Now().UTC(), Runs: make([]api.ProbeRun, len(runs))}
	for i, r := range runs {
		body.Runs[i] = r.run
		body.Runs[i].EndedAgo = max(int64(time.Since(r.ended)/time.Second), 0)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return res, err
	}
	resp, err := p.do(ctx, http.MethodPost, "probe/runs", raw, "")
	if err != nil {
		p.reached(err)
		return res, err
	}
	defer resp.Body.Close()
	if err := refused(resp); err != nil {
		p.reached(err)
		return res, err
	}
	p.reached(nil)
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&res); err != nil {
		return res, fmt.Errorf("the server's answer cannot be read: %w", err)
	}
	if res.Ignored > 0 {
		p.Log.Warn("the server ignored runs, as seen before, too old, or not valid", "runs", res.Ignored)
	}
	p.mu.Lock()
	p.lastPost = time.Now()
	var off int64
	if res.ClockOff != nil {
		off = *res.ClockOff
	}
	changed := off != 0 && p.clockOff == 0 || off == 0 && p.clockOff != 0
	p.clockOff = off
	p.mu.Unlock()
	if changed && off != 0 {
		p.Log.Error("this machine's clock is off from the server's, so the checks here count as unknown until it is set right", "seconds", off)
	} else if changed {
		p.Log.Info("this machine's clock agrees with the server's again")
	}
	return res, nil
}

// reached notes how the server answered, and says so when that changes: any
// refusal, as a wrong -server gets nothing else, and when it cannot be reached.
func (p *Probe) reached(err error) {
	r, answered := errors.AsType[*Refused](err)
	refusal := 0
	if answered {
		refusal = r.Status
	}
	unreach := err != nil && !answered
	p.mu.Lock()
	wasRefused, wasUnreach := p.refusal, p.unreach
	p.refusal, p.unreach = refusal, unreach
	p.mu.Unlock()
	switch {
	case refusal != 0 && refusal != wasRefused:
		p.Log.Error("the server refused this probe; runs wait until it takes them", "status", refusal, "error", err)
	case unreach && !wasUnreach:
		p.Log.Warn("the server cannot be reached; runs wait until it can", "error", err)
	case err == nil && (wasRefused != 0 || wasUnreach):
		p.Log.Info("the server takes this probe's posts again")
	}
}

// limited keeps a reason within what the API takes.
func limited(reason string) string {
	if r := []rune(reason); len(r) > maxReason {
		return string(r[:maxReason-1]) + "…"
	}
	return reason
}

// current is the checks it last fetched.
func (p *Probe) current() []active.Check {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checks
}

// fetch asks the server for its checks, if they changed. On any trouble it keeps
// what it had, and goes on running those.
func (p *Probe) fetch(ctx context.Context) error {
	p.mu.Lock()
	etag := p.etag
	p.mu.Unlock()
	resp, err := p.do(ctx, http.MethodGet, "probe/checks", nil, etag)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if err := refused(resp); err != nil {
		return err
	}
	var body api.ProbeChecks
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return fmt.Errorf("the server's answer cannot be read: %w", err)
	}
	checks := make([]active.Check, 0, len(body.Checks))
	for _, c := range body.Checks {
		ac, err := checkOf(c)
		if err != nil {
			// From a newer server, say: the others still run.
			p.Log.Error("a check cannot be read", "check", c.Id, "error", err)
			continue
		}
		checks = append(checks, ac)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.etag == "" || len(checks) != len(p.checks) {
		p.Log.Info("checks to run", "checks", len(checks))
	}
	p.etag, p.checks = resp.Header.Get("ETag"), checks
	return nil
}

// checkOf reads a check as the API gives it, through the rules the store
// holds it to.
func checkOf(c api.ProbeCheck) (active.Check, error) {
	ac := active.Check{ID: c.Id, Every: time.Duration(c.Interval) * time.Second}
	if ac.Every < time.Minute {
		return ac, errors.New("its interval is under a minute")
	}
	var raw []byte
	var err error
	switch c.Kind {
	case api.CheckKind("http"):
		if raw, err = json.Marshal(c.Http); err == nil {
			ac.HTTP, err = active.ParseHTTP(raw)
		}
	case api.CheckKind("tcp"):
		var t active.TCP
		if raw, err = json.Marshal(c.Tcp); err == nil {
			t, err = active.ParseTCP(raw)
		}
		ac.TCP = &t
	default:
		err = fmt.Errorf("this probe does not know the kind %q: update it", c.Kind)
	}
	return ac, err
}

func (p *Probe) do(ctx context.Context, method, path string, body []byte, etag string) (*http.Response, error) {
	u := p.Server.JoinPath("api/v1", path)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Allstill-Probe-Protocol", fmt.Sprint(api.ProbeProtocolVersion))
	req.Header.Set("User-Agent", p.Runner.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	return p.HTTP.Do(req)
}

// refused turns anything but 200 into a Refused.
func refused(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var problem api.Problem
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&problem)
	what := strings.TrimSpace(problem.Title)
	if problem.Detail != nil {
		what += " " + *problem.Detail
	}
	if what == "" {
		what = fmt.Sprintf("the server answered %d: is -server its address?", resp.StatusCode)
	}
	return &Refused{resp.StatusCode, what}
}
