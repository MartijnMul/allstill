// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

const uuid = "5c2b1d2e-8f3a-4b7c-9d1e-2f3a4b5c6d7e"
const key = "abcdefghijklmnopqrstuv"

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

type fakeStore struct {
	got []store.PingIn
	out store.PingOut
	err error
}

func (f *fakeStore) Ping(_ context.Context, in store.PingIn) (store.PingOut, error) {
	f.got = append(f.got, in)
	return f.out, f.err
}

func serve(t *testing.T, f *fakeStore, method, target string, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{Store: f, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return t0 }}
	mux := http.NewServeMux()
	h.Register(mux)
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestPaths(t *testing.T) {
	cases := []struct {
		path string
		kind check.Kind
		exit int
		slug string
	}{
		{"/ping/" + uuid, check.Success, -1, ""},
		{"/ping/" + strings.ToUpper(uuid), check.Success, -1, ""},
		{"/ping/" + uuid + "/start", check.Start, -1, ""},
		{"/ping/" + uuid + "/fail", check.Fail, -1, ""},
		{"/ping/" + uuid + "/log", check.Log, -1, ""},
		{"/ping/" + uuid + "/0", check.Success, 0, ""},
		{"/ping/" + uuid + "/3", check.Fail, 3, ""},
		{"/ping/" + uuid + "/255", check.Fail, 255, ""},
		{"/ping/" + uuid + "/007", check.Fail, 7, ""},
		{"/ping/" + uuid + "/", check.Success, -1, ""},
		{"/ping/" + uuid + "/fail/", check.Fail, -1, ""},
		{"/ping/" + key + "/db-dump/", check.Success, -1, "db-dump"},
		{"/ping/" + key + "/db-dump", check.Success, -1, "db-dump"},
		{"/ping/" + key + "/db-dump/start", check.Start, -1, "db-dump"},
		{"/ping/" + key + "/db-dump/fail", check.Fail, -1, "db-dump"},
		{"/ping/" + key + "/db-dump/log", check.Log, -1, "db-dump"},
		{"/ping/" + key + "/db-dump/1", check.Fail, 1, "db-dump"},
	}
	for _, tc := range cases {
		f := &fakeStore{}
		w := serve(t, f, "GET", tc.path, "", nil)
		if w.Code != 200 || w.Body.String() != "OK" || len(f.got) != 1 {
			t.Fatalf("%s: %d %q", tc.path, w.Code, w.Body)
		}
		in := f.got[0]
		if in.Kind != tc.kind || in.ExitStatus != tc.exit || in.Slug != tc.slug || (tc.slug == "") != (in.CheckUUID == uuid) {
			t.Errorf("%s: %+v", tc.path, in)
		}
	}
}

func TestBadPaths(t *testing.T) {
	for _, p := range []string{
		"/ping/not-a-uuid",
		"/ping/" + uuid + "/256",
		"/ping/" + uuid + "/-1",
		"/ping/" + uuid + "/+1",
		"/ping/" + key + "/start?create=1",
		"/ping/" + key + "/a/b/c",
		"/ping/" + uuid + "/restart",
		"/ping/" + uuid + "/start/x",
		"/ping/" + key,
		"/ping/" + key + "/Upper",
		"/ping/" + key + "/db/2x",
		"/ping/" + uuid + "?rid=12",
		"/ping/" + uuid + "?v=size",
		"/ping/" + uuid + "?v=Size:1",
		"/ping/" + uuid + "?v=size:1e9",
		"/ping/" + uuid + "?v=size:1234567890123456",
	} {
		f := &fakeStore{}
		if w := serve(t, f, "GET", p, "", nil); w.Code != 400 || len(f.got) != 0 {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
}

func TestAnswers(t *testing.T) {
	f := &fakeStore{err: store.ErrNotFound}
	if w := serve(t, f, "GET", "/ping/"+uuid, "", nil); w.Code != 404 || w.Body.String() != "not found" {
		t.Fatalf("not found: %d %q", w.Code, w.Body)
	}
	// A check Allstill runs itself is not pinged: `curl -f` notices.
	if w := serve(t, &fakeStore{err: check.ErrActive}, "GET", "/ping/"+uuid, "", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "run by Allstill") {
		t.Fatalf("a ping to an active check: %d %q", w.Code, w.Body)
	}
	f = &fakeStore{out: store.PingOut{Created: true}}
	w := serve(t, f, "GET", "/ping/"+key+"/new-job?create=1", "", nil)
	if w.Code != 201 || w.Body.String() != "Created" || !f.got[0].Create {
		t.Fatalf("created: %d %q %+v", w.Code, w.Body, f.got)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Ping-Body-Limit") != "100000" {
		t.Fatalf("headers %v", w.Header())
	}
	if w := serve(t, &fakeStore{}, "PUT", "/ping/"+uuid, "", nil); w.Code != 200 {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := serve(t, &fakeStore{}, "OPTIONS", "/ping/"+uuid, "", nil); w.Code != 204 {
		t.Fatalf("OPTIONS: %d", w.Code)
	}
	if w := serve(t, &fakeStore{}, "HEAD", "/ping/"+uuid, "", nil); w.Code != 200 {
		t.Fatalf("HEAD: %d", w.Code)
	}
}

// A script stuck in a loop: after the burst, about one ping a second counts.
// By UUID the answer stays 200, so `curl -f` in the script does not fail.
func TestAPingLoopIsLimited(t *testing.T) {
	var logged strings.Builder
	now := t0
	f := &fakeStore{}
	h := &Handler{Store: f, Log: slog.New(slog.NewTextHandler(&logged, nil)), Now: func() time.Time { return now }}
	mux := http.NewServeMux()
	h.Register(mux)
	ping := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	for range Burst {
		if w := ping("/ping/" + uuid); w.Body.String() != "OK" {
			t.Fatalf("within the burst: %d %q", w.Code, w.Body)
		}
	}
	if w := ping("/ping/" + uuid); w.Code != 200 || w.Body.String() != "OK (rate limited)" || len(f.got) != Burst {
		t.Fatalf("past the burst: %d %q, %d stored", w.Code, w.Body, len(f.got))
	}
	if strings.Count(logged.String(), "dropped") != 1 || strings.Contains(logged.String(), uuid) {
		t.Fatalf("log %q", logged.String())
	}
	ping("/ping/" + uuid)
	if strings.Count(logged.String(), "dropped") != 1 {
		t.Fatal("logged twice within the hour")
	}
	// Other checks are not held up.
	for range Burst {
		if w := ping("/ping/" + key + "/db-dump"); w.Code != 200 {
			t.Fatalf("another check: %d", w.Code)
		}
	}
	if w := ping("/ping/" + key + "/db-dump"); w.Code != 429 || w.Header().Get("Retry-After") != "1" || w.Body.String() != "rate limit exceeded" {
		t.Fatalf("a slug past the burst: %d %q", w.Code, w.Body)
	}
	now = now.Add(2 * time.Second)
	for i := range 2 {
		if w := ping("/ping/" + uuid); w.Body.String() != "OK" {
			t.Fatalf("ping %d two seconds later: %q", i, w.Body)
		}
	}
	if w := ping("/ping/" + uuid); w.Body.String() != "OK (rate limited)" {
		t.Fatalf("a third ping in those two seconds: %q", w.Body)
	}
}

func TestTheLimiterForgetsQuietChecks(t *testing.T) {
	var l limiter
	l.allow("quiet", t0)
	for range Burst + 1 {
		l.allow("loud", t0) // emptied, and warned about
	}
	l.allow("busy", t0)
	for range Burst - 1 {
		l.allow("busy", t0.Add(50*time.Second))
	}
	// A minute on: quiet is full again and forgotten; busy is still
	// refilling; loud is full but was warned about, so it is not warned again
	// within the hour.
	l.allow("other", t0.Add(61*time.Second))
	if l.buckets["quiet"] != nil || l.buckets["busy"] == nil || l.buckets["loud"] == nil {
		t.Fatalf("buckets %v", l.buckets)
	}
}

func TestBodyRidAndValues(t *testing.T) {
	f := &fakeStore{}
	big := strings.Repeat("x", BodyLimit+10)
	serve(t, f, "POST", "/ping/"+uuid+"?rid="+strings.ToUpper(uuid)+"&v=size:18400000000&v=ratio:-0.25", big,
		map[string]string{"Allstill-Values": "size=1, rows=312", "User-Agent": "allstill-run"})
	in := f.got[0]
	if len(in.Body) != BodyLimit || !in.BodyTruncated || in.RID != uuid || in.UserAgent != "allstill-run" || !in.At.Equal(t0) {
		t.Fatalf("%d bytes, truncated %v, rid %q, %+v", len(in.Body), in.BodyTruncated, in.RID, in)
	}
	want := map[string]float64{"size": 18400000000, "ratio": -0.25, "rows": 312}
	for k, v := range want {
		if in.Values[k] != v {
			t.Fatalf("values %v, want %v", in.Values, want)
		}
	}
	if len(in.Values) != 3 {
		t.Fatalf("values %v", in.Values)
	}
}

func TestStoreFailureHidesThePath(t *testing.T) {
	var logged strings.Builder
	h := &Handler{Store: &fakeStore{err: io.ErrUnexpectedEOF}, Log: slog.New(slog.NewTextHandler(&logged, nil)), Now: time.Now}
	mux := http.NewServeMux()
	h.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/ping/"+uuid, nil))
	if w.Code != 500 || strings.Contains(logged.String(), uuid) {
		t.Fatalf("%d, log %q", w.Code, logged.String())
	}
}

func TestNudge(t *testing.T) {
	var got time.Time
	h := &Handler{Store: &fakeStore{out: store.PingOut{Deadline: t0}}, Log: slog.New(slog.DiscardHandler),
		Now: time.Now, Nudge: func(d time.Time) { got = d }}
	mux := http.NewServeMux()
	h.Register(mux)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ping/"+uuid, nil))
	if !got.Equal(t0) {
		t.Fatalf("nudged with %v", got)
	}
}

func FuzzValues(f *testing.F) {
	f.Add("size:1", "rows=2, a=-1.5")
	f.Fuzz(func(t *testing.T, q, header string) {
		vals, err := values([]string{q}, header)
		if err == nil && len(vals) > 64 {
			t.Fatalf("%d values", len(vals))
		}
	})
}
