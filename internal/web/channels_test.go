// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/webpush"
)

func subscriptionJSON(endpoint string) map[string]any {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	b64 := base64.RawURLEncoding
	return map[string]any{
		"name":     "iPhone, Safari",
		"endpoint": endpoint,
		"keys":     map[string]string{"p256dh": b64.EncodeToString(ua.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)},
	}
}

func TestADeviceIsAddedTestedAndConfirmed(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	key, err := f.st.VAPIDKey(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.PushKey = key.PublicKey()
	woken := 0
	f.srv.Wake = func() { woken++ }
	b := f.browser(t)
	f.register(t, b, f.ada)
	page := string(b.ok("GET", "/channels", nil))
	if !strings.Contains(page, "No devices yet") || !strings.Contains(page, `name="allstill-push-key" content="`+key.PublicKey()+`"`) {
		t.Fatalf("devices page:\n%s", page)
	}

	const endpoint = "https://push.example.net/push/very-secret-token"
	sub := subscriptionJSON(endpoint)
	var added struct {
		ID        string
		Confirmed bool
	}
	json.Unmarshal(b.ok("POST", "/channels/push", sub), &added)
	if added.ID == "" || added.Confirmed {
		t.Fatalf("added %+v", added)
	}
	// Sending the same subscription again, as every page does, keeps it.
	var again struct{ ID string }
	json.Unmarshal(b.ok("POST", "/channels/push", sub), &again)
	if again.ID != added.ID {
		t.Fatalf("the same subscription made a second channel: %s, %s", added.ID, again.ID)
	}
	page = string(b.ok("GET", "/channels", nil))
	if !strings.Contains(page, "iPhone, Safari") || !strings.Contains(page, "Not confirmed") {
		t.Fatalf("devices page:\n%s", page)
	}
	if strings.Contains(page, "very-secret-token") {
		t.Fatal("the page shows the endpoint")
	}

	if w := b.do("POST", "/channels/"+added.ID+"/test", nil); w.Code != http.StatusSeeOther || woken != 1 {
		t.Fatalf("test: %d, woken %d", w.Code, woken)
	}
	if w := b.do("GET", "/channels/"+added.ID+"/confirm?token=guessed", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a wrong token: %d", w.Code)
	}
	// The sender puts a token in the test message; opening it confirms.
	auth, _ := base64.RawURLEncoding.DecodeString(sub["keys"].(map[string]string)["auth"])
	if err := f.st.SetConfirm(ctx, store.Pending{ChannelID: added.ID, PersonID: f.ada, Sub: webpush.Subscription{Auth: auth}}, "the-token"); err != nil {
		t.Fatal(err)
	}
	w := b.do("GET", "/channels/"+added.ID+"/confirm?token=the-token", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/channels?confirmed" {
		t.Fatalf("confirm: %d %v", w.Code, w.Header())
	}
	if page := string(b.ok("GET", "/channels?confirmed", nil)); !strings.Contains(page, "Confirmed: alerts can reach") || !strings.Contains(page, "Confirmed ") {
		t.Fatalf("devices page:\n%s", page)
	}
	targets, _ := f.st.PushTargets(ctx, f.ada, key.PublicKey())
	if len(targets) != 1 || targets[0].Sub.Endpoint != endpoint {
		t.Fatalf("targets %+v", targets)
	}

	// Someone else cannot test, confirm or remove it.
	bob := f.browser(t)
	f.register(t, bob, f.bob)
	for _, path := range []string{"/channels/" + added.ID + "/test", "/channels/" + added.ID + "/delete"} {
		if w := bob.do("POST", path, nil); w.Code != http.StatusNotFound {
			t.Errorf("Bob %s: %d", path, w.Code)
		}
	}
	if w := bob.do("GET", "/channels/"+added.ID+"/confirm?token=the-token", nil); w.Code != http.StatusNotFound {
		t.Errorf("Bob confirms: %d", w.Code)
	}
	// The device changes hands: it moves to Bob, and must be confirmed again.
	bob.ok("POST", "/channels/push", sub)
	if cs, _ := f.st.Channels(ctx, f.ada); len(cs) != 0 {
		t.Fatalf("Ada still has %+v", cs)
	}
	if cs, _ := f.st.Channels(ctx, f.bob); len(cs) != 1 || !cs[0].VerifiedAt.IsZero() {
		t.Fatalf("Bob has %+v", cs)
	}
	if w := bob.do("POST", "/channels/"+added.ID+"/delete", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", w.Code)
	}
	if cs, _ := f.st.Channels(ctx, f.bob); len(cs) != 0 {
		t.Fatalf("Bob still has %+v", cs)
	}
}

func TestABadSubscriptionIsRefused(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	f.register(t, b, f.ada)
	for _, endpoint := range []string{"http://push.example.net/x", "https://[fe80::1%251]/x", "not a url"} {
		if w := b.do("POST", "/channels/push", subscriptionJSON(endpoint)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", endpoint, w.Code)
		}
	}
	if w := f.browser(t).do("POST", "/channels/push", subscriptionJSON("https://push.example.net/x")); w.Code == http.StatusOK {
		t.Error("a subscription without signing in was taken")
	}
}

func TestTheServiceWorker(t *testing.T) {
	f := setup(t)
	w := f.browser(t).do("GET", "/sw.js", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || !strings.Contains(w.Body.String(), "showNotification") {
		t.Fatalf("sw.js: %d %v", w.Code, w.Header())
	}
	csp := f.browser(t).do("GET", "/login", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "worker-src 'self'") || !strings.Contains(csp, "manifest-src 'self'") {
		t.Fatalf("policy %s", csp)
	}
	if w := f.browser(t).do("GET", "/static/manifest.webmanifest", nil); w.Code != http.StatusOK {
		t.Fatalf("manifest: %d", w.Code)
	}
}

// A browser still subscribed for an old key is shown as such, and never
// pushed to (ADR 0003).
func TestASubscriptionForAnOldKey(t *testing.T) {
	f := setup(t)
	key, _ := f.st.VAPIDKey(t.Context(), f.ws)
	f.srv.PushKey = key.PublicKey()
	b := f.browser(t)
	f.register(t, b, f.ada)
	sub := subscriptionJSON("https://push.example.net/push/old")
	sub["key"] = strings.Repeat("A", len(key.PublicKey()))
	b.ok("POST", "/channels/push", sub)
	if page := string(b.ok("GET", "/channels", nil)); !strings.Contains(page, "Must subscribe again") {
		t.Fatalf("devices page:\n%s", page)
	}
}

func TestHealth(t *testing.T) {
	f := setup(t)
	answer := func() (int, string) {
		w := f.browser(t).do("GET", "/health", nil)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	f.srv.Behind = func(context.Context) (time.Duration, error) { return 30 * time.Second, nil }
	if code, body := answer(); code != http.StatusOK || body != `{"status":"ok","sender_behind":30}` {
		t.Errorf("keeping up: %d %s", code, body)
	}
	f.srv.Behind = func(context.Context) (time.Duration, error) { return 5 * time.Minute, nil }
	if code, body := answer(); code != http.StatusServiceUnavailable || body != `{"status":"behind","sender_behind":300}` {
		t.Errorf("behind: %d %s", code, body)
	}
	f.srv.Behind = func(context.Context) (time.Duration, error) { return 0, errors.New("disk I/O error") }
	if code, body := answer(); code != http.StatusServiceUnavailable || body != `{"status":"error","sender_behind":0}` {
		t.Errorf("an error: %d %s", code, body)
	}
}

// What may keep alerts from someone shows first on the overview (R11).
func TestAlertProblemsNeedAttention(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	key, _ := f.st.VAPIDKey(ctx, f.ws)
	f.srv.PushKey = key.PublicKey()
	b := f.browser(t)
	f.register(t, b, f.ada)
	// A message left out because it was no longer true did not fail.
	if err := f.st.Record(ctx, &store.Delivery{WorkspaceID: f.ws, PersonID: f.ada, Kind: "alert", State: "failed",
		Error: store.NoLongerTrue, At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if page := string(b.ok("GET", "/", nil)); strings.Contains(page, "Alerts may not reach you") {
		t.Fatal("alert problems before there are any")
	}

	f.srv.Behind = func(context.Context) (time.Duration, error) { return 5 * time.Minute, nil }
	if err := f.st.Record(ctx, &store.Delivery{WorkspaceID: f.ws, PersonID: f.ada, Kind: "alert", State: "failed",
		Error: "no confirmed device to reach this person", At: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	sub := subscriptionJSON("https://push.example.net/push/old")
	sub["key"] = strings.Repeat("A", len(key.PublicKey()))
	b.ok("POST", "/channels/push", sub)
	// A project where nobody gets alerts: Bob and Ada only view it, and as a
	// member Ada follows it through that membership, not as an administrator.
	lab, _, _ := f.st.CreateProject(ctx, f.ws, "Lab")
	f.st.SetMember(ctx, f.ws, lab, f.bob, store.Viewer)
	f.st.SetMember(ctx, f.ws, lab, f.ada, store.Viewer)
	bob := f.browser(t)
	f.register(t, bob, f.bob)

	page := string(b.ok("GET", "/", nil))
	for _, want := range []string{"Alerts may not reach you", "Alerts are 5 min behind", "1 message to you failed", "iPhone, Safari must subscribe again"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview without %q:\n%s", want, page)
		}
	}
	if page := string(bob.ok("GET", "/", nil)); !strings.Contains(page, "Nobody would be told about Lab") {
		t.Errorf("Bob's overview:\n%s", page)
	}
}
