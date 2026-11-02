// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func asked(t *testing.T, g *grants) *grant {
	t.Helper()
	gr, ok := g.ask("laptop-1", "manage", t0)
	if !ok {
		t.Fatal("refused")
	}
	return gr
}

func TestUserCodes(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		c := newUserCode()
		if len(c) != userCodeLength || strings.ContainsAny(c, "AEIOUY0123456789") {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 990 {
		t.Fatalf("only %d different codes", len(seen))
	}
	if got := showUserCode("WDJBMJHT"); got != "WDJB-MJHT" {
		t.Errorf("shown as %q", got)
	}
	for _, typed := range []string{"WDJB-MJHT", "wdjbmjht", " wdjb mjht\n", "Wdjb-Mjht"} {
		if got := normalUserCode(typed); got != "WDJBMJHT" {
			t.Errorf("%q reads as %q", typed, got)
		}
	}
}

// RFC 8628's answers, in the order someone meets them.
func TestAGrantFromPendingToRedeemed(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	if len(gr.deviceCode) < 22 || gr.deviceCode == gr.id {
		t.Fatalf("device code %q, id %q", gr.deviceCode, gr.id)
	}
	at := t0
	poll := func(want error) grant {
		t.Helper()
		at = at.Add(pollInterval)
		got, err := g.poll(gr.deviceCode, at)
		if !errors.Is(err, want) {
			t.Fatalf("poll: %v, want %v", err, want)
		}
		return got
	}
	poll(errPending)
	if _, err := g.poll("no-such-code", at); !errors.Is(err, errBadGrant) {
		t.Fatalf("an unknown code: %v", err)
	}
	shown, ok := g.waiting(showUserCode(gr.userCode), at)
	if !ok || shown.id != gr.id || shown.name != "laptop-1" {
		t.Fatalf("waiting: %+v, %v", shown, ok)
	}
	if !g.claim(gr.id, at) {
		t.Fatal("not claimed")
	}
	if _, ok := g.waiting(gr.userCode, at); ok {
		t.Fatal("a claimed code can still be entered")
	}
	if g.claim(gr.id, at) {
		t.Fatal("claimed twice")
	}
	if g.deny(gr.userCode, at) {
		t.Fatal("a claimed code was refused")
	}
	poll(errPending)
	g.approved(gr.id, "alt_x", at)
	got := poll(nil)
	if got.token != "alt_x" || got.scope != "manage" {
		t.Fatalf("handed over %+v", got)
	}
	poll(errBadGrant)
}

// A token that could not be made lets someone answer again.
func TestAGrantAfterAFailedApproval(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	g.claim(gr.id, t0)
	g.unclaim(gr.id)
	if _, ok := g.waiting(gr.userCode, t0); !ok {
		t.Fatal("it cannot be entered again")
	}
	if !g.claim(gr.id, t0) {
		t.Fatal("it cannot be claimed again")
	}
}

func TestADeviceCodeRedeemsOnceAtOneMoment(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	g.claim(gr.id, t0)
	g.approved(gr.id, "alt_x", t0)
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := g.poll(gr.deviceCode, t0); err == nil {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("redeemed %d times", won.Load())
	}
	if g.byCode[gr.deviceCode].token != "" {
		t.Error("the token stayed in memory after it was fetched")
	}
}

// Someone who gets a code approved and waits on purpose gets nothing, as a real
// command line polls every few seconds.
func TestAnApprovedTokenIsFetchedSoonOrNever(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	g.claim(gr.id, t0)
	g.approved(gr.id, "alt_x", t0)
	if _, err := g.poll(gr.deviceCode, t0.Add(fetchWithin)); !errors.Is(err, errExpired) {
		t.Fatalf("a minute later: %v", err)
	}
	if g.byCode[gr.deviceCode].token != "" {
		t.Error("the token stayed in memory")
	}
	// Approved near its end, it keeps the end it had.
	h := newGrants()
	two := asked(t, h)
	at := t0.Add(grantLifetime - time.Second)
	h.claim(two.id, at)
	h.approved(two.id, "alt_y", at)
	if _, err := h.poll(two.deviceCode, t0.Add(grantLifetime)); !errors.Is(err, errExpired) {
		t.Fatalf("past its first end: %v", err)
	}
}

func TestPollingTooFast(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	if _, err := g.poll(gr.deviceCode, t0); !errors.Is(err, errPending) {
		t.Fatalf("first: %v", err)
	}
	if _, err := g.poll(gr.deviceCode, t0.Add(time.Second)); !errors.Is(err, errSlowDown) {
		t.Fatalf("after a second: %v", err)
	}
	// Each slow_down adds five seconds (RFC 8628), so five is no longer enough.
	at := t0.Add(time.Second).Add(pollInterval)
	if _, err := g.poll(gr.deviceCode, at); !errors.Is(err, errSlowDown) {
		t.Fatalf("at the old pace: %v", err)
	}
	at = at.Add(pollInterval + 2*pollSlower)
	if _, err := g.poll(gr.deviceCode, at); !errors.Is(err, errPending) {
		t.Fatalf("at the new pace: %v", err)
	}
	// A poll a little early after a wait of the right length is fine.
	h := newGrants()
	two, _ := h.ask("x", "read", t0)
	h.poll(two.deviceCode, t0)
	if _, err := h.poll(two.deviceCode, t0.Add(pollInterval-pollMargin)); !errors.Is(err, errPending) {
		t.Fatalf("a little early: %v", err)
	}
}

func TestAGrantExpires(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	end := t0.Add(grantLifetime)
	if _, ok := g.waiting(gr.userCode, end.Add(-time.Second)); !ok {
		t.Fatal("not there just before its end")
	}
	if _, ok := g.waiting(gr.userCode, end); ok {
		t.Fatal("still there at its end")
	}
	if g.claim(gr.id, end) {
		t.Fatal("approved at its end")
	}
	if g.deny(gr.userCode, end) {
		t.Fatal("denied at its end")
	}
	if _, err := g.poll(gr.deviceCode, end); !errors.Is(err, errExpired) {
		t.Fatalf("at its end: %v", err)
	}
	// Then it is forgotten, and the memory it held is free.
	if _, err := g.poll(gr.deviceCode, end.Add(grantKept)); !errors.Is(err, errBadGrant) {
		t.Fatalf("a while later: %v", err)
	}
	if len(g.byCode) != 0 || len(g.byUser) != 0 {
		t.Fatalf("still held: %d, %d", len(g.byCode), len(g.byUser))
	}
}

func TestADeniedGrant(t *testing.T) {
	g := newGrants()
	gr := asked(t, g)
	if !g.deny(gr.userCode, t0) {
		t.Fatal("not denied")
	}
	if _, err := g.poll(gr.deviceCode, t0); !errors.Is(err, errDenied) {
		t.Fatalf("poll: %v", err)
	}
	if g.claim(gr.id, t0) {
		t.Fatal("a denied grant was approved")
	}
	if g.deny(gr.userCode, t0) {
		t.Fatal("denied twice")
	}
}

// Asking is limited over the whole installation, which bounds what is held.
func TestAskingIsLimited(t *testing.T) {
	g := newGrants()
	for range asksPerMinute {
		if _, ok := g.ask("x", "read", t0); !ok {
			t.Fatal("refused too soon")
		}
	}
	if _, ok := g.ask("x", "read", t0); ok {
		t.Fatal("one too many was handed out")
	}
	if _, ok := g.ask("x", "read", t0.Add(time.Minute)); !ok {
		t.Fatal("still refused a minute later")
	}
}
