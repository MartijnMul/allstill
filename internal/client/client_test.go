// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestParseServer(t *testing.T) {
	for raw, want := range map[string]string{
		"https://allstill.example.com":   "https://allstill.example.com",
		"https://allstill.example.com/":  "https://allstill.example.com",
		"HTTPS://Allstill.Example.COM":   "https://allstill.example.com",
		"http://localhost:8000":          "http://localhost:8000",
		"http://127.0.0.1:8000":          "http://127.0.0.1:8000",
		"http://[::1]:8000":              "http://[::1]:8000",
		"  https://allstill.example.com": "https://allstill.example.com",
	} {
		got, err := ParseServer(raw)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, says := range map[string]string{
		"http://allstill.lan":              "unprotected",
		"http://allstill.example.com:8000": "unprotected",
		"http://localhost.example.com":     "unprotected",
		"http://127.0.0.1.example.com":     "unprotected",
		"allstill.example.com":             "not an address",
		"ftp://allstill.example.com":       "not an address",
		"":                                 "not an address",
		"https://allstill.example.com/a":   "more in it",
		"https://allstill.example.com?a=b": "more in it",
		"https://user:p@example.com":       "more in it",
		"https://allstill.example.com#a":   "more in it",
		"https://allstill.example.com?":    "more in it",
		"https://allstill.example.com#":    "more in it",
	} {
		if _, err := ParseServer(raw); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want it to say %q", raw, err, says)
		}
	}
}

func TestCleanLeavesNothingThatSteersATerminal(t *testing.T) {
	for raw, want := range map[string]string{
		"plain name":            "plain name",
		"Café ✓ 日本語":            "Café ✓ 日本語",
		"\x1b[2J\x1b]0;pwned\a": "[2J]0;pwned",
		"line\nbreak\r\ttab":    "linebreaktab",
		"turn\u202earound":      "turnaround",
		"zero\u200bwidth":       "zerowidth",
		"bad\xffbyte":           "badbyte",
		"del\x7f and \u0085":    "del and ",
		"\ue000private":         "private",
	} {
		if got := Clean(raw); got != want {
			t.Errorf("%q cleans to %q, want %q", raw, got, want)
		}
	}
}

func TestCredentialsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allstill", "credentials.json")
	c, err := LoadCredentials(path)
	if err != nil || c.Current != "" || c.Servers == nil {
		t.Fatalf("no file yet: %+v, %v", c, err)
	}
	c.Current = "https://a.example.com"
	c.Servers["https://a.example.com"] = Credential{Token: "alt_a"}
	c.Servers["https://b.example.com"] = Credential{Token: "alt_b"}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", info.Mode().Perm())
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %o", dir.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	got, err := LoadCredentials(path)
	if err != nil || got.Current != c.Current || got.Servers["https://b.example.com"].Token != "alt_b" {
		t.Fatalf("read back %+v, %v", got, err)
	}

	// One others can read is refused, and says so without its contents.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil || !strings.Contains(err.Error(), "others can read") || strings.Contains(err.Error(), "alt_") {
		t.Fatalf("a looser file: %v", err)
	}
	os.Chmod(path, 0o600)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("a broken file: %v", err)
	}
}

// A credential goes nowhere else, also when a server says so (R9).
func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("the token went elsewhere")
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/me", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "alt_secret", "1.2.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Me(t.Context())
	if err == nil || !strings.Contains(err.Error(), "another address") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "alt_secret") {
		t.Errorf("the message: %q", err)
	}
	if err := c.EndThisToken(t.Context()); err == nil || !strings.Contains(err.Error(), "another address") {
		t.Errorf("ending: %v", err)
	}
	if _, err := c.RequestDeviceCode(t.Context(), "x", "read"); err == nil || !strings.Contains(err.Error(), "another address") {
		t.Errorf("asking for a code: %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("%d requests went elsewhere", elsewhere.Load())
	}
}

// What a server says goes through Clean, and a token sent is not repeated.
func TestARefusalIsCleanAndHoldsNoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"status":403,"title":"No\u001b[31m way.","detail":"Ask\u202e someone."}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "alt_secret", "1.2.3", nil)
	_, err := c.Me(t.Context())
	if err == nil || err.Error() != "No[31m way. Ask someone." {
		t.Fatalf("got %q", err)
	}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gone.Close()
	c, _ = New(gone.URL, "alt_secret", "1.2.3", nil)
	if _, err := c.Me(t.Context()); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("a 401: %v", err)
	}
	// Nothing listening: nothing about the token here either.
	gone.Close()
	if _, err := c.Me(t.Context()); err == nil || !strings.Contains(err.Error(), "cannot reach") || strings.Contains(err.Error(), "alt_secret") {
		t.Fatalf("no server: %v", err)
	}
}

// The other version says which, where it can tell (R45).
func TestDifferentVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"kind":"person","scopes":["read"],"projects":[],"server_version":"1.0.0"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "alt_x", "2.0.0", nil)
	err := c.EndThisToken(t.Context())
	if err == nil || !strings.Contains(err.Error(), "1.0.0") || !strings.Contains(err.Error(), "2.0.0") {
		t.Fatalf("got %v", err)
	}
}

// answers replies to each poll in turn, in time that passes only by waiting.
type answers struct {
	t     *testing.T
	polls []string
	at    []time.Time
}

func (a *answers) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/oauth/token") {
		a.t.Errorf("asked for %s", r.URL.Path)
	}
	a.at = append(a.at, time.Now())
	next := a.polls[0]
	if len(a.polls) > 1 {
		a.polls = a.polls[1:]
	}
	if next == "network" {
		return nil, errors.New("no route to host")
	}
	code := http.StatusBadRequest
	if strings.Contains(next, "access_token") {
		code = http.StatusOK
	}
	if next == "status502" {
		code = http.StatusBadGateway
	}
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(next)), Request: r}, nil
}

func waitWith(t *testing.T, polls []string, check func(t *testing.T, start time.Time, a *answers, token Token, err error)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		a := &answers{t: t, polls: polls}
		c, err := New("https://allstill.example.com", "", "1.2.3", a)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		token, err := c.WaitForToken(t.Context(), DeviceCode{DeviceCode: "dc", UserCode: "WDJB-MJHT", ExpiresIn: 600, Interval: 5})
		check(t, start, a, token, err)
	})
}

const pending = `{"error":"authorization_pending"}`

func TestWaitingForAToken(t *testing.T) {
	waitWith(t, []string{pending, pending, `{"access_token":"alt_x","token_type":"Bearer","scope":"read"}`}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err != nil || token.AccessToken != "alt_x" || token.Scope != "read" {
			t.Fatalf("%+v, %v", token, err)
		}
		if got := time.Since(start); got != 15*time.Second {
			t.Errorf("took %v, at 5 seconds a poll", got)
		}
	})
}

func TestSlowDownWaitsLonger(t *testing.T) {
	waitWith(t, []string{`{"error":"slow_down"}`, `{"error":"slow_down"}`, `{"access_token":"alt_x"}`}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err != nil {
			t.Fatal(err)
		}
		// 5, then 10, then 15: five more each time, as RFC 8628 says.
		if got := time.Since(start); got != 30*time.Second {
			t.Errorf("took %v", got)
		}
	})
}

func TestWaitingEnds(t *testing.T) {
	for answer, says := range map[string]string{
		`{"error":"access_denied"}`:                          "refused",
		`{"error":"expired_token"}`:                          "expired",
		`{"error":"invalid_grant"}`:                          "no longer knows",
		`{"error":"server_error"}`:                           "status 400",
		`{"error":"odd","error_description":"Plain words."}`: "Plain words.",
		`not json`: "status 400",
	} {
		waitWith(t, []string{answer}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
			if err == nil || !strings.Contains(err.Error(), says) {
				t.Errorf("%s: %v, want it to say %q", answer, err, says)
			}
			if len(a.at) != 1 {
				t.Errorf("%s: polled %d times", answer, len(a.at))
			}
		})
	}
}

// It gives up when the code does, whatever the server says.
func TestACodeThatIsNeverApprovedExpires(t *testing.T) {
	waitWith(t, []string{pending}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("got %v", err)
		}
		if got := time.Since(start); got != 600*time.Second {
			t.Errorf("took %v", got)
		}
	})
}

// A network that comes and goes does not end it.
func TestANetworkThatComesBack(t *testing.T) {
	waitWith(t, []string{"network", pending, "network", `{"access_token":"alt_x"}`}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err != nil || token.AccessToken != "alt_x" {
			t.Fatalf("%+v, %v", token, err)
		}
	})
	// And nor does a proxy having a bad moment.
	waitWith(t, []string{"status502", `{"access_token":"alt_x"}`}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err != nil || token.AccessToken != "alt_x" {
			t.Fatalf("after a 502: %+v, %v", token, err)
		}
	})
	waitWith(t, []string{"network"}, func(t *testing.T, start time.Time, a *answers, token Token, err error) {
		if err == nil || !strings.Contains(err.Error(), "cannot reach") {
			t.Fatalf("a network that stays away: %v", err)
		}
	})
}

// Numbers from a server cannot make the wait spin or last for ever.
func TestAServersNumbersAreKeptInBounds(t *testing.T) {
	for _, body := range []string{
		`{"device_code":"d","user_code":"U","verification_uri":"https://a.example.com/device","expires_in":99999999999999,"interval":99999999999999}`,
		`{"device_code":"d","user_code":"U","verification_uri":"https://a.example.com/device","expires_in":-1,"interval":0}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c, _ := New(srv.URL, "", "1.2.3", nil)
		d, err := c.RequestDeviceCode(t.Context(), "x", "read")
		srv.Close()
		if err != nil || d.ExpiresIn != 600 || d.Interval != 5 {
			t.Errorf("%s: %+v, %v", body, d, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"device_code":"d","user_code":"U","verification_uri":"javascript:alert(1)","expires_in":60,"interval":5}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "", "1.2.3", nil)
	if _, err := c.RequestDeviceCode(t.Context(), "x", "read"); err == nil {
		t.Error("an address that is none was taken")
	}
}
