// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
)

// An invented project as Healthchecks' API v3 lists it.
const listing = `{"checks": [
  {"name": "Nightly backup", "slug": "nightly-backup", "tags": "backup prod", "desc": "Files to the NAS",
   "grace": 3600, "schedule": "0 3 * * *", "tz": "Europe/Amsterdam",
   "uuid": "31365bce-8da9-4729-8ff3-aaa71d56b712", "manual_resume": true, "methods": "POST"},
  {"name": "Heartbeat web-1", "slug": "heartbeat", "tags": "", "grace": 300, "timeout": 600,
   "uuid": "41365bce-8da9-4729-8ff3-aaa71d56b712"},
  {"name": "Heartbeat web-2", "slug": "heartbeat", "tags": "", "grace": 300, "timeout": 600,
   "uuid": "51365bce-8da9-4729-8ff3-aaa71d56b712"},
  {"name": "Weekly report", "slug": "", "grace": 3600, "schedule": "Mon *-*-* 06:00", "tz": "UTC",
   "uuid": "61365bce-8da9-4729-8ff3-aaa71d56b712"}
]}`

func TestImport(t *testing.T) {
	var gotKey string
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/checks/" || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		gotKey = r.Header.Get("X-Api-Key")
		w.Write([]byte(listing))
	}))
	defer hc.Close()

	checks, err := Fetch(t.Context(), http.DefaultClient, hc.URL+"/", "rw-key")
	if err != nil || len(checks) != 4 || gotKey != "rw-key" {
		t.Fatalf("fetch: %d checks, key %q, %v", len(checks), gotKey, err)
	}

	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ws, _ := st.Workspace(ctx)
	project, _, _ := st.CreateProject(ctx, ws, "Imported")
	admin, _ := st.AdminAccess(ctx, ws)

	results, err := Import(ctx, st, admin, project, checks)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		imported bool
		note     string
	}{
		{true, "kept settings Allstill does not use yet: manual_resume, methods"},
		{true, ""},
		{true, `its slug "heartbeat" is used by an earlier check, so it was left out`},
		{false, `its schedule "Mon *-*-* 06:00" cannot be read`},
	}
	for i, w := range want {
		r := results[i]
		if (r.CheckID != "") != w.imported || !strings.HasPrefix(r.Note, w.note) || (w.note == "" && r.Note != "") {
			t.Errorf("%s: %+v, want %+v", r.Name, r, w)
		}
	}

	// A ping to the check's old UUID now reaches Allstill.
	if _, err := st.Ping(ctx, store.PingIn{CheckUUID: "31365bce-8da9-4729-8ff3-aaa71d56b712", Kind: "success", ExitStatus: -1, Method: "GET"}); err != nil {
		t.Fatalf("a ping to the imported UUID: %v", err)
	}

	// Again: nothing is imported twice.
	results, _ = Import(ctx, st, admin, project, checks)
	for _, r := range results[:3] {
		if r.CheckID != "" || r.Note != "already in Allstill" {
			t.Errorf("second import of %s: %+v", r.Name, r)
		}
	}
}

func TestWithTags(t *testing.T) {
	var all struct{ Checks []HCCheck }
	if err := json.Unmarshal([]byte(listing), &all); err != nil {
		t.Fatal(err)
	}
	got := WithTags(all.Checks, "prod", "elsewhere")
	if len(got) != 1 || got[0].Name != "Nightly backup" || len(all.Checks) != 4 {
		t.Fatalf("with tags: %+v", got)
	}
	if got := WithTags(all.Checks, "none"); len(got) != 0 {
		t.Fatalf("an unknown tag: %+v", got)
	}
}

func TestReadOnlyKeysAreRefused(t *testing.T) {
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"checks": [{"name": "x", "unique_key": "abc"}]}`))
	}))
	defer hc.Close()
	if _, err := Fetch(t.Context(), http.DefaultClient, hc.URL, "ro-key"); err == nil || !strings.Contains(err.Error(), "read-write") {
		t.Fatalf("%v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong key", http.StatusUnauthorized)
	}))
	defer bad.Close()
	if _, err := Fetch(t.Context(), http.DefaultClient, bad.URL, "nope"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("%v", err)
	}
}

func TestTheKeyStaysWithHealthchecks(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the key reached another host: %q", r.Header.Get("X-Api-Key"))
	}))
	defer elsewhere.Close()
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/v3/checks/", http.StatusFound)
	}))
	defer hc.Close()
	// httptest listens on 127.0.0.1 under another port: another host.
	if _, err := Fetch(t.Context(), http.DefaultClient, hc.URL, "rw-key"); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("%v", err)
	}
	if _, err := Fetch(t.Context(), http.DefaultClient, "http://healthchecks.example.com", "rw-key"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http: %v", err)
	}
}

func TestTagsAndPausedChecks(t *testing.T) {
	long := strings.Repeat("x", 60)
	n, _ := convert(HCCheck{Name: "x", UUID: "31365bce-8da9-4729-8ff3-aaa71d56b712", Grace: 60, Timeout: 60, Status: "paused",
		Tags: strings.Repeat("t ", 25) + long})
	if len(n.Tags) != 20 || !n.Paused {
		t.Fatalf("%d tags, paused %v", len(n.Tags), n.Paused)
	}
	n, _ = convert(HCCheck{Name: "x", UUID: "31365bce-8da9-4729-8ff3-aaa71d56b712", Grace: 60, Timeout: 60, Tags: long})
	if len(n.Tags[0]) != 50 {
		t.Fatalf("%q", n.Tags[0])
	}
}

// Invented UUIDs: one Healthchecks knows, one it does not.
const (
	known   = "31365bce-8da9-4729-8ff3-aaa71d56b712"
	unknown = "61365bce-8da9-4729-8ff3-aaa71d56b712"
)

func TestFetchFlips(t *testing.T) {
	var gotQuery string
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/checks/"+known+"/flips/" || r.Header.Get("X-Api-Key") != "rw-key" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		// Newest first, as Healthchecks lists them.
		w.Write([]byte(`{"flips": [{"timestamp": "2026-10-02T03:40:00+00:00", "up": 1}, {"timestamp": "2026-10-02T03:12:00+02:00", "up": 0}]}`))
	}))
	defer hc.Close()
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	flips, err := FetchFlips(t.Context(), http.DefaultClient, hc.URL, "rw-key", known, since)
	if err != nil {
		t.Fatal(err)
	}
	want := []Flip{{At: time.Date(2026, 10, 2, 1, 12, 0, 0, time.UTC)}, {At: time.Date(2026, 10, 2, 3, 40, 0, 0, time.UTC), Up: true}}
	if gotQuery != "start=1790812800" || !slices.Equal(flips, want) {
		t.Fatalf("query %q, flips %+v", gotQuery, flips)
	}
	if _, err := FetchFlips(t.Context(), http.DefaultClient, hc.URL, "rw-key", unknown, since); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("an unknown check: %v", err)
	}
}

func TestReadFlips(t *testing.T) {
	change := `{"timestamp": "2026-10-02T03:40:00+00:00", "up": 1}`
	for _, c := range []struct {
		answer string
		n      int // changes read, or -1 for refused
	}{
		{`{"flips": [` + change + `]}`, 1},
		{`[` + change + `]`, 1}, // as the documentation shows it
		{`{"flips": []}`, 0},
		{`[]`, 0},
		// Never taken for no changes:
		{`{}`, -1},
		{`{"detail": "something else"}`, -1},
		{`{"flips": null}`, -1},
		{`null`, -1},
		{`"text"`, -1},
		{`{"flips": [{}]}`, -1},
		{`{"flips": [{"timestamp": "2026-10-02T03:40:00+00:00"}]}`, -1},
		{`{"flips": [{"timestamp": "2026-10-02T03:40:00+00:00", "up": true}]}`, -1},
		{`{"flips": [{"timestamp": "2026-10-02T03:40:00+00:00", "up": 2}]}`, -1},
	} {
		flips, err := readFlips(json.RawMessage(c.answer))
		if (c.n < 0) != (err != nil) || (c.n >= 0 && len(flips) != c.n) {
			t.Errorf("%s: %d changes, %v", c.answer, len(flips), err)
		}
	}
}
