// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
			writeProblem(w, problem(http.StatusBadRequest, "The request cannot be read.", "It needs the header Allstill-Probe-Protocol."))
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerContextKey{}, caller{a, t})))
}

// GetMe says who is asking, on which version of the server.
func (a apiServer) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	c := ctx.Value(callerContextKey{}).(caller)
	p, err := a.s.Store.Person(ctx, c.Access.WorkspaceID, c.Access.PersonID)
	if err != nil {
		return nil, err
	}
	scopes := []api.Scope{api.ScopeRead}
	if c.Token.Scope == store.ScopeManage {
		scopes = append(scopes, api.ScopeManage)
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
	err := a.s.Store.EndPersonToken(ctx, c.Access.Actor, c.Access.WorkspaceID, c.Access.PersonID, c.Token.ID, a.s.Now())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return api.EndThisToken204Response{}, nil
}
