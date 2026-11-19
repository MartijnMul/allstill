// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

// network stands for the internet in a bubble: each host answers as its
// function says, and the canary answers while canary is true.
type network struct {
	mu      sync.Mutex
	hosts   map[string]func(*http.Request) (*http.Response, error)
	canary  bool
	asked   int // how often the canary was asked
	running map[string]int
	most    int // the most attempts at once
	overlap []string
	starts  map[string][]time.Time
}

func newNetwork() *network {
	return &network{hosts: map[string]func(*http.Request) (*http.Response, error){}, canary: true,
		running: map[string]int{}, starts: map[string][]time.Time{}}
}

func (n *network) set(host string, f func(*http.Request) (*http.Response, error)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hosts[host] = f
}

func (n *network) setCanary(up bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.canary = up
}

func (n *network) RoundTrip(req *http.Request) (*http.Response, error) {
	n.mu.Lock()
	f := n.hosts[req.URL.Hostname()]
	n.mu.Unlock()
	if f == nil {
		f = answer(200, "")
	}
	return f(req)
}

// answer is a host that answers at once.
func answer(code int, body string, header ...string) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		h := http.Header{}
		for i := 0; i+1 < len(header); i += 2 {
			h.Set(header[i], header[i+1])
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
}

// slow is a host that answers after d.
func slow(d time.Duration) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		select {
		case <-time.After(d):
			return answer(200, "")(req)
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}

// silent is a host that never answers.
func silent(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// refuse is a host whose connections fail with err, such as refused.
func refuse(err syscall.Errno) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
}

// sink applies what the loop records to each check's status, as the store
// does, with the default of down after two failures.
type sink struct {
	mu       sync.Mutex
	status   map[string]check.Status
	done     []Done
	batches  int
	attempts map[string][]Done
}

func newSink() *sink { return &sink{status: map[string]check.Status{}, attempts: map[string][]Done{}} }

func (s *sink) record(_ context.Context, done []Done) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches++
	for _, d := range done {
		cfg := check.Config{Active: true, FailAfter: 2, Schedule: check.Interval(d.Check.Every), Grace: max(2*time.Minute, d.Check.Every)}
		// These cases are about sites that have worked; one that never has stays
		// new, which internal/check tests.
		before, seen := s.status[d.Check.ID]
		if !seen {
			before.EverPassed = true
		}
		st, err := check.OnAttempt(cfg, before, check.Attempt{Outcome: d.Result.Outcome, Reason: d.Result.Reason, At: d.Result.Ended})
		if err != nil {
			return err
		}
		s.status[d.Check.ID] = st
		s.done = append(s.done, d)
		s.attempts[d.Check.ID] = append(s.attempts[d.Check.ID], d)
	}
	return nil
}

func (s *sink) state(id string) check.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[id]
}

func (s *sink) count(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts[id])
}

// loop runs checks on the network until the test ends. With canary false,
// the server has no canary. Its checks can be set again; the loop reads
// them every Reload.
func loop(t *testing.T, n *network, s *sink, canary bool, checks ...Check) (func(...Check), *Loop) {
	t.Helper()
	r := &Runner{UserAgent: "Allstill/test", Transport: func(*tls.Config, time.Duration) http.RoundTripper { return n }}
	var mu sync.Mutex
	l := &Loop{
		Checks: func(context.Context) ([]Check, error) {
			mu.Lock()
			defer mu.Unlock()
			return checks, nil
		},
		Record: s.record,
		Attempt: func(ctx context.Context, c Check) Result {
			host := c.host()
			n.mu.Lock()
			n.running[host]++
			total := 0
			for _, k := range n.running {
				total += k
			}
			n.most = max(n.most, total)
			if n.running[host] > 1 {
				n.overlap = append(n.overlap, host)
			}
			n.starts[c.ID] = append(n.starts[c.ID], time.Now())
			n.mu.Unlock()
			defer func() {
				n.mu.Lock()
				n.running[host]--
				n.mu.Unlock()
			}()
			return r.HTTP(ctx, c.HTTP)
		},
		Log: slog.New(slog.DiscardHandler),
	}
	if canary {
		l.Canary = func(ctx context.Context) bool {
			n.mu.Lock()
			up := n.canary
			n.asked++
			n.mu.Unlock()
			if !up {
				// A canary that does not answer takes its timeout.
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
				}
			}
			return up
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := l.Run(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return func(cs ...Check) {
		mu.Lock()
		defer mu.Unlock()
		checks = cs
	}, l
}

func site(id, host string, h HTTP) Check {
	h.URL = "https://" + host + "/health?key=" + token
	return Check{ID: id, WorkspaceID: "wsp_1", Every: time.Minute, HTTP: h}
}

// Each check starts at its own offset within its interval, the same one
// every time, so attempts spread over the minute.
func TestLoopSpreadsAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, s := newNetwork(), newSink()
		var checks []Check
		for i := range 10 {
			checks = append(checks, site(fmt.Sprintf("chk_%d", i), fmt.Sprintf("site-%d.example.com", i), HTTP{}))
		}
		loop(t, n, s, false, checks...)
		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()
		offsets := map[time.Duration]bool{}
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, c := range checks {
			starts := n.starts[c.ID]
			if len(starts) < 3 || len(starts) > 4 {
				t.Fatalf("%s started %d times in 3 minutes", c.ID, len(starts))
			}
			for i, at := range starts {
				if at.Sub(at.Truncate(time.Minute)) != c.offset() || (i > 0 && at.Sub(starts[i-1]) != time.Minute) {
					t.Errorf("%s started at %v, offset %v", c.ID, starts, c.offset())
				}
			}
			offsets[c.offset()] = true
		}
		if len(offsets) < 8 {
			t.Errorf("10 checks share %d offsets", len(offsets))
		}
	})
}

func TestSlot(t *testing.T) {
	c := Check{ID: "chk_1", Every: time.Hour}
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	s := c.Slot(t0)
	if s.Before(t0) || !s.Before(t0.Add(time.Hour)) || c.Slot(s) != s || c.Slot(s.Add(time.Nanosecond)) != s.Add(time.Hour) {
		t.Errorf("slots %v, %v, %v", s, c.Slot(s), c.Slot(s.Add(time.Nanosecond)))
	}
}

// One attempt per host at a time: checks on one host wait their turn, and a
// slot that comes while a check still waits is skipped.
func TestLoopOneAttemptPerHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, s := newNetwork(), newSink()
		n.set("shared.example.com", slow(25*time.Second))
		loop(t, n, s, false,
			site("chk_a", "shared.example.com", HTTP{Timeout: 60}),
			site("chk_b", "shared.example.com", HTTP{Timeout: 60}),
			site("chk_c", "shared.example.com", HTTP{Timeout: 60}),
			site("chk_d", "other.example.com", HTTP{}))
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		n.mu.Lock()
		defer n.mu.Unlock()
		if len(n.overlap) > 0 {
			t.Errorf("attempts on one host at once: %v", n.overlap)
		}
		total := 0
		for _, id := range []string{"chk_a", "chk_b", "chk_c"} {
			total += len(n.starts[id])
		}
		// 25 seconds each, one after the other: about 12 in 5 minutes, and
		// never more than one per check per minute.
		if total < 10 || total > 15 {
			t.Errorf("%d attempts on the shared host", total)
		}
		if len(n.starts["chk_d"]) != 5 {
			t.Errorf("the other host waited: %d attempts", len(n.starts["chk_d"]))
		}
	})
}

// At most MaxRunning attempts at once.
func TestLoopCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, s := newNetwork(), newSink()
		var checks []Check
		for i := range 3 * MaxRunning {
			host := fmt.Sprintf("site-%d.example.com", i)
			n.set(host, slow(40*time.Second))
			checks = append(checks, site(fmt.Sprintf("chk_%02d", i), host, HTTP{Timeout: 60}))
		}
		loop(t, n, s, false, checks...)
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.most != MaxRunning {
			t.Errorf("at most %d at once, want %d", n.most, MaxRunning)
		}
		// Attempts that end together are recorded together, a second's
		// worth at a time.
		if s.batches*2 > len(s.done) {
			t.Errorf("%d attempts in %d batches", len(s.done), s.batches)
		}
	})
}

// The spec's cases with the network in them (intent/http-checks/spec.md).
func TestLoopCases(t *testing.T) {
	t.Run("1: a public site goes down at night", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			loop(t, n, s, true, site("chk_shop", "shop.example.com", HTTP{}))
			time.Sleep(3 * time.Minute)
			n.set("shop.example.com", answer(503, ""))
			// One failure is no outage.
			time.Sleep(time.Minute)
			synctest.Wait()
			if st := s.state("chk_shop"); st.State != check.Up || st.FailStreak != 1 {
				t.Fatalf("after one failure: %+v", st)
			}
			time.Sleep(time.Minute)
			synctest.Wait()
			if st := s.state("chk_shop"); st.State != check.Down || st.Reason != "HTTP 503" {
				t.Fatalf("after two failures: %+v", st)
			}
			n.set("shop.example.com", answer(200, ""))
			time.Sleep(time.Minute)
			synctest.Wait()
			if st := s.state("chk_shop"); st.State != check.Up {
				t.Fatalf("back: %+v", st)
			}
		})
	})
	t.Run("2: a health endpoint says degraded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			n.set("api.example.com", answer(200, `{"status": "degraded"}`))
			loop(t, n, s, true, site("chk_api", "api.example.com", HTTP{JSON: &JSONField{"/status", "ok"}}))
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			if st := s.state("chk_api"); st.State != check.Down || st.Reason != "status is degraded instead of ok" {
				t.Fatalf("%+v", st)
			}
		})
	})
	t.Run("5: a shared host dies", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			var checks []Check
			for i := range 5 {
				host := fmt.Sprintf("site-%d.example.com", i)
				checks = append(checks, site(fmt.Sprintf("chk_%d", i), host, HTTP{}))
				if i < 2 {
					n.set(host, refuse(syscall.ECONNREFUSED))
				} else {
					n.set(host, silent)
				}
			}
			_, l := loop(t, n, s, true, checks...)
			time.Sleep(2*time.Minute + 20*time.Second)
			synctest.Wait()
			for i := range 5 {
				if st := s.state(fmt.Sprintf("chk_%d", i)); st.State != check.Down {
					t.Errorf("site %d: %+v", i, st)
				}
			}
			if _, off := l.Offline(); off {
				t.Error("offline while the canary answers")
			}
		})
	})
	t.Run("6: Cloudflare is down", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			n.set("a.example.com", answer(502, "", "Server", "cloudflare"))
			n.set("b.example.com", silent)
			loop(t, n, s, true, site("chk_a", "a.example.com", HTTP{}), site("chk_b", "b.example.com", HTTP{}))
			time.Sleep(2*time.Minute + 20*time.Second)
			synctest.Wait()
			if a, b := s.state("chk_a"), s.state("chk_b"); a.State != check.Down || a.Reason != "HTTP 502" || b.State != check.Down || b.Reason != "no answer within 10 s" {
				t.Errorf("%+v, %+v", a, b)
			}
		})
	})
	t.Run("7: Allstill's own network drops", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			var checks []Check
			for i := range 12 {
				checks = append(checks, site(fmt.Sprintf("chk_%02d", i), fmt.Sprintf("site-%d.example.com", i), HTTP{}))
			}
			_, l := loop(t, n, s, true, checks...)
			time.Sleep(2 * time.Minute)
			n.mu.Lock()
			for _, c := range checks {
				n.hosts[c.host()] = silent
			}
			n.canary = false
			n.mu.Unlock()
			at := time.Now()
			time.Sleep(3 * time.Minute)
			synctest.Wait()
			for _, c := range checks {
				if st := s.state(c.ID); st.State != check.Unknown || st.Reason != NotJudged {
					t.Errorf("%s: %+v", c.ID, st)
				}
			}
			since, off := l.Offline()
			if !off || since.Before(at) || since.After(at.Add(time.Minute+15*time.Second)) {
				t.Errorf("offline %v since %v; the network dropped at %v", off, since, at)
			}
			// Twelve attempts fail together and share the canary's answers.
			n.mu.Lock()
			if asked := n.asked; asked > 9 {
				t.Errorf("the canary was asked %d times for 36 attempts", asked)
			}
			for _, c := range checks {
				n.hosts[c.host()] = answer(200, "")
			}
			n.canary = true
			n.mu.Unlock()
			time.Sleep(time.Minute)
			synctest.Wait()
			for _, c := range checks {
				if st := s.state(c.ID); st.State != check.Up {
					t.Errorf("back: %s: %+v", c.ID, st)
				}
			}
			if _, off := l.Offline(); off {
				t.Error("still offline once its checks pass")
			}
		})
	})
	t.Run("7, back with the site down: no longer offline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			n.set("shop.example.com", silent)
			n.setCanary(false)
			_, l := loop(t, n, s, true, site("chk_shop", "shop.example.com", HTTP{}))
			time.Sleep(time.Minute + 20*time.Second)
			synctest.Wait()
			if _, off := l.Offline(); !off {
				t.Fatal("not offline while nothing answers")
			}
			// The network is back, and the site answers that it is down: the
			// answer proves the network works, even while the canary is not
			// asked, or does not answer itself.
			n.set("shop.example.com", answer(503, ""))
			time.Sleep(time.Minute)
			synctest.Wait()
			if _, off := l.Offline(); off {
				t.Error("still offline once a site answers")
			}
			// One failure so far, and the check unknown until a second.
			if st := s.state("chk_shop"); st.FailStreak != 1 || st.State != check.Unknown {
				t.Errorf("%+v", st)
			}
		})
	})
	t.Run("7 without a canary: Allstill leans towards alerting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			n.set("shop.example.com", silent)
			n.setCanary(false)
			_, l := loop(t, n, s, false, site("chk_shop", "shop.example.com", HTTP{}))
			time.Sleep(2*time.Minute + 20*time.Second)
			synctest.Wait()
			if st := s.state("chk_shop"); st.State != check.Down {
				t.Errorf("%+v", st)
			}
			if _, off := l.Offline(); off || n.asked > 0 {
				t.Error("offline without a canary")
			}
		})
	})
	t.Run("8: the server has no IPv6", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			n, s := newNetwork(), newSink()
			n.set("v6only.example.com", refuse(syscall.EHOSTUNREACH))
			loop(t, n, s, true, site("chk_v6", "v6only.example.com", HTTP{}))
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			if st := s.state("chk_v6"); st.State != check.Down || st.Reason != "no route to the address" {
				t.Errorf("%+v", st)
			}
		})
	})
}

// Checks added, changed and removed by the admin commands reach the loop
// within Reload.
func TestLoopReloads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, s := newNetwork(), newSink()
		a, b := site("chk_a", "a.example.com", HTTP{}), site("chk_b", "b.example.com", HTTP{})
		set, _ := loop(t, n, s, false, a)
		time.Sleep(2 * time.Minute)
		set(a, b)
		time.Sleep(Reload + time.Minute)
		synctest.Wait()
		if s.count("chk_b") == 0 {
			t.Fatal("an added check did not run")
		}
		// Every hour instead: no attempt until its slot in the hour.
		a.Every = time.Hour
		set(a)
		time.Sleep(Reload)
		synctest.Wait()
		ran, now := s.count("chk_a"), time.Now()
		next := a.Slot(now)
		time.Sleep(next.Sub(now) - time.Second)
		synctest.Wait()
		if s.count("chk_a") != ran {
			t.Error("ran before its new slot")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if s.count("chk_a") != ran+1 {
			t.Error("did not run in its new slot")
		}
		// Removed: it runs no more.
		set()
		time.Sleep(Reload)
		synctest.Wait()
		ranB := s.count("chk_b")
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if s.count("chk_b") != ranB {
			t.Error("a removed check still runs")
		}
	})
}

// When the loop ends, attempts still running are cut short and not
// recorded; those that finished are.
func TestLoopEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, s := newNetwork(), newSink()
		n.set("slow.example.com", silent)
		l := &Loop{
			Checks: func(context.Context) ([]Check, error) {
				return []Check{site("chk_fast", "fast.example.com", HTTP{}), site("chk_slow", "slow.example.com", HTTP{Timeout: 60})}, nil
			},
			Record:  s.record,
			Attempt: (&Runner{Transport: func(*tls.Config, time.Duration) http.RoundTripper { return n }}).Attempt,
			Log:     slog.New(slog.DiscardHandler),
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { done <- l.Run(ctx) }()
		// Both have started once their offsets passed; the slow one runs on.
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s.count("chk_fast") == 0 || s.count("chk_slow") != 0 {
			t.Errorf("recorded %d fast and %d cut short", s.count("chk_fast"), s.count("chk_slow"))
		}
	})
}

// R30 of intent/manage-checks/spec.md: one project's dead hosts wait on each other,
// and do not take every running slot from the others.
func TestAProjectHasItsShareOfTheAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var mu sync.Mutex
		running, most := map[string]int{}, map[string]int{}
		done := map[string]int{}
		attempt := func(ctx context.Context, c Check) Result {
			mu.Lock()
			running[c.ProjectID]++
			most[c.ProjectID] = max(most[c.ProjectID], running[c.ProjectID])
			mu.Unlock()
			// The dead hosts of a project time out; the others answer at once.
			if c.ProjectID == "dead" {
				select {
				case <-time.After(30 * time.Second):
				case <-ctx.Done():
				}
			}
			mu.Lock()
			running[c.ProjectID]--
			done[c.ProjectID]++
			mu.Unlock()
			return Result{Outcome: check.Passed, Started: time.Now(), Ended: time.Now()}
		}
		var checks []Check
		for i := range 30 {
			checks = append(checks, Check{ID: fmt.Sprintf("chk_dead%02d", i), ProjectID: "dead", Every: time.Minute,
				HTTP: HTTP{URL: fmt.Sprintf("https://dead%d.example.com/", i)}})
		}
		for i := range 10 {
			checks = append(checks, Check{ID: fmt.Sprintf("chk_live%02d", i), ProjectID: "live", Every: time.Minute,
				HTTP: HTTP{URL: fmt.Sprintf("https://live%d.example.com/", i)}})
		}
		// A probe's checks have no project, and no share to keep to.
		for i := range 8 {
			checks = append(checks, Check{ID: fmt.Sprintf("chk_prob%02d", i), Every: time.Minute,
				HTTP: HTTP{URL: fmt.Sprintf("https://probe%d.example.com/", i)}})
		}
		l := &Loop{
			Checks:  func(context.Context) ([]Check, error) { return checks, nil },
			Attempt: attempt,
			Record:  func(context.Context, []Done) error { return nil },
			Log:     slog.New(slog.DiscardHandler),
		}
		end := make(chan error)
		go func() { end <- l.Run(ctx) }()
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		mu.Lock()
		if most["dead"] > MaxPerProject {
			t.Errorf("a project ran %d at once", most["dead"])
		}
		if most["dead"] < MaxPerProject {
			t.Errorf("a project with many checks ran at most %d at once", most["dead"])
		}
		// 10 checks a minute for 5 minutes, about one slot each may be lost at the
		// edges.
		if done["live"] < 40 {
			t.Errorf("the other project's checks made %d attempts in 5 minutes", done["live"])
		}
		mu.Unlock()
		stop()
		<-end
	})
}

// Teams take turns when attempts wait for room: one team's dead hosts cannot
// keep another team's checks from starting (teams spec, R35).
func TestTeamsTakeTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		defer stop()
		var mu sync.Mutex
		done := map[string]int{}
		var firstLive time.Duration
		began := time.Now()
		attempt := func(ctx context.Context, c Check) Result {
			// One team's hosts hang until they time out; the other's answer at once.
			if c.TeamID == "slow" {
				select {
				case <-time.After(30 * time.Second):
				case <-ctx.Done():
				}
			}
			mu.Lock()
			if done[c.TeamID]++; c.TeamID == "quick" && firstLive == 0 {
				firstLive = time.Since(began)
			}
			mu.Unlock()
			return Result{Outcome: check.Passed, Started: time.Now(), Ended: time.Now()}
		}
		// Forty projects of the slow team, so that its share per project is no
		// limit: two hundred dead hosts a minute for twenty places, which is five
		// times what fits, so its checks always wait.
		var checks []Check
		for i := range 200 {
			checks = append(checks, Check{ID: fmt.Sprintf("chk_a%03d", i), ProjectID: fmt.Sprintf("slow%d", i%40), TeamID: "slow", Every: time.Minute,
				HTTP: HTTP{URL: fmt.Sprintf("https://slow%d.example.com/", i)}})
		}
		for i := range 3 {
			checks = append(checks, Check{ID: fmt.Sprintf("chk_z%02d", i), ProjectID: "quick", TeamID: "quick", Every: time.Minute,
				HTTP: HTTP{URL: fmt.Sprintf("https://quick%d.example.com/", i)}})
		}
		l := &Loop{
			Checks:  func(context.Context) ([]Check, error) { return checks, nil },
			Attempt: attempt,
			Record:  func(context.Context, []Done) error { return nil },
			Log:     slog.New(slog.DiscardHandler),
		}
		end := make(chan error)
		go func() { end <- l.Run(ctx) }()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		mu.Lock()
		// Each of the quick team's checks ran in nearly every one of its minutes,
		// where in the queue's order alone the slow team would have held every place.
		if done["quick"] < 3*8 {
			t.Errorf("the quick team's 3 checks ran %d times in ten minutes, beside a team of dead hosts", done["quick"])
		}
		if done["slow"] == 0 {
			t.Error("the slow team's checks never ran")
		}
		mu.Unlock()
		stop()
		<-end
	})
}

// A check reaches the private ranges its runner was given only when it may:
// one that does not say so is held to the public internet (teams spec, R13).
func TestACheckReachesPrivateRangesOnlyWhenItMay(t *testing.T) {
	r := &Runner{Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	c := Check{TCP: &TCP{Host: "10.0.0.5", Port: 5432, Timeout: 1}}
	if res := r.Attempt(t.Context(), c); !res.Local {
		t.Errorf("a check of a team the ranges are not open to reached one: %+v", res)
	}
	// Within a range it may reach it tries; nothing answers there.
	c.Private = true
	if res := r.Attempt(t.Context(), c); res.Local {
		t.Errorf("a check of a team they are open to was refused: %+v", res)
	}
	if len(r.Allow) != 1 {
		t.Error("the runner lost its ranges")
	}
}
