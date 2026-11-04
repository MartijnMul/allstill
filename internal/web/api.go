// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/store"
)

// The JSON API, on the one server generated from the API description. Each request
// is turned into a credential before anything else happens to it, from its bearer
// value alone: no cookie is read here, so a web page cannot use someone's session
// against it (intent/cli-anywhere/spec.md, R38).

// credentialKinds says which kinds of credential an operation takes.
type credentialKinds int

const (
	signInCredential credentialKinds = 1 << iota
	keyCredential
	probeCredential

	// anyCredential is a person's sign-in or a project's key: what either may do with
	// it is decided by the access it holds.
	anyCredential = signInCredential | keyCredential
)

// apiOperations is every operation served, with who it is for. A route not listed is
// refused, and a test holds this list to the generated one, so an operation cannot
// be open by accident.
var apiOperations = []struct {
	name, route string
	takes       credentialKinds
}{
	{"GetMe", "GET /api/v1/me", anyCredential},
	{"EndThisToken", "DELETE /api/v1/me/token", signInCredential},
	{"GetProbeChecks", "GET /api/v1/probe/checks", probeCredential},
	{"PostProbeRuns", "POST /api/v1/probe/runs", probeCredential},
	{"GetStatus", "GET /api/v1/status", anyCredential},
	{"ListLog", "GET /api/v1/log", anyCredential},
	{"ListProbes", "GET /api/v1/probes", anyCredential},
	{"ListProjects", "GET /api/v1/projects", anyCredential},
	{"GetProject", "GET /api/v1/projects/{project_id}", anyCredential},
	{"ListChecks", "GET /api/v1/projects/{project_id}/checks", anyCredential},
	{"CreateCheck", "POST /api/v1/projects/{project_id}/checks", anyCredential},
	{"GetCheck", "GET /api/v1/checks/{check_id}", anyCredential},
	{"UpdateCheck", "PATCH /api/v1/checks/{check_id}", anyCredential},
	{"DeleteCheck", "DELETE /api/v1/checks/{check_id}", anyCredential},
	{"PauseCheck", "POST /api/v1/checks/{check_id}/pause", anyCredential},
	{"ResumeCheck", "POST /api/v1/checks/{check_id}/resume", anyCredential},
	{"MoveCheck", "POST /api/v1/checks/{check_id}/move", anyCredential},
	{"GetPingUrl", "GET /api/v1/checks/{check_id}/ping-url", anyCredential},
	{"RotatePingUrl", "POST /api/v1/checks/{check_id}/ping-url", anyCredential},
	{"ListRuns", "GET /api/v1/checks/{check_id}/runs", anyCredential},
	{"ListKeys", "GET /api/v1/keys", signInCredential},
	{"CreateKey", "POST /api/v1/projects/{project_id}/keys", signInCredential},
	{"RevokeKey", "DELETE /api/v1/keys/{key_id}", signInCredential},
}

// apiBodies are the requests that hold a body, by what they read it as, so that a field
// the server does not know is refused rather than ignored (manage-checks R23): sent
// misspelled along with a whole `http`, it would otherwise lose what it meant to set.
var apiBodies = map[string]func() any{
	"POST /api/v1/projects/{project_id}/checks": func() any { return &api.CheckInput{} },
	"PATCH /api/v1/checks/{check_id}":           func() any { return &api.CheckPatch{} },
	"POST /api/v1/checks/{check_id}/move":       func() any { return &api.MoveCheckJSONRequestBody{} },
	"POST /api/v1/projects/{project_id}/keys":   func() any { return &api.CreateKeyJSONRequestBody{} },
}

// maxAPIBody caps what a request can hold, before anything reads it.
const maxAPIBody = 1 << 20

// apiServer implements the generated server interface.
type apiServer struct{ s *Server }

// registerAPI adds the JSON API to mux.
func (s *Server) registerAPI(mux *http.ServeMux) {
	strict := api.NewStrictHandlerWithOptions(apiServer{s}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				if !strings.HasPrefix(r.URL.Path, "/api/v1/probe/") {
					writeProblem(w, problem(http.StatusRequestEntityTooLarge, "That request is too large.", fmt.Sprintf("A request holds at most %d bytes.", maxAPIBody)))
					return
				}
				writeProblem(w, problem(http.StatusRequestEntityTooLarge, "Too many runs in one post.", fmt.Sprintf("A post holds at most %d runs and %d bytes.", maxProbeRuns, maxProbeBody)))
				return
			}
			writeProblem(w, problem(http.StatusBadRequest, "The post cannot be read.", ""))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if p, ok := refusalOf(r.Context(), err); ok {
				writeProblem(w, p)
				return
			}
			s.Log.Error("API request", "path", r.URL.Path, "error", err)
			writeProblem(w, problem(http.StatusInternalServerError, "Something went wrong on the server.", ""))
		},
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseURL: "/api/v1", BaseRouter: mux, Middlewares: []api.MiddlewareFunc{s.credential},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			detail := ""
			if strings.HasPrefix(r.URL.Path, "/api/v1/probe/") {
				detail = "It needs the header Allstill-Probe-Protocol."
			}
			writeProblem(w, problem(http.StatusBadRequest, "The request cannot be read.", detail))
		},
	})
}

// credential finds who is asking, by what the operation takes.
func (s *Server) credential(next http.Handler) http.Handler {
	takes := map[string]credentialKinds{}
	for _, op := range apiOperations {
		takes[op.route] = op.takes
	}
	probe := s.probeKey(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch kinds, ok := takes[r.Pattern]; {
		case !ok:
			s.Log.Error("an API operation lists no credentials", "route", r.Pattern)
			writeProblem(w, problem(http.StatusInternalServerError, "Something went wrong on the server.", ""))
		case kinds == probeCredential:
			probe.ServeHTTP(w, r)
		default:
			s.personOrKey(w, r, kinds, next)
		}
	})
}

type callerContextKey struct{}

// caller is who a request came from: a person's sign-in, or a project's key, whichever
// of Token and Key is set.
type caller struct {
	Access store.Access
	Token  store.PersonToken
	Key    store.CurrentKey
}

// id names the credential, for what is limited per credential.
func (c caller) id() string {
	if c.Key.ID != "" {
		return c.Key.ID
	}
	return c.Token.ID
}

// scope is what the credential was given.
func (c caller) scope() string {
	if c.Key.ID != "" {
		return c.Key.Scope
	}
	return c.Token.Scope
}

// what names the credential, in a sentence.
func (c caller) what() string {
	if c.Key.ID != "" {
		return "this key"
	}
	return "this sign-in"
}

// personOrKey finds who is asking, from a sign-in or a project's key, and refuses
// whichever of those the operation does not take.
func (s *Server) personOrKey(w http.ResponseWriter, r *http.Request, kinds credentialKinds, next http.Handler) {
	refuse := func() {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, problem(http.StatusUnauthorized, "You are not signed in here, or that sign-in or key has ended.", "Sign in with allstill login."))
	}
	failed := func(err error) {
		s.Log.Error("reading a credential", "error", err)
		writeProblem(w, problem(http.StatusInternalServerError, "Something went wrong on the server.", ""))
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		refuse()
		return
	}
	var who caller
	var err error
	switch {
	case store.IsPersonToken(token):
		who.Access, who.Token, err = s.Store.AccessOfPersonToken(r.Context(), token, s.Now())
	case store.IsAPIKey(token):
		who.Access, who.Key, err = s.Store.AccessOfAPIKey(r.Context(), token, s.Now())
	default:
		refuse()
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		refuse()
		return
	}
	if err != nil {
		failed(err)
		return
	}
	// A key can never make a key or end a sign-in, so a leaked one stays a leak of one
	// project (spec, Design).
	if who.Key.ID != "" && kinds&keyCredential == 0 {
		writeProblem(w, problem(http.StatusForbidden, "A key cannot do this.", "It takes a person's own sign-in: use allstill login."))
		return
	}
	// After the lookup, so that made-up values do not fill it (R42).
	if wait, ok := s.apiLimit.allow(who.id(), s.Now()); !ok {
		tooManyRequests(w, wait, who.what())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
	if newBody, ok := apiBodies[r.Pattern]; ok && !knownFieldsOnly(w, r, newBody()) {
		return
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerContextKey{}, who)))
}

// knownFieldsOnly refuses a body with a field the description does not have, saying
// which, and leaves the body for the handler. What cannot be read at all is the
// handler's to say.
func knownFieldsOnly(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			writeProblem(w, problem(http.StatusRequestEntityTooLarge, "That request is too large.", fmt.Sprintf("A request holds at most %d bytes.", maxAPIBody)))
		} else {
			writeProblem(w, problem(http.StatusBadRequest, "The request cannot be read.", ""))
		}
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	err = d.Decode(into)
	if err == nil {
		return true
	}
	field, unknown := strings.CutPrefix(err.Error(), "json: unknown field ")
	if !unknown {
		return true
	}
	writeProblem(w, problem(http.StatusUnprocessableEntity, "That cannot be saved.", "Allstill does not know the field "+field+", so nothing was changed."))
	return false
}

// tooManyRequests says a credential has asked too often, and how long to wait.
func tooManyRequests(w http.ResponseWriter, wait int, what string) {
	w.Header().Set("Retry-After", strconv.Itoa(wait))
	writeProblem(w, problem(http.StatusTooManyRequests, "Too many requests with "+what+".",
		fmt.Sprintf("It gets %d a minute. Try again in %s.", apiRequestsPerMinute, plural(wait, "second", "seconds"))))
}

// apiRequestsPerMinute is a credential's limit: a brake on a loop in a script, so it
// cannot slow pings down. A restart resets it, which is fine for a brake.
const apiRequestsPerMinute = 120

// apiLimiter keeps a token bucket per credential. Only real credentials get one, so
// it holds no more than there are.
type apiLimiter struct {
	mu      sync.Mutex
	buckets map[string]*apiBucket
}

type apiBucket struct {
	tokens float64
	at     time.Time
}

// allow takes a token for a credential. Without one, it says how many seconds to wait.
func (l *apiLimiter) allow(id string, now time.Time) (wait int, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*apiBucket{}
	}
	b := l.buckets[id]
	if b == nil {
		b = &apiBucket{tokens: apiRequestsPerMinute, at: now}
		l.buckets[id] = b
	}
	perSecond := float64(apiRequestsPerMinute) / 60
	b.tokens = min(apiRequestsPerMinute, b.tokens+now.Sub(b.at).Seconds()*perSecond)
	b.at = now
	if b.tokens < 1 {
		return int(math.Ceil((1 - b.tokens) / perSecond)), false
	}
	b.tokens--
	return 0, true
}

// GetMe says who is asking, on which version of the server.
func (a apiServer) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	c := callerOf(ctx)
	scopes := []api.Scope{api.Read}
	if c.scope() == store.ScopeManage {
		scopes = append(scopes, api.Manage)
	}
	me := api.GetMe200JSONResponse{Kind: api.MeKindPerson, Scopes: scopes, ServerVersion: a.s.Version, Projects: []api.MeProject{}}
	if c.Key.ID != "" {
		me.Kind = api.MeKindApiKey
		me.Key = &api.MeKey{Id: c.Key.ID, Name: c.Key.Name}
	} else {
		p, err := a.s.Store.Person(ctx, c.Token.WorkspaceID, c.Token.PersonID)
		if err != nil {
			return nil, err
		}
		me.SignIn = &api.MeSignIn{Id: c.Token.ID, Name: c.Token.Name}
		me.Person = &api.MePerson{Id: p.ID, Name: p.Name, WorkspaceAdmin: &c.Access.Admin}
		if p.Email != "" {
			me.Person.Email = &p.Email
		}
	}
	for _, project := range c.Access.Projects {
		me.Projects = append(me.Projects, api.MeProject{Id: project.ID, Name: project.Name, Role: api.Role(project.Role)})
	}
	return me, nil
}

// EndThisToken ends the sign-in a request came through.
func (a apiServer) EndThisToken(ctx context.Context, _ api.EndThisTokenRequestObject) (api.EndThisTokenResponseObject, error) {
	c := callerOf(ctx)
	err := a.s.Store.EndPersonToken(ctx, c.Access.Actor, c.Token.WorkspaceID, c.Token.PersonID, c.Token.ID, a.s.Now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return api.EndThisToken204Response{}, nil
}
