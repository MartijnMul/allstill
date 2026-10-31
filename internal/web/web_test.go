// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

const site = "https://allstill.example.com"

type fixture struct {
	path    string
	st      *store.Store
	srv     *Server
	mux     *http.ServeMux
	ws      string
	ada     string // an administrator
	project string
	checkID string
	pingURL string
	hidden  string // a check in a project Bob may not see
	bob     string // a member of one project
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "allstill.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := fixture{path: path, st: st}
	f.ws, _ = st.Workspace(ctx)
	f.ada, _ = st.CreatePerson(ctx, f.ws, store.NewPerson{Name: "Ada", Email: "ada@example.com", WorkspaceAdmin: true, TimeZone: "Europe/Amsterdam"})
	f.bob, _ = st.CreatePerson(ctx, f.ws, store.NewPerson{Name: "Bob", Email: "bob@example.com"})
	f.project, _, _ = st.CreateProject(ctx, f.ws, "Backups")
	other, _, _ := st.CreateProject(ctx, f.ws, "Customer")
	st.SetMember(ctx, f.ws, f.project, f.bob, store.Viewer)
	admin, _ := st.AdminAccess(ctx, f.ws)
	c, err := st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "nightly-backup", Cron: "0 3 * * *", TimeZone: "Europe/Amsterdam", Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.checkID, f.pingURL = c.ID, site+"/ping/"+c.PingUUID
	h, _ := st.CreateCheck(ctx, admin, other, store.NewCheck{Name: "customer-sync", Interval: time.Hour, Grace: time.Hour})
	f.hidden = h.ID

	at := time.Now().Add(-time.Hour)
	st.Ping(ctx, store.PingIn{CheckUUID: c.PingUUID, Kind: check.Start, ExitStatus: -1, Method: "GET", At: at})
	st.Ping(ctx, store.PingIn{CheckUUID: c.PingUUID, Kind: check.Success, ExitStatus: 0, Method: "POST", At: at.Add(40 * time.Minute),
		Body: []byte("uploaded <1284> files\n"), Values: map[string]float64{"size": 18.4e9}})

	base, _ := ParseBaseURL(site)
	f.srv, err = New(st, f.ws, base, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f.mux = http.NewServeMux()
	f.srv.Register(f.mux)
	return f
}

// browser keeps cookies, as a browser does.
type browser struct {
	t       *testing.T
	mux     *http.ServeMux
	cookies map[string]*http.Cookie
	header  http.Header
}

func (f fixture) browser(t *testing.T) *browser {
	return &browser{t: t, mux: f.mux, cookies: map[string]*http.Cookie{}, header: http.Header{}}
}

func (b *browser) do(method, path string, body any) *httptest.ResponseRecorder {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, site+path, r)
	for k, v := range b.header {
		req.Header[k] = v
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	b.mux.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return w
}

func (b *browser) ok(method, path string, body any) []byte {
	b.t.Helper()
	w := b.do(method, path, body)
	if w.Code != http.StatusOK {
		b.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

// register makes a passkey through a link and returns it, signed in.
func (f fixture) register(t *testing.T, b *browser, personID string) *authenticator {
	t.Helper()
	token, err := f.st.CreatePasskeyLink(t.Context(), f.ws, personID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a := &authenticator{t: t, origin: site}
	opts := b.ok("POST", "/setup/begin", map[string]string{"token": token, "name": "MacBook"})
	b.ok("POST", "/setup/finish", map[string]any{"token": token, "name": "MacBook", "credential": a.create(opts)})
	return a
}

func (b *browser) signIn(a *authenticator) *httptest.ResponseRecorder {
	b.t.Helper()
	opts := b.ok("POST", "/login/begin", nil)
	return b.do("POST", "/login/finish", a.get(opts))
}

func TestFromLinkToDashboard(t *testing.T) {
	f := setup(t)
	b := f.browser(t)

	if w := b.do("GET", "/", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("signed out: %d %v", w.Code, w.Header())
	}
	token, _ := f.st.CreatePasskeyLink(t.Context(), f.ws, f.ada, time.Now())
	var link map[string]string
	json.Unmarshal(b.ok("POST", "/setup/link", map[string]string{"token": token}), &link)
	if link["name"] != "Ada" || link["signed_in_as"] != "" {
		t.Fatalf("the link says %v", link)
	}
	a := &authenticator{t: t, origin: site}
	opts := b.ok("POST", "/setup/begin", map[string]string{"token": token, "name": "MacBook"})
	if _, ok := b.cookies["__Host-allstill-ceremony"]; !ok {
		t.Fatalf("no ceremony cookie: %v", b.cookies)
	}
	b.ok("POST", "/setup/finish", map[string]any{"token": token, "name": "MacBook", "credential": a.create(opts)})
	session := b.cookies["__Host-allstill-session"]
	if session == nil || !session.Secure || !session.HttpOnly || session.Path != "/" || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie %+v", session)
	}
	if _, ok := b.cookies["__Host-allstill-ceremony"]; ok {
		t.Fatal("the ceremony cookie outlived the ceremony")
	}
	if keys, _ := f.st.Passkeys(t.Context(), f.ws, f.ada); len(keys) != 1 || keys[0].Name != "MacBook" || keys[0].RPID != "allstill.example.com" {
		t.Fatalf("passkeys %+v", keys)
	}

	page := string(b.ok("GET", "/", nil))
	for _, want := range []string{"nightly-backup", "customer-sync", "Backups", "size 18.4 G", "Ada", "0 3 * * *", "Europe/Amsterdam · grace 1 h", "1 check has not pinged yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	page = string(b.ok("GET", "/checks/"+f.checkID, nil))
	for _, want := range []string{f.pingURL, "uploaded &lt;1284&gt; files", "40 min", "exit 0"} {
		if !strings.Contains(page, want) {
			t.Errorf("the check page lacks %q", want)
		}
	}

	// The link worked once.
	w := b.do("POST", "/setup/begin", map[string]string{"token": token})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "has been used") {
		t.Fatalf("a used link: %d %s", w.Code, w.Body)
	}

	// Signed out, then in again with the passkey: no name to type.
	b.do("POST", "/logout", nil)
	if w := b.do("GET", "/", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("after signing out: %d", w.Code)
	}
	if w := b.signIn(a); w.Code != http.StatusOK {
		t.Fatalf("signing in: %d %s", w.Code, w.Body)
	}
	b.ok("GET", "/", nil)
	if keys, _ := f.st.Passkeys(t.Context(), f.ws, f.ada); keys[0].LastUsed.IsZero() {
		t.Fatal("the sign-in was not recorded")
	}
}

func TestAMemberSeesTheirProjectsOnly(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.bob)
	page := string(b.ok("GET", "/", nil))
	if !strings.Contains(page, "nightly-backup") || strings.Contains(page, "customer-sync") || strings.Contains(page, "Customer") {
		t.Fatal("Bob's overview shows another project's checks")
	}
	if !strings.Contains(page, "All is well") {
		t.Error("Bob's one check is up, but the overview does not say all is well")
	}
	if w := b.do("GET", "/checks/"+f.hidden, nil); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "customer-sync") {
		t.Fatalf("Bob opens another project's check: %d", w.Code)
	}
	if w := b.do("GET", "/checks/"+f.checkID+"?run=run_nope", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a run that is not the check's: %d", w.Code)
	}
	// Bob only views the project: its ping URL is a secret they do not see.
	uuid := strings.TrimPrefix(f.pingURL, site+"/ping/")
	if page := string(b.ok("GET", "/checks/"+f.checkID, nil)); strings.Contains(page, uuid) || !strings.Contains(page, "editors see it here") {
		t.Fatal("a viewer sees the ping URL")
	}
	// An editor sees it.
	carol, _ := f.st.CreatePerson(t.Context(), f.ws, store.NewPerson{Name: "Carol"})
	f.st.SetMember(t.Context(), f.ws, f.project, carol, store.Editor)
	ed := f.browser(t)
	f.register(t, ed, carol)
	if page := string(ed.ok("GET", "/checks/"+f.checkID, nil)); !strings.Contains(page, uuid) {
		t.Fatal("an editor does not see the ping URL")
	}
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	if page := string(ada.ok("GET", "/checks/"+f.checkID, nil)); !strings.Contains(page, uuid) {
		t.Fatal("an administrator does not see the ping URL")
	}
}

func TestAChallengeIsAnsweredOnceAndOnlyWhereItWasAsked(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	a.synced = true // so only the challenge stops a replay

	opts := b.ok("POST", "/login/begin", nil)
	answer := a.get(opts)
	nonce := b.cookies["__Host-allstill-ceremony"]

	// Another browser posts this browser's answer: a login forced on a victim.
	other := f.browser(t)
	other.ok("POST", "/login/begin", nil)
	if w := other.do("POST", "/login/finish", answer); w.Code != http.StatusBadRequest {
		t.Fatalf("an answer from another browser: %d %s", w.Code, w.Body)
	}

	if w := b.do("POST", "/login/finish", answer); w.Code != http.StatusOK {
		t.Fatalf("the answer: %d %s", w.Code, w.Body)
	}
	b.cookies[nonce.Name] = nonce
	if w := b.do("POST", "/login/finish", answer); w.Code != http.StatusBadRequest {
		t.Fatalf("the same answer again: %d %s", w.Code, w.Body)
	}

	// A challenge past its five minutes.
	b.ok("POST", "/login/begin", nil)
	opts = b.ok("POST", "/login/begin", nil)
	f.srv.Now = func() time.Time { return time.Now().Add(ceremonyLifetime) }
	if w := b.do("POST", "/login/finish", a.get(opts)); w.Code != http.StatusBadRequest {
		t.Fatalf("a late answer: %d %s", w.Code, w.Body)
	}
}

func TestACopiedPasskeyIsRefused(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	a.count = 10
	if w := b.signIn(a); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	a.count = 3 // a copy that signed less often
	if w := b.signIn(a); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "copied") {
		t.Fatalf("a counter that went back: %d %s", w.Code, w.Body)
	}
	a.count = 5 // more than the copy, less than the original: the record was kept at 10
	if w := b.signIn(a); w.Code != http.StatusForbidden {
		t.Fatalf("the record was rewritten after the refusal: %d %s", w.Code, w.Body)
	}
	a.count = 11 // the original keeps working
	if w := b.signIn(a); w.Code != http.StatusOK {
		t.Fatalf("the original after the copy: %d %s", w.Code, w.Body)
	}
}

func TestAPasskeyForAnotherSiteIsRefused(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	b.do("POST", "/logout", nil)
	opts := b.ok("POST", "/login/begin", nil)
	var o options
	json.Unmarshal(opts, &o)
	if w := b.do("POST", "/login/finish", a.sign("evil.example.net", o.PublicKey.Challenge)); w.Code != http.StatusForbidden {
		t.Fatalf("a signature for another site: %d %s", w.Code, w.Body)
	}
	a.origin = "https://evil.example.net"
	if w := b.signIn(a); w.Code != http.StatusForbidden {
		t.Fatalf("an answer from another origin: %d %s", w.Code, w.Body)
	}
}

func TestAChallengeBelongsToItsLink(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	mine, _ := f.st.CreatePasskeyLink(t.Context(), f.ws, f.bob, time.Now())
	theirs, _ := f.st.CreatePasskeyLink(t.Context(), f.ws, f.ada, time.Now())
	a := &authenticator{t: t, origin: site}
	opts := b.ok("POST", "/setup/begin", map[string]string{"token": mine})
	w := b.do("POST", "/setup/finish", map[string]any{"token": theirs, "credential": a.create(opts)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a passkey begun for one link, finished with another: %d %s", w.Code, w.Body)
	}
	if keys, _ := f.st.Passkeys(t.Context(), f.ws, f.ada); len(keys) != 0 {
		t.Fatal("a passkey was added to the other link's person")
	}
}

func TestADisabledPersonCannotSignIn(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.bob)
	b.do("POST", "/logout", nil)
	disable(t, f, f.bob)
	if w := b.signIn(a); w.Code != http.StatusForbidden {
		t.Fatalf("a disabled person signs in: %d %s", w.Code, w.Body)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	b.header.Set("Sec-Fetch-Site", "cross-site")
	for _, path := range []string{"/logout", "/login/begin", "/setup/begin"} {
		if w := b.do("POST", path, map[string]string{}); w.Code != http.StatusForbidden {
			t.Errorf("a cross-site POST %s: %d", path, w.Code)
		}
	}
	b.header.Del("Sec-Fetch-Site")
	b.ok("GET", "/", nil) // still signed in
}

func TestSigningInReplacesTheSession(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	first := b.cookies["__Host-allstill-session"].Value

	token, _ := f.st.CreatePasskeyLink(t.Context(), f.ws, f.bob, time.Now())
	var link map[string]string
	json.Unmarshal(b.ok("POST", "/setup/link", map[string]string{"token": token}), &link)
	if link["name"] != "Bob" || link["signed_in_as"] != "Ada" {
		t.Fatalf("the link says %v", link)
	}
	a := &authenticator{t: t, origin: site}
	opts := b.ok("POST", "/setup/begin", map[string]string{"token": token})
	b.ok("POST", "/setup/finish", map[string]any{"token": token, "credential": a.create(opts)})
	if _, err := f.st.Session(t.Context(), first, time.Now()); err == nil {
		t.Fatal("Ada's session outlived Bob's sign-in in the same browser")
	}
	if page := string(b.ok("GET", "/", nil)); !strings.Contains(page, "Bob") {
		t.Fatal("the browser is not Bob's")
	}
}

func TestHeaders(t *testing.T) {
	f := setup(t)
	w := f.browser(t).do("GET", "/login", nil)
	h := w.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") || h.Get("Referrer-Policy") != "no-referrer" ||
		h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers %v", h)
	}
	if w := f.browser(t).do("GET", "/static/app.js", nil); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("app.js: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(f.browser(t).do("GET", "/login", nil).Body.String(), `<link rel="icon" href="/static/favicon.svg"`) {
		t.Fatal("the page does not link the favicon")
	}
	if w := f.browser(t).do("GET", "/static/favicon.svg", nil); w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("favicon.svg: %d %v", w.Code, w.Header())
	}
	if w := f.browser(t).do("GET", "/static/", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a listing of /static/: %d", w.Code)
	}
	if strings.Contains(f.browser(t).do("GET", "/login", nil).Body.String(), "style=") {
		t.Fatal("inline styles, which the policy blocks")
	}
}

func TestBaseURLs(t *testing.T) {
	for _, ok := range []string{"https://allstill.example.com", "https://allstill.example.com:8443/", "http://localhost:8000"} {
		if _, err := ParseBaseURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://allstill.example.com", "https://192.0.2.1", "http://127.0.0.1:8000", "https://allstill.example.com/dash", "allstill.example.com", "https://"} {
		if _, err := ParseBaseURL(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	if u, _ := ParseBaseURL("https://Allstill.Example.com"); u.Hostname() != "allstill.example.com" {
		t.Errorf("a host in capitals: %s", u)
	}
	base, _ := ParseBaseURL("http://localhost:8000")
	s, _ := New(nil, "", base, slog.New(slog.DiscardHandler))
	if s.cookie("session") != "allstill-session" || s.secure {
		t.Fatalf("over http on localhost: %s", s.cookie("session"))
	}
}

// disable stands in for disabling a person, which has no command yet.
func disable(t *testing.T, f fixture, personID string) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE people SET disabled_at = 1 WHERE id = ?", personID); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesShowWhatWouldHaveBeenSent(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	b := f.browser(t)
	f.register(t, b, f.ada)
	if !strings.Contains(string(b.ok("GET", "/messages", nil)), "Nothing yet") {
		t.Fatal("messages before any incident")
	}

	// A critical check, so quiet hours do not hold the alert back whenever the test runs.
	if err := f.st.SetAlerting(ctx, f.ws, f.checkID, "now", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Ping(ctx, store.PingIn{CheckUUID: strings.TrimPrefix(f.pingURL, site+"/ping/"), Kind: check.Fail, ExitStatus: 3, Method: "POST", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sender := &alert.Sender{Store: f.st, Log: slog.New(slog.DiscardHandler), Now: time.Now}
	if err := sender.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	page := string(b.ok("GET", "/messages", nil))
	if !strings.Contains(page, "Down: nightly-backup is down") || !strings.Contains(page, "Would have been sent") || !strings.Contains(page, "Shadow mode") {
		t.Fatalf("messages page:\n%s", page)
	}
	if !strings.Contains(string(b.ok("GET", "/checks/"+f.checkID, nil)), "An incident is open since") {
		t.Fatal("the check page does not show its incident")
	}
	// Bob is a viewer: he gets no alerts.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	if !strings.Contains(string(bob.ok("GET", "/messages", nil)), "Nothing yet") {
		t.Fatal("a viewer was alerted")
	}
}

// Every signed-in page renders with its sidebar, for every kind of check:
// one whose runs sent values, one whose runs did not, and one Allstill runs.
func TestEveryPageRenders(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	admin, _ := f.st.AccessOfPerson(ctx, f.ws, f.ada)
	plain, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "log-rotate", Interval: time.Hour, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Ping(ctx, store.PingIn{CheckUUID: plain.PingUUID, Kind: check.Fail, ExitStatus: 1, Method: "POST", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	site, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "shop.example.com", Kind: "http", Interval: time.Minute,
		Settings: map[string]any{"url": "https://shop.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	// Two attempts, so its page draws their response times.
	for _, at := range []time.Time{time.Now().Add(-2 * time.Minute), time.Now().Add(-time.Minute)} {
		if _, err := f.st.RecordAttempt(ctx, f.ws, site.ID, store.Attempt{Attempt: check.Attempt{Outcome: check.Passed, At: at},
			Started: at.Add(-time.Second), Measured: map[string]float64{"status": 200}}); err != nil {
			t.Fatal(err)
		}
	}

	b := f.browser(t)
	f.register(t, b, f.ada)
	for path, want := range map[string]string{
		"/":                      "health-bar",
		"/?show=attention":       "Needs attention",
		"/?show=paused":          "No checks here.",
		"/?project=" + f.project: "log-rotate",
		"/?q=nothing-like-it":    "No check matches",
		"/checks/" + f.checkID:   "size 18.4 G",
		"/checks/" + plain.ID:    "The job failed with exit status 1.",
		"/checks/" + site.ID:     "Response time",
		"/messages":              "History",
		"/channels":              "Where alerts go",
	} {
		page := string(b.ok("GET", path, nil))
		if !strings.Contains(page, want) {
			t.Errorf("%s lacks %q", path, want)
		}
		if !strings.Contains(page, `id="nav"`) || !strings.Contains(page, "Backups") {
			t.Errorf("%s has no sidebar with the projects", path)
		}
	}
	if page := string(b.ok("GET", "/", nil)); !strings.Contains(page, "<title>(1) Checks · Allstill</title>") {
		t.Error("the overview's title does not count the check that needs someone")
	}
}

// The view of what needs attention says all is well only when Allstill
// knows it: a check that is unknown, as when its own network is down, needs
// no one to act on it, but is not known to be up.
func TestTheAttentionViewClaimsOnlyWhatItKnows(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	b := f.browser(t)
	f.register(t, b, f.bob)
	if page := string(b.ok("GET", "/?show=attention", nil)); !strings.Contains(page, "Nothing needs you") {
		t.Fatal("the attention view of checks that are all well does not say so")
	}
	admin, _ := f.st.AdminAccess(ctx, f.ws)
	site, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{Name: "shop.example.com", Kind: "http", Interval: time.Minute,
		Settings: map[string]any{"url": "https://shop.example.com/"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordAttempt(ctx, f.ws, site.ID, store.Attempt{Attempt: check.Attempt{Outcome: check.Unjudged, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	page := string(b.ok("GET", "/?show=attention", nil))
	if strings.Contains(page, "Nothing needs you") || !strings.Contains(page, "No check is down, late or in warning.") {
		t.Fatal("the attention view says all is well while a check is unknown")
	}
}
