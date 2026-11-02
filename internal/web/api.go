// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"errors"
	"fmt"
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
	probeCredential
)

// apiOperations is every operation served, with who it is for. A route not listed is
// refused, and a test holds this list to the generated one, so an operation cannot
// be open by accident.
var apiOperations = []struct {
	name, route string
	takes       credentialKinds
}{
	{"GetMe", "GET /api/v1/me", signInCredential},
	{"EndThisToken", "DELETE /api/v1/me/token", signInCredential},
	{"GetProbeChecks", "GET /api/v1/probe/checks", probeCredential},
	{"PostProbeRuns", "POST /api/v1/probe/runs", probeCredential},
	{"GetStatus", "GET /api/v1/status", signInCredential},
	{"ListLog", "GET /api/v1/log", signInCredential},
	{"ListProbes", "GET /api/v1/probes", signInCredential},
	{"ListProjects", "GET /api/v1/projects", signInCredential},
	{"GetProject", "GET /api/v1/projects/{project_id}", signInCredential},
	{"ListChecks", "GET /api/v1/projects/{project_id}/checks", signInCredential},
	{"GetCheck", "GET /api/v1/checks/{check_id}", signInCredential},
	{"ListRuns", "GET /api/v1/checks/{check_id}/runs", signInCredential},
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
				writeProblem(w, problem(http.StatusRequestEntityTooLarge, "Too many runs in one post.", fmt.Sprintf("A post holds at most %d runs and %d bytes.", maxProbeRuns, maxProbeBody)))
				return
			}
			writeProblem(w, problem(http.StatusBadRequest, "The post cannot be read.", ""))
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
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
			s.signInCredential(w, r, next)
		}
	})
}

type callerContextKey struct{}

// caller is who a request came from: for now, a person's sign-in.
type caller struct {
	Access store.Access
	Token  store.PersonToken
}

func (s *Server) signInCredential(w http.ResponseWriter, r *http.Request, next http.Handler) {
	refuse := func() {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, problem(http.StatusUnauthorized, "You are not signed in here, or that sign-in has ended.", "Sign in with allstill login."))
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !store.IsPersonToken(token) {
		refuse()
		return
	}
	a, t, err := s.Store.AccessOfPersonToken(r.Context(), token, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		refuse()
		return
	}
	if err != nil {
		s.Log.Error("reading a sign-in", "error", err)
		writeProblem(w, problem(http.StatusInternalServerError, "Something went wrong on the server.", ""))
		return
	}
	// After the lookup, so that made-up values do not fill it (R42).
	if wait, ok := s.apiLimit.allow(t.ID, s.Now()); !ok {
		tooManyRequests(w, wait, "this sign-in")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerContextKey{}, caller{a, t})))
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
	c := ctx.Value(callerContextKey{}).(caller)
	p, err := a.s.Store.Person(ctx, c.Token.WorkspaceID, c.Token.PersonID)
	if err != nil {
		return nil, err
	}
	scopes := []api.Scope{api.Read}
	if c.Token.Scope == store.ScopeManage {
		scopes = append(scopes, api.Manage)
	}
	me := api.GetMe200JSONResponse{
		Kind: api.Person, Scopes: scopes, ServerVersion: a.s.Version, Projects: []api.MeProject{},
		SignIn: &api.MeSignIn{Id: c.Token.ID, Name: c.Token.Name},
		Person: &api.MePerson{Id: p.ID, Name: p.Name, WorkspaceAdmin: &c.Access.Admin},
	}
	if p.Email != "" {
		me.Person.Email = &p.Email
	}
	for _, project := range c.Access.Projects {
		me.Projects = append(me.Projects, api.MeProject{Id: project.ID, Name: project.Name, Role: api.Role(project.Role)})
	}
	return me, nil
}

// EndThisToken ends the sign-in a request came through.
func (a apiServer) EndThisToken(ctx context.Context, _ api.EndThisTokenRequestObject) (api.EndThisTokenResponseObject, error) {
	c := ctx.Value(callerContextKey{}).(caller)
	err := a.s.Store.EndPersonToken(ctx, c.Access.Actor, c.Token.WorkspaceID, c.Token.PersonID, c.Token.ID, a.s.Now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return api.EndThisToken204Response{}, nil
}
