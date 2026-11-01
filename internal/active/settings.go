// SPDX-License-Identifier: AGPL-3.0-only

// Package active runs the checks Allstill runs itself, instead of waiting for
// a ping (intent/http-checks/spec.md): one attempt, judged, and the loop that
// starts each attempt in its time. The server runs it inside `allstill serve`;
// a probe will run the same loop inside another network.
//
// An attempt passes, warns (a certificate running out), fails, or cannot be
// judged; the store applies it with check.OnAttempt. A failure the server's
// own network could have caused, such as a timeout, is Unsure: the loop asks
// the canary whether the network works before it counts against the target.
//
// A reason never holds the check's URL, nor where a redirect led: both may
// hold someone else's secret (ADR 0004).
package active

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults of a check's settings.
const (
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 60 * time.Second
	DefaultWarnDays = 14
	DefaultFailDays = 3
	MaxCA           = 64 << 10 // bytes of certificates in PEM
)

// HTTP is an HTTP check's settings, as the check's settings hold them and the
// API's HttpCheck describes them.
type HTTP struct {
	URL    string `json:"url"`
	Method string `json:"method,omitempty"` // GET (the default) or HEAD
	// Expect lists the status codes that count as up: by default any 2xx, or
	// any 3xx when Location is set.
	Expect []int      `json:"expected_status,omitempty"`
	JSON   *JSONField `json:"json_field,omitempty"`
	// Location is where a redirect must lead; the answer must be one.
	Location string `json:"location,omitempty"`
	Follow   bool   `json:"follow_redirects,omitempty"`
	Timeout  int    `json:"timeout,omitempty"` // seconds
	// WarnDays and FailDays are the certificate's thresholds; 0 turns one
	// off, nil is the default.
	WarnDays *int `json:"warn_days,omitempty"`
	FailDays *int `json:"fail_days,omitempty"`
	// CA holds certificates in PEM to trust beyond the system's, such as
	// an internal CA's.
	CA       string `json:"ca,omitempty"`
	NoVerify bool   `json:"no_verify,omitempty"`
	// FailAfter is read by the store (check.Config); it is here so the
	// settings are checked in one place.
	FailAfter int `json:"fail_after,omitempty"`
}

// JSONField is a field in a JSON body that must equal a value.
type JSONField struct {
	Pointer string `json:"pointer"` // RFC 6901, such as /status
	Equals  string `json:"equals"`
}

// SettingError says which setting is wrong and why.
type SettingError struct{ Field, Problem string }

func (e *SettingError) Error() string { return e.Field + ": " + e.Problem }

// ParseHTTP reads and checks an HTTP check's settings. Settings it does not
// know are left for other features.
func ParseHTTP(raw []byte) (HTTP, error) {
	var h HTTP
	if err := json.Unmarshal(raw, &h); err != nil {
		return h, &SettingError{"settings", "cannot be read: " + err.Error()}
	}
	return h, h.Validate()
}

// Validate checks the settings.
func (h HTTP) Validate() error {
	if err := checkURL(h.URL); err != nil {
		return &SettingError{"url", err.Error()}
	}
	switch {
	case h.Method != "" && h.Method != "GET" && h.Method != "HEAD":
		return &SettingError{"method", "is GET or HEAD"}
	case len(h.Expect) > 20:
		return &SettingError{"expected_status", "at most 20"}
	case h.Location != "" && h.Follow:
		return &SettingError{"location", "checks a redirect, which follow_redirects would follow instead"}
	}
	for _, code := range h.Expect {
		if code < 100 || code > 599 {
			return &SettingError{"expected_status", "each must be 100 to 599"}
		}
	}
	if h.JSON != nil {
		switch {
		case h.Method == "HEAD":
			return &SettingError{"json_field", "needs a body, which HEAD does not get"}
		case !strings.HasPrefix(h.JSON.Pointer, "/") || len(h.JSON.Pointer) > 200:
			return &SettingError{"json_field.pointer", "must be a JSON Pointer such as /status"}
		case len(h.JSON.Equals) > 200:
			return &SettingError{"json_field.equals", "at most 200 characters"}
		}
	}
	if h.Location != "" {
		if err := checkURL(h.Location); err != nil {
			return &SettingError{"location", err.Error()}
		}
	}
	return validateCommon(h.Timeout, h.FailAfter, h.WarnDays, h.FailDays, h.CA)
}

// validateCommon checks the settings every kind of check has.
func validateCommon(timeout, failAfter int, warn, fail *int, ca string) error {
	switch {
	case timeout < 0 || time.Duration(timeout)*time.Second > MaxTimeout:
		return &SettingError{"timeout", "must be 1 to 60 seconds"}
	case warn != nil && (*warn < 0 || *warn > 365):
		return &SettingError{"warn_days", "must be 0 (off) to 365"}
	case fail != nil && (*fail < 0 || *fail > 365):
		return &SettingError{"fail_days", "must be 0 (off) to 365"}
	case daysOr(warn, DefaultWarnDays) > 0 && daysOr(fail, DefaultFailDays) >= daysOr(warn, DefaultWarnDays):
		return &SettingError{"fail_days", "must be fewer than warn_days"}
	case failAfter < 0 || failAfter > 100:
		return &SettingError{"fail_after", "must be a whole number from 1 to 100"}
	case len(ca) > MaxCA:
		return &SettingError{"ca", "at most 64 kB"}
	case ca != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)):
		return &SettingError{"ca", "holds no certificate in PEM"}
	}
	return nil
}

func daysOr(days *int, def int) int {
	if days == nil {
		return def
	}
	return *days
}

func checkURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("is needed")
	}
	if len(raw) > 2000 {
		return fmt.Errorf("at most 2000 characters")
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		// The error would repeat the URL, which may be a secret.
		return fmt.Errorf("cannot be read")
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("must start with http:// or https://")
	case u.Hostname() == "":
		return fmt.Errorf("must name a host")
	case u.User != nil:
		// Headers and passwords are secrets of their own, which wait for
		// encryption at rest (ADR 0004); a token in the path or query works.
		return fmt.Errorf("a user name or password in the URL is not supported; use a token in its path or query")
	case u.Fragment != "":
		return fmt.Errorf("a fragment (#) is never sent; leave it out")
	}
	return nil
}

// TCP is a TCP check's settings, as the check's settings hold them and the
// API's TcpCheck describes them: a port that should accept a connection, and
// with TLS, the handshake and its certificate.
type TCP struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls,omitempty"`
	// Timeout is in seconds.
	Timeout int `json:"timeout,omitempty"`
	// WarnDays, FailDays, CA and NoVerify are as an HTTP check's, and only
	// mean something with TLS.
	WarnDays *int   `json:"warn_days,omitempty"`
	FailDays *int   `json:"fail_days,omitempty"`
	CA       string `json:"ca,omitempty"`
	NoVerify bool   `json:"no_verify,omitempty"`
	// FailAfter is read by the store, as an HTTP check's is.
	FailAfter int `json:"fail_after,omitempty"`
}

// ParseTCP reads and checks a TCP check's settings.
func ParseTCP(raw []byte) (TCP, error) {
	var t TCP
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, &SettingError{"settings", "cannot be read: " + err.Error()}
	}
	return t, t.Validate()
}

// Validate checks the settings.
func (t TCP) Validate() error {
	if err := checkHost(t.Host); err != nil {
		return &SettingError{"host", err.Error()}
	}
	if t.Port < 1 || t.Port > 65535 {
		return &SettingError{"port", "must be 1 to 65535"}
	}
	if !t.TLS {
		for field, set := range map[string]bool{"warn_days": t.WarnDays != nil, "fail_days": t.FailDays != nil, "ca": t.CA != "", "no_verify": t.NoVerify} {
			if set {
				return &SettingError{field, "is about a certificate, so it needs tls"}
			}
		}
	}
	return validateCommon(t.Timeout, t.FailAfter, t.WarnDays, t.FailDays, t.CA)
}

// checkHost checks a host name or IP address, without a scheme or a port.
func checkHost(host string) error {
	switch {
	case host == "":
		return fmt.Errorf("is needed")
	case len(host) > 253:
		return fmt.Errorf("at most 253 characters")
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" {
			return fmt.Errorf("an address with a zone is not supported")
		}
		return nil
	}
	for _, r := range host {
		if !(r == '-' || r == '.' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return fmt.Errorf("must be a host name or IP address, without a scheme or a port")
		}
	}
	return nil
}

// Address is where a TCP check connects, as host:port.
func (t TCP) Address() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

func (t TCP) timeout() time.Duration {
	if t.Timeout == 0 {
		return DefaultTimeout
	}
	return time.Duration(t.Timeout) * time.Second
}

func (h HTTP) method() string {
	if h.Method == "" {
		return "GET"
	}
	return h.Method
}

func (h HTTP) timeout() time.Duration {
	if h.Timeout == 0 {
		return DefaultTimeout
	}
	return time.Duration(h.Timeout) * time.Second
}

// Host is the host an HTTP check reads, in lower case, and its port: the
// certificate it watches, and the host it waits its turn for.
func (h HTTP) Host() (host, port string) {
	u, err := url.Parse(h.URL)
	if err != nil {
		return "", ""
	}
	port = u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return strings.ToLower(u.Hostname()), port
}

// Shown is the URL as everyone may see it: its scheme and host (R12).
func Shown(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
