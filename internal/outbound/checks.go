// SPDX-License-Identifier: AGPL-3.0-only

package outbound

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// MaxRedirects is how many redirects a check that follows them may take
// (intent/http-checks/spec.md, R5).
const MaxRedirects = 5

// ErrTooManyRedirects and ErrRedirectScheme end a check's redirects.
var (
	ErrTooManyRedirects = fmt.Errorf("more than %d redirects", MaxRedirects)
	ErrRedirectScheme   = errors.New("a redirect to an address that is not http or https")
)

// ErrOwn is the error for one of the server's own addresses, which a check
// never reaches, not even within the ranges it may (ADR 0004).
var ErrOwn = fmt.Errorf("%w: it is the server's own", ErrRefused)

// Transport returns the transport for one attempt of a check, the server
// fetching an address someone chose (ADR 0004). It keeps no connection, so
// each attempt resolves, connects and shakes hands as a visitor would. Every
// address is checked as it is dialled, for every redirect too: a public
// address, or one within allow, but never one of the server's own.
func Transport(tlsConfig *tls.Config, timeout time.Duration, allow []netip.Prefix) *http.Transport {
	return &http.Transport{
		Proxy:                  nil, // a proxy would dial for us, past the check
		DialContext:            Dialer(timeout, allow).DialContext,
		TLSClientConfig:        tlsConfig,
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 64 << 10,
	}
}

// Dialer returns the dialer of a check, which checks every address as it is
// dialled: a public address, or one within allow, but never one of the
// server's own.
func Dialer(timeout time.Duration, allow []netip.Prefix) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrRefused, address)
			}
			if own(ap.Addr()) {
				return ErrOwn
			}
			if !Allowed(ap.Addr(), allow) {
				return fmt.Errorf("%w: %s", ErrRefused, ap.Addr())
			}
			return nil
		},
	}
}

// Unreachable is an address a check may not reach. Its message is for anyone:
// what the name resolved to, and why, is for someone at the server's own shell.
type Unreachable struct {
	Host string
	Addr netip.Addr
	Own  bool // one of the server's own addresses, rather than a private one
}

func (u *Unreachable) Error() string {
	return "this address is not on the public internet, and this server may not reach it"
}

// ErrNoSuchHost means a host name does not resolve now, which a check can report
// for itself.
var ErrNoSuchHost = errors.New("the host name does not resolve now")

// CheckHost says whether a check may reach a host as it resolves now. The dialer
// decides for each connection; this is to refuse a mistake when a check is saved.
func CheckHost(ctx context.Context, host string, allow []netip.Prefix) error {
	if host == "" {
		return nil // what is wrong with it is for the check's own rules to say
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if addrs, err = net.DefaultResolver.LookupNetIP(lookup, "ip", host); err != nil {
			return ErrNoSuchHost
		}
	}
	for _, a := range addrs {
		if Own(a) {
			return &Unreachable{Host: host, Addr: a, Own: true}
		}
		if !Allowed(a, allow) {
			return &Unreachable{Host: host, Addr: a}
		}
	}
	return nil
}

// Redirects is a check's redirect policy for http.Client: none are followed
// unless follow, then at most MaxRedirects, to http or https only. The
// transport checks each hop's address as it dials it. A hop is not told where
// it came from: the URL before it may hold a token (ADR 0004).
func Redirects(follow bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		switch {
		case !follow:
			return http.ErrUseLastResponse
		case len(via) > MaxRedirects:
			return ErrTooManyRedirects
		case req.URL.Scheme != "http" && req.URL.Scheme != "https":
			return ErrRedirectScheme
		}
		return nil
	}
}

// Own says whether a is one of the server's own addresses, which a check
// never reaches.
func Own(a netip.Addr) bool { return own(a) }

// own says whether a is one of the server's own addresses: loopback,
// unspecified, or one of its interfaces'. The interfaces are read again at
// most every ownFresh, as an address from DHCP can change.
var own = func(a netip.Addr) bool {
	a = a.Unmap()
	// The well-known NAT64 prefix reaches the IPv4 address in it, as in
	// Allowed.
	if nat64.Contains(a.WithZone("")) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	if a.IsLoopback() || a.IsUnspecified() {
		return true
	}
	return ownAddrs.has(a)
}

const ownFresh = time.Minute

var ownAddrs = &interfaceAddrs{}

type interfaceAddrs struct {
	mu    sync.Mutex
	read  time.Time
	addrs map[netip.Addr]bool
}

func (i *interfaceAddrs) has(a netip.Addr) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.addrs == nil || time.Since(i.read) > ownFresh {
		addrs := map[netip.Addr]bool{}
		if ifs, err := net.InterfaceAddrs(); err == nil {
			for _, ia := range ifs {
				if p, err := netip.ParsePrefix(ia.String()); err == nil {
					addrs[p.Addr().Unmap().WithZone("")] = true
				}
			}
		}
		i.addrs, i.read = addrs, time.Now()
	}
	return i.addrs[a.WithZone("")]
}
