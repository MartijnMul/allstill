// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
)

var tcpRunner = &Runner{Dial: (&net.Dialer{}).DialContext}

// tcpAt is a TCP check of a listener on loopback.
func tcpAt(t *testing.T, ln net.Listener) TCP {
	t.Helper()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return TCP{Host: host, Port: n}
}

// serve accepts connections until the test ends, handing each to handle.
func serve(t *testing.T, ln net.Listener, handle func(net.Conn)) {
	t.Helper()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestTCPPassesWhenThePortAccepts(t *testing.T) {
	ln := listen(t)
	serve(t, ln, func(net.Conn) {})
	r := tcpRunner.TCP(t.Context(), tcpAt(t, ln))
	if r.Outcome != check.Passed || r.Reason != "" {
		t.Fatalf("%s: %s", r.Outcome, r.Reason)
	}
	if _, ok := r.Measured["ms"]; !ok || r.Ended.Before(r.Started) {
		t.Errorf("measured %v, %v to %v", r.Measured, r.Started, r.Ended)
	}
	if _, ok := r.Measured["cert_days"]; ok {
		t.Error("a port without TLS has no certificate")
	}
}

func TestTCPFailsWhenThePortIsClosed(t *testing.T) {
	ln := listen(t)
	c := tcpAt(t, ln)
	ln.Close()
	r := tcpRunner.TCP(t.Context(), c)
	if r.Outcome != check.Failed || r.Reason != "connection refused" || r.Unsure {
		t.Fatalf("%+v", r)
	}
	noSecret(t, r.Reason)
}

// A timeout could be the server's own network, so the canary decides.
func TestTCPTimeoutIsUnsure(t *testing.T) {
	blocked := &Runner{Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	r := blocked.TCP(t.Context(), TCP{Host: "mail.example.com", Port: 993, Timeout: 1})
	if r.Outcome != check.Failed || r.Reason != "no answer within 1 s" || !r.Unsure {
		t.Fatalf("%+v", r)
	}
	noSecret(t, r.Reason, "mail.example.com")
}

// Without the replaced dialer it goes through the outbound rules (ADR 0004).
func TestTCPRefusesOwnAddress(t *testing.T) {
	ln := listen(t)
	serve(t, ln, func(net.Conn) {})
	r := (&Runner{}).TCP(t.Context(), tcpAt(t, ln))
	if r.Outcome != check.Failed || !r.Local || r.Reason != "the address is the server's own, which checks never reach" {
		t.Fatalf("%+v", r)
	}
}

func TestTCPRefusesAPrivateAddress(t *testing.T) {
	c := TCP{Host: "10.0.0.5", Port: 5432, Timeout: 1}
	r := (&Runner{}).TCP(t.Context(), c)
	if r.Outcome != check.Failed || !r.Local || r.Unsure || r.Reason != "the address is not on the public internet, and this server may not reach it" {
		t.Fatalf("%+v", r)
	}
	// Within a range the server may reach it tries; nothing answers there.
	r = (&Runner{Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}).TCP(t.Context(), c)
	if r.Local {
		t.Fatalf("an allowed range was refused: %+v", r)
	}
}

// A port that speaks TLS but refuses the handshake answered, so the network
// works and the canary has nothing to say.
func TestTCPRefusedHandshake(t *testing.T) {
	now := time.Now()
	cert, ca := certificateFor(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "127.0.0.1")
	ln := tls.NewListener(listen(t), &tls.Config{Certificates: []tls.Certificate{cert}, MaxVersion: tls.VersionTLS11})
	serve(t, ln, func(conn net.Conn) { conn.(*tls.Conn).Handshake() })
	c := tcpAt(t, ln)
	c.TLS, c.CA = true, ca
	r := tcpRunner.TCP(t.Context(), c)
	if r.Outcome != check.Failed || r.Reason != "the TLS handshake failed" || r.Unsure {
		t.Fatalf("%+v", r)
	}
}

// tlsListener is a TLS port on loopback with this certificate.
func tlsListener(t *testing.T, cert tls.Certificate) TCP {
	t.Helper()
	ln := tls.NewListener(listen(t), &tls.Config{Certificates: []tls.Certificate{cert}})
	serve(t, ln, func(conn net.Conn) { conn.(*tls.Conn).Handshake() })
	return tcpAt(t, ln)
}

// Spec case 14: a port with TLS is judged by its certificate as a site is.
func TestTCPCertificate(t *testing.T) {
	day := 24 * time.Hour
	now := time.Now()
	off := 0
	cases := []struct {
		name          string
		before, after time.Duration
		warn          *int
		want          check.Outcome
		reason        string
	}{
		{name: "plenty left", before: 30 * day, after: 60 * day, want: check.Passed},
		{name: "running low", before: 80 * day, after: 10*day + time.Hour, want: check.Warned, reason: "certificate expires in 10 days"},
		{name: "warnings off", before: 80 * day, after: 10 * day, warn: &off, want: check.Passed},
		{name: "about to run out", before: 80 * day, after: 2*day + time.Hour, want: check.Failed, reason: "certificate expires in 2 days"},
		{name: "expired", before: 80 * day, after: -day, want: check.Failed, reason: "certificate expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cert, ca := certificateFor(t, now.Add(-c.before), now.Add(c.after), "127.0.0.1")
			port := tlsListener(t, cert)
			port.TLS, port.CA, port.WarnDays = true, ca, c.warn
			r := tcpRunner.TCP(t.Context(), port)
			if r.Outcome != c.want || r.Reason != c.reason {
				t.Fatalf("%s: %q, want %s: %q", r.Outcome, r.Reason, c.want, c.reason)
			}
			// An expired one fails its verification, before it can be read.
			if _, ok := r.Measured["cert_days"]; ok == (c.after < 0) {
				t.Errorf("measured %v", r.Measured)
			}
		})
	}
}

func TestTCPVerifiesTheCertificate(t *testing.T) {
	now := time.Now()
	cert, ca := certificateFor(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "mail.example.com")
	port := tlsListener(t, cert)
	port.TLS = true

	if r := tcpRunner.TCP(t.Context(), port); r.Outcome != check.Failed || r.Reason != "certificate not trusted: its issuer is unknown" {
		t.Errorf("no CA: %+v", r)
	}
	// The name it asks for is the check's host, here through a dialer that
	// finds the listener whatever the name.
	address := port.Address()
	found := &Runner{Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	named := TCP{Host: "mail.example.com", Port: port.Port, TLS: true, CA: ca}
	if r := found.TCP(t.Context(), named); r.Outcome != check.Passed {
		t.Errorf("by name: %+v", r)
	}
	named.Host = "other.example.com"
	if r := found.TCP(t.Context(), named); r.Outcome != check.Failed || r.Reason != "certificate is for another host name" {
		t.Errorf("another name: %+v", r)
	}
	// Without verification its expiry is still read.
	skip := TCP{Host: port.Host, Port: port.Port, TLS: true, NoVerify: true}
	if r := tcpRunner.TCP(t.Context(), skip); r.Outcome != check.Passed || r.Measured["cert_days"] < 89 {
		t.Errorf("no verify: %+v", r)
	}
}

// readHello reads what the client sent first, so that closing does not reset
// the connection.
func readHello(conn net.Conn) { conn.Read(make([]byte, 4096)) }

func TestTCPWithTLSOnAPortWithout(t *testing.T) {
	banner := listen(t)
	serve(t, banner, func(conn net.Conn) {
		conn.Write([]byte("220 mail.example.com ESMTP\r\n"))
		readHello(conn)
	})
	c := tcpAt(t, banner)
	c.TLS = true
	if r := tcpRunner.TCP(t.Context(), c); r.Outcome != check.Failed || r.Reason != "the server does not speak TLS" {
		t.Errorf("a greeting in plain text: %+v", r)
	}

	closes := listen(t)
	serve(t, closes, readHello)
	c = tcpAt(t, closes)
	c.TLS = true
	if r := tcpRunner.TCP(t.Context(), c); r.Outcome != check.Failed || r.Reason != "the connection closed without an answer" {
		t.Errorf("closed at once: %+v", r)
	}

	quiet := listen(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serve(t, quiet, func(net.Conn) { <-release })
	c = tcpAt(t, quiet)
	c.TLS, c.Timeout = true, 1
	if r := tcpRunner.TCP(t.Context(), c); r.Outcome != check.Failed || r.Reason != "no answer within 1 s" {
		t.Errorf("silent: %+v", r)
	}
}

func TestTCPValidate(t *testing.T) {
	seven, one, big := 7, 1, 400
	ok := TCP{Host: "mail.example.com", Port: 993, TLS: true, WarnDays: &seven, FailDays: &one, Timeout: 60, FailAfter: 3}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	for _, host := range []string{"192.0.2.1", "2001:db8::1", "MAIL.example.com", "under_score.example.com"} {
		if err := (TCP{Host: host, Port: 25}).Validate(); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	cases := []struct {
		field string
		t     TCP
	}{
		{"host", TCP{Port: 25}},
		{"host", TCP{Host: "https://mail.example.com", Port: 25}},
		{"host", TCP{Host: "mail.example.com:25", Port: 25}},
		{"host", TCP{Host: "mail example.com", Port: 25}},
		{"host", TCP{Host: "fe80::1%eth0", Port: 25}},
		{"host", TCP{Host: strings.Repeat("a", 254), Port: 25}},
		{"port", TCP{Host: "a.example.com"}},
		{"port", TCP{Host: "a.example.com", Port: 65536}},
		{"timeout", TCP{Host: "a.example.com", Port: 25, Timeout: 61}},
		{"fail_after", TCP{Host: "a.example.com", Port: 25, FailAfter: 101}},
		{"warn_days", TCP{Host: "a.example.com", Port: 25, TLS: true, WarnDays: &big}},
		{"fail_days", TCP{Host: "a.example.com", Port: 25, TLS: true, WarnDays: &one, FailDays: &seven}},
		{"ca", TCP{Host: "a.example.com", Port: 25, TLS: true, CA: "not a certificate"}},
		{"warn_days", TCP{Host: "a.example.com", Port: 25, WarnDays: &seven}},
		{"fail_days", TCP{Host: "a.example.com", Port: 25, FailDays: &one}},
		{"no_verify", TCP{Host: "a.example.com", Port: 25, NoVerify: true}},
		{"ca", TCP{Host: "a.example.com", Port: 25, CA: "x"}},
	}
	for _, c := range cases {
		var se *SettingError
		if err := c.t.Validate(); !errors.As(err, &se) || se.Field != c.field {
			t.Errorf("%+v: %v, want a problem with %s", c.t, err, c.field)
		}
	}
	if _, err := ParseTCP([]byte(`{"host":"a.example.com","port":"25"}`)); err == nil {
		t.Error("a port as text was taken")
	}
}

func TestAttemptAndCertificateByKind(t *testing.T) {
	ln := listen(t)
	serve(t, ln, func(net.Conn) {})
	port := tcpAt(t, ln)
	if r := tcpRunner.Attempt(t.Context(), Check{TCP: &port}); r.Outcome != check.Passed {
		t.Errorf("attempt: %+v", r)
	}
	off := 0
	cases := []struct {
		c    Check
		host string
		port string
		on   bool
	}{
		{Check{TCP: &TCP{Host: "Mail.Example.com", Port: 993, TLS: true}}, "mail.example.com", "993", true},
		{Check{TCP: &TCP{Host: "mail.example.com", Port: 993, TLS: true, WarnDays: &off}}, "mail.example.com", "993", false},
		{Check{TCP: &TCP{Host: "db.example.com", Port: 5432}}, "db.example.com", "5432", false},
		{Check{HTTP: HTTP{URL: "https://shop.example.com/health"}}, "shop.example.com", "443", true},
		{Check{HTTP: HTTP{URL: "http://shop.example.com/"}}, "shop.example.com", "80", false},
	}
	for _, c := range cases {
		host, port, on := c.c.Certificate()
		if host != c.host || port != c.port || on != c.on {
			t.Errorf("%+v: %s %s %v", c.c, host, port, on)
		}
		if c.c.host() != c.host {
			t.Errorf("%+v waits for %s", c.c, c.c.host())
		}
	}
}

// A probe's -ca and a check's own both count, also when one has no final newline.
func TestTrustJoinsBoth(t *testing.T) {
	now := time.Now()
	_, one := certificateFor(t, now.Add(-time.Hour), now.Add(time.Hour), "a.example.com")
	_, two := certificateFor(t, now.Add(-time.Hour), now.Add(time.Hour), "b.example.com")
	r := &Runner{CA: two}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(r.trust(strings.TrimRight(one, "\n")))) {
		t.Fatal("no certificate")
	}
	for name, raw := range map[string]string{"a.example.com": one, "b.example.com": two} {
		block, _ := pem.Decode([]byte(raw))
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Errorf("%s is not trusted: %v", name, err)
		}
	}
	if (&Runner{}).trust(one) != one {
		t.Error("without a probe's -ca it changed the check's own")
	}
}
