// SPDX-License-Identifier: AGPL-3.0-only

package outbound

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func client(follow bool, allow ...netip.Prefix) *http.Client {
	return &http.Client{Transport: Transport(nil, 5*time.Second, allow), Timeout: 5 * time.Second, CheckRedirect: Redirects(follow)}
}

// The server's own addresses stay refused, even when a range that holds them
// is allowed.
func TestTransportRefusesOwnAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := client(false, netip.MustParsePrefix("127.0.0.0/8")).Get(srv.URL)
	if !errors.Is(err, ErrOwn) {
		t.Fatalf("got %v, want the server's own address refused", err)
	}
	if !own(netip.MustParseAddr("::ffff:127.0.0.1")) || !own(netip.MustParseAddr("::")) || !own(netip.MustParseAddr("64:ff9b::7f00:1")) {
		t.Error("loopback or unspecified is not the server's own")
	}
}

// withoutOwn lets a test server on loopback stand for a public site.
func withoutOwn(t *testing.T) {
	saved := own
	own = func(netip.Addr) bool { return false }
	t.Cleanup(func() { own = saved })
}

// Every redirect hop is dialled through the same checks: a public site that
// redirects into a private range is refused there (ADR 0004, case 3).
func TestRedirectIntoPrivateRangeRefused(t *testing.T) {
	withoutOwn(t)
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.5/", http.StatusFound)
	}))
	defer public.Close()
	_, err := client(true, netip.MustParsePrefix("127.0.0.1/32")).Get(public.URL)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want the hop into 10.0.0.5 refused", err)
	}
}

func TestRedirectsFollowedOnlyWhenAsked(t *testing.T) {
	withoutOwn(t)
	hops := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loop":
			hops++
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/ftp":
			http.Redirect(w, r, "ftp://example.com/", http.StatusFound)
		case "/end":
		default:
			http.Redirect(w, r, "/end", http.StatusMovedPermanently)
		}
	}))
	defer srv.Close()
	allow := netip.MustParsePrefix("127.0.0.1/32")
	resp, err := client(false, allow).Get(srv.URL + "/start")
	if err != nil || resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("without follow: %v %v", resp, err)
	}
	resp.Body.Close()
	resp, err = client(true, allow).Get(srv.URL + "/start")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("with follow: %v %v", resp, err)
	}
	resp.Body.Close()
	if _, err := client(true, allow).Get(srv.URL + "/loop"); !errors.Is(err, ErrTooManyRedirects) || hops != MaxRedirects+1 {
		t.Errorf("a redirect loop: %v after %d hops", err, hops)
	}
	if _, err := client(true, allow).Get(srv.URL + "/ftp"); !errors.Is(err, ErrRedirectScheme) {
		t.Errorf("a redirect to ftp: %v", err)
	}
}

// A check's URL may hold a token, so where a redirect leads must not be told
// where it came from (ADR 0004). Go's client says so in Referer by itself.
func TestARedirectDoesNotTellWhereItCameFrom(t *testing.T) {
	withoutOwn(t)
	var referers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referers = append(referers, r.Header.Get("Referer"))
		switch r.URL.Path {
		case "/health":
			http.Redirect(w, r, "/next?token=s3cr3t", http.StatusFound)
		case "/next":
			http.Redirect(w, r, "/end", http.StatusFound)
		}
	}))
	defer srv.Close()
	resp, err := client(true, netip.MustParsePrefix("127.0.0.0/8")).Get(srv.URL + "/health?token=s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(referers) != 3 {
		t.Fatalf("%d requests", len(referers))
	}
	for _, r := range referers {
		if r != "" {
			t.Errorf("Referer %q", r)
		}
	}
}
