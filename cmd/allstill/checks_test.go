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

func TestAddAndSetTCPCheck(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	st.CreateProject(ctx, ws, "Sites")
	st.SetSetting(ctx, "allow_private", "192.0.2.0/24")
	st.Close()
	admin := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, append([]string{"admin"}, append(args, "-db", dbPath)...), &out, &bytes.Buffer{})
		return out.String(), err
	}
	add := func(name string, args ...string) (string, error) {
		return admin(append([]string{"add-check", "-project", "Sites", "-name", name, "-timeout", "1s"}, args...)...)
	}
	set := func(name string, args ...string) (string, error) {
		return admin(append([]string{"set-check", "-project", "Sites", "-check", name}, args...)...)
	}

	for what, c := range map[string]struct {
		args []string
		want string
	}{
		"both kinds":        {[]string{"-tcp", "192.0.2.1:25", "-http", "https://192.0.2.1/"}, "one of -http and -tcp"},
		"neither":           {nil, "one of -http and -tcp"},
		"no port":           {[]string{"-tcp", "192.0.2.1"}, "give a host and a port"},
		"a port in words":   {[]string{"-tcp", "192.0.2.1:smtp"}, "give a host and a port"},
		"out of range":      {[]string{"-tcp", "192.0.2.1:70000"}, "port"},
		"a flag for HTTP":   {[]string{"-tcp", "192.0.2.1:25", "-expect", "200"}, "-expect is for HTTP checks, and this is a TCP check"},
		"certificate flags": {[]string{"-tcp", "192.0.2.1:25", "-warn-days", "7"}, "warn_days"},
		"private":           {[]string{"-tcp", "10.0.0.5:5432"}, "-allow-private 10.0.0.0/8"},
		"its own":           {[]string{"-tcp", "127.0.0.1:8000"}, "own address"},
	} {
		if _, err := add("bad", c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", what, err, c.want)
		}
	}
	if _, err := add("bad", "-http", "https://192.0.2.1/", "-tls"); err == nil || !strings.Contains(err.Error(), "-tls is for TCP checks, and this is an HTTP check") {
		t.Errorf("-tls on an HTTP check: %v", err)
	}

	out, err := add("mail", "-tcp", "192.0.2.1:993", "-tls", "-warn-days", "7", "-no-verify", "-every", "5m")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Added mail to Sites", "every 5m", "Tried it now: it fails", " ms)"} {
		if !strings.Contains(out, want) {
			t.Errorf("add-check said %q, without %q", out, want)
		}
	}
	// A second check on that host and port reads the same certificate.
	out, err = add("mail-too", "-tcp", "192.0.2.1:993", "-tls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Another check already watches the certificate of 192.0.2.1:993") {
		t.Errorf("the second check: %q", out)
	}
	// A site on the same port reads it too.
	out, err = add("site", "-http", "https://192.0.2.1:993/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Another check already watches the certificate") {
		t.Errorf("the site: %q", out)
	}

	if _, err := set("mail", "-expect", "200"); err == nil || !strings.Contains(err.Error(), "for HTTP checks") {
		t.Errorf("set-check -expect: %v", err)
	}
	if _, err := set("site", "-tcp", "192.0.2.1:25"); err == nil || !strings.Contains(err.Error(), "for TCP checks") {
		t.Errorf("set-check -tcp on a site: %v", err)
	}
	if _, err := set("mail", "-tcp", "10.0.0.5:993"); err == nil || !strings.Contains(err.Error(), "-allow-private") {
		t.Errorf("set-check to a private address: %v", err)
	}
	if _, err := set("mail", "-tcp", "192.0.2.2:995", "-timeout", "5s"); err != nil {
		t.Fatal(err)
	}
	byName := func(address string) *active.TCP {
		t.Helper()
		st, err := store.Open(ctx, dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		checks, err := st.ActiveChecks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			if c.TCP != nil && c.TCP.Address() == address {
				return c.TCP
			}
		}
		t.Fatalf("no check on %s among %d", address, len(checks))
		return nil
	}
	if m := byName("192.0.2.2:995"); !m.TLS || !m.NoVerify || *m.WarnDays != 7 || m.Timeout != 5 {
		t.Errorf("after set-check: %+v", m)
	}
	// Without TLS, what it knew of a certificate goes with it, and asking
	// for it anyway is refused.
	if _, err := set("mail", "-tls=false", "-warn-days", "3"); err == nil || !strings.Contains(err.Error(), "warn_days") {
		t.Errorf("-tls=false with -warn-days: %v", err)
	}
	if _, err := set("mail", "-tls=false"); err != nil {
		t.Fatal(err)
	}
	if m := byName("192.0.2.2:995"); m.TLS || m.NoVerify || m.WarnDays != nil {
		t.Errorf("after -tls=false: %+v", m)
	}
}

func TestProbeCommands(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Workspace(ctx)
	st.CreateProject(ctx, ws, "Customer")
	st.CreateProject(ctx, ws, "Other")
	st.Close()
	admin := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(ctx, append([]string{"admin"}, append(args, "-db", dbPath)...), &out, &bytes.Buffer{})
		return out.String(), err
	}
	if _, err := admin("create-probe", "-project", "Customer"); err == nil {
		t.Error("a probe without a name")
	}
	if _, err := admin("create-probe", "inside"); err == nil {
		t.Error("a probe without a project")
	}
	if _, err := admin("create-probe", "inside", "-project", "None"); err == nil || !strings.Contains(err.Error(), `no project is named "None"`) {
		t.Errorf("no such project: %v", err)
	}
	out, err := admin("create-probe", "inside", "-project", "Customer")
	if err != nil {
		t.Fatal(err)
	}
	key := ""
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "alp_") {
			key = f
		}
	}
	if key == "" {
		t.Fatalf("no key in %q", out)
	}

	// On a probe, a private address is the point: the server does not judge it,
	// and cannot try it.
	out, err = admin("add-check", "-project", "Customer", "-name", "intranet", "-http", "http://10.0.0.5/", "-probe", "inside")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "The probe inside starts it") || strings.Contains(out, "Tried it now") {
		t.Errorf("add-check said %q", out)
	}
	if _, err := admin("add-check", "-project", "Customer", "-name", "x", "-http", "http://10.0.0.5/", "-probe", "none"); err == nil || !strings.Contains(err.Error(), `no probe is named "none"`) {
		t.Errorf("no such probe: %v", err)
	}
	if _, err := admin("add-check", "-project", "Other", "-name", "x", "-http", "http://10.0.0.5/", "-probe", "inside"); err == nil || !strings.Contains(err.Error(), "does not serve") {
		t.Errorf("a project it does not serve: %v", err)
	}
	// Back to the server, its address is the server's to refuse.
	if _, err := admin("set-check", "-project", "Customer", "-check", "intranet", "-probe", ""); err == nil || !strings.Contains(err.Error(), "-allow-private") {
		t.Errorf("a private address back on the server: %v", err)
	}
	// While on the probe its address can change to another private one.
	if _, err := admin("set-check", "-project", "Customer", "-check", "intranet", "-http", "http://10.0.0.6/"); err != nil {
		t.Errorf("another address on the probe: %v", err)
	}
	if _, err := admin("add-check", "-project", "Customer", "-name", "site", "-http", "https://192.0.2.1/", "-timeout", "1s"); err == nil {
		t.Error("a documentation address was taken without being allowed")
	}
	if out, err := admin("rekey-probe", "inside"); err != nil || !strings.Contains(out, "alp_") || strings.Contains(out, key) {
		t.Errorf("rekey: %q, %v", out, err)
	}
	if _, err := admin("rekey-probe", "none"); err == nil {
		t.Error("rekeyed a probe that does not exist")
	}

	// The probe itself refuses what it cannot run with.
	probe := func(args ...string) error {
		return run(ctx, append([]string{"probe"}, args...), &bytes.Buffer{}, &bytes.Buffer{})
	}
	t.Setenv("ALLSTILL_PROBE_KEY", "")
	t.Setenv("ALLSTILL_SERVER", "")
	if err := probe(); err == nil || !strings.Contains(err.Error(), "-server") {
		t.Errorf("no server: %v", err)
	}
	if err := probe("-server", "https://allstill.example.com"); err == nil || !strings.Contains(err.Error(), "ALLSTILL_PROBE_KEY") {
		t.Errorf("no key: %v", err)
	}
	if err := probe("-server", "ftp://allstill.example.com"); err == nil || !strings.Contains(err.Error(), "give an address") {
		t.Errorf("a bad server: %v", err)
	}
	t.Setenv("ALLSTILL_PROBE_KEY", "alp_x")
	if err := probe("-server", "https://allstill.example.com", "-allow", "nonsense"); err == nil || !strings.Contains(err.Error(), "-allow") {
		t.Errorf("a bad range: %v", err)
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
	if err := tryOnce(t.Context(), runner, "http", map[string]any{"url": srv.URL, "method": "HEAD"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "it fails, HTTP 405 (HTTP 405") || !strings.Contains(out.String(), "answers HEAD with 405") {
		t.Errorf("%q", out.String())
	}
}
