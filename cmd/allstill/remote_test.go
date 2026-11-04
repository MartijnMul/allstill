// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/client"
	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/web"
)

// fakeServer answers as an installation does, with a sign-in approved at once. What
// is behind it is tested in internal/web, against the real thing.
type fakeServer struct {
	*httptest.Server
	mu    sync.Mutex
	asked map[string]string
	ended []string
	valid map[string]bool
	next  int
	hold  chan struct{} // a token waits for this, if set
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{valid: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.asked = map[string]string{"name": r.PostForm.Get("device_name"), "scope": r.PostForm.Get("scope"), "client": r.PostForm.Get("client_id")}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc", "user_code": "WDJB-MJHT", "verification_uri": f.URL + "/device\x1b[31m", "expires_in": 60, "interval": 1,
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if f.hold != nil {
			<-f.hold
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.next++
		token := "alt_token" + string(rune('0'+f.next))
		f.valid[token] = true
		json.NewEncoder(w).Encode(map[string]string{"access_token": token, "token_type": "Bearer", "scope": f.asked["scope"]})
	})
	authorized := func(r *http.Request) (string, bool) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		return token, ok && f.valid[token]
	}
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorized(r); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		scope := f.asked["scope"]
		f.mu.Unlock()
		scopes := []string{"read"}
		if scope == "manage" {
			scopes = append(scopes, "manage")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"kind": "person", "scopes": scopes, "server_version": version(),
			"person":   map[string]any{"id": "per_1", "name": "Ada\x1b[2J", "email": "ada@example.com", "workspace_admin": scope == "manage"},
			"sign_in":  map[string]any{"id": "sgn_1", "name": "laptop-1"},
			"projects": []map[string]any{{"id": "prj_1", "name": "Backups", "role": "owner"}},
		})
	})
	mux.HandleFunc("DELETE /api/v1/me/token", func(w http.ResponseWriter, r *http.Request) {
		token, ok := authorized(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		delete(f.valid, token)
		f.ended = append(f.ended, token)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// aHome gives a test its own configuration folder.
func aHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", home)
	t.Setenv("ALLSTILL_SERVER", "")
	t.Setenv("ALLSTILL_TOKEN", "")
	path, err := client.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func command(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	err := run(ctx, args, &out, &errOut)
	return out.String(), err
}

func TestSigningInLookingAndSigningOut(t *testing.T) {
	path := aHome(t)
	srv := newFakeServer(t)

	out, err := command(t, "login", srv.URL, "-name", "laptop-1")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{"WDJB-MJHT", "/device", "started this yourself", "Signed in as Ada[2J on " + srv.URL} {
		if !strings.Contains(out, want) {
			t.Errorf("login lacks %q: %q", want, out)
		}
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("login printed a control character: %q", out)
	}
	if srv.asked["name"] != "laptop-1" || srv.asked["scope"] != store.ScopeManage || srv.asked["client"] != "allstill" {
		t.Errorf("asked with %v", srv.asked)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("the file: %v, %v", info, err)
		}
	}
	saved, _ := client.LoadCredentials(path)
	if saved.Current != srv.URL || saved.Servers[srv.URL].Token != "alt_token1" {
		t.Fatalf("saved %+v", saved)
	}

	out, err = command(t, "whoami")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, want := range []string{"Ada[2J (ada@example.com) on " + srv.URL, "signed in as laptop-1", "administrator", "Backups: owner"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami lacks %q: %q", want, out)
		}
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("whoami printed a control character: %q", out)
	}

	// Signing in again ends the one it replaces, as nothing could later.
	if _, err := command(t, "login", srv.URL, "-name", "laptop-1"); err != nil {
		t.Fatal(err)
	}
	if len(srv.ended) != 1 || srv.ended[0] != "alt_token1" {
		t.Errorf("ended %v", srv.ended)
	}

	out, err = command(t, "logout")
	if err != nil || !strings.Contains(out, "Signed out of "+srv.URL) {
		t.Fatalf("%v: %s", err, out)
	}
	if len(srv.ended) != 2 {
		t.Errorf("ended %v", srv.ended)
	}
	saved, _ = client.LoadCredentials(path)
	if saved.Current != "" || len(saved.Servers) != 0 {
		t.Errorf("still saved: %+v", saved)
	}
	if _, err := command(t, "whoami"); err == nil || !strings.Contains(err.Error(), "allstill login") {
		t.Errorf("whoami after signing out: %v", err)
	}
	if _, err := command(t, "logout"); err == nil {
		t.Error("signing out when not signed in said it worked")
	}
}

func TestAReadOnlySignInFromTheCommandLine(t *testing.T) {
	aHome(t)
	srv := newFakeServer(t)
	// Flags go before the address too.
	out, err := command(t, "login", "-read-only", srv.URL)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	host, _ := os.Hostname()
	if srv.asked["scope"] != store.ScopeRead || srv.asked["name"] != host {
		t.Errorf("asked with %v", srv.asked)
	}
	if !strings.Contains(out, "to read only") {
		t.Errorf("login: %q", out)
	}
	out, err = command(t, "whoami")
	if err != nil || !strings.Contains(out, "can only read") || strings.Contains(out, "administrator") {
		t.Errorf("whoami: %v: %q", err, out)
	}
}

func TestSeveralInstallations(t *testing.T) {
	path := aHome(t)
	one, two := newFakeServer(t), newFakeServer(t)
	if _, err := command(t, "login", one.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t, "login", two.URL); err != nil {
		t.Fatal(err)
	}
	// The last is the default, and either way picks the other.
	if out, _ := command(t, "whoami"); !strings.Contains(out, two.URL) {
		t.Errorf("default: %q", out)
	}
	if out, _ := command(t, "whoami", "-server", one.URL); !strings.Contains(out, one.URL) {
		t.Errorf("-server: %q", out)
	}
	t.Setenv("ALLSTILL_SERVER", one.URL)
	if out, _ := command(t, "whoami"); !strings.Contains(out, one.URL) {
		t.Errorf("ALLSTILL_SERVER: %q", out)
	}
	t.Setenv("ALLSTILL_SERVER", "")
	if _, err := command(t, "logout", "-server", one.URL); err != nil {
		t.Fatal(err)
	}
	saved, _ := client.LoadCredentials(path)
	if len(saved.Servers) != 1 || saved.Current != two.URL {
		t.Errorf("after signing out of one: %+v", saved)
	}
	if _, err := command(t, "whoami", "-server", one.URL); err == nil || !strings.Contains(err.Error(), "not signed in on") {
		t.Errorf("one signed out of: %v", err)
	}
	// Signing out of the one in use moves on to one that is left, and says so.
	if _, err := command(t, "login", one.URL); err != nil {
		t.Fatal(err)
	}
	out, err := command(t, "logout")
	if err != nil || !strings.Contains(out, "From now on, commands use "+two.URL) {
		t.Errorf("logout: %v: %q", err, out)
	}
	if out, _ := command(t, "whoami"); !strings.Contains(out, two.URL) {
		t.Errorf("after signing out of the one in use: %q", out)
	}
}

// Plain http stops before anything is asked (R9).
func TestLoginRefusesPlainHTTP(t *testing.T) {
	path := aHome(t)
	_, err := command(t, "login", "http://allstill.lan")
	if err == nil || !strings.Contains(err.Error(), "unprotected") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("it wrote a file")
	}
	if _, err := command(t, "whoami", "-server", "http://allstill.lan"); err == nil || !strings.Contains(err.Error(), "unprotected") {
		t.Errorf("whoami: %v", err)
	}
}

// A sign-in that went elsewhere says so, and stays saved until someone acts.
func TestAnEndedSignIn(t *testing.T) {
	path := aHome(t)
	srv := newFakeServer(t)
	if _, err := command(t, "login", srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	clear(srv.valid)
	srv.mu.Unlock()
	if _, err := command(t, "whoami"); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Errorf("whoami: %v", err)
	}
	// Signing out of one that ended already does what it says.
	if _, err := command(t, "logout"); err != nil {
		t.Errorf("logout: %v", err)
	}
	if saved, _ := client.LoadCredentials(path); len(saved.Servers) != 0 {
		t.Errorf("still saved: %+v", saved)
	}
}

// One that cannot be reached is not forgotten, or nothing here could end it.
func TestLogoutWithTheServerAway(t *testing.T) {
	path := aHome(t)
	srv := newFakeServer(t)
	if _, err := command(t, "login", srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err := command(t, "logout")
	if err == nil || !strings.Contains(err.Error(), "still signed in") {
		t.Fatalf("got %v", err)
	}
	if saved, _ := client.LoadCredentials(path); len(saved.Servers) != 1 {
		t.Errorf("forgotten: %+v", saved)
	}
}

func TestALooserCredentialsFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no such modes")
	}
	path := aHome(t)
	srv := newFakeServer(t)
	if _, err := command(t, "login", srv.URL); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o644)
	for _, args := range [][]string{{"whoami"}, {"logout"}, {"login", srv.URL}} {
		if _, err := command(t, args...); err == nil || !strings.Contains(err.Error(), "others can read") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// Commands that take no sign-in run with none, and read no file (R35).
func TestRunNeedsNoSignIn(t *testing.T) {
	path := aHome(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a ping with a credential: %v", r.Header)
		}
		io.WriteString(w, "OK")
	}))
	defer srv.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Refused if it were read.
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := "true"
	if runtime.GOOS == "windows" {
		t.Skip("no true here")
	}
	if _, err := command(t, "run", "--url", srv.URL+"/ping/x", "--", exe); err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Error("nothing was pinged")
	}
}

func TestEndSessionsAndRemovePasskey(t *testing.T) {
	dbPath := serverDatabase(t)
	ctx := t.Context()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ws, _ := st.Workspace(ctx)
	ada, _ := st.CreatePerson(ctx, store.Command("test"), ws, store.NewPerson{Name: "Ada", Email: "ada@example.com", WorkspaceAdmin: true})
	now := time.Now()
	link, _ := st.CreatePasskeyLink(ctx, store.Command("test"), ws, ada, now)
	if _, err := st.AddPasskey(ctx, link, store.NewPasskey{CredentialID: []byte("k1"), RPID: "allstill.example.com", Name: "MacBook\x1b[2K\npky_fake  Phone", Record: []byte(`{"id":"x"}`)}, "Firefox", now); err != nil {
		t.Fatal(err)
	}
	keys, _ := st.Passkeys(ctx, ws, ada)
	token, err := st.CreatePersonToken(ctx, store.NewPersonToken{WorkspaceID: ws, PersonID: ada, PasskeyID: keys[0].ID, Record: keys[0].Record, Name: "laptop-1", Scope: store.ScopeManage}, now)
	if err != nil {
		t.Fatal(err)
	}

	out, err := command(t, "admin", "end-sessions", "-db", dbPath, "-email", "ada@example.com")
	if err != nil || !strings.Contains(out, "Ended 1 sign-ins and 1 browser sessions of Ada") {
		t.Fatalf("%v: %q", err, out)
	}
	if _, _, err := st.AccessOfPersonToken(ctx, token, now); err == nil {
		t.Error("the sign-in still works")
	}
	if _, err := command(t, "admin", "end-sessions", "-db", dbPath, "-email", "nobody@example.com"); err == nil || !strings.Contains(err.Error(), "no one with") {
		t.Errorf("someone unknown: %v", err)
	}

	// Without -passkey it lists theirs, and does nothing.
	out, err = command(t, "admin", "remove-passkey", "-db", dbPath, "-person", ada)
	if err != nil || !strings.Contains(out, keys[0].ID) || !strings.Contains(out, "MacBook[2Kpky_fake  Phone") || !strings.Contains(out, "Give -passkey") {
		t.Fatalf("listing: %v: %q", err, out)
	}
	if strings.ContainsRune(out, '\x1b') || strings.Count(out, "\n") != 2 {
		t.Errorf("a passkey's name steers the list: %q", out)
	}
	if left, _ := st.Passkeys(ctx, ws, ada); len(left) != 1 {
		t.Fatalf("listing removed it: %+v", left)
	}
	out, err = command(t, "admin", "remove-passkey", "-db", dbPath, "-person", ada, "-passkey", keys[0].ID)
	if err != nil || !strings.Contains(out, "Removed that passkey of Ada") {
		t.Fatalf("%v: %q", err, out)
	}
	if left, _ := st.Passkeys(ctx, ws, ada); len(left) != 0 {
		t.Fatalf("still there: %+v", left)
	}
	if _, err := command(t, "admin", "remove-passkey", "-db", dbPath, "-person", ada, "-passkey", keys[0].ID); err == nil || !strings.Contains(err.Error(), "has no passkey") {
		t.Errorf("twice: %v", err)
	}
}

// Two logins waiting at once, or a logout meanwhile, do not lose each other's sign-in.
func TestALoginKeepsWhatChangedWhileItWaited(t *testing.T) {
	path := aHome(t)
	one, two := newFakeServer(t), newFakeServer(t)
	one.hold = make(chan struct{})
	done := make(chan error)
	go func() {
		_, err := command(t, "login", one.URL)
		done <- err
	}()
	// The other starts and finishes while the first is still waiting for someone.
	if _, err := command(t, "login", two.URL); err != nil {
		t.Fatal(err)
	}
	close(one.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	saved, err := client.LoadCredentials(path)
	if err != nil || len(saved.Servers) != 2 {
		t.Errorf("after two logins at once: %+v, %v", saved, err)
	}
}

// A real server, and a sign-in as it would be kept, for reading from a command line.
func aRealServer(t *testing.T) (st *store.Store, ws, url string, signIn func(person, scope string)) {
	t.Helper()
	path := aHome(t)
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "allstill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ws, _ = st.Workspace(ctx)
	base, _ := web.ParseBaseURL("https://allstill.example.com")
	dashboard, err := web.New(st, ws, base, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	dashboard.Version = version()
	mux := http.NewServeMux()
	dashboard.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	n := 0
	return st, ws, srv.URL, func(person, scope string) {
		t.Helper()
		now := time.Now()
		n++
		keys, _ := st.Passkeys(ctx, ws, person)
		if len(keys) == 0 {
			link, err := st.CreatePasskeyLink(ctx, store.Command("test"), ws, person, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPasskey(ctx, link, store.NewPasskey{CredentialID: []byte(person), RPID: "allstill.example.com", Name: "k", Record: []byte(`{"id":"x"}`)}, "", now); err != nil {
				t.Fatal(err)
			}
			keys, _ = st.Passkeys(ctx, ws, person)
		}
		token, err := st.CreatePersonToken(ctx, store.NewPersonToken{WorkspaceID: ws, PersonID: person, PasskeyID: keys[0].ID, Record: keys[0].Record, Name: "laptop-1", Scope: scope}, now)
		if err != nil {
			t.Fatal(err)
		}
		c := client.Credentials{Current: srv.URL, Servers: map[string]client.Credential{srv.URL: {Token: token}}}
		if err := c.Save(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadingFromTheCommandLine(t *testing.T) {
	st, ws, url, signIn := aRealServer(t)
	ctx := t.Context()
	test := store.Command("test")
	ada, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Ada", WorkspaceAdmin: true})
	carol, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Carol"})
	backups, _, _ := st.CreateProject(ctx, test, ws, "Backups")
	shop, _, _ := st.CreateProject(ctx, test, ws, "Shop")
	st.SetMember(ctx, test, ws, shop, carol, store.Viewer)
	admin, _ := st.AccessOfPerson(ctx, ws, ada)
	job, err := st.CreateCheck(ctx, admin, backups, store.NewCheck{Name: "nightly", Cron: "0 3 * * *", TimeZone: "UTC", Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	both, _ := st.CreateCheck(ctx, admin, shop, store.NewCheck{Name: "nightly", Interval: time.Hour, Grace: time.Hour})
	site, err := st.CreateCheck(ctx, admin, shop, store.NewCheck{Name: "site\x1b[31m", Kind: "http", Interval: time.Minute, Settings: map[string]any{"url": "https://shop.example.com/health?token=s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st.Ping(ctx, store.PingIn{CheckUUID: job.PingUUID, Kind: check.Fail, ExitStatus: 3, Method: "POST", At: now})

	signIn(ada, store.ScopeManage)
	for _, tt := range []struct {
		args []string
		has  []string
		not  []string
	}{
		{[]string{"status"}, []string{"1 of 3 checks needs attention", "down", "nightly", "Backups", "1 down", "Shop"}, nil},
		{[]string{"projects"}, []string{"Backups", "owner", "1 down", shop}, nil},
		{[]string{"checks"}, []string{"nightly", "Backups", "0 3 * * * (UTC)", "every hour", "https://shop.example.com/health?token=s3cret"}, nil},
		{[]string{"checks", "-project", "Backups"}, []string{"nightly"}, []string{"shop.example.com"}},
		{[]string{"checks", "show", "-check", "nightly", "-project", "Backups"}, []string{job.ID, "Backups", "down since", "Grace", "1 h"}, nil},
		{[]string{"checks", "show", "-check", job.ID}, []string{"nightly", "Backups"}, nil},
		{[]string{"checks", "-project", "Backups", "show", "-check", "nightly"}, []string{job.ID}, nil},
		{[]string{"checks", "-project", "Backups", "runs", "-check", "nightly"}, []string{"exit 3"}, nil},
		{[]string{"checks", "runs", "-check", job.ID}, []string{"fail", "exit 3"}, nil},
		{[]string{"log", "-check", job.ID}, []string{"Ada", "added", "nightly", "Backups"}, nil},
		{[]string{"log", "-name", "nightly", "-project", "Shop"}, []string{"Ada added nightly."}, nil},
		{[]string{"log", "-what", "done", "-limit", "1"}, []string{"There is more", "-before"}, nil},
		{[]string{"probes"}, []string{"No probes"}, nil},
		{[]string{"status", "-json"}, []string{`"overall": "incident"`, `"attention"`}, nil},
		{[]string{"checks", "-json"}, []string{`"items"`, `"state": "down"`}, []string{"ping_url", job.PingUUID}},
		{[]string{"checks", "runs", "-json", "-check", job.ID}, []string{`"outcome": "fail"`}, nil},
	} {
		out, err := command(t, tt.args...)
		if err != nil {
			t.Errorf("%v: %v", tt.args, err)
			continue
		}
		for _, want := range tt.has {
			if !strings.Contains(out, want) {
				t.Errorf("%v lacks %q:\n%s", tt.args, want, out)
			}
		}
		for _, not := range tt.not {
			if strings.Contains(out, not) {
				t.Errorf("%v holds %q:\n%s", tt.args, not, out)
			}
		}
		if strings.ContainsRune(out, '\x1b') && !slices.Contains(tt.args, "-json") {
			t.Errorf("%v printed a control character:\n%q", tt.args, out)
		}
	}

	// A name two share lists both, and changes nothing.
	_, err = command(t, "checks", "show", "-check", "nightly")
	if err == nil || !strings.Contains(err.Error(), job.ID) || !strings.Contains(err.Error(), both.ID) {
		t.Errorf("two with one name: %v", err)
	}
	if _, err := command(t, "checks", "show", "-check", "nothing"); err == nil || !strings.Contains(err.Error(), "no check named") {
		t.Errorf("an unknown name: %v", err)
	}
	if _, err := command(t, "log", "-check", "nothing"); err == nil || !strings.Contains(err.Error(), "-name") {
		t.Errorf("the log of an unknown check: %v", err)
	}
	if _, err := command(t, "log", "-what", "everything"); err == nil {
		t.Error("-what took anything")
	}
	if _, err := command(t, "checks", "nonsense"); err == nil {
		t.Error("an unknown checks command")
	}
	if out, err := command(t, "status", "-server", url); err != nil || !strings.Contains(out, url) {
		t.Errorf("-server: %v, %s", err, out)
	}

	// A viewer reads an address as far as they may, and no one's name.
	signIn(carol, store.ScopeManage)
	out, err := command(t, "checks")
	if err != nil || !strings.Contains(out, "https://shop.example.com") || strings.Contains(out, "s3cret") || strings.Contains(out, "Backups") {
		t.Errorf("a viewer's checks: %v:\n%s", err, out)
	}
	if out, err = command(t, "log", "-check", site.ID); err != nil || strings.Contains(out, "Ada") || !strings.Contains(out, "The team") {
		t.Errorf("a viewer's log: %v:\n%s", err, out)
	}
	if out, err := command(t, "checks", "show", "-check", job.ID); err == nil {
		t.Errorf("a check in another project: %s", out)
	}
	// A read-only administrator reads as a viewer does, everywhere.
	signIn(ada, store.ScopeRead)
	out, err = command(t, "checks", "-json")
	if err != nil || strings.Contains(out, "s3cret") || !strings.Contains(out, "https://shop.example.com") {
		t.Errorf("read-only: %v:\n%s", err, out)
	}
	if out, _ = command(t, "whoami"); strings.Contains(out, "administrator") {
		t.Errorf("read-only whoami: %s", out)
	}
	// A sign-in that has ended says so.
	if _, _, err := st.EndEverySignIn(ctx, test, ws, ada, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := command(t, "status"); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Errorf("ended: %v", err)
	}
}

// Stopping a waiting login says so in plain words.
func TestStoppingALogin(t *testing.T) {
	aHome(t)
	srv := newFakeServer(t)
	srv.hold = make(chan struct{})
	defer close(srv.hold)
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut bytes.Buffer
	done := make(chan error)
	go func() { done <- run(ctx, []string{"login", srv.URL}, &out, &errOut) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("got %v", err)
	}
}

// The whole way from a person's machine, with what a person is refused (R20 to R22).
func TestManagingFromTheCommandLine(t *testing.T) {
	st, ws, _, signIn := aRealServer(t)
	ctx := t.Context()
	test := store.Command("test")
	bob, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Bob"})
	carol, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Carol"})
	backups, _, _ := st.CreateProject(ctx, test, ws, "Backups")
	shop, _, _ := st.CreateProject(ctx, test, ws, "Shop")
	st.SetMember(ctx, test, ws, backups, bob, store.Editor)
	st.SetMember(ctx, test, ws, shop, bob, store.Editor)
	st.SetMember(ctx, test, ws, backups, carol, store.Viewer)
	t.Setenv("TZ", "Europe/Amsterdam")

	signIn(bob, store.ScopeManage)
	must := func(args ...string) string {
		t.Helper()
		out, err := command(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	has := func(out string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("lacks %q:\n%s", want, out)
			}
		}
	}

	// A job: its ping URL, a line to copy, and the machine's time zone.
	out := must("checks", "add", "-project", "Backups", "-name", "nightly", "-cron", "0 3 * * *", "-tags", "a, b", "-description", "the backup")
	has(out, "Added nightly to Backups", "https://allstill.example.com/ping/", "ALLSTILL_URL=https://allstill.example.com/ping/", "allstill run -- ./backup.sh", "waits for its first ping")
	shown := must("checks", "show", "-check", "nightly")
	has(shown, "Europe/Amsterdam", "the backup")
	if strings.Contains(shown, "/ping/") {
		t.Errorf("showing it prints its ping URL:\n%s", shown)
	}
	url := regexp.MustCompile(`https://allstill\.example\.com/ping/[0-9a-f-]+`).FindString(out)
	again := must("checks", "ping-url", "-check", "nightly")
	has(again, url, "ALLSTILL_URL="+url+" allstill run")
	if strings.Contains(out+again, "--url") {
		t.Errorf("a line to copy shows the secret to anyone who can list processes:\n%s%s", out, again)
	}
	if js := must("checks", "show", "-check", "nightly", "-json"); strings.Contains(js, "ping_url") {
		t.Errorf("-json of a read holds a ping URL:\n%s", js)
	}

	// Changing: only what is given.
	has(must("checks", "change", "-check", "nightly", "-every", "1h", "-grace", "30m", "-new-name", "hourly"), "Changed hourly", "counts from now")
	has(must("checks", "show", "-check", "hourly"), "every hour", "30 min", "the backup")
	has(must("checks", "change", "-check", "hourly", "-description", ""), "Changed hourly")
	if out := must("checks", "show", "-check", "hourly"); strings.Contains(out, "the backup") {
		t.Errorf("an empty -description left it:\n%s", out)
	}
	if _, err := command(t, "checks", "change", "-check", "hourly"); err == nil {
		t.Error("nothing to change said nothing")
	}
	if _, err := command(t, "checks", "change", "-check", "hourly", "-every", "1h", "-cron", "0 3 * * *"); err == nil {
		t.Error("both schedules")
	}
	if _, err := command(t, "checks", "change", "-check", "hourly", "-probe", "office"); err == nil || !strings.Contains(err.Error(), "Allstill runs") {
		t.Errorf("a probe for a job: %v", err)
	}

	// Pausing, moving, and a new ping URL.
	has(must("checks", "pause", "-check", "hourly"), "Paused hourly", "next ping resumes it")
	has(must("checks", "resume", "-check", "hourly"), "Resumed hourly")
	has(must("checks", "resume", "-check", "hourly"), "hourly is not paused")
	has(must("checks", "move", "-check", "hourly", "-to", "Shop"), "Moved hourly to Shop")
	has(must("checks", "-project", "Shop"), "hourly")
	has(must("checks", "move", "-check", "hourly", "-to", "Shop"), "in Shop already")
	if _, err := command(t, "checks", "pause", "-check", "nothing"); err == nil || !strings.HasPrefix(err.Error(), "you have no check named") {
		t.Errorf("an unknown name reads: %v", err)
	}
	if _, err := command(t, "checks", "change", "-check", "hourly", "-grace", "10s"); err == nil || !strings.Contains(err.Error(), "The grace period must be") {
		t.Errorf("a refusal does not say which field: %v", err)
	}
	replaced := must("checks", "ping-url", "-check", "hourly", "-replace")
	has(replaced, "Replaced")
	if strings.Contains(replaced, url) {
		t.Errorf("the old ping URL:\n%s", replaced)
	}

	// A site: the settings stay as they are unless given.
	out = must("checks", "add", "-project", "Shop", "-name", "site", "-http", "https://shop.example.com/health", "-expect", "200,204", "-json-field", "/status=ok")
	has(out, "Added site", "Not tried now")
	if js := must("checks", "add", "-project", "Shop", "-name", "site-2", "-http", "https://shop.example.com/", "-json"); !strings.Contains(js, `"try"`) {
		t.Errorf("-json gives the API's answer:\n%s", js)
	}
	must("checks", "delete", "-check", "site-2")
	has(must("checks", "change", "-check", "site", "-timeout", "20s"), "Changed site")
	shownSite := must("checks", "show", "-check", "site", "-json")
	has(shownSite, "https://shop.example.com/health", `"timeout": 20`, `"expected_status"`, "204", `"pointer": "/status"`, `"equals": "ok"`)
	if _, err := command(t, "checks", "change", "-check", "site", "-every", "500ms"); err == nil {
		t.Error("an interval that reads as none")
	}
	if _, err := command(t, "checks", "add", "-project", "Shop", "-name", "x", "-http", "https://a.example.com/", "-tcp", "a.example.com:1"); err == nil {
		t.Error("both -http and -tcp")
	}
	if _, err := command(t, "checks", "add", "-project", "Shop", "-name", "x", "-http", "http://10.0.0.5/"); err == nil || !strings.Contains(err.Error(), "not on the public internet") {
		t.Errorf("an address it may not reach: %v", err)
	}
	has(must("checks", "add", "-project", "Shop", "-name", "mail", "-tcp", "mail.example.com:993", "-tls"), "Added mail")

	// With more than one project, it asks which.
	if _, err := command(t, "checks", "add", "-name", "x"); err == nil || !strings.Contains(err.Error(), "-project") {
		t.Errorf("no project: %v", err)
	}
	has(must("checks", "delete", "-check", "mail"), "Deleted mail")

	// In the log, as who did it, and through which sign-in.
	has(must("log", "-name", "site"), "Bob", "laptop-1")

	// A viewer is refused, with who to ask.
	signIn(carol, store.ScopeManage)
	if _, err := command(t, "checks", "add", "-project", "Backups", "-name", "x"); err == nil || !strings.Contains(err.Error(), "Ask an owner") {
		t.Errorf("a viewer adds: %v", err)
	}
	if _, err := command(t, "checks", "add", "-project", "Shop", "-name", "x"); err == nil || !strings.Contains(err.Error(), "no project named") {
		t.Errorf("a viewer adds elsewhere: %v", err)
	}
	// One project: left out.
	if _, err := command(t, "checks", "add", "-name", "x"); err == nil || !strings.Contains(err.Error(), "Ask an owner") {
		t.Errorf("a viewer's only project: %v", err)
	}
	// A read-only sign-in says what it is.
	signIn(bob, store.ScopeRead)
	if _, err := command(t, "checks", "pause", "-check", "site"); err == nil || !strings.Contains(err.Error(), "can only read") {
		t.Errorf("read-only: %v", err)
	}
	if _, err := command(t, "checks", "ping-url", "-check", "hourly"); err == nil || !strings.Contains(err.Error(), "can only read") {
		t.Errorf("a ping URL, read-only: %v", err)
	}
}

// A key for a script, from making it to revoking it (R11, R12, case 9).
func TestAKeyFromTheCommandLine(t *testing.T) {
	st, ws, url, signIn := aRealServer(t)
	ctx := t.Context()
	test := store.Command("test")
	olga, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Olga"})
	edith, _ := st.CreatePerson(ctx, test, ws, store.NewPerson{Name: "Edith"})
	shop, _, _ := st.CreateProject(ctx, test, ws, "Shop")
	other, _, _ := st.CreateProject(ctx, test, ws, "Other")
	st.SetMember(ctx, test, ws, shop, olga, store.Owner)
	st.SetMember(ctx, test, ws, other, olga, store.Owner)
	st.SetMember(ctx, test, ws, shop, edith, store.Editor)

	signIn(olga, store.ScopeManage)
	out, err := command(t, "keys")
	if err != nil || !strings.Contains(out, "No keys") {
		t.Errorf("no keys: %v, %s", err, out)
	}
	if _, err := command(t, "keys", "make", "monitor"); err == nil || !strings.Contains(err.Error(), "-project") {
		t.Errorf("no project: %v", err)
	}
	if _, err := command(t, "keys", "make", "monitor", "-project", "Shop", "-scope", "root"); err == nil {
		t.Error("a scope that is not one")
	}
	out, err = command(t, "keys", "make", "monitor", "-project", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`alk_\S+`).FindString(out)
	if key == "" || !strings.Contains(out, "only time") || !strings.Contains(out, "ALLSTILL_SERVER="+url) {
		t.Fatalf("%s", out)
	}
	out, err = command(t, "keys", "make", "-project", "Shop", "-scope", "manage", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	manageKey := regexp.MustCompile(`alk_\S+`).FindString(out)
	out, err = command(t, "keys", "-project", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"monitor", "deploy", "read", "manage", "Shop", "Olga, with laptop-1", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, key) || strings.Contains(out, "alk_") {
		t.Errorf("the list holds a key:\n%s", out)
	}

	// Used by a script: no sign-in here, and no credentials file needed.
	home, config := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME")
	elsewhere := t.TempDir()
	t.Setenv("HOME", elsewhere)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(elsewhere, ".config"))
	t.Setenv("AppData", elsewhere)
	if _, err := command(t, "status"); err == nil {
		t.Error("nothing signed in, and no key")
	}
	t.Setenv("ALLSTILL_TOKEN", key)
	if _, err := command(t, "status"); err == nil || !strings.Contains(err.Error(), "ALLSTILL_SERVER") {
		t.Errorf("a key with no server: %v", err)
	}
	t.Setenv("ALLSTILL_SERVER", url)
	out, err = command(t, "status", "-json")
	if err != nil || !strings.Contains(out, `"Shop"`) && !strings.Contains(out, "Shop") || strings.Contains(out, "Other") {
		t.Errorf("a key's status: %v\n%s", err, out)
	}
	out, err = command(t, "whoami")
	if err != nil || !strings.Contains(out, "The key monitor") || !strings.Contains(out, "only read") {
		t.Errorf("whoami: %v\n%s", err, out)
	}
	if _, err := command(t, "checks", "add", "-name", "x"); err == nil || !strings.Contains(err.Error(), "can only read") {
		t.Errorf("a read key adds: %v", err)
	}
	if _, err := command(t, "logout"); err == nil || !strings.Contains(err.Error(), "revoke") {
		t.Errorf("logout with a key: %v", err)
	}
	if _, err := command(t, "keys"); err == nil || !strings.Contains(err.Error(), "A key cannot") {
		t.Errorf("a key lists keys: %v", err)
	}
	if _, err := command(t, "keys", "make", "more", "-scope", "manage"); err == nil || !strings.Contains(err.Error(), "A key cannot") {
		t.Errorf("a key makes a key: %v", err)
	}

	// A manage key adds a check, and the ping URL is for that one answer.
	t.Setenv("ALLSTILL_TOKEN", manageKey)
	out, err = command(t, "checks", "add", "-name", "by-script")
	if err != nil || !strings.Contains(out, "/ping/") {
		t.Errorf("a manage key adds: %v\n%s", err, out)
	}
	out, err = command(t, "whoami")
	if err != nil || !strings.Contains(out, "as an editor") {
		t.Errorf("whoami: %v\n%s", err, out)
	}
	if out, err := command(t, "log", "-name", "by-script"); err != nil || !strings.Contains(out, "deploy") {
		t.Errorf("the log: %v\n%s", err, out)
	}

	// An editor is told who may.
	t.Setenv("ALLSTILL_TOKEN", "")
	t.Setenv("ALLSTILL_SERVER", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AppData", home)
	signIn(edith, store.ScopeManage)
	if _, err := command(t, "keys", "-project", "Shop"); err == nil || !strings.Contains(err.Error(), "Ask an owner") {
		t.Errorf("an editor lists: %v", err)
	}
	if _, err := command(t, "keys", "revoke", "monitor"); err == nil || !strings.Contains(err.Error(), "no key named") {
		t.Errorf("an editor revokes: %v", err)
	}

	// Revoked, it stops.
	signIn(olga, store.ScopeManage)
	out, err = command(t, "keys", "revoke", "monitor")
	if err != nil || !strings.Contains(out, "Revoked monitor") {
		t.Errorf("revoking: %v\n%s", err, out)
	}
	if _, err := command(t, "keys", "revoke", "monitor"); err == nil {
		t.Error("revoked twice")
	}
	t.Setenv("ALLSTILL_SERVER", url)
	t.Setenv("ALLSTILL_TOKEN", key)
	if _, err := command(t, "status"); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("a revoked key: %v", err)
	}
}
