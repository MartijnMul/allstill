// SPDX-License-Identifier: AGPL-3.0-only

package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.215.14":            true,
		"2606:4700::6810:84e5":     true,
		"127.0.0.1":                false,
		"10.1.2.3":                 false,
		"172.16.0.1":               false,
		"192.168.1.1":              false,
		"169.254.169.254":          false, // cloud metadata
		"100.64.12.34":             false, // shared address space
		"0.0.0.0":                  false,
		"224.0.0.1":                false,
		"255.255.255.255":          false,
		"192.0.2.1":                false,
		"::1":                      false,
		"::":                       false,
		"fe80::1":                  false,
		"fd00::1":                  false,
		"::ffff:127.0.0.1":         false,
		"::ffff:10.0.0.1":          false,
		"64:ff9b::a00:1":           false, // NAT64 to 10.0.0.1
		"64:ff9b::5db8:d70e":       true,  // NAT64 to 93.184.215.14
		"64:ff9b::a00:1%1":         false,
		"2002:a00:1::%eth0":        false,
		"2606:4700::6810:84e5%1":   false,
		"::a00:1":                  false,
		"::ffff:0:a00:1":           false,
		"fec0::1":                  false,
		"2002:a00:1::":             false,
		"2001:db8::1":              false,
		"::ffff:93.184.215.14":     true,
		"2a00:1450:4001:80b::200e": true,
	} {
		if got := Allowed(netip.MustParseAddr(addr), nil); got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
	allow := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	if !Allowed(netip.MustParseAddr("10.1.2.3"), allow) || Allowed(netip.MustParseAddr("10.2.0.1"), allow) {
		t.Error("an allowed range is not followed")
	}
}

func TestClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/secret-path", nil)
	_, err := Do(context.Background(), Client(5*time.Second), req)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want refused", err)
	}
	if strings.Contains(err.Error(), "secret-path") {
		t.Errorf("the error holds the URL: %v", err)
	}
}

func TestClientFollowsNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/inside" {
			t.Error("the redirect was followed")
		}
		http.Redirect(w, r, "/inside", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := Client(5*time.Second, netip.MustParsePrefix("127.0.0.0/8"))
	req, _ := http.NewRequest("POST", srv.URL+"/push", nil)
	resp, err := Do(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status %d", resp.StatusCode)
	}
}
