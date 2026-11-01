// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/store"
)

// aProbeOf makes a probe that serves the fixture's project, with two checks on
// it, and returns its key and the checks' ids.
func aProbeOf(t *testing.T, f fixture) (key, httpID, tcpID string) {
	t.Helper()
	ctx := t.Context()
	admin, err := f.st.AdminAccess(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	probeID, key, err := f.st.CreateProbe(ctx, admin, "inside", f.project, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "intranet", Kind: "http", Interval: time.Minute, ProbeID: probeID,
		Settings: map[string]any{"url": "http://10.0.0.5/health?token=s3cr3t", "fail_after": 1}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "database", Kind: "tcp", Interval: 2 * time.Minute, ProbeID: probeID,
		Settings: map[string]any{"host": "10.0.0.6", "port": 5432}})
	if err != nil {
		t.Fatal(err)
	}
	return key, h.ID, p.ID
}

func probeRequest(f fixture, method, path, key string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://allstill.example.com"+path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Allstill-Probe-Protocol", strconv.Itoa(api.ProbeProtocolVersion))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func runsBody(t *testing.T, sent time.Time, runs ...map[string]any) []byte {
	t.Helper()
	if runs == nil {
		runs = []map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"sent_at": sent.UTC().Format(time.RFC3339), "runs": runs})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAProbeFetchesItsChecks(t *testing.T) {
	f := setup(t)
	key, httpID, tcpID := aProbeOf(t, f)
	w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, nil)
	if w.Code != 200 {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	var got api.ProbeChecks
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Protocol != api.ProbeProtocolVersion || got.ServerTime.IsZero() || len(got.Checks) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, c := range got.Checks {
		switch c.Id {
		case httpID:
			if c.Kind != "http" || c.Interval != 60 || c.Http == nil || c.Http.Url != "http://10.0.0.5/health?token=s3cr3t" {
				t.Errorf("the HTTP check: %+v", c)
			}
		case tcpID:
			if c.Kind != "tcp" || c.Interval != 120 || c.Tcp == nil || c.Tcp.Host != "10.0.0.6" || c.Tcp.Port != 5432 {
				t.Errorf("the TCP check: %+v", c)
			}
		default:
			t.Errorf("a check that is not its own: %+v", c)
		}
	}
	// Unchanged, it says so; changed, it does not.
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, map[string]string{"If-None-Match": etag}); w.Code != 304 {
		t.Errorf("unchanged: %d", w.Code)
	}
	admin, _ := f.st.AdminAccess(t.Context(), f.ws)
	if err := f.st.SetActiveCheckOn(t.Context(), admin, tcpID, 2*time.Minute, map[string]any{"host": "10.0.0.6", "port": 5432}, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, map[string]string{"If-None-Match": etag}); w.Code != 200 || strings.Contains(w.Body.String(), tcpID) {
		t.Errorf("changed: %d %s", w.Code, w.Body)
	}
}

// Only the probe's key opens these, and it opens nothing else.
func TestTheProbesEndpointsNeedItsKey(t *testing.T) {
	f := setup(t)
	key, _, _ := aProbeOf(t, f)
	for name, k := range map[string]string{"no key": "", "a wrong key": "alp_wrong"} {
		for _, c := range []struct {
			method, path string
			body         []byte
		}{{"GET", "/api/v1/probe/checks", nil}, {"POST", "/api/v1/probe/runs", runsBody(t, time.Now())}} {
			w := probeRequest(f, c.method, c.path, k, c.body, nil)
			if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("%s, %s: %d %v", name, c.path, w.Code, w.Header())
			}
			if strings.Contains(w.Body.String(), "intranet") {
				t.Errorf("%s: it told about a check", name)
			}
		}
	}
	// A dashboard page takes no probe key.
	if w := probeRequest(f, "GET", "/checks/"+f.checkID, key, nil, nil); w.Code == 200 {
		t.Error("a probe's key opened the dashboard")
	}
}

func TestAProbeOfAnotherVersionIsTold(t *testing.T) {
	f := setup(t)
	key, _, _ := aProbeOf(t, f)
	old := map[string]string{"Allstill-Probe-Protocol": strconv.Itoa(api.ProbeProtocolVersion + 1)}
	for _, c := range []struct {
		method, path string
		body         []byte
	}{{"GET", "/api/v1/probe/checks", nil}, {"POST", "/api/v1/probe/runs", runsBody(t, time.Now())}} {
		w := probeRequest(f, c.method, c.path, key, c.body, old)
		if w.Code != 426 || !strings.Contains(w.Body.String(), "same version") {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Body)
		}
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, map[string]string{"Allstill-Probe-Protocol": ""}); w.Code != 400 {
		t.Errorf("without a version: %d", w.Code)
	}
}

func TestAProbePostsRuns(t *testing.T) {
	f := setup(t)
	key, httpID, _ := aProbeOf(t, f)
	admin, _ := f.st.AdminAccess(t.Context(), f.ws)
	var nudged time.Time
	f.srv.Nudge = func(d time.Time) { nudged = d }
	state := func() store.CheckView {
		t.Helper()
		c, err := f.st.Check(t.Context(), admin, httpID)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	run := map[string]any{"id": "r1", "check_id": httpID, "outcome": "failed", "reason": "HTTP 503", "ended_ago": 3, "values": map[string]float64{"status": 503, "ms": 120}}
	post := func(body []byte) api.ProbeRunResult {
		t.Helper()
		w := probeRequest(f, "POST", "/api/v1/probe/runs", key, body, nil)
		if w.Code != 200 {
			t.Fatalf("%d: %s", w.Code, w.Body)
		}
		var res api.ProbeRunResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := post(runsBody(t, time.Now(), run))
	if res.Applied != 1 || res.Ignored != 0 || res.ClockOff != nil || res.ServerTime.IsZero() {
		t.Fatalf("%+v", res)
	}
	if nudged.IsZero() {
		t.Error("the scheduler was not told")
	}
	c := state()
	if c.Status.State != "down" || c.Status.Reason != "HTTP 503" || len(c.Recent) != 1 || c.Recent[0].Values["status"] != 503 || c.Recent[0].Duration() != 120*time.Millisecond {
		t.Errorf("%+v", c)
	}
	// Spec case 11: sent again, it does not count again.
	if res := post(runsBody(t, time.Now(), run)); res.Applied != 0 || res.Ignored != 1 {
		t.Errorf("sent again: %+v", res)
	}
	if res := post(runsBody(t, time.Now().Add(-10*time.Minute))); res.ClockOff == nil || *res.ClockOff > -590 {
		t.Errorf("a wrong clock: %+v", res)
	}

	many := make([]map[string]any, 501)
	for i := range many {
		many[i] = map[string]any{"id": strconv.Itoa(i), "check_id": httpID, "outcome": "passed", "ended_ago": 0}
	}
	if w := probeRequest(f, "POST", "/api/v1/probe/runs", key, runsBody(t, time.Now(), many...), nil); w.Code != 413 {
		t.Errorf("too many runs: %d", w.Code)
	}
	if w := probeRequest(f, "POST", "/api/v1/probe/runs", key, []byte(`{"sent_at":"`+strings.Repeat("x", 2<<20)), nil); w.Code != 413 {
		t.Errorf("too large: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string][]byte{
		"not JSON":   []byte("{"),
		"no sent_at": []byte(`{"runs":[]}`),
	} {
		if w := probeRequest(f, "POST", "/api/v1/probe/runs", key, body, nil); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// A run that is not valid is ignored, not the post: it also holds the
	// probe's sign of life and the runs of other checks.
	good := map[string]any{"id": "good", "check_id": httpID, "outcome": "passed", "ended_ago": 0}
	res = post(runsBody(t, time.Now(), good,
		map[string]any{"id": "x", "check_id": httpID, "outcome": "maybe", "ended_ago": 0},
		map[string]any{"id": "", "check_id": httpID, "outcome": "passed", "ended_ago": 0},
		map[string]any{"id": strings.Repeat("x", 65), "check_id": httpID, "outcome": "passed", "ended_ago": 0},
		map[string]any{"id": "y", "check_id": httpID, "outcome": "failed", "reason": strings.Repeat("x", 501), "ended_ago": 0},
		map[string]any{"id": "z", "check_id": httpID, "outcome": "passed", "ended_ago": -1},
	))
	if res.Applied != 1 || res.Ignored != 5 {
		t.Errorf("runs that are not valid: %+v", res)
	}
	// A reason is measured in characters, as the API says.
	if res := post(runsBody(t, time.Now(), map[string]any{"id": "long", "check_id": httpID, "outcome": "passed", "reason": strings.Repeat("é", 500), "ended_ago": 0})); res.Applied != 1 {
		t.Errorf("500 characters: %+v", res)
	}
}

// A check on a probe says so on its page.
func TestACheckOnAProbeSaysSo(t *testing.T) {
	f := setup(t)
	_, httpID, _ := aProbeOf(t, f)
	b := f.browser(t)
	f.register(t, b, f.ada)
	page := string(b.ok("GET", "/checks/"+httpID, nil))
	for _, want := range []string{"the probe inside", "from the probe inside", "the probe's network"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}
