// SPDX-License-Identifier: AGPL-3.0-only

// Package outbound is the one HTTP client for everything the server sends
// to an address someone else chose, such as a browser's push endpoint
// (ADR 0001, ADR 0003). It keeps out of the server's own network: every
// address is checked as it is dialled, after DNS, so a host name cannot be
// made to point inside later, and redirects are never followed.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrRefused is the error for an address inside the server's own network.
var ErrRefused = errors.New("refused: the address is not on the public internet")

// Client returns a client that refuses every address that is not public
// (loopback, private, link-local, unique local, multicast, unspecified and the
// like), unless it falls within allow. Push endpoints pass no allow: an
// administrator's private ranges are for HTTP checks only (ADR 0003).
func Client(timeout time.Duration, allow ...netip.Prefix) *http.Client {
	dialer := &net.Dialer{
		Timeout: min(timeout, 10*time.Second),
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrRefused, address)
			}
			if !Allowed(ap.Addr(), allow) {
				return fmt.Errorf("%w: %s", ErrRefused, ap.Addr())
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil, // a proxy would dial for us, past the check
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Allowed says whether the server may connect to a: a public address, or one
// within allow.
func Allowed(a netip.Addr, allow []netip.Prefix) bool {
	// A zone only belongs on a link-local address, and Prefix.Contains never
	// matches an address that has one.
	if a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	// The well-known NAT64 prefix reaches the IPv4 address in its last 32
	// bits, which is judged instead. A network's own NAT64 prefix cannot be
	// told from a public address.
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	for _, p := range allow {
		if p.Contains(a) {
			return true
		}
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// notPublic are the ranges IsGlobalUnicast and IsPrivate let through that are
// still not on the public internet (the IANA special-purpose registries).
var notPublic = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // shared address space (carrier-grade NAT, Tailscale)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved
		"::/96",           // IPv4-compatible, deprecated
		"::ffff:0:0:0/96", // IPv4-translated (SIIT)
		"64:ff9b:1::/48",  // local-use IPv4/IPv6 translation
		"100::/64",        // discard
		"2001::/23",       // IETF protocol assignments
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4, which can reach private IPv4
		"3fff::/20",       // documentation
		"5f00::/16",       // segment routing (SRv6)
		"fec0::/10",       // site-local, deprecated
	} {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// Do sends req with c; it is http.Client.Do, but its error never holds the
// URL, which may be a secret such as a push endpoint.
func Do(ctx context.Context, c *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s to %s: %w", req.Method, req.URL.Hostname(), err)
	}
	return resp, nil
}
