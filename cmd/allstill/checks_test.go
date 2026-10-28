// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/store"
)

func TestAddAndSetCheck(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	project, _, _ := st.CreateProject(ctx, ws, "Sites")
	admin, _ := st.AdminAccess(ctx, ws)
	if _, err := st.CreateCheck(ctx, admin, project, store.NewCheck{Name: "backup", Interval: time.Hour, Grace: time.Hour}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	admin2 := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, append([]string{"admin"}, append(args, "-db", dbPath)...), &out, &bytes.Buffer{})
		return out.String(), err
	}

	// Addresses the server may not reach are refused, with what to do.
	if _, err := admin2("add-check", "-project", "Sites", "-name", "inside", "-http", "http://10.0.0.5/health"); err == nil || !strings.Contains(err.Error(), "-allow-private 10.0.0.0/8") {
		t.Errorf("a private address: %v", err)
	}
	if _, err := admin2("add-check", "-project", "Sites", "-name", "self", "-http", "http://127.0.0.1:8000/"); err == nil || !strings.Contains(err.Error(), "own address") {
		t.Errorf("the server's own address: %v", err)
	}
	if _, err := admin2("add-check", "-project", "Sites", "-name", "bad", "-http", "https://shop.example.com", "-json", "status"); err == nil {
		t.Error("a JSON field without a value was taken")
	}

	// The server allows a documentation range, which goes nowhere.
	st, _ = store.Open(ctx, dbPath)
	st.SetSetting(ctx, "allow_private", "192.0.2.0/24")
	st.Close()
	out, err := admin2("add-check", "-project", "Sites", "-name", "www", "-http", "https://192.0.2.1/", "-expect", "301",
		"-location", "https://example.com/", "-timeout", "1s", "-tag", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Added www to Sites (chk_", "), every 1m.", "no canary", "Tried it now: it fails"} {
		if !strings.Contains(out, want) {
			t.Errorf("add-check says %q, without %q", out, want)
		}
	}
	out, err = admin2("add-check", "-project", "Sites", "-name", "apex", "-http", "https://192.0.2.1/health", "-json", "/status=ok", "-timeout", "1s", "-every", "5m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already watches the certificate of 192.0.2.1:443") || !strings.Contains(out, `-check "apex" -warn-days 0`) {
		t.Errorf("a second check on the same certificate: %q", out)
	}

	if out, err = admin2("set-check", "-project", "Sites", "-check", "apex", "-warn-days", "0", "-json", "", "-expect", "200,204", "-every", "2m"); err != nil || out != "Changed apex in Sites, every 2m.\n" {
		t.Fatalf("set-check: %q %v", out, err)
	}
	if _, err := admin2("set-check", "-project", "Sites", "-check", "backup", "-every", "2m"); err == nil || !strings.Contains(err.Error(), "pinged by a job") {
		t.Errorf("set-check on a ping check: %v", err)
	}
	if _, err := admin2("set-check", "-project", "Sites", "-check", "apex", "-http", "http://10.0.0.5/"); err == nil {
		t.Error("set-check took an address the server may not reach")
	}
	st, _ = store.Open(ctx, dbPath)
	defer st.Close()
	checks, _ := st.ActiveChecks(ctx)
	got := map[string]active.Check{}
	for _, c := range checks {
		got[c.HTTP.URL] = c
	}
	apex, www := got["https://192.0.2.1/health"], got["https://192.0.2.1/"]
	if apex.Every != 2*time.Minute || apex.HTTP.JSON != nil || *apex.HTTP.WarnDays != 0 || len(apex.HTTP.Expect) != 2 || apex.HTTP.Timeout != 1 {
		t.Errorf("apex after set-check: %+v", apex)
	}
	if www.Every != time.Minute || www.HTTP.Location != "https://example.com/" || www.HTTP.Expect[0] != 301 {
		t.Errorf("www: %+v", www)
	}
}

// A HEAD check against a site that refuses HEAD is pointed out.
func TestTryOnceWarnsOfHEAD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	runner := &active.Runner{UserAgent: "Allstill/test", Transport: func(c *tls.Config, _ time.Duration) http.RoundTripper {
		return &http.Transport{TLSClientConfig: c}
	}}
	var out bytes.Buffer
	if err := tryOnce(t.Context(), runner, map[string]any{"url": srv.URL, "method": "HEAD"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "it fails, HTTP 405 (HTTP 405") || !strings.Contains(out.String(), "answers HEAD with 405") {
		t.Errorf("%q", out.String())
	}
}
