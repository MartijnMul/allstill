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
	"net/url"
	"strings"
	"time"
)

// Defaults of an HTTP check's settings.
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
	case h.Timeout < 0 || time.Duration(h.Timeout)*time.Second > MaxTimeout:
		return &SettingError{"timeout", "must be 1 to 60 seconds"}
	case h.WarnDays != nil && (*h.WarnDays < 0 || *h.WarnDays > 365):
		return &SettingError{"warn_days", "must be 0 (off) to 365"}
	case h.FailDays != nil && (*h.FailDays < 0 || *h.FailDays > 365):
		return &SettingError{"fail_days", "must be 0 (off) to 365"}
	case h.warnDays() > 0 && h.failDays() >= h.warnDays():
		return &SettingError{"fail_days", "must be fewer than warn_days"}
	case h.FailAfter < 0 || h.FailAfter > 100:
		return &SettingError{"fail_after", "must be a whole number from 1 to 100"}
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
	if len(h.CA) > MaxCA {
		return &SettingError{"ca", "at most 64 kB"}
	}
	if h.CA != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(h.CA)) {
			return &SettingError{"ca", "holds no certificate in PEM"}
		}
	}
	return nil
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

func (h HTTP) warnDays() int {
	if h.WarnDays == nil {
		return DefaultWarnDays
	}
	return *h.WarnDays
}

func (h HTTP) failDays() int {
	if h.FailDays == nil {
		return DefaultFailDays
	}
	return *h.FailDays
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
