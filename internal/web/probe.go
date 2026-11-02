// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// The probe's two endpoints (intent/http-checks/spec.md), on the interface
// generated from the API description, with the probe's key as bearer.

const (
	maxProbeBody = 1 << 20 // bytes
	maxProbeRuns = 500
)

type probeContextKey struct{}

func problem(status int, title, detail string) api.Problem {
	p := api.Problem{Status: status, Title: title}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

func writeProblem(w http.ResponseWriter, p api.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	json.NewEncoder(w).Encode(p)
}

// probeKey finds the probe by the key it sends, and limits what it may post.
func (s *Server) probeKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, problem(http.StatusUnauthorized, "This needs a probe's key.", ""))
			return
		}
		p, err := s.Store.ProbeByKey(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, problem(http.StatusUnauthorized, "This is not a probe's key.", "It may have been replaced: allstill admin rekey-probe prints a new one."))
			return
		}
		if err != nil {
			s.Log.Error("probe key", "error", err)
			writeProblem(w, problem(http.StatusInternalServerError, "Something went wrong on the server.", ""))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxProbeBody)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), probeContextKey{}, p)))
	})
}

// otherProtocol says what a probe of another protocol version must do, or
// returns nil when it speaks this one.
func otherProtocol(version int) *api.Problem {
	if version == api.ProbeProtocolVersion {
		return nil
	}
	p := problem(http.StatusUpgradeRequired, "This probe speaks another protocol than the server.",
		fmt.Sprintf("The probe speaks version %d and the server version %d. Run the same version of Allstill on both.", version, api.ProbeProtocolVersion))
	return &p
}

// GetProbeChecks returns the checks a probe runs.
func (a apiServer) GetProbeChecks(ctx context.Context, req api.GetProbeChecksRequestObject) (api.GetProbeChecksResponseObject, error) {
	if p := otherProtocol(req.Params.AllstillProbeProtocol); p != nil {
		return api.GetProbeChecks426ApplicationProblemPlusJSONResponse(*p), nil
	}
	probe := ctx.Value(probeContextKey{}).(store.Probe)
	checks, err := a.s.Store.ChecksOfProbe(ctx, probe.ID)
	if err != nil {
		return nil, err
	}
	etag := store.ProbeETag(checks)
	if req.Params.IfNoneMatch != nil && *req.Params.IfNoneMatch == etag {
		return api.GetProbeChecks304Response{}, nil
	}
	body := api.ProbeChecks{Protocol: api.ProbeProtocolVersion, ServerTime: a.s.Now().UTC(), Checks: []api.ProbeCheck{}}
	for _, c := range checks {
		revision := c.Revision
		out := api.ProbeCheck{Id: c.ID, Kind: api.CheckKind(c.Kind), Interval: int64(c.Interval / time.Second), Revision: &revision}
		switch c.Kind {
		case "http":
			err = json.Unmarshal(c.Settings, &out.Http)
		case "tcp":
			err = json.Unmarshal(c.Settings, &out.Tcp)
		}
		if err != nil {
			// The store checks settings as they are saved; leave out one that
			// cannot be read, so the others still run.
			a.s.Log.Error("a check's settings cannot be read", "check", c.ID, "error", err)
			continue
		}
		body.Checks = append(body.Checks, out)
	}
	return api.GetProbeChecks200JSONResponse{Body: body, Headers: api.GetProbeChecks200ResponseHeaders{ETag: &etag}}, nil
}

// PostProbeRuns takes a probe's runs, and that it is alive.
func (a apiServer) PostProbeRuns(ctx context.Context, req api.PostProbeRunsRequestObject) (api.PostProbeRunsResponseObject, error) {
	if p := otherProtocol(req.Params.AllstillProbeProtocol); p != nil {
		return api.PostProbeRuns426ApplicationProblemPlusJSONResponse(*p), nil
	}
	bad := func(detail string) (api.PostProbeRunsResponseObject, error) {
		return api.PostProbeRuns400ApplicationProblemPlusJSONResponse{ProbeProblemApplicationProblemPlusJSONResponse: api.ProbeProblemApplicationProblemPlusJSONResponse(problem(http.StatusBadRequest, "The post is not valid.", detail))}, nil
	}
	if req.Body == nil || req.Body.SentAt.IsZero() {
		return bad("It needs sent_at and runs.")
	}
	if len(req.Body.Runs) > maxProbeRuns {
		return api.PostProbeRuns413ApplicationProblemPlusJSONResponse(problem(http.StatusRequestEntityTooLarge, "Too many runs in one post.", fmt.Sprintf("A post holds at most %d runs.", maxProbeRuns))), nil
	}
	// A run that is not valid is ignored, not the post: it also says the probe
	// is alive, and holds the runs of other checks.
	probe := ctx.Value(probeContextKey{}).(store.Probe)
	runs := make([]store.ProbeRun, 0, len(req.Body.Runs))
	notValid, firstWhy := 0, ""
	for _, r := range req.Body.Runs {
		var reason string
		if r.Reason != nil {
			reason = *r.Reason
		}
		var values map[string]float64
		if r.Values != nil {
			values = *r.Values
		}
		why := ""
		switch {
		case r.Id == "" || utf8.RuneCountInString(r.Id) > 64:
			why = "id"
		case !r.Outcome.Valid():
			why = "outcome"
		case utf8.RuneCountInString(reason) > 500:
			why = "reason"
		case len(values) > 10:
			why = "values"
		case r.EndedAgo < 0:
			why = "ended_ago"
		}
		if why != "" {
			notValid++
			if firstWhy == "" {
				firstWhy = why
			}
			continue
		}
		ago := time.Duration(min(r.EndedAgo, int64(time.Duration(1<<62)/time.Second))) * time.Second
		var revision string
		if r.Revision != nil {
			revision = *r.Revision
		}
		runs = append(runs, store.ProbeRun{ID: r.Id, CheckID: r.CheckId, Revision: revision, Outcome: check.Outcome(r.Outcome), Reason: reason, EndedAgo: ago, Values: values})
	}
	if notValid > 0 {
		a.s.Log.Warn("a probe sent runs that are not valid", "probe", probe.Name, "runs", notValid, "first_problem", firstWhy)
	}
	now := a.s.Now()
	res, err := a.s.Store.ApplyProbeRuns(ctx, probe, runs, req.Body.SentAt, now)
	if err != nil {
		return nil, err
	}
	if !res.Deadline.IsZero() && a.s.Nudge != nil {
		a.s.Nudge(res.Deadline)
	}
	out := api.PostProbeRuns200JSONResponse{ServerTime: now.UTC(), Applied: res.Applied, Ignored: res.Ignored + notValid}
	if res.ClockOff != 0 {
		off := int64(res.ClockOff / time.Second)
		out.ClockOff = &off
		a.s.Log.Warn("a probe's clock is off, so its runs are not judged", "probe", probe.Name, "seconds", off)
	}
	return out, nil
}
