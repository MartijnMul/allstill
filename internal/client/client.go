// SPDX-License-Identifier: AGPL-3.0-only

// Package client is how the command line reaches an Allstill server from anywhere:
// the API client, signing in, the credentials file and what is safe to print
// (intent/cli-anywhere/spec.md). cmd/allstill only wires it to commands.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/MartijnMul/allstill/internal/apiclient"
)

const (
	// clientID is the one client the server knows.
	clientID = "allstill"
	// How long waiting for a code can last, and between two polls, in seconds.
	maxExpiresIn = 3600
	maxInterval  = 300
)

// ErrSignedOut means the server does not take this credential: it never did, or it
// has ended.
var ErrSignedOut = errors.New("signed out")

// ParseServer checks an installation's address, and returns it in one form. A
// credential only travels over https, or over http on this machine (R9).
func ParseServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not an address. Give one such as https://allstill.example.com", Clean(raw))
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	switch {
	case u.Path != "" || strings.ContainsAny(raw, "?#") || u.User != nil:
		return "", fmt.Errorf("%q has more in it than a server's address. Give one such as https://allstill.example.com", Clean(raw))
	case u.Scheme == "https":
	case u.Scheme == "http" && onThisMachine(u.Hostname()):
	case u.Scheme == "http":
		return "", fmt.Errorf("%s is plain http, so your sign-in would travel unprotected. Use https; http only works on this machine", Clean(u.String()))
	default:
		return "", fmt.Errorf("%q is not an address. Give one such as https://allstill.example.com", Clean(raw))
	}
	return u.String(), nil
}

func onThisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Clean takes out of text from elsewhere what could steer a terminal or turn text
// around, so a monitored site or a typed name cannot (R19). Everything the command
// line prints from a server goes through it.
func Clean(s string) string {
	return strings.Map(func(c rune) rune {
		if steers(c) || c == unicode.ReplacementChar {
			return -1
		}
		return c
	}, strings.ToValidUTF8(s, ""))
}

// steers says a character could steer a terminal or turn text around.
func steers(c rune) bool {
	return unicode.In(c, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co)
}

// Client talks to one installation.
type Client struct {
	// Server is the installation's address, from ParseServer.
	Server string
	// Version is this command line's, for saying when it differs from the server's.
	Version string

	http *http.Client
	api  *apiclient.ClientWithResponses
}

// New returns a client for an address from ParseServer. It sends token, if any, on
// API requests, and follows no redirect, so a credential goes nowhere else (R9).
// Transport is nil for the default.
func New(server, token, version string, transport http.RoundTripper) (*Client, error) {
	hc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	userAgent := "allstill/" + version
	api, err := apiclient.NewClientWithResponses(server+"/api/v1", apiclient.WithHTTPClient(hc),
		apiclient.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			r.Header.Set("User-Agent", userAgent)
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &Client{Server: server, Version: version, http: hc, api: api}, nil
}

// passing is a failure that may be gone in a moment.
type passing struct{ message string }

func (e passing) Error() string { return e.message }

// unreachable says a server could not be reached, without what a URL may hold.
func (c *Client) unreachable(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return passing{fmt.Sprintf("cannot reach %s: %s", c.Server, Clean(err.Error()))}
}

// refusal says why the server did not do what was asked, in its own words where it
// has some, and what to do.
func (c *Client) refusal(ctx context.Context, resp *http.Response, body []byte, known bool) error {
	switch status := resp.StatusCode; {
	case status == http.StatusUnauthorized:
		return ErrSignedOut
	case status >= 300 && status < 400:
		return fmt.Errorf("%s sent this to another address (%s), and a sign-in does not follow that. Use the address people reach it at", c.Server, Clean(resp.Header.Get("Location")))
	case status == http.StatusNotFound && !strings.Contains(resp.Header.Get("Content-Type"), "problem"):
		return c.otherVersion(ctx, known)
	}
	var p apiclient.Problem
	if json.Unmarshal(body, &p) == nil && p.Title != "" {
		if p.Detail != nil {
			return fmt.Errorf("%s %s", Clean(p.Title), Clean(*p.Detail))
		}
		return errors.New(Clean(p.Title))
	}
	return fmt.Errorf("%s answered with status %d", c.Server, resp.StatusCode)
}

// otherVersion says a server does not know something asked of it, which the two
// being of different versions explains (R45). Known says it can ask its version.
func (c *Client) otherVersion(ctx context.Context, known bool) error {
	if known {
		if me, err := c.Me(ctx); err == nil && !SameVersion(me.ServerVersion, c.Version) {
			return fmt.Errorf("%s does not know this. It runs version %s and this command line is %s: use the same version of both", c.Server, Clean(me.ServerVersion), c.Version)
		}
	}
	return fmt.Errorf("%s does not know this. It may run an older version than this command line (%s), or something in front of it may not let /oauth and /api through", c.Server, c.Version)
}

// Me says who is asking.
func (c *Client) Me(ctx context.Context) (*apiclient.Me, error) {
	r, err := c.api.GetMeWithResponse(ctx)
	if err != nil {
		return nil, c.unreachable(err)
	}
	if r.JSON200 == nil {
		return nil, c.refusal(ctx, r.HTTPResponse, r.Body, false)
	}
	return r.JSON200, nil
}

// EndThisToken ends the sign-in in use.
func (c *Client) EndThisToken(ctx context.Context) error {
	r, err := c.api.EndThisTokenWithResponse(ctx)
	if err != nil {
		return c.unreachable(err)
	}
	if r.StatusCode() != http.StatusNoContent {
		return c.refusal(ctx, r.HTTPResponse, r.Body, true)
	}
	return nil
}

// DeviceCode is what a server gives someone to approve (RFC 8628).
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is a sign-in that was approved.
type Token struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// oauth posts a form to one of the two /oauth endpoints.
func (c *Client) oauth(ctx context.Context, path string, form url.Values) (*http.Response, []byte, error) {
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "allstill/"+c.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return resp, body, nil
}

// RequestDeviceCode asks for a code for someone to approve. Name is what this
// machine is called, and scope is read or manage.
func (c *Client) RequestDeviceCode(ctx context.Context, name, scope string) (DeviceCode, error) {
	resp, body, err := c.oauth(ctx, "/oauth/device_authorization", url.Values{"device_name": {name}, "scope": {scope}})
	if err != nil {
		return DeviceCode{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		wait := "a moment"
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = strconv.Itoa(s) + " seconds"
		}
		return DeviceCode{}, fmt.Errorf("too many sign-ins are being asked for at %s. Try again in %s", c.Server, wait)
	default:
		return DeviceCode{}, c.signInRefusal(ctx, resp, body)
	}
	var d DeviceCode
	if err := json.Unmarshal(body, &d); err != nil || d.DeviceCode == "" || d.UserCode == "" || d.VerificationURI == "" {
		return DeviceCode{}, fmt.Errorf("%s does not answer as Allstill does. Check the address", c.Server)
	}
	d.UserCode, d.VerificationURI = Clean(d.UserCode), Clean(d.VerificationURI)
	if u, err := url.Parse(d.VerificationURI); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return DeviceCode{}, fmt.Errorf("%s does not answer as Allstill does. Check the address", c.Server)
	}
	// Whatever a server says, within what makes sense for waiting on someone.
	if d.ExpiresIn <= 0 || d.ExpiresIn > maxExpiresIn {
		d.ExpiresIn = 600
	}
	if d.Interval <= 0 || d.Interval > maxInterval {
		d.Interval = 5
	}
	return d, nil
}

// signInRefusal is refusal for the /oauth endpoints, where a 404 means something in
// front of the server, or one that has no sign-in yet.
func (c *Client) signInRefusal(ctx context.Context, resp *http.Response, body []byte) error {
	if resp.StatusCode == http.StatusNotFound {
		return c.otherVersion(ctx, false)
	}
	var e oauthError
	if json.Unmarshal(body, &e) == nil && e.Description != "" {
		return errors.New(Clean(e.Description))
	}
	return c.refusal(ctx, resp, body, false)
}

// errExpiredCode covers a code that was approved and not fetched in time as well, as
// when this machine was asleep: that sign-in is listed, and nobody holds it.
var errExpiredCode = errors.New("the code expired. If you did approve it, end that sign-in under Devices. Run allstill login again for a new one")

// ErrPending and ErrSlowDown are what Poll says while nobody has answered yet.
var (
	ErrPending  = errors.New("authorization_pending")
	ErrSlowDown = errors.New("slow_down")
)

// Poll asks once what became of a code.
func (c *Client) Poll(ctx context.Context, d DeviceCode) (Token, error) {
	resp, body, err := c.oauth(ctx, "/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {d.DeviceCode},
	})
	if err != nil {
		return Token{}, err
	}
	if resp.StatusCode == http.StatusOK {
		var t Token
		if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
			return Token{}, fmt.Errorf("%s does not answer as Allstill does. Check the address", c.Server)
		}
		return t, nil
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return Token{}, passing{fmt.Sprintf("%s answered with status %d", c.Server, resp.StatusCode)}
	}
	var e oauthError
	if json.Unmarshal(body, &e) != nil || e.Error == "" {
		return Token{}, c.signInRefusal(ctx, resp, body)
	}
	switch e.Error {
	case "authorization_pending":
		return Token{}, ErrPending
	case "slow_down":
		return Token{}, ErrSlowDown
	case "access_denied":
		return Token{}, errors.New("the sign-in was refused, so nothing changed")
	case "expired_token":
		return Token{}, errExpiredCode
	case "invalid_grant":
		return Token{}, errors.New("the server no longer knows this code, perhaps as it restarted. Run allstill login again for a new one")
	}
	return Token{}, c.signInRefusal(ctx, resp, body)
}

// WaitForToken polls until someone has approved or refused a code, or it expires.
// A poll that fails on the way is tried again, as a network comes and goes while
// someone finds their phone.
func (c *Client) WaitForToken(ctx context.Context, d DeviceCode) (Token, error) {
	interval := time.Duration(d.Interval) * time.Second
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.ExpiresIn)*time.Second)
	defer cancel()
	var lastFailure error
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				if lastFailure != nil {
					return Token{}, lastFailure
				}
				return Token{}, errExpiredCode
			}
			return Token{}, ctx.Err()
		case <-time.After(interval):
		}
		t, err := c.Poll(ctx, d)
		switch {
		case err == nil:
			return t, nil
		case errors.Is(err, ErrPending):
			lastFailure = nil
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second // as RFC 8628 says
		case errors.As(err, new(passing)):
			lastFailure = err
		default:
			return Token{}, err
		}
	}
}
