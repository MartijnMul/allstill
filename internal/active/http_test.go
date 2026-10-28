// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/outbound"
)

// token stands for a secret in a check's URL; it must never be in a reason.
const token = "s3cr3t-t0k3n"

// direct reaches a test server on loopback, which the outbound transport
// refuses as the server's own address.
func direct(conf *tls.Config, timeout time.Duration) http.RoundTripper {
	return &http.Transport{TLSClientConfig: conf, DisableKeepAlives: true, ResponseHeaderTimeout: timeout}
}

var runner = &Runner{UserAgent: UserAgent("1.2.3", ""), Transport: direct}

// noSecret fails the test when a reason holds any part of an address.
func noSecret(t *testing.T, reason string, parts ...string) {
	t.Helper()
	for _, p := range append(parts, token, "://", "127.0.0.1", "localhost") {
		if strings.Contains(reason, p) {
			t.Errorf("the reason %q holds %q", reason, p)
		}
	}
}

// Every reason an answer can fail for, in plain words, none with a URL.
func TestHTTPReasons(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "Allstill/1.2.3" {
			t.Errorf("User-Agent %q", r.UserAgent())
		}
		switch r.URL.Path {
		case "/ok":
		case "/503":
			w.WriteHeader(503)
		case "/429":
			w.WriteHeader(429)
		case "/challenge":
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(403)
		case "/moved":
			http.Redirect(w, r, "https://other.example.net/"+token, http.StatusMovedPermanently)
		case "/moved-here":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		case "/moved-home":
			http.Redirect(w, r, "https://example.com/", http.StatusMovedPermanently)
		case "/no-location":
			w.WriteHeader(http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop?"+token, http.StatusFound)
		case "/ftp":
			http.Redirect(w, r, "ftp://other.example.net/"+token, http.StatusFound)
		case "/degraded":
			fmt.Fprint(w, `{"status": "degraded", "checks": [{"db": true}]}`)
		case "/healthy":
			fmt.Fprint(w, `{"status": "ok", "checks": [{"db": true}], "a/b": 1}`)
		case "/html":
			fmt.Fprint(w, "<html>hello</html>")
		case "/large":
			fmt.Fprintf(w, `{"status": "%s"}`, strings.Repeat("x", MaxBody))
		case "/slow":
			time.Sleep(1500 * time.Millisecond)
		case "/close":
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		}
	}))
	defer srv.Close()
	at := func(path string) string { return srv.URL + path + "?key=" + token }
	pass := check.Passed
	for _, c := range []struct {
		name   string
		h      HTTP
		want   check.Outcome
		reason string
		unsure bool
	}{
		{"up", HTTP{URL: at("/ok")}, pass, "", false},
		{"HEAD", HTTP{URL: at("/ok"), Method: "HEAD"}, pass, "", false},
		{"5xx", HTTP{URL: at("/503")}, check.Failed, "HTTP 503", false},
		{"not the expected code", HTTP{URL: at("/ok"), Expect: []int{301}}, check.Failed, "HTTP 200 instead of 301", false},
		{"one of the codes", HTTP{URL: at("/503"), Expect: []int{200, 301, 302}}, check.Failed, "HTTP 503 instead of 200, 301 or 302", false},
		{"an expected 5xx", HTTP{URL: at("/503"), Expect: []int{503}}, pass, "", false},
		{"rate limited", HTTP{URL: at("/429")}, check.Failed, "rate limited (HTTP 429)", false},
		{"bot challenge", HTTP{URL: at("/challenge")}, check.Failed, "blocked by a bot challenge (Cloudflare)", false},
		{"a redirect not followed", HTTP{URL: at("/moved")}, check.Failed, "HTTP 301", false},
		{"a redirect expected", HTTP{URL: at("/moved-home"), Location: "https://EXAMPLE.com"}, pass, "", false},
		{"a redirect with its code", HTTP{URL: at("/moved-home"), Expect: []int{301}, Location: "https://example.com/"}, pass, "", false},
		{"a redirect elsewhere", HTTP{URL: at("/moved"), Location: "https://example.com/"}, check.Failed, "the redirect leads elsewhere than expected", false},
		{"no redirect", HTTP{URL: at("/ok"), Location: "https://example.com/"}, check.Failed, "HTTP 200 instead of a redirect", false},
		{"a redirect without Location", HTTP{URL: at("/no-location"), Location: "https://example.com/"}, check.Failed, "no Location in the redirect", false},
		{"redirects followed", HTTP{URL: at("/moved-here"), Follow: true}, pass, "", false},
		{"a redirect loop", HTTP{URL: at("/loop"), Follow: true}, check.Failed, "more than 5 redirects", false},
		{"a redirect to ftp", HTTP{URL: at("/ftp"), Follow: true}, check.Failed, "redirected to an address that is not http or https", false},
		{"a JSON field", HTTP{URL: at("/healthy"), JSON: &JSONField{"/status", "ok"}}, pass, "", false},
		{"a JSON field in an array", HTTP{URL: at("/healthy"), JSON: &JSONField{"/checks/0/db", "true"}}, pass, "", false},
		{"an escaped pointer", HTTP{URL: at("/healthy"), JSON: &JSONField{"/a~1b", "1"}}, pass, "", false},
		{"degraded", HTTP{URL: at("/degraded"), JSON: &JSONField{"/status", "ok"}}, check.Failed, "status is degraded instead of ok", false},
		{"no such field", HTTP{URL: at("/degraded"), JSON: &JSONField{"/state", "ok"}}, check.Failed, "no state in the answer", false},
		{"not JSON", HTTP{URL: at("/html"), JSON: &JSONField{"/status", "ok"}}, check.Failed, "the answer is not JSON", false},
		{"too large", HTTP{URL: at("/large"), JSON: &JSONField{"/status", "ok"}}, check.Failed, "the answer is larger than 1 MB", false},
		{"a JSON field after a bad code", HTTP{URL: at("/503"), JSON: &JSONField{"/status", "ok"}}, check.Failed, "HTTP 503", false},
		{"too slow", HTTP{URL: at("/slow"), Timeout: 1}, check.Failed, "no answer within 1 s", true},
		{"closed", HTTP{URL: at("/close")}, check.Failed, "the connection closed without an answer", false},
		{"not TLS", HTTP{URL: "https" + strings.TrimPrefix(at("/ok"), "http")}, check.Failed, "the server does not speak TLS", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runner.HTTP(t.Context(), c.h)
			if r.Outcome != c.want || !strings.HasPrefix(r.Reason, c.reason) || r.Unsure != c.unsure {
				t.Errorf("%s %q unsure %v, want %s %q unsure %v", r.Outcome, r.Reason, r.Unsure, c.want, c.reason, c.unsure)
			}
			if c.reason == "" && r.Reason != "" {
				t.Errorf("passed with the reason %q", r.Reason)
			}
			noSecret(t, r.Reason, "other", "example.com", "example.net")
			if r.Ended.Before(r.Started) || r.Measured["ms"] < 0 {
				t.Errorf("timing %v to %v, %v ms", r.Started, r.Ended, r.Measured["ms"])
			}
		})
	}
}

func TestHTTPMeasures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	r := runner.HTTP(t.Context(), HTTP{URL: srv.URL})
	if r.Outcome != check.Passed || r.Measured["status"] != 204 || r.Measured["ms"] < 20 {
		t.Errorf("%+v", r)
	}
	if _, ok := r.Measured["cert_days"]; ok {
		t.Error("certificate days over plain http")
	}
}

// Failures from the network, each in plain words, and whether the server's
// own network could be the cause (C4).
func TestExplain(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
	}
	for _, c := range []struct {
		err    error
		reason string
		unsure bool
	}{
		{op(syscall.ECONNREFUSED), "connection refused", false},
		{op(syscall.ECONNRESET), "connection reset", false},
		{op(syscall.EHOSTUNREACH), "no route to the address", true},
		{op(syscall.ENETUNREACH), "the network is unreachable", true},
		{&net.DNSError{Err: "no such host", Name: "shop.example.com", IsNotFound: true}, "the host name does not exist", false},
		{&net.DNSError{Err: "i/o timeout", Name: "shop.example.com", IsTimeout: true}, "the host name could not be looked up", true},
		{context.DeadlineExceeded, "no answer within 10 s", true},
		{io.ErrUnexpectedEOF, "the connection closed without an answer", false},
		{fmt.Errorf("dial: %w", outbound.ErrOwn), "the address is the server's own, which checks never reach", false},
		{fmt.Errorf("dial: %w: 10.0.0.5", outbound.ErrRefused), "the address is not on the public internet, and this server may not reach it", false},
		{errors.New("something else at https://shop.example.com/" + token), "the request failed", true},
	} {
		reason, unsure := explain(c.err, DefaultTimeout)
		if reason != c.reason || unsure != c.unsure {
			t.Errorf("%v: %q unsure %v, want %q unsure %v", c.err, reason, unsure, c.reason, c.unsure)
		}
		noSecret(t, reason, "shop", "10.0.0.5")
	}
}

// The outbound transport keeps checks off the server's own addresses, with a
// reason that names none.
func TestHTTPRefusesOwnAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the server's own address was reached")
	}))
	defer srv.Close()
	r := (&Runner{UserAgent: "Allstill/test"}).HTTP(t.Context(), HTTP{URL: srv.URL + "/" + token})
	if r.Outcome != check.Failed || r.Reason != "the address is the server's own, which checks never reach" || r.Unsure || !r.Local {
		t.Errorf("%+v", r)
	}
}

// A certificate made for a test: valid from notBefore to notAfter, for the
// names given, signed by itself as its own CA.
func certificateFor(t *testing.T, notBefore, notAfter time.Time, names ...string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: notBefore, NotAfter: notAfter, DNSNames: names,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:        true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func tlsServer(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshakes that fail on purpose
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// The thresholds of R4 and C5: a 90-day certificate warns from 14 days and
// fails from 3, a 6-day one warns under 2 days and fails under 14 hours.
func TestCertificateThresholds(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	off := 0
	ten := 10
	for _, c := range []struct {
		name         string
		life, left   time.Duration
		warn, fail   *int
		want         check.Outcome
		reason       string
		daysLeftOver float64
	}{
		{"90 days, 30 left", 90 * day, 30 * day, nil, nil, check.Passed, "", 29.9},
		{"90 days, 13 left", 90 * day, 13 * day, nil, nil, check.Warned, "certificate expires in 12 days", 12.9},
		{"90 days, 13 left, warnings off", 90 * day, 13 * day, &off, nil, check.Passed, "", 12.9},
		{"90 days, 13 left, warn at 10", 90 * day, 13 * day, &ten, nil, check.Passed, "", 12.9},
		{"90 days, 2 left", 90 * day, 2*day + time.Hour, nil, nil, check.Failed, "certificate expires in 2 days", 2},
		{"90 days, 2 left, failing off", 90 * day, 2*day + time.Hour, nil, &off, check.Warned, "certificate expires in 2 days", 2},
		{"6 days, 3 left", 6 * day, 3 * day, nil, nil, check.Passed, "", 2.9},
		{"6 days, 1.9 left", 6 * day, 46 * time.Hour, nil, nil, check.Warned, "certificate expires in 45 hours", 1.8},
		{"6 days, 13 hours left", 6 * day, 13 * time.Hour, nil, nil, check.Failed, "certificate expires in 12 hours", 0.5},
	} {
		t.Run(c.name, func(t *testing.T) {
			cert, ca := certificateFor(t, now.Add(c.left-c.life), now.Add(c.left), "localhost")
			srv := tlsServer(t, cert)
			r := runner.HTTP(t.Context(), HTTP{URL: srv.URL, CA: ca, WarnDays: c.warn, FailDays: c.fail})
			if r.Outcome != c.want || r.Reason != c.reason {
				t.Errorf("%s %q, want %s %q", r.Outcome, r.Reason, c.want, c.reason)
			}
			if days := r.Measured["cert_days"]; days < c.daysLeftOver || days > c.left.Hours()/24+0.01 {
				t.Errorf("%v days left, want about %v", days, c.left.Hours()/24)
			}
		})
	}
}

// A certificate that does not verify fails the attempt, unless verification
// is off; then its expiry is still read (spec case 12).
func TestCertificateVerification(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	good, ca := certificateFor(t, now.Add(-day), now.Add(89*day), "localhost")
	expired, expiredCA := certificateFor(t, now.Add(-91*day), now.Add(-day), "localhost")
	_, otherCA := certificateFor(t, now.Add(-day), now.Add(89*day), "localhost")
	ending, _ := certificateFor(t, now.Add(-80*day), now.Add(5*day), "localhost")
	srv, old, near := tlsServer(t, good), tlsServer(t, expired), tlsServer(t, ending)
	for _, c := range []struct {
		name   string
		h      HTTP
		want   check.Outcome
		reason string
	}{
		{"its CA", HTTP{URL: srv.URL, CA: ca}, check.Passed, ""},
		{"no CA", HTTP{URL: srv.URL}, check.Failed, "certificate not trusted: its issuer is unknown"},
		{"another CA", HTTP{URL: srv.URL, CA: otherCA}, check.Failed, "certificate not trusted: its issuer is unknown"},
		{"by its name instead of its address", HTTP{URL: strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), CA: ca}, check.Passed, ""},
		{"expired", HTTP{URL: old.URL, CA: expiredCA}, check.Failed, "certificate expired"},
		{"self-signed, not verified", HTTP{URL: srv.URL, NoVerify: true}, check.Passed, ""},
		{"self-signed and ending, not verified", HTTP{URL: near.URL, NoVerify: true}, check.Warned, "certificate expires in 4 days"},
		{"expired, not verified", HTTP{URL: old.URL, NoVerify: true}, check.Failed, "certificate expired"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runner.HTTP(t.Context(), c.h)
			if r.Outcome != c.want || r.Reason != c.reason {
				t.Errorf("%s %q, want %s %q", r.Outcome, r.Reason, c.want, c.reason)
			}
			noSecret(t, r.Reason)
		})
	}

	// A name the certificate does not hold.
	other, _ := certificateFor(t, now.Add(-day), now.Add(89*day), "example.com")
	tmpl, _ := x509.ParseCertificate(other.Certificate[0])
	tmpl.IPAddresses = nil
	key := other.PrivateKey.(*ecdsa.PrivateKey)
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	other.Certificate = [][]byte{der}
	otherNameCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	r := runner.HTTP(t.Context(), HTTP{URL: tlsServer(t, other).URL, CA: otherNameCA})
	if r.Outcome != check.Failed || r.Reason != "certificate is for another host name" {
		t.Errorf("another name: %s %q", r.Outcome, r.Reason)
	}
}

func TestValidate(t *testing.T) {
	zero, five, twenty := 0, 5, 20
	_, ca := certificateFor(t, time.Now(), time.Now().Add(time.Hour), "localhost")
	for _, c := range []struct {
		h     HTTP
		field string
	}{
		{HTTP{URL: "https://example.com/health?key=" + token}, ""},
		{HTTP{URL: "https://example.com", Method: "HEAD", Expect: []int{200, 405}, Timeout: 60, WarnDays: &zero, FailDays: &five, CA: ca, NoVerify: true}, ""},
		{HTTP{URL: "https://www.example.com", Location: "https://example.com/"}, ""},
		{HTTP{URL: ""}, "url"},
		{HTTP{URL: "ftp://example.com"}, "url"},
		{HTTP{URL: "https://"}, "url"},
		{HTTP{URL: "https://user:" + token + "@example.com"}, "url"},
		{HTTP{URL: "https://example.com/#top"}, "url"},
		{HTTP{URL: "https://example.com/%zz"}, "url"},
		{HTTP{URL: "https://example.com", Method: "POST"}, "method"},
		{HTTP{URL: "https://example.com", Expect: []int{700}}, "expected_status"},
		{HTTP{URL: "https://example.com", Timeout: 61}, "timeout"},
		{HTTP{URL: "https://example.com", WarnDays: &five, FailDays: &twenty}, "fail_days"},
		{HTTP{URL: "https://example.com", Method: "HEAD", JSON: &JSONField{"/status", "ok"}}, "json_field"},
		{HTTP{URL: "https://example.com", JSON: &JSONField{"status", "ok"}}, "json_field.pointer"},
		{HTTP{URL: "https://example.com", Location: "/elsewhere"}, "location"},
		{HTTP{URL: "https://example.com", Location: "https://example.com/", Follow: true}, "location"},
		{HTTP{URL: "https://example.com", CA: "not a certificate"}, "ca"},
		{HTTP{URL: "https://example.com", FailAfter: 101}, "fail_after"},
	} {
		err := c.h.Validate()
		var se *SettingError
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%+v: %v", c.h, err)
		case c.field != "" && (!errors.As(err, &se) || se.Field != c.field):
			t.Errorf("%+v: %v, want a problem with %s", c.h, err, c.field)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("the error %q holds the URL's secret", err)
		}
	}
}

func TestShownAndHost(t *testing.T) {
	if s := Shown("https://Shop.example.com:8443/health?key=" + token); s != "https://Shop.example.com:8443" {
		t.Errorf("shown %q", s)
	}
	for raw, want := range map[string]string{
		"https://Shop.example.com/x": "shop.example.com 443",
		"http://shop.example.com/":   "shop.example.com 80",
		"https://[2001:db8::1]:8443": "2001:db8::1 8443",
	} {
		h, p := HTTP{URL: raw}.Host()
		if h+" "+p != want {
			t.Errorf("%s: %s %s", raw, h, p)
		}
	}
	if ua := UserAgent("v1.2.3", "ops@example.com"); ua != "Allstill/1.2.3 (ops@example.com)" {
		t.Errorf("User-Agent %q", ua)
	}
	if CheckContact("ops@example.com") != nil || CheckContact("ops@example.com\r\nX-Evil: 1") == nil {
		t.Error("a contact is not checked as a header can carry it")
	}
}
