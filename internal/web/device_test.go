// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/client"
	"github.com/MartijnMul/allstill/internal/store"
)

// oauthPost posts a form as a command line does: no cookies, no browser.
func oauthPost(f fixture, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", site+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

type askedCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func askCode(t *testing.T, f fixture, name, scope string) askedCode {
	t.Helper()
	w := oauthPost(f, "/oauth/device_authorization", url.Values{"client_id": {clientID}, "device_name": {name}, "scope": {scope}})
	if w.Code != http.StatusOK {
		t.Fatalf("asking: %d %s", w.Code, w.Body)
	}
	var c askedCode
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// pollFor polls as a command line does, and returns its status and answer.
func pollFor(f fixture, deviceCode string) (int, map[string]string) {
	w := oauthPost(f, "/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrantType}, "device_code": {deviceCode}})
	var answer map[string]string
	json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer
}

// clockOf gives a server a clock a test moves, so that polling is not too fast.
func clockOf(f fixture) (advance func(time.Duration)) {
	now := time.Now()
	f.srv.Now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

// approve goes through the page, with a passkey, and returns the finishing answer.
func approve(t *testing.T, b *browser, code string, a *authenticator) *httptest.ResponseRecorder {
	t.Helper()
	opts := b.ok("POST", "/device/begin", map[string]string{"code": code})
	return b.do("POST", "/device/finish", map[string]any{"code": code, "credential": a.get(opts)})
}

// bearer asks the API with a token, as a command line does.
func bearer(f fixture, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, site+"/api/v1"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

// signedInToken goes the whole way for a person, and returns the token.
func signedInToken(t *testing.T, f fixture, b *browser, a *authenticator, name, scope string) string {
	t.Helper()
	advance := clockOf(f)
	c := askCode(t, f, name, scope)
	if w := approve(t, b, c.UserCode, a); w.Code != http.StatusOK {
		t.Fatalf("approving: %d %s", w.Code, w.Body)
	}
	advance(pollInterval)
	code, answer := pollFor(f, c.DeviceCode)
	if code != http.StatusOK || answer["access_token"] == "" {
		t.Fatalf("redeeming: %d %v", code, answer)
	}
	return answer["access_token"]
}

func TestSigningInFromACommandLine(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	f.srv.Version = "1.2.3"
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)

	c := askCode(t, f, "laptop-1", "manage")
	if c.VerificationURI != site+"/device" || c.ExpiresIn != 600 || c.Interval != 5 || len(c.UserCode) != 9 || c.UserCode[4] != '-' {
		t.Fatalf("asked: %+v", c)
	}
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "authorization_pending" {
		t.Fatalf("before approval: %d %v", code, answer)
	}

	// The page says what asks, and only then is a passkey asked for.
	if page := string(b.ok("GET", "/device", nil)); !strings.Contains(page, `name="code"`) || strings.Contains(page, "device-allow") {
		t.Fatalf("the first page: %s", page)
	}
	shown := b.post("/device", url.Values{"code": {strings.ToLower(strings.ReplaceAll(c.UserCode, "-", ""))}})
	if shown.Code != http.StatusOK {
		t.Fatalf("entering the code: %d %s", shown.Code, shown.Body)
	}
	for _, want := range []string{"laptop-1", "anyone can claim a name", c.UserCode, "started this yourself", "device-allow", "Do everything you can", "just now"} {
		if !strings.Contains(shown.Body.String(), want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	before, _ := f.st.BrowserSessions(ctx, f.ws, f.ada, time.Now())
	if w := approve(t, b, c.UserCode, a); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "approved") {
		t.Fatalf("approving: %d %s", w.Code, w.Body)
	}
	// From the moment it is approved, its person sees it and it is in the log.
	if page := b.get("/channels").Body.String(); !strings.Contains(page, "laptop-1") {
		t.Errorf("Devices does not show it before it is fetched: %s", page)
	}
	after, _ := f.st.BrowserSessions(ctx, f.ws, f.ada, time.Now())
	if len(after) != len(before) {
		t.Errorf("approving opened a session: %d, then %d", len(before), len(after))
	}
	// No address says it worked, for a message to send someone to.
	if page := string(b.ok("GET", "/device?approved", nil)); strings.Contains(page, "Signed in<") {
		t.Errorf("an address says it worked: %s", page)
	}

	advance(pollInterval)
	code, answer := pollFor(f, c.DeviceCode)
	if code != http.StatusOK || answer["token_type"] != "Bearer" || answer["scope"] != "manage" || !store.IsPersonToken(answer["access_token"]) {
		t.Fatalf("redeeming: %d %v", code, answer)
	}
	token := answer["access_token"]
	advance(pollInterval)
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "invalid_grant" {
		t.Fatalf("redeeming twice: %d %v", code, answer)
	}

	w := bearer(f, "GET", "/me", token)
	if w.Code != http.StatusOK {
		t.Fatalf("/me: %d %s", w.Code, w.Body)
	}
	var me api.Me
	json.Unmarshal(w.Body.Bytes(), &me)
	if me.Kind != api.Person || me.Person.Name != "Ada" || !*me.Person.WorkspaceAdmin || me.ServerVersion != "1.2.3" || me.SignIn.Name != "laptop-1" || !slices.Contains(me.Scopes, api.Manage) || len(me.Projects) != 2 {
		t.Fatalf("/me: %+v", me)
	}

	// Under Devices, and ending it there counts at once.
	page := b.get("/channels").Body.String()
	if !strings.Contains(page, "laptop-1") || !strings.Contains(page, "This browser") || !strings.Contains(page, "can do everything you can") {
		t.Fatalf("Devices lacks the sign-in: %s", page)
	}
	tokens, _ := f.st.PersonTokens(ctx, f.ws, f.ada, time.Now())
	if w := b.post("/sign-ins/"+tokens[0].ID+"/end", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("ending it: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/me", token); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("after ending it: %d %v", w.Code, w.Header())
	}
}

// A code gets its own name and scope out of the form, and nothing else counts.
func TestAskingForACode(t *testing.T) {
	f := setup(t)
	for _, tt := range []struct {
		form url.Values
		code int
		err  string
	}{
		{url.Values{"client_id": {"other"}}, http.StatusUnauthorized, "invalid_client"},
		{url.Values{}, http.StatusUnauthorized, "invalid_client"},
		{url.Values{"client_id": {clientID}, "scope": {"admin"}}, http.StatusBadRequest, "invalid_scope"},
		{url.Values{"client_id": {clientID}, "scope": {"read manage"}}, http.StatusBadRequest, "invalid_scope"},
	} {
		w := oauthPost(f, "/oauth/device_authorization", tt.form)
		if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.err) {
			t.Errorf("%v: %d %s", tt.form, w.Code, w.Body)
		}
	}
	if w := oauthPost(f, "/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {"password"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_grant_type") {
		t.Errorf("another grant: %d %s", w.Code, w.Body)
	}
	if w := oauthPost(f, "/oauth/device_authorization", url.Values{"client_id": {clientID}, "device_name": {strings.Repeat("x", maxOAuthBody)}}); w.Code != http.StatusBadRequest {
		t.Errorf("a body over the limit: %d", w.Code)
	}

	// A name is one short line of what a person can read.
	for raw, want := range map[string]string{
		"laptop-1":                 "laptop-1",
		"":                         unnamedDevice,
		"  \n":                     unnamedDevice,
		"evil\x1b[31m\u202e\nhost": "evil[31mhost",
		"\xff\xfe":                 unnamedDevice,
		strings.Repeat("é", 200):   strings.Repeat("é", maxDeviceName),
	} {
		if got := cleanDeviceName(raw); got != want {
			t.Errorf("%q reads as %q, want %q", raw, got, want)
		}
	}

	// Asking writes nothing, however often, and past the limit others wait too.
	entries := func() (n int) {
		t.Helper()
		db, err := sql.Open("sqlite", f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM log_entries) + (SELECT count(*) FROM person_tokens)`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := entries()
	for range asksPerMinute {
		askCode(t, f, "x", "")
	}
	if got := entries(); got != before {
		t.Errorf("asking wrote %d rows", got-before)
	}
	w := oauthPost(f, "/oauth/device_authorization", url.Values{"client_id": {clientID}})
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("past the limit: %d %v", w.Code, w.Header())
	}
}

// Someone signed out comes back to the page, and nowhere else can be asked for.
func TestSignedOutAtTheApprovalPage(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	if w := b.do("GET", "/device", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=device" {
		t.Fatalf("signed out: %d %v", w.Code, w.Header())
	}
	if page := string(b.ok("GET", "/login?next=device", nil)); !strings.Contains(page, `data-next="/device"`) {
		t.Errorf("the sign-in page does not return: %s", page)
	}
	for _, next := range []string{"https://evil.example.com", "//evil.example.com", "/checks/new", "device/"} {
		if page := string(b.ok("GET", "/login?next="+url.QueryEscape(next), nil)); strings.Contains(page, "data-next") || strings.Contains(page, "evil") {
			t.Errorf("next=%q is followed: %s", next, page)
		}
	}
	if w := b.do("POST", "/device/begin", map[string]string{"code": "x"}); w.Code != http.StatusUnauthorized {
		t.Errorf("a script signed out: %d", w.Code)
	}
	f.register(t, b, f.ada)
	if w := b.do("GET", "/login?next=device", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/device" {
		t.Errorf("signed in already: %d %v", w.Code, w.Header())
	}
	if w := b.do("GET", "/login?next=https://evil.example.com", nil); w.Header().Get("Location") != "/" {
		t.Errorf("signed in already, elsewhere: %v", w.Header())
	}
}

// A link cannot arrive with a code filled in (C3).
func TestTheCodeIsNeverReadFromTheAddress(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	c := askCode(t, f, "laptop-1", "manage")
	for _, q := range []string{"code", "user_code"} {
		page := string(b.ok("GET", "/device?"+q+"="+url.QueryEscape(c.UserCode), nil))
		if strings.Contains(page, c.UserCode) {
			t.Errorf("GET with %s shows it: %s", q, page)
		}
		w := b.post("/device?"+q+"="+url.QueryEscape(c.UserCode), url.Values{})
		if strings.Contains(w.Body.String(), "laptop-1") || strings.Contains(w.Body.String(), "device-allow") {
			t.Errorf("POST with %s in the address takes it: %s", q, w.Body)
		}
	}
	// Nor does the answer give one to follow.
	if strings.Contains(oauthPost(f, "/oauth/device_authorization", url.Values{"client_id": {clientID}}).Body.String(), "verification_uri_complete") {
		t.Error("the answer holds a link with the code in it")
	}
}

func TestTooManyWrongCodes(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	other := f.browser(t)
	f.register(t, other, f.bob)
	advance := clockOf(f)
	c := askCode(t, f, "laptop-1", "manage")
	for range wrongCodesPerMinute {
		if w := b.post("/device", url.Values{"code": {"BBBB-BBBB"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "does not work") {
			t.Fatalf("a wrong code: %d %s", w.Code, w.Body)
		}
	}
	// Past the limit even the right one is refused, on each way in, or guessing goes on.
	if w := b.post("/device", url.Values{"code": {c.UserCode}}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the right code past the limit: %d %s", w.Code, w.Body)
	}
	if w := b.do("POST", "/device/begin", map[string]string{"code": c.UserCode}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("beginning past the limit: %d %s", w.Code, w.Body)
	}
	if w := b.post("/device/deny", url.Values{"code": {c.UserCode}}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("refusing past the limit: %d %s", w.Code, w.Body)
	}
	// A limit of one person, for a minute.
	if w := other.post("/device", url.Values{"code": {c.UserCode}}); w.Code != http.StatusOK {
		t.Fatalf("someone else: %d", w.Code)
	}
	advance(time.Minute)
	if w := approve(t, b, c.UserCode, a); w.Code != http.StatusOK {
		t.Fatalf("a minute later: %d %s", w.Code, w.Body)
	}
}

func TestRefusingACode(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	clockOf(f)
	c := askCode(t, f, "laptop-1", "manage")
	w := b.post("/device/deny", url.Values{"code": {c.UserCode}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Nobody signed in") {
		t.Fatalf("refusing: %d %s", w.Code, w.Body)
	}
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "access_denied" {
		t.Fatalf("polling: %d %v", code, answer)
	}
	if w := b.do("POST", "/device/begin", map[string]string{"code": c.UserCode}); w.Code != http.StatusBadRequest {
		t.Fatalf("approving it afterwards: %d", w.Code)
	}
}

// The ceremony looks like signing in and must not act like it (the plan's Risks).
func TestAnotherPersonsPasskeyApprovesNothing(t *testing.T) {
	f := setup(t)
	ada := f.browser(t)
	adas := f.register(t, ada, f.ada)
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	clockOf(f)
	c := askCode(t, f, "laptop-1", "manage")

	// Bob is signed in, and answers with Ada's passkey.
	if w := approve(t, bob, c.UserCode, adas); w.Code != http.StatusForbidden {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "authorization_pending" {
		t.Fatalf("still waiting: %d %v", code, answer)
	}
}

func TestAnAnswerIsBoundToItsCodeAndPerson(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)
	one := askCode(t, f, "one", "read")
	two := askCode(t, f, "two", "manage")

	opts := b.ok("POST", "/device/begin", map[string]string{"code": one.UserCode})
	w := b.do("POST", "/device/finish", map[string]any{"code": two.UserCode, "credential": a.get(opts)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an answer for one code on another: %d %s", w.Code, w.Body)
	}
	for _, c := range []askedCode{one, two} {
		if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "authorization_pending" {
			t.Fatalf("%v: %d %v", c, code, answer)
		}
	}

	// Nor can an answer be used twice, or one from signing in be brought along.
	opts = b.ok("POST", "/login/begin", nil)
	if w := b.do("POST", "/device/finish", map[string]any{"code": one.UserCode, "credential": a.get(opts)}); w.Code != http.StatusBadRequest {
		t.Fatalf("an answer from signing in: %d %s", w.Code, w.Body)
	}
	opts = b.ok("POST", "/device/begin", map[string]string{"code": one.UserCode})
	answer := a.get(opts)
	if w := b.do("POST", "/device/finish", map[string]any{"code": one.UserCode, "credential": answer}); w.Code != http.StatusOK {
		t.Fatalf("the real answer: %d %s", w.Code, w.Body)
	}
	if w := b.do("POST", "/device/finish", map[string]any{"code": one.UserCode, "credential": answer}); w.Code == http.StatusOK {
		t.Fatal("an answer was used twice")
	}
	advance(pollInterval)
	if code, got := pollFor(f, one.DeviceCode); code != http.StatusOK || got["scope"] != "read" {
		t.Fatalf("the one approved: %d %v", code, got)
	}
}

// And an answer from here cannot sign anyone in to a browser.
func TestAnApprovalsAnswerOpensNoSession(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	clockOf(f)
	c := askCode(t, f, "laptop-1", "manage")
	opts := b.ok("POST", "/device/begin", map[string]string{"code": c.UserCode})
	fresh := f.browser(t)
	fresh.cookies = b.cookies // the ceremony's nonce too
	if w := fresh.do("POST", "/login/finish", a.get(opts)); w.Code == http.StatusOK {
		t.Fatalf("it signed someone in: %s", w.Body)
	}
}

func TestACopiedPasskeyApprovesNothing(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	clockOf(f)
	a.count = 10
	if w := approve(t, b, askCode(t, f, "first", "manage").UserCode, a); w.Code != http.StatusOK {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	a.count = 3
	c := askCode(t, f, "second", "manage")
	if w := approve(t, b, c.UserCode, a); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "looks copied") {
		t.Fatalf("a counter gone backwards: %d %s", w.Code, w.Body)
	}
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "authorization_pending" {
		t.Fatalf("it was approved: %d %v", code, answer)
	}
	access, _ := f.st.AccessOfPerson(t.Context(), f.ws, f.ada)
	page, _ := f.st.ReadLog(t.Context(), access, store.LogFilter{What: "signins", Limit: 10})
	if !slices.ContainsFunc(page.Entries, func(e store.LogEntry) bool { return e.Kind == "passkey.looks_copied" }) {
		t.Errorf("not logged: %+v", page.Entries)
	}
	// The record stayed as it was, so the real one keeps working.
	a.count = 11
	if w := approve(t, b, c.UserCode, a); w.Code != http.StatusOK {
		t.Fatalf("the original: %d %s", w.Code, w.Body)
	}
}

// Someone tricked into approving a code can end it before whoever asked fetches it,
// and what is fetched then does nothing.
func TestEndingAnApprovedSignInBeforeItIsFetched(t *testing.T) {
	for name, end := range map[string]func(f fixture, b *browser){
		"on Devices": func(f fixture, b *browser) {
			tokens, _ := f.st.PersonTokens(t.Context(), f.ws, f.ada, time.Now())
			b.post("/sign-ins/"+tokens[0].ID+"/end", nil)
		},
		"every sign-in, on the server": func(f fixture, b *browser) {
			f.st.EndEverySignIn(t.Context(), store.Command("end-sessions"), f.ws, f.ada, time.Now())
		},
		"its passkey, on the server": func(f fixture, b *browser) {
			keys, _ := f.st.Passkeys(t.Context(), f.ws, f.ada)
			f.st.RemovePasskey(t.Context(), store.Command("remove-passkey"), f.ws, f.ada, keys[0].ID, time.Now())
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			b := f.browser(t)
			a := f.register(t, b, f.ada)
			advance := clockOf(f)
			c := askCode(t, f, "not-mine", "manage")
			if w := approve(t, b, c.UserCode, a); w.Code != http.StatusOK {
				t.Fatalf("approving: %d %s", w.Code, w.Body)
			}
			access, _ := f.st.AccessOfPerson(t.Context(), f.ws, f.ada)
			page, _ := f.st.ReadLog(t.Context(), access, store.LogFilter{What: "signins", Limit: 10})
			if !slices.ContainsFunc(page.Entries, func(e store.LogEntry) bool { return e.Kind == "token.made" && e.AboutName == "not-mine" }) {
				t.Errorf("not in the log before it is fetched: %+v", page.Entries)
			}
			end(f, b)
			advance(pollInterval)
			code, answer := pollFor(f, c.DeviceCode)
			if code != http.StatusOK {
				t.Fatalf("fetching: %d %v", code, answer)
			}
			if w := bearer(f, "GET", "/me", answer["access_token"]); w.Code != http.StatusUnauthorized {
				t.Errorf("what was fetched still works: %d", w.Code)
			}
		})
	}
}

// Whoever waits on purpose past the minute gets nothing.
func TestAnApprovedCodeNotFetchedInTime(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)
	c := askCode(t, f, "not-mine", "manage")
	approve(t, b, c.UserCode, a)
	advance(fetchWithin)
	if code, answer := pollFor(f, c.DeviceCode); code != http.StatusBadRequest || answer["error"] != "expired_token" {
		t.Fatalf("got %d %v", code, answer)
	}
}

// A code cannot be refused once it was approved, and refusing does not say it was.
func TestRefusingAnApprovedCodeSaysSo(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	clockOf(f)
	c := askCode(t, f, "laptop-1", "manage")
	approve(t, b, c.UserCode, a)
	if w := b.post("/device/deny", url.Values{"code": {c.UserCode}}); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "Nobody signed in") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

// A long post is cut, and shows only what a code could be.
func TestALongCodeIsCut(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	for _, typed := range []string{strings.Repeat("x", 100), strings.Repeat("x", 3*maxOAuthBody)} {
		w := b.post("/device", url.Values{"code": {typed}})
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), strings.Repeat("x", maxTypedCode+1)) {
			t.Errorf("%d characters: %d", len(typed), w.Code)
		}
	}
}

// Only a bearer counts, never a browser's cookie (R38).
func TestTheAPITakesOnlyABearer(t *testing.T) {
	f := setup(t)
	ada := f.browser(t)
	adas := f.register(t, ada, f.ada)
	bob := f.browser(t)
	f.register(t, bob, f.bob)

	for _, op := range apiOperations {
		method, path, _ := strings.Cut(op.route, " ")
		if w := ada.do(method, path, nil); w.Code != http.StatusUnauthorized && w.Code != http.StatusBadRequest {
			t.Errorf("%s with a session and no bearer: %d %s", op.route, w.Code, w.Body)
		}
	}
	if w := ada.do("GET", "/api/v1/me", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("/me with only a cookie: %d", w.Code)
	}

	// A bearer sent with someone else's cookie acts as the bearer's.
	token := signedInToken(t, f, ada, adas, "laptop-1", "manage")
	bob.header.Set("Authorization", "Bearer "+token)
	var me api.Me
	if err := json.Unmarshal(bob.ok("GET", "/api/v1/me", nil), &me); err != nil {
		t.Fatal(err)
	}
	if me.Person.Id != f.ada {
		t.Errorf("acted as %s", me.Person.Name)
	}

	// What is not a sign-in gets the same answer as what has ended, with nothing to tell apart.
	gone := bearer(f, "GET", "/me", "alt_"+strings.Repeat("x", 22))
	for _, value := range []string{"", "nonsense", "alp_abc", "alk_abc", token + "x", "Basic abc"} {
		w := bearer(f, "GET", "/me", value)
		if w.Code != http.StatusUnauthorized || w.Body.String() != gone.Body.String() {
			t.Errorf("%q: %d %s", value, w.Code, w.Body)
		}
	}
}

// Each credential reaches what it is for.
func TestCredentialsStayOnTheirOperations(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	token := signedInToken(t, f, b, a, "laptop-1", "manage")
	key, _, _ := aProbeOf(t, f)

	if w := bearer(f, "GET", "/me", key); w.Code != http.StatusUnauthorized {
		t.Errorf("a probe's key on /me: %d", w.Code)
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", token, nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("a sign-in on a probe's endpoint: %d", w.Code)
	}
	if w := probeRequest(f, "GET", "/api/v1/probe/checks", key, nil, nil); w.Code != http.StatusOK {
		t.Errorf("a probe on its own: %d %s", w.Code, w.Body)
	}
}

// A new operation cannot be served without saying who it is for, and the command
// line's client cannot drift from what is served.
func TestEveryOperationHasCredentials(t *testing.T) {
	var served []string
	strict := reflect.TypeFor[api.StrictServerInterface]()
	for i := range strict.NumMethod() {
		served = append(served, strict.Method(i).Name)
	}
	var listed []string
	for _, op := range apiOperations {
		listed = append(listed, op.name)
	}
	slices.Sort(served)
	slices.Sort(listed)
	if !slices.Equal(served, listed) {
		t.Fatalf("served %v, listed %v: add each to apiOperations with who it is for", served, listed)
	}

	f := setup(t)
	for _, op := range apiOperations {
		method, path, _ := strings.Cut(op.route, " ")
		if _, pattern := f.mux.Handler(httptest.NewRequest(method, site+path, nil)); pattern != op.route {
			t.Errorf("%s is at %q", op.route, pattern)
		}
		if op.takes == 0 {
			t.Errorf("%s takes no credential", op.name)
		}
	}

	var clients []string
	inClient := reflect.TypeFor[apiclient.ClientInterface]()
	for i := range inClient.NumMethod() {
		clients = append(clients, inClient.Method(i).Name)
	}
	var wanted []string
	for _, op := range apiOperations {
		if op.takes != probeCredential {
			wanted = append(wanted, op.name)
		}
	}
	slices.Sort(clients)
	slices.Sort(wanted)
	if !slices.Equal(clients, wanted) {
		t.Errorf("the client has %v, but the server serves %v apart from the probe's: internal/apiclient/oapi-codegen.yaml lists what internal/api's does", clients, wanted)
	}
}

func TestAReadOnlySignIn(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)

	c := askCode(t, f, "assistant", "read")
	shown := b.post("/device", url.Values{"code": {c.UserCode}}).Body.String()
	if !strings.Contains(shown, "Only read") {
		t.Errorf("the page does not say it only reads: %s", shown)
	}
	approve(t, b, c.UserCode, a)
	advance(pollInterval)
	_, answer := pollFor(f, c.DeviceCode)
	token := answer["access_token"]

	var me api.Me
	if err := json.Unmarshal(bearer(f, "GET", "/me", token).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(me.Scopes, api.Manage) || !slices.Contains(me.Scopes, api.Read) || *me.Person.WorkspaceAdmin || me.Projects[0].Role != api.Viewer {
		t.Fatalf("read-only reads as %+v", me)
	}
	// It can end itself, and that is all it can end.
	if w := bearer(f, "DELETE", "/me/token", token); w.Code != http.StatusNoContent {
		t.Fatalf("ending itself: %d %s", w.Code, w.Body)
	}
	if w := bearer(f, "GET", "/me", token); w.Code != http.StatusUnauthorized {
		t.Fatalf("after ending itself: %d", w.Code)
	}
}

func TestEndingABrowserFromElsewhere(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	here := f.browser(t)
	a := f.register(t, here, f.ada)
	there := f.browser(t)
	there.signIn(a)
	if there.get("/channels").Code != http.StatusOK {
		t.Fatal("not signed in there")
	}
	sessions, _ := f.st.BrowserSessions(ctx, f.ws, f.ada, time.Now())
	if len(sessions) != 2 {
		t.Fatalf("sessions: %+v", sessions)
	}
	page := here.get("/channels").Body.String()
	if !strings.Contains(page, "Sign out") || !strings.Contains(page, "/sessions/") {
		t.Fatalf("the page: %s", page)
	}

	// Someone else's cannot be reached, by what it is or by guessing.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	for _, s := range sessions {
		if w := bob.post("/sessions/"+s.ID+"/end", nil); w.Code != http.StatusNotFound {
			t.Fatalf("someone else's session: %d", w.Code)
		}
	}
	token := signedInToken(t, f, here, a, "laptop-1", "manage")
	tokens, _ := f.st.PersonTokens(ctx, f.ws, f.ada, time.Now())
	if w := bob.post("/sign-ins/"+tokens[0].ID+"/end", nil); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's sign-in: %d", w.Code)
	}
	if w := bearer(f, "GET", "/me", token); w.Code != http.StatusOK {
		t.Fatalf("it should still work: %d", w.Code)
	}

	// The other browser, which is the first listed that is not this one.
	end := ""
	for _, s := range sessions {
		w := here.post("/sessions/"+s.ID+"/end", nil)
		if w.Code == http.StatusSeeOther && there.get("/channels").Code != http.StatusOK {
			end = s.ID
			break
		}
		// Ended this browser: sign in again, and try the other.
		here.signIn(a)
	}
	if end == "" {
		t.Fatal("the other browser stayed signed in")
	}
}

func TestBrowserNames(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15": "Safari on macOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1":   "Safari on iPhone",
		"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0":                                                "Firefox on Linux",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/128.0 Safari/537.36 Edg/128.0":                     "Edge on Windows",
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/128.0 Mobile Safari/537.36":                                  "Chrome on Android",
		"curl/8.0": "A browser",
		"":         "A browser",
	} {
		if got := browserName(ua); got != want {
			t.Errorf("%q reads as %q, want %q", ua, got, want)
		}
	}
}

// No credential is ever in a log line, an entry or an error (R41).
func TestNoSecretInALogOrAnError(t *testing.T) {
	f := setup(t)
	var server bytes.Buffer
	f.srv.Log = slog.New(slog.NewTextHandler(&server, nil))
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)

	c := askCode(t, f, "laptop-1", "manage")
	secrets := []string{c.DeviceCode, normalUserCode(c.UserCode), c.UserCode}
	var answers []string
	keep := func(w *httptest.ResponseRecorder) { answers = append(answers, w.Body.String()) }

	keep(oauthPost(f, "/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrantType}, "device_code": {"wrong-" + c.DeviceCode}}))
	keep(oauthPost(f, "/oauth/token", url.Values{"client_id": {"wrong"}, "grant_type": {deviceGrantType}, "device_code": {c.DeviceCode}}))
	keep(oauthPost(f, "/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrantType}, "device_code": {c.DeviceCode}}))
	keep(oauthPost(f, "/oauth/token", url.Values{"client_id": {clientID}, "grant_type": {deviceGrantType}, "device_code": {c.DeviceCode}}))
	approve(t, b, c.UserCode, a)
	advance(pollInterval + pollSlower)
	_, answer := pollFor(f, c.DeviceCode)
	token := answer["access_token"]
	secrets = append(secrets, token)
	keep(bearer(f, "GET", "/me", token+"x"))
	keep(probeRequest(f, "GET", "/api/v1/probe/checks", token, nil, nil))
	keep(bearer(f, "DELETE", "/me/token", token))
	keep(bearer(f, "GET", "/me", token))

	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT details || about_name || about_id || by_name || by_id FROM log_entries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var entries strings.Builder
	for rows.Next() {
		var line string
		rows.Scan(&line)
		entries.WriteString(line)
	}
	if !strings.Contains(entries.String(), "laptop-1") {
		t.Fatal("the entries were not read")
	}
	for _, secret := range secrets {
		if strings.Contains(server.String(), secret) {
			t.Errorf("%q is in the server's log: %s", secret, server.String())
		}
		if strings.Contains(entries.String(), secret) {
			t.Errorf("%q is in the log", secret)
		}
		for _, a := range answers {
			if strings.Contains(a, secret) {
				t.Errorf("%q is in an answer: %s", secret, a)
			}
		}
	}
}

// The command line's client against the real thing.
func TestTheClientAgainstTheServer(t *testing.T) {
	f := setup(t)
	f.srv.Version = "1.2.3"
	b := f.browser(t)
	a := f.register(t, b, f.ada)
	advance := clockOf(f)
	srv := httptest.NewServer(f.mux)
	defer srv.Close()
	ctx := t.Context()

	anon, err := client.New(srv.URL, "", "1.2.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := anon.RequestDeviceCode(ctx, "laptop\x1b[31m-1", store.ScopeManage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.Poll(ctx, d); err != client.ErrPending {
		t.Fatalf("polling first: %v", err)
	}
	if w := approve(t, b, d.UserCode, a); w.Code != http.StatusOK {
		t.Fatalf("approving: %d %s", w.Code, w.Body)
	}
	if _, err := anon.Poll(ctx, d); err != client.ErrSlowDown {
		t.Fatalf("polling at once: %v", err)
	}
	advance(pollInterval + pollSlower)
	token, err := anon.Poll(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(srv.URL, token.AccessToken, "1.2.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	me, err := c.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if me.Person.Name != "Ada" || me.SignIn.Name != "laptop[31m-1" || me.ServerVersion != "1.2.3" {
		t.Fatalf("me: %+v", me)
	}
	if err := c.EndThisToken(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(ctx); err != client.ErrSignedOut {
		t.Fatalf("after ending it: %v", err)
	}
	if err := c.EndThisToken(ctx); err != client.ErrSignedOut {
		t.Fatalf("ending it twice: %v", err)
	}

	// An older server, without this: it says what to do.
	older, _ := client.New(srv.URL+"/nowhere", "", "1.2.3", nil)
	if _, err := older.RequestDeviceCode(ctx, "x", "read"); err == nil || !strings.Contains(err.Error(), "does not know this") {
		t.Fatalf("a server without sign-ins: %v", err)
	}
	if _, err := older.Me(ctx); err == nil || !strings.Contains(err.Error(), "1.2.3") {
		t.Fatalf("a server without /me: %v", err)
	}
}
