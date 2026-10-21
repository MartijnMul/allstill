// SPDX-License-Identifier: AGPL-3.0-only

// Package ingest serves the ping endpoints jobs call. They follow Healthchecks,
// so a job moves over by changing the base URL:
//
//	/ping/{uuid}                 success
//	/ping/{uuid}/start|fail|log  start, failure, log only
//	/ping/{uuid}/{exit-status}   0 is success, 1 to 255 failure
//	/ping/{ping-key}/{slug}...   the same, by a project's ping key and a slug
//
// Answers are plain text and those of Healthchecks: "OK", "Created" (201)
// for a check made by `create=1`, and "not found" (404). A check pinged far
// too often has its extra pings dropped, as on hc-ping.com: by UUID the answer
// is still 200, "OK (rate limited)", so a script that checks the status does
// not start failing; by slug it is 429. A ping never needs a token and never
// gets a cookie; the ping URL is the credential, so it is never logged.
package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store"
)

// BodyLimit is the most of a ping's body that is kept, as on hc-ping.com.
const BodyLimit = 100_000

// Store is what ingest needs of the store.
type Store interface {
	Ping(ctx context.Context, in store.PingIn) (store.PingOut, error)
}

// Handler serves pings.
type Handler struct {
	Store Store
	// Nudge tells the scheduler about a check's new deadline.
	Nudge func(deadline time.Time)
	Log   *slog.Logger
	Now   func() time.Time

	limit limiter
}

// Register adds the ping routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("/ping/", h)
}

var (
	pingKey   = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	slug      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)
	valueName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	number    = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,6})?$`)
)

type badRequest string

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Pings come from scripts and browsers alike; there is nothing to protect
	// with CORS on a URL that is its own credential.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// As in Healthchecks, any method pings, except a browser's preflight.
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST")
		w.Header().Set("Access-Control-Allow-Headers", "Allstill-Values, Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	in, err := h.parse(r)
	var bad badRequest
	if errors.As(err, &bad) {
		http.Error(w, string(bad), http.StatusBadRequest)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	key := in.CheckUUID
	if key == "" {
		key = in.PingKey + "/" + in.Slug
	}
	if ok, warn := h.limit.allow(key, in.At); !ok {
		if warn {
			// Enough of the URL to find the job, not enough to ping it.
			h.Log.Warn("a check is pinged more than once a second; the extra pings are dropped",
				"ping", "…"+key[len(key)-4:], "slug", in.Slug)
		}
		if in.CheckUUID != "" {
			io.WriteString(w, "OK (rate limited)")
			return
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "rate limit exceeded")
		return
	}
	out, err := h.Store.Ping(r.Context(), in)
	switch {
	case errors.Is(err, store.ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "not found")
		return
	case err != nil:
		h.fail(w, r, err)
		return
	}
	if h.Nudge != nil && !out.Deadline.IsZero() {
		h.Nudge(out.Deadline)
	}
	w.Header().Set("Ping-Body-Limit", strconv.Itoa(BodyLimit))
	if out.Created {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "Created")
		return
	}
	io.WriteString(w, "OK")
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	// The path holds the credential: log the error, not the URL.
	h.Log.Error("ping failed", "method", r.Method, "error", err)
	http.Error(w, "Internal error", http.StatusInternalServerError)
}

func (h *Handler) parse(r *http.Request) (store.PingIn, error) {
	in := store.PingIn{
		Kind:       check.Success,
		ExitStatus: -1,
		Method:     r.Method,
		UserAgent:  truncate(r.UserAgent(), 200),
		At:         h.Now().UTC(),
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		in.RemoteAddr = host
	}

	// One trailing slash is allowed, as in Healthchecks.
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ping/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) > 3 || slices.Contains(parts, "") {
		return in, badRequest("Unknown ping path")
	}
	var rest []string
	if u, ok := id.ParseUUID(parts[0]); ok {
		in.CheckUUID, rest = u, parts[1:]
		if len(rest) > 1 {
			return in, badRequest("Unknown ping path")
		}
	} else if pingKey.MatchString(parts[0]) && len(parts) >= 2 && slug.MatchString(parts[1]) && !reserved(parts[1]) {
		in.PingKey, in.Slug, rest = parts[0], parts[1], parts[2:]
		in.Create = r.URL.Query().Get("create") == "1"
	} else {
		return in, badRequest("Unknown ping path")
	}
	if len(rest) == 1 {
		switch rest[0] {
		case "start":
			in.Kind = check.Start
		case "fail":
			in.Kind = check.Fail
		case "log":
			in.Kind = check.Log
		default:
			// Digits only, leading zeros allowed, as Healthchecks reads them.
			n, err := strconv.Atoi(rest[0])
			if err != nil || n > 255 || strings.TrimLeft(rest[0], "0123456789") != "" {
				return in, badRequest("Unknown ping path")
			}
			in.ExitStatus = n
			if n > 0 {
				in.Kind = check.Fail
			}
		}
	}

	q := r.URL.Query()
	if rid := q.Get("rid"); rid != "" {
		u, ok := id.ParseUUID(rid)
		if !ok {
			return in, badRequest("Invalid rid: it must be a UUID")
		}
		in.RID = u
	}
	var err error
	if in.Values, err = values(q["v"], r.Header.Get("Allstill-Values")); err != nil {
		return in, err
	}
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		body, err := io.ReadAll(io.LimitReader(r.Body, BodyLimit+1))
		if err != nil {
			return in, badRequest("Could not read the body")
		}
		if len(body) > BodyLimit {
			body, in.BodyTruncated = body[:BodyLimit], true
		}
		in.Body = body
	}
	return in, nil
}

// values reads `v=name:number` query values and the Allstill-Values header, a
// Structured Field dictionary of numbers (RFC 9651). Query values win.
func values(query []string, header string) (map[string]float64, error) {
	members := strings.Split(header, ",")
	if len(query) > 32 || header != "" && len(members) > 32 {
		return nil, badRequest("Too many values: at most 32")
	}
	vals := map[string]float64{}
	if header != "" {
		for _, member := range members {
			name, num, ok := strings.Cut(strings.TrimSpace(member), "=")
			if !ok {
				return nil, badRequest("Invalid Allstill-Values: each member is name=number")
			}
			f, err := parseValue(name, num)
			if err != nil {
				return nil, err
			}
			vals[name] = f
		}
	}
	for _, v := range query {
		name, num, ok := strings.Cut(v, ":")
		if !ok {
			return nil, badRequest("Invalid value: use v=name:number")
		}
		f, err := parseValue(name, num)
		if err != nil {
			return nil, err
		}
		vals[name] = f
	}
	if len(vals) == 0 {
		return nil, nil
	}
	return vals, nil
}

func parseValue(name, num string) (float64, error) {
	if !valueName.MatchString(name) {
		return 0, badRequest("Invalid value name: lower-case letters, digits and _, starting with a letter")
	}
	if !number.MatchString(num) {
		return 0, badRequest("Invalid value: a decimal number with at most 15 digits before the point and 6 after")
	}
	return strconv.ParseFloat(num, 64)
}

// reserved slugs would read as an action.
func reserved(s string) bool { return s == "start" || s == "fail" || s == "log" }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func (b badRequest) Error() string { return string(b) }
