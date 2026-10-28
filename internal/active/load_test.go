// SPDX-License-Identifier: AGPL-3.0-only

package active_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/ingest"
	"github.com/MartijnMul/allstill/internal/store"
)

// The load the plan asks of step 2: 1000 HTTP checks against a local test
// server, through the real loop, runner and store, while jobs ping. Every
// attempt must start within 5 seconds of its slot, and pings must be
// answered as fast as without the checks.
//
// To finish in half a minute, the checks run every 15 seconds instead of
// every minute: four times the load of 1000 checks a minute. It runs with
// `make load`, before a release, without the race detector, which slows the
// database down so much that it measures itself.
func TestLoad(t *testing.T) {
	if os.Getenv("ALLSTILL_LOAD_TEST") != "1" {
		t.Skip("a load test: make load")
	}
	const (
		checks = 1000
		every  = 15 * time.Second
		rounds = 2
		pings  = 300
	)
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ws, _ := st.Workspace(ctx)
	project, _, _ := st.CreateProject(ctx, ws, "Sites")
	admin, _ := st.AdminAccess(ctx, ws)

	// One site, under a thousand host names, over TLS: each attempt
	// resolves nothing, but connects and shakes hands anew.
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status": "ok"}`)
	}))
	defer site.Close()
	for i := range checks {
		if _, err := st.CreateCheck(ctx, admin, project, store.NewCheck{
			Name: fmt.Sprintf("site %d", i), Kind: "http", Interval: time.Minute,
			Settings: map[string]any{"url": fmt.Sprintf("https://site-%d.example.com/health", i), "json_field": map[string]string{"pointer": "/status", "equals": "ok"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var uuids []string
	for i := range 2 * pings {
		c, err := st.CreateCheck(ctx, admin, project, store.NewCheck{Name: fmt.Sprintf("job %d", i), Interval: time.Hour, Grace: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		uuids = append(uuids, c.PingUUID)
	}
	mux := http.NewServeMux()
	(&ingest.Handler{Store: st, Nudge: func(time.Time) {}, Log: slog.New(slog.DiscardHandler), Now: time.Now}).Register(mux)
	ping := func(uuid string) time.Duration {
		start := time.Now()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/ping/"+uuid, nil))
		if w.Code != http.StatusOK {
			t.Errorf("a ping answered %d", w.Code)
		}
		return time.Since(start)
	}
	// Pings spread over a period, as jobs send them.
	pingFor := func(uuids []string, over time.Duration) []time.Duration {
		var took []time.Duration
		gap := over / time.Duration(len(uuids))
		for _, u := range uuids {
			took = append(took, ping(u))
			time.Sleep(gap)
		}
		slices.Sort(took)
		return took
	}
	without := pingFor(uuids[:pings], 3*time.Second)

	addr := site.Listener.Addr().String()
	dialer := &net.Dialer{}
	runner := &active.Runner{UserAgent: "Allstill/load", Transport: func(conf *tls.Config, timeout time.Duration) http.RoundTripper {
		conf.InsecureSkipVerify = true // the test server's certificate is for 127.0.0.1
		return &http.Transport{
			TLSClientConfig: conf, DisableKeepAlives: true, ResponseHeaderTimeout: timeout,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
		}
	}}
	var mu sync.Mutex
	var late []time.Duration
	attempted := map[string]bool{}
	failed := 0
	loop := &active.Loop{
		Checks: func(ctx context.Context) ([]active.Check, error) {
			cs, err := st.ActiveChecks(ctx)
			for i := range cs {
				cs[i].Every = every
			}
			return cs, err
		},
		Attempt: runner.Attempt,
		Record: func(ctx context.Context, done []active.Done) error {
			attempts := make([]store.AttemptOf, len(done))
			mu.Lock()
			for i, d := range done {
				late = append(late, d.Result.Started.Sub(d.Slot))
				attempted[d.Check.ID] = true
				if d.Result.Outcome != check.Passed {
					failed++
				}
				attempts[i] = store.AttemptOf{WorkspaceID: d.Check.WorkspaceID, CheckID: d.Check.ID, Attempt: store.Attempt{
					Attempt: check.Attempt{Outcome: d.Result.Outcome, Reason: d.Result.Reason, At: d.Result.Ended},
					Started: d.Result.Started, Measured: d.Result.Measured,
				}}
			}
			mu.Unlock()
			_, err := st.RecordAttempts(ctx, attempts)
			return err
		},
		Log: slog.New(slog.DiscardHandler),
	}
	loopCtx, stop := context.WithCancel(ctx)
	ran := make(chan error)
	go func() { ran <- loop.Run(loopCtx) }()
	with := pingFor(uuids[pings:], rounds*every)
	time.Sleep(2 * time.Second) // the last attempts end and are recorded
	stop()
	if err := <-ran; err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	slices.Sort(late)
	if len(attempted) != checks || failed > 0 {
		t.Errorf("%d of %d checks attempted, %d attempts failed", len(attempted), checks, failed)
	}
	if worst := late[len(late)-1]; worst > 5*time.Second || late[0] < 0 {
		t.Errorf("attempts started from %v to %v after their slot", late[0], worst)
	}
	q := func(d []time.Duration, p float64) time.Duration { return d[int(float64(len(d)-1)*p)] }
	t.Logf("%d attempts, %d checks every %s; started within %v of their slot (median %v)", len(late), checks, every, late[len(late)-1], q(late, 0.5))
	t.Logf("pings without checks: median %v, 95th %v; with: median %v, 95th %v", q(without, 0.5), q(without, 0.95), q(with, 0.5), q(with, 0.95))
	// As fast, give or take what a busy test machine adds.
	if q(with, 0.5) > 2*q(without, 0.5)+2*time.Millisecond || q(with, 0.95) > 3*q(without, 0.95)+10*time.Millisecond {
		t.Errorf("pings are slower with the checks running")
	}
}
