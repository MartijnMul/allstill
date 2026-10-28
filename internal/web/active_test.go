// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

const secretURL = "https://shop.example.com/health?key=s3cr3t-t0k3n"

func (f fixture) httpCheck(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	admin, _ := f.st.AdminAccess(ctx, f.ws)
	c, err := f.st.CreateCheck(ctx, admin, f.project, store.NewCheck{
		Name: "shop", Kind: "http", Interval: time.Minute,
		Settings: map[string]any{"url": secretURL, "location": "https://www.example.com/s3cr3t-l0cat10n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-3 * time.Minute)
	for i, a := range []struct {
		o      check.Outcome
		reason string
		status float64
	}{{check.Passed, "", 301}, {check.Failed, "HTTP 503 instead of a redirect", 503}, {check.Failed, "HTTP 503 instead of a redirect", 503}} {
		end := at.Add(time.Duration(i) * time.Minute)
		if _, err := f.st.RecordAttempt(ctx, f.ws, c.ID, store.Attempt{
			Attempt: check.Attempt{Outcome: a.o, Reason: a.reason, At: end}, Started: end.Add(-120 * time.Millisecond),
			Measured: map[string]float64{"status": a.status, "ms": 120, "cert_days": 61.5},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

// An HTTP check shows its attempts, and its URL by role (R12): whole for
// editors and administrators, the scheme and host for viewers.
func TestAnHTTPCheck(t *testing.T) {
	f := setup(t)
	id := f.httpCheck(t)
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	page := string(bob.ok("GET", "/checks/"+id, nil))
	for _, want := range []string{"Recent attempts", "HTTP 503 instead of a redirect", "120 ms", "61 days", "https://shop.example.com", "https://www.example.com", "Down after", "2 failed attempts in a row", "editors see it here"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, secret := range []string{"s3cr3t", "/ping/"} {
		if strings.Contains(page, secret) {
			t.Errorf("a viewer sees %q", secret)
		}
	}
	ada := f.browser(t)
	f.register(t, ada, f.ada)
	page = string(ada.ok("GET", "/checks/"+id, nil))
	if !strings.Contains(page, "key=s3cr3t-t0k3n") || !strings.Contains(page, "s3cr3t-l0cat10n") || strings.Contains(page, "/ping/") {
		t.Error("an administrator does not see the URL whole, or sees a ping URL")
	}

	overview := string(bob.ok("GET", "/", nil))
	for _, want := range []string{"HTTP · shop.example.com", "503 · 120 ms · cert 61 days", "every 1 min", "shop is down", "HTTP 503 instead of a redirect"} {
		if !strings.Contains(overview, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
	if strings.Contains(overview, "s3cr3t") || strings.Contains(overview, "No ping") {
		t.Error("the overview shows a secret, or a missed ping for a check that is not pinged")
	}
}

// When the server cannot judge its checks, the overview says so first.
func TestOfflineForItsChecks(t *testing.T) {
	f := setup(t)
	f.srv.Offline = func() (time.Time, bool) { return time.Now().Add(-time.Minute), true }
	b := f.browser(t)
	f.register(t, b, f.bob)
	if page := string(b.ok("GET", "/", nil)); !strings.Contains(page, "Allstill is offline for its checks") {
		t.Error("the overview does not say the server is offline")
	}
}
