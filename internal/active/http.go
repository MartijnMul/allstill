// SPDX-License-Identifier: AGPL-3.0-only

package active

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/outbound"
)

// MaxBody is how much of a body an attempt reads, and only to judge a JSON
// field (R11).
const MaxBody = 1 << 20

// Result is one attempt, judged. Measured holds what it measured: the status
// code (status), how long it took (ms), and the days the certificate has
// left (cert_days).
type Result struct {
	Outcome check.Outcome
	Reason  string
	// Unsure marks a failure the server's own network could have caused,
	// such as a timeout: the canary decides whether it counts.
	Unsure bool
	// Local marks a failure the server decided alone, without the network,
	// such as an address it may not reach: it proves nothing of the network.
	Local          bool
	Started, Ended time.Time
	Measured       map[string]float64
}

// Runner makes attempts.
type Runner struct {
	// Allow holds the private ranges the server may reach (-allow-private).
	Allow []netip.Prefix
	// CA holds certificates in PEM to trust for every check, beyond the
	// system's and the check's own: a probe's -ca.
	CA string
	// UserAgent is sent with every request: Allstill/<version>, with a
	// contact address when the administrator gives one.
	UserAgent string
	// Transport, when set, replaces the outbound transport, for tests.
	Transport func(*tls.Config, time.Duration) http.RoundTripper
	// Dial, when set, replaces the outbound dialer of a TCP check, for tests.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// trust is what a check with this ca trusts beyond the system's.
func (r *Runner) trust(ca string) string {
	if r.CA == "" {
		return ca
	}
	return ca + "\n" + r.CA
}

// CheckContact checks a contact address for the User-Agent: one line of
// printable text, which a header can carry.
func CheckContact(contact string) error {
	if len(contact) > 200 || strings.ContainsFunc(contact, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("the contact must be one line of at most 200 characters, such as ops@example.com")
	}
	return nil
}

// UserAgent is the User-Agent of a request: Allstill and its version, and
// the administrator's contact address when there is one (R11).
func UserAgent(version, contact string) string {
	ua := "Allstill/" + strings.TrimPrefix(version, "v")
	if contact != "" {
		ua += " (" + contact + ")"
	}
	return ua
}

// HTTP makes one attempt of an HTTP check and judges it.
func (r *Runner) HTTP(ctx context.Context, h HTTP) Result {
	res := Result{Started: time.Now(), Measured: map[string]float64{}}
	timeout := h.timeout()
	certs := &certificates{}
	conf := certs.config(r.trust(h.CA), h.NoVerify)
	var rt http.RoundTripper
	if r.Transport != nil {
		rt = r.Transport(conf, timeout)
	} else {
		t := outbound.Transport(conf, timeout, r.Allow)
		defer t.CloseIdleConnections()
		rt = t
	}
	client := &http.Client{Transport: rt, Timeout: timeout, CheckRedirect: outbound.Redirects(h.Follow)}
	req, err := http.NewRequestWithContext(ctx, h.method(), h.URL, nil)
	if err != nil {
		res.Local = true
		return res.fail("the URL cannot be requested", false)
	}
	req.Header.Set("User-Agent", r.UserAgent)
	if h.JSON != nil {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		res.Ended = time.Now()
		res.Measured["ms"] = float64(res.Ended.Sub(res.Started).Milliseconds())
		res.measureCertificate(certs)
		res.Local = errors.Is(err, outbound.ErrRefused)
		return res.fail(explain(err, timeout))
	}
	defer resp.Body.Close()
	var body []byte
	var tooLarge bool
	if h.JSON != nil && h.expects(resp.StatusCode) {
		body, err = io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
		tooLarge = len(body) > MaxBody
		if err != nil {
			return res.fail(explain(err, timeout))
		}
	}
	res.Ended = time.Now()
	res.Measured["status"] = float64(resp.StatusCode)
	res.Measured["ms"] = float64(res.Ended.Sub(res.Started).Milliseconds())
	cert := res.measureCertificate(certs)

	if reason := h.judgeStatus(resp, req.URL); reason != "" {
		return res.fail(reason, false)
	}
	if h.JSON != nil {
		if reason := h.judgeJSON(body, tooLarge); reason != "" {
			return res.fail(reason, false)
		}
	}
	return res.judgeCertificate(h.WarnDays, h.FailDays, cert)
}

func (r Result) fail(reason string, unsure bool) Result {
	if r.Ended.IsZero() {
		r.Ended = time.Now()
	}
	r.Outcome, r.Reason, r.Unsure = check.Failed, reason, unsure
	return r
}

// expects says whether a status code counts as up.
func (h HTTP) expects(code int) bool {
	switch {
	case len(h.Expect) > 0:
		return slices.Contains(h.Expect, code)
	case h.Location != "":
		return code >= 300 && code < 400
	}
	return code >= 200 && code < 300
}

// judgeStatus says what is wrong with the answer's status and redirect, or
// "" when nothing is. It never says where a redirect led.
func (h HTTP) judgeStatus(resp *http.Response, asked *url.URL) string {
	code := resp.StatusCode
	if !h.expects(code) {
		switch {
		case resp.Header.Get("Cf-Mitigated") == "challenge":
			return "blocked by a bot challenge (Cloudflare)"
		case code == http.StatusTooManyRequests:
			return "rate limited (HTTP 429)"
		case len(h.Expect) > 0:
			return fmt.Sprintf("HTTP %d instead of %s", code, orList(h.Expect))
		case h.Location != "":
			return fmt.Sprintf("HTTP %d instead of a redirect", code)
		}
		return fmt.Sprintf("HTTP %d", code)
	}
	if h.Location == "" {
		return ""
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "no Location in the redirect"
	}
	// One that cannot be read leads nowhere expected.
	got, _ := asked.Parse(loc)
	want, err := url.Parse(h.Location)
	if got == nil || err != nil || !sameURL(got, want) {
		return "the redirect leads elsewhere than expected"
	}
	return ""
}

// sameURL compares two URLs as a browser would treat them: the scheme and
// host in any case, the path as written, and an empty path as /.
func sameURL(a, b *url.URL) bool {
	norm := func(u *url.URL) string {
		host := strings.ToLower(u.Host)
		switch {
		case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
			host = strings.TrimSuffix(host, ":443")
		case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
			host = strings.TrimSuffix(host, ":80")
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		s := strings.ToLower(u.Scheme) + "://" + host + path
		if u.RawQuery != "" || u.ForceQuery {
			s += "?" + u.RawQuery
		}
		return s
	}
	return norm(a) == norm(b)
}

func orList(codes []int) string {
	s := make([]string, len(codes))
	for i, c := range codes {
		s[i] = strconv.Itoa(c)
	}
	if len(s) == 1 {
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

// judgeJSON says what is wrong with the JSON field, or "" when it is right.
func (h HTTP) judgeJSON(body []byte, tooLarge bool) string {
	name := strings.TrimPrefix(h.JSON.Pointer, "/")
	if tooLarge {
		return fmt.Sprintf("the answer is larger than %d MB", MaxBody>>20)
	}
	var doc any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		return "the answer is not JSON"
	}
	v, ok := pointer(doc, h.JSON.Pointer)
	if !ok {
		return "no " + name + " in the answer"
	}
	var got string
	switch v := v.(type) {
	case string:
		got = v
	case nil:
		got = "null"
	default:
		b, _ := json.Marshal(v)
		got = string(b)
	}
	if got != h.JSON.Equals {
		return fmt.Sprintf("%s is %s instead of %s", name, brief(got), brief(h.JSON.Equals))
	}
	return ""
}

// pointer finds a value by a JSON Pointer (RFC 6901).
func pointer(doc any, p string) (any, bool) {
	if p == "" {
		return doc, true
	}
	for _, token := range strings.Split(p[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch v := doc.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok {
				return nil, false
			}
			doc = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(v) || (len(token) > 1 && token[0] == '0') {
				return nil, false
			}
			doc = v[i]
		default:
			return nil, false
		}
	}
	return doc, true
}

// brief keeps a value from the answer to one short line, so it cannot make
// a reason of its own.
func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:39]) + "…"
	}
	if s == "" {
		return `""`
	}
	return s
}

// certificates keeps the certificate that runs out first, of every TLS
// connection an attempt makes: one per redirect it follows. With
// verification, it reads the verified chain; without, the server's own
// certificate.
type certificates struct {
	mu        sync.Mutex
	notBefore time.Time
	notAfter  time.Time
}

// config is the TLS configuration of an attempt that keeps its certificates
// here, trusting ca beyond the system's.
func (c *certificates) config(ca string, noVerify bool) *tls.Config {
	conf := &tls.Config{MinVersion: tls.VersionTLS12, VerifyConnection: c.see, InsecureSkipVerify: noVerify}
	if ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(ca))
		conf.RootCAs = pool
	}
	return conf
}

func (c *certificates) see(cs tls.ConnectionState) error {
	chain := cs.PeerCertificates
	if len(cs.VerifiedChains) > 0 {
		chain = cs.VerifiedChains[0]
	} else if len(chain) > 1 {
		chain = chain[:1]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cert := range chain {
		if c.notAfter.IsZero() || cert.NotAfter.Before(c.notAfter) {
			c.notBefore, c.notAfter = cert.NotBefore, cert.NotAfter
		}
	}
	return nil
}

// certificate is what an attempt learnt of the certificate: how long it has
// left and how long it lived. Zero when it spoke no TLS.
type certificate struct {
	left, life time.Duration
}

func (r *Result) measureCertificate(c *certificates) certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notAfter.IsZero() {
		return certificate{}
	}
	cert := certificate{left: c.notAfter.Sub(r.Ended), life: c.notAfter.Sub(c.notBefore)}
	r.Measured["cert_days"] = math.Round(cert.left.Hours()/24*100) / 100
	return cert
}

// judgeCertificate passes, warns or fails an attempt that got the answer it
// expected, on the time its certificate has left (R4): a warning below
// warn_days or a third of its life, whichever is shorter, a failure below
// fail_days or a tenth. Hours are compared, so a short-lived certificate is
// measured as such (C5).
func (r Result) judgeCertificate(warn, fail *int, c certificate) Result {
	r.Outcome = check.Passed
	if c == (certificate{}) {
		return r
	}
	if c.left <= 0 {
		return r.fail("certificate expired", false)
	}
	day := 24 * time.Hour
	if d := daysOr(fail, DefaultFailDays); d > 0 && c.left < min(time.Duration(d)*day, c.life/10) {
		return r.fail("certificate expires in "+left(c.left), false)
	}
	if d := daysOr(warn, DefaultWarnDays); d > 0 && c.left < min(time.Duration(d)*day, c.life/3) {
		r.Outcome, r.Reason = check.Warned, "certificate expires in "+left(c.left)
	}
	return r
}

// left says how long a certificate has: in hours under two days.
func left(d time.Duration) string {
	if h := int(d.Hours()); h < 48 {
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", max(h, 0))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// explain says in plain words why a request failed, without the URL, the
// addresses it dialled or where a redirect led, and whether the server's own
// network could be the cause (C4): any answer, a refused connection, a TLS
// error or a name that does not exist prove it works.
func explain(err error, timeout time.Duration) (reason string, unsure bool) {
	var (
		dnsErr    *net.DNSError
		unknownCA x509.UnknownAuthorityError
		hostErr   x509.HostnameError
		invalid   x509.CertificateInvalidError
		verify    *tls.CertificateVerificationError
		record    tls.RecordHeaderError
		netErr    net.Error
	)
	switch {
	case errors.Is(err, outbound.ErrOwn):
		return "the address is the server's own, which checks never reach", false
	case errors.Is(err, outbound.ErrRefused):
		return "the address is not on the public internet, and this server may not reach it", false
	case errors.Is(err, outbound.ErrTooManyRedirects):
		return fmt.Sprintf("more than %d redirects", outbound.MaxRedirects), false
	case errors.Is(err, outbound.ErrRedirectScheme):
		return "redirected to an address that is not http or https", false
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		// Also when not yet valid: the server's clock would be wrong, or the
		// certificate is.
		if invalid.Cert != nil && time.Now().Before(invalid.Cert.NotBefore) {
			return "certificate not valid yet", false
		}
		return "certificate expired", false
	case errors.As(err, &hostErr):
		return "certificate is for another host name", false
	case errors.As(err, &unknownCA):
		return "certificate not trusted: its issuer is unknown", false
	case errors.As(err, &invalid), errors.As(err, &verify):
		return "certificate not valid", false
	case errors.As(err, &record), strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		// net/http turns the record error into text when the answer is HTTP.
		return "the server does not speak TLS", false
	case strings.Contains(err.Error(), "tls: "):
		// crypto/tls has no type for these outside QUIC: an alert from the
		// server, or no version or cipher in common. Either way it answered.
		return "the TLS handshake failed", false
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "the host name does not exist", false
	case errors.As(err, &dnsErr):
		return "the host name could not be looked up", true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused", false
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset", false
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to the address", true
	case errors.Is(err, syscall.ENETUNREACH):
		return "the network is unreachable", true
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "no answer within " + seconds(timeout), true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the connection closed without an answer", false
	}
	return requestFailed, true
}

// requestFailed is the reason of a failure explain knows nothing more about.
const requestFailed = "the request failed"

func seconds(d time.Duration) string { return strconv.Itoa(int(d.Seconds())) + " s" }
