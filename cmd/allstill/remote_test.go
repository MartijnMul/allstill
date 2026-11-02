// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/client"
	"github.com/MartijnMul/allstill/internal/store"
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
