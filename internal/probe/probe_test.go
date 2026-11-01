// SPDX-License-Identifier: AGPL-3.0-only

package probe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
)

// server stands in for Allstill, without a network, so a test can run in fake
// time. down makes every request fail as an unreachable network does.
type server struct {
	mu     sync.Mutex
	checks []api.ProbeCheck
	down   bool
	hang   bool            // answers nothing, until the client gives up
	lose   bool            // takes a post, and loses the answer
	reject map[string]bool // refuses a post holding one of these checks' runs
	during func()          // runs once, during a post
	status int             // refuses with this
	runs   []api.ProbeRun
	seen   map[string]int
	posts  int
	gets   []string // the If-None-Match of each fetch
	keys   []string
	off    *int64
}

func (s *server) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	if s.hang {
		s.mu.Unlock()
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	defer s.mu.Unlock()
	if s.down {
		return nil, errors.New("network is unreachable")
	}
	s.keys = append(s.keys, r.Header.Get("Authorization"))
	w := httptest.NewRecorder()
	switch {
	case s.status != 0:
		w.WriteHeader(s.status)
		json.NewEncoder(w).Encode(api.Problem{Status: s.status, Title: "Refused."})
	case r.Method == http.MethodGet:
		s.gets = append(s.gets, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"same"` {
			w.WriteHeader(http.StatusNotModified)
			break
		}
		w.Header().Set("ETag", `"same"`)
		json.NewEncoder(w).Encode(api.ProbeChecks{Protocol: api.ProbeProtocolVersion, Checks: s.checks, ServerTime: time.Now()})
	default:
		var batch api.ProbeRunBatch
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		for _, run := range batch.Runs {
			if s.reject[run.CheckId] {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(api.Problem{Status: 400, Title: "Not valid."})
				return w.Result(), nil
			}
		}
		if f := s.during; f != nil {
			s.during = nil
			s.mu.Unlock()
			f()
			s.mu.Lock()
		}
		s.posts++
		for _, run := range batch.Runs {
			if s.seen == nil {
				s.seen = map[string]int{}
			}
			s.seen[run.Id]++
			if s.seen[run.Id] == 1 {
				s.runs = append(s.runs, run)
			}
		}
		if s.lose {
			s.lose = false
			return nil, errors.New("connection lost")
		}
		json.NewEncoder(w).Encode(api.ProbeRunResult{Applied: len(batch.Runs), ServerTime: time.Now(), ClockOff: s.off})
	}
	return w.Result(), nil
}

func (s *server) got() (posts int, runs []api.ProbeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts, append([]api.ProbeRun(nil), s.runs...)
}

func (s *server) set(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
}

var httpCheck = api.ProbeCheck{Id: "chk_1", Kind: "http", Interval: 60, Http: &api.HttpCheck{Url: "http://10.0.0.5/"}}

// aProbe runs a probe against s, with attempts that go as attempt says.
func aProbe(s *server, attempt func() active.Result) *Probe {
	base, _ := url.Parse("https://allstill.example.com/")
	return &Probe{
		Server: base, Key: "alp_key", Log: slog.New(slog.DiscardHandler),
		Runner: &active.Runner{UserAgent: "Allstill/test", Transport: func(*tls.Config, time.Duration) http.RoundTripper {
			return roundTrip(func(*http.Request) (*http.Response, error) {
				r := attempt()
				if r.Outcome == check.Failed {
					return nil, errors.New("i/o timeout, as it were")
				}
				w := httptest.NewRecorder()
				return w.Result(), nil
			})
		}},
		HTTP: &http.Client{Transport: s},
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func passing() active.Result { return active.Result{Outcome: check.Passed} }

func TestItPostsItsRunsAndItsSignOfLife(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{checks: []api.ProbeCheck{httpCheck}}
		ctx, stop := context.WithCancel(t.Context())
		done := make(chan error)
		p := aProbe(s, passing)
		go func() { done <- p.Run(ctx) }()

		// One post at the start, then at least one every 30 seconds, also with
		// nothing to tell.
		time.Sleep(time.Second)
		synctest.Wait()
		if posts, _ := s.got(); posts != 1 {
			t.Errorf("%d posts at the start", posts)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		posts, runs := s.got()
		if posts < 4 {
			t.Errorf("%d posts in 2 minutes", posts)
		}
		if len(runs) < 1 || len(runs) > 3 {
			t.Fatalf("%d runs in 2 minutes of a check a minute", len(runs))
		}
		r := runs[0]
		if r.CheckId != "chk_1" || r.Outcome != "passed" || r.Id == "" || r.EndedAgo < 0 || r.EndedAgo > int64(sendEvery/time.Second)+2 {
			t.Errorf("%+v", r)
		}
		s.mu.Lock()
		if s.keys[0] != "Bearer alp_key" {
			t.Errorf("the key: %q", s.keys[0])
		}
		// It asks whether its checks changed, every 30 seconds.
		if len(s.gets) < 4 || s.gets[0] != "" || s.gets[len(s.gets)-1] != `"same"` {
			t.Errorf("fetches: %q", s.gets)
		}
		s.mu.Unlock()
		stop()
		if err := <-done; err != nil {
			t.Errorf("at the end: %v", err)
		}
	})
}

// Spec case 11: an answer lost after a post sends the runs again, with the same
// ids, so the server can tell.
func TestALostAnswerSendsTheSameRunsAgain(t *testing.T) {
	s := &server{lose: true}
	p := aProbe(s, passing)
	p.record(t.Context(), []active.Done{{Check: active.Check{ID: "chk_1"}, Result: active.Result{Outcome: check.Passed, Ended: time.Now()}}})
	if err := p.send(t.Context()); err == nil {
		t.Fatal("the lost answer went unnoticed")
	}
	if len(p.waiting) != 1 {
		t.Fatalf("%d runs wait after the lost answer", len(p.waiting))
	}
	if err := p.send(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(p.waiting) != 0 {
		t.Errorf("%d runs wait after the answer", len(p.waiting))
	}
	if _, runs := s.got(); len(runs) != 1 || s.seen[runs[0].Id] != 2 {
		t.Errorf("runs %+v, seen %v", runs, s.seen)
	}
}

// A run says which settings it was made under, so the server can tell.
func TestARunCarriesItsChecksRevision(t *testing.T) {
	revision := "abc123"
	c, err := checkOf(api.ProbeCheck{Id: "chk_1", Kind: "http", Interval: 60, Revision: &revision, Http: &api.HttpCheck{Url: "https://example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	p := &Probe{Log: slog.New(slog.DiscardHandler)}
	p.record(t.Context(), []active.Done{{Check: c, Result: active.Result{Outcome: check.Passed, Ended: time.Now()}}})
	if got := p.waiting[0].run.Revision; got == nil || *got != revision {
		t.Errorf("%v", got)
	}
}

// With the server out of reach, runs wait, and say their age when it is back.
func TestRunsWaitUntilTheServerHasThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{checks: []api.ProbeCheck{httpCheck}}
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		done := make(chan error)
		p := aProbe(s, passing)
		go func() { done <- p.Run(ctx) }()
		time.Sleep(time.Second)
		synctest.Wait()

		_, before := s.got()
		s.set(func() { s.down = true })
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if _, during := s.got(); len(during) != len(before) {
			t.Errorf("%d runs arrived while it was out of reach", len(during)-len(before))
		}
		s.set(func() { s.down = false })
		time.Sleep(10 * time.Second)
		synctest.Wait()
		_, after := s.got()
		if len(after) < len(before)+3 {
			t.Fatalf("%d runs waited for 5 minutes of a check a minute", len(after)-len(before))
		}
		if oldest := after[len(before)]; oldest.EndedAgo < 200 {
			t.Errorf("the oldest says it ended %d s ago", oldest.EndedAgo)
		}
		stop()
		<-done
	})
}

// The spec's canary: when a target times out and the server cannot be reached
// either, its own network is down, so the attempt judges nothing.
func TestOwnNetworkDownJudgesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{checks: []api.ProbeCheck{httpCheck}}
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		done := make(chan error)
		failing := false
		p := aProbe(s, func() active.Result {
			s.mu.Lock()
			defer s.mu.Unlock()
			if failing {
				return active.Result{Outcome: check.Failed}
			}
			return passing()
		})
		go func() { done <- p.Run(ctx) }()
		time.Sleep(time.Second)
		synctest.Wait()

		s.set(func() { failing, s.down = true, true })
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		_, before := s.got()
		s.set(func() { s.down = false })
		time.Sleep(10 * time.Second)
		synctest.Wait()
		_, runs := s.got()
		if len(runs) <= len(before) {
			t.Fatal("no runs after the network came back")
		}
		for _, r := range runs[len(before):] {
			if r.Outcome != "unjudged" || r.Reason == nil || *r.Reason != notJudged {
				t.Errorf("%+v", r)
			}
		}
		// With the server in reach, the target's failure counts.
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		_, runs = s.got()
		if last := runs[len(runs)-1]; last.Outcome != "failed" {
			t.Errorf("the last run: %+v", last)
		}
		stop()
		<-done
	})
}

// The server hanging, as on a network that drops packets, must not hold up the
// attempts: they are what it is there for.
func TestAHangingServerDoesNotStopTheChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{checks: []api.ProbeCheck{httpCheck}}
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var mu sync.Mutex
		attempts := 0
		p := aProbe(s, func() active.Result {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			return passing()
		})
		p.HTTP.Timeout = 30 * time.Second
		done := make(chan error)
		go func() { done <- p.Run(ctx) }()
		time.Sleep(time.Minute)
		synctest.Wait()

		s.set(func() { s.hang = true })
		mu.Lock()
		before := attempts
		mu.Unlock()
		time.Sleep(6 * time.Minute)
		synctest.Wait()
		mu.Lock()
		during := attempts - before
		mu.Unlock()
		if during < 5 {
			t.Errorf("%d attempts in 6 minutes of a check a minute", during)
		}
		_, sent := s.got()
		s.set(func() { s.hang = false })
		time.Sleep(time.Minute)
		synctest.Wait()
		if _, runs := s.got(); len(runs)-len(sent) < 5 {
			t.Errorf("%d runs arrived after the hang", len(runs)-len(sent))
		}
		stop()
		<-done
	})
}

// A post the server will not take must not silence the others: its runs go, and
// the next post is the sign of life.
func TestARejectedPostDoesNotSilenceTheProbe(t *testing.T) {
	s := &server{reject: map[string]bool{"chk_bad": true}}
	p := aProbe(s, passing)
	done := func(id string) active.Done {
		return active.Done{Check: active.Check{ID: id}, Result: active.Result{Outcome: check.Failed, Ended: time.Now()}}
	}
	p.record(t.Context(), []active.Done{done("chk_bad")})
	if err := p.send(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(p.waiting) != 0 {
		t.Errorf("%d rejected runs still wait", len(p.waiting))
	}
	p.record(t.Context(), []active.Done{done("chk_good")})
	if err := p.send(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, runs := s.got(); len(runs) != 1 || runs[0].CheckId != "chk_good" {
		t.Errorf("%+v", runs)
	}
}

// A server with trouble of its own did answer: the network works, so a failing
// target counts against the target, and runs wait for it to be well.
func TestAServerErrorKeepsTheRuns(t *testing.T) {
	s := &server{status: http.StatusInternalServerError}
	p := aProbe(s, passing)
	p.record(t.Context(), []active.Done{{Check: active.Check{ID: "chk_1"}, Result: active.Result{Outcome: check.Passed, Ended: time.Now()}}})
	err := p.send(t.Context())
	var r *Refused
	if !errors.As(err, &r) || r.fatal() || r.rejected() {
		t.Errorf("%v", err)
	}
	if len(p.waiting) != 1 {
		t.Errorf("%d runs wait", len(p.waiting))
	}
}

func TestALongReasonIsCutToWhatTheAPITakes(t *testing.T) {
	p := &Probe{Log: slog.New(slog.DiscardHandler)}
	p.record(t.Context(), []active.Done{{Check: active.Check{ID: "chk_1"}, Result: active.Result{Outcome: check.Failed, Ended: time.Now(), Reason: strings.Repeat("é", 600)}}})
	if got := []rune(*p.waiting[0].run.Reason); len(got) != maxReason || got[len(got)-1] != '…' {
		t.Errorf("%d characters", len(got))
	}
}

// Runs dropped from the waiting list during a post must not make it lose others.
func TestADropDuringAPostKeepsTheRest(t *testing.T) {
	s := &server{}
	p := aProbe(s, passing)
	run := func() active.Done {
		return active.Done{Check: active.Check{ID: "chk_1"}, Result: active.Result{Outcome: check.Passed, Ended: time.Now()}}
	}
	done := make([]active.Done, keepRuns)
	for i := range done {
		done[i] = run()
	}
	p.record(t.Context(), done)
	// As if the post were out when more runs came in, and the oldest were dropped.
	s.during = func() { p.record(t.Context(), []active.Done{run(), run(), run()}) }
	if err := p.send(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, runs := s.got(); len(runs) != keepRuns+3 {
		t.Errorf("%d of %d runs arrived", len(runs), keepRuns+3)
	}
}

// A wrong -server gets an answer that is no yes: it must not pass in silence.
func TestARefusalIsLoggedOnce(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway} {
		var log strings.Builder
		s := &server{status: status}
		p := aProbe(s, passing)
		p.Log = slog.New(slog.NewTextHandler(&log, nil))
		for range 3 {
			if _, err := p.post(t.Context(), nil); err == nil {
				t.Fatalf("%d: no error", status)
			}
		}
		if n := strings.Count(log.String(), "the server refused"); n != 1 {
			t.Errorf("%d: logged %d times: %s", status, n, log.String())
		}
		if !strings.Contains(log.String(), "status="+strconv.Itoa(status)) {
			t.Errorf("%d: %s", status, log.String())
		}
		s.set(func() { s.status = 0 })
		if _, err := p.post(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "takes this probe's posts again") {
			t.Errorf("%d: back: %s", status, log.String())
		}
	}
}

// A wrong key or another version is for a person: it stops and says so.
func TestItStopsWhenRefused(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusUpgradeRequired} {
		synctest.Test(t, func(t *testing.T) {
			s := &server{status: status}
			err := aProbe(s, passing).Run(t.Context())
			var r *Refused
			if !errors.As(err, &r) || !r.fatal() || !strings.Contains(err.Error(), "Refused.") {
				t.Errorf("%d: %v", status, err)
			}
		})
		synctest.Test(t, func(t *testing.T) {
			// Refused later on, as when its key is replaced.
			s := &server{checks: []api.ProbeCheck{httpCheck}}
			done := make(chan error)
			go func() { done <- aProbe(s, passing).Run(t.Context()) }()
			time.Sleep(time.Second)
			synctest.Wait()
			s.set(func() { s.status = status })
			select {
			case err := <-done:
				if r, ok := errors.AsType[*Refused](err); !ok || !r.fatal() {
					t.Errorf("%d: %v", status, err)
				}
			case <-time.After(time.Minute):
				t.Errorf("%d: it kept running", status)
			}
		})
	}
	// A server that has trouble of its own is not a reason to stop.
	synctest.Test(t, func(t *testing.T) {
		s := &server{status: http.StatusInternalServerError}
		ctx, stop := context.WithTimeout(t.Context(), time.Minute)
		defer stop()
		if err := aProbe(s, passing).Run(ctx); err != nil {
			t.Errorf("a 500: %v", err)
		}
	})
}

func TestItKeepsAtMostSoManyRuns(t *testing.T) {
	p := &Probe{Log: slog.New(slog.DiscardHandler)}
	done := make([]active.Done, keepRuns+10)
	for i := range done {
		done[i] = active.Done{Check: active.Check{ID: "chk_1"}, Result: active.Result{Outcome: check.Passed, Ended: time.Now(), Reason: strings.Repeat("x", i%2)}}
	}
	if err := p.record(t.Context(), done); err != nil {
		t.Fatal(err)
	}
	if len(p.waiting) != keepRuns {
		t.Errorf("%d runs wait", len(p.waiting))
	}
}

func TestChecksAreHeldToTheSameRules(t *testing.T) {
	port := 993
	useTLS := true
	cases := []struct {
		name string
		c    api.ProbeCheck
		ok   bool
	}{
		{"HTTP", httpCheck, true},
		{"TCP", api.ProbeCheck{Id: "c", Kind: "tcp", Interval: 60, Tcp: &api.TcpCheck{Host: "mail.example.com", Port: port, Tls: &useTLS}}, true},
		{"a bad URL", api.ProbeCheck{Id: "c", Kind: "http", Interval: 60, Http: &api.HttpCheck{Url: "ftp://example.com"}}, false},
		{"a bad port", api.ProbeCheck{Id: "c", Kind: "tcp", Interval: 60, Tcp: &api.TcpCheck{Host: "mail.example.com", Port: 0}}, false},
		{"no settings", api.ProbeCheck{Id: "c", Kind: "http", Interval: 60}, false},
		{"no interval", api.ProbeCheck{Id: "c", Kind: "http", Http: &api.HttpCheck{Url: "https://example.com"}}, false},
		{"under a minute", api.ProbeCheck{Id: "c", Kind: "http", Interval: 1, Http: &api.HttpCheck{Url: "https://example.com"}}, false},
		{"an unknown kind", api.ProbeCheck{Id: "c", Kind: "dns", Interval: 60}, false},
	}
	for _, c := range cases {
		got, err := checkOf(c.c)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.name == "TCP" && (got.TCP == nil || got.TCP.Address() != "mail.example.com:993" || !got.TCP.TLS) {
			t.Errorf("TCP: %+v", got)
		}
	}
}
