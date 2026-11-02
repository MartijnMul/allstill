// SPDX-License-Identifier: AGPL-3.0-only

// Package web serves the dashboard: signing in with a passkey, the overview of
// every check a person may see, and a check's detail. Pages are rendered on
// the server; a little plain JavaScript signs in and copies ping URLs.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/MartijnMul/allstill/internal/active"
	"github.com/MartijnMul/allstill/internal/alert"
	"github.com/MartijnMul/allstill/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server serves the dashboard.
type Server struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
	// PushKey is the public VAPID key browsers subscribe for (ADR 0003).
	PushKey string
	// Wake makes the sender look at once, for a test message.
	Wake func()
	// Behind says how far the sender lags (R11).
	Behind func(context.Context) (time.Duration, error)
	// MailOn says whether the server can send email, and MailProblem
	// whether it fails as it is set up.
	MailOn      bool
	MailProblem func() *alert.MailProblem
	// Offline says whether the server is offline for the checks it runs,
	// and since when (intent/http-checks/spec.md, R7).
	Offline func() (time.Time, bool)
	// Nudge tells the scheduler of a new deadline, from a probe's post or a form.
	Nudge func(time.Time)
	// Allow holds the private ranges the server's checks may reach, and Try makes
	// one attempt, to say what a new address finds (intent/manage-checks/spec.md, R4).
	Allow []netip.Prefix
	Try   func(context.Context, active.Check) active.Result
	// Version is this program's, which the API tells a command line (R45).
	Version string

	base       *url.URL
	workspace  string
	secure     bool // served over HTTPS, so cookies are Secure and __Host-
	wa         *webauthn.WebAuthn
	ceremonies *ceremonies
	pages      map[string]*template.Template
	memory     *memory
	grants     *grants
}

// ParseBaseURL checks the address people use to reach Allstill: https, or
// http on localhost, with a host name and nothing after the host.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("the base URL %q cannot be read: %w", raw, err)
	}
	// A passkey belongs to its host name exactly, as authenticators hash it.
	u.Host = strings.ToLower(u.Host)
	host := u.Hostname()
	switch {
	case u.Scheme != "https" && !(u.Scheme == "http" && host == "localhost"):
		return nil, fmt.Errorf("the base URL %q must use https; passkeys only work over http on localhost", raw)
	case host == "" || net.ParseIP(host) != nil:
		return nil, fmt.Errorf("the base URL %q must name a host, not an IP address: passkeys belong to a host name", raw)
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return nil, fmt.Errorf("the base URL %q must be only a scheme and a host, such as https://allstill.example.com", raw)
	}
	return u, nil
}

// New returns a dashboard for the workspace, reached at base.
func New(st *store.Store, workspaceID string, base *url.URL, log *slog.Logger) (*Server, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:                  base.Hostname(),
		RPDisplayName:         "Allstill",
		RPOrigins:             []string{base.String()},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			// A passkey is the only factor, so the device must check it is
			// its owner: a PIN, a fingerprint or a face.
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Timeout: ceremonyLifetime},
			Registration: webauthn.TimeoutConfig{Timeout: ceremonyLifetime},
		},
	})
	if err != nil {
		return nil, err
	}
	s := &Server{
		Store: st, Log: log, Now: time.Now,
		base: base, workspace: workspaceID, secure: base.Scheme == "https",
		wa: wa, ceremonies: newCeremonies(), pages: map[string]*template.Template{}, memory: newMemory(), grants: newGrants(),
	}
	layout := template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/parts.html"))
	for _, name := range []string{"login", "setup", "overview", "check", "active", "messages", "log", "channels", "device", "error", "check-new", "check-edit", "check-confirm"} {
		t := template.Must(layout.Clone())
		s.pages[name] = template.Must(t.ParseFS(templateFS, "templates/"+name+".html"))
	}
	return s, nil
}

// Register adds the dashboard's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	// Requests that change something must come from the dashboard itself.
	cop := http.NewCrossOriginProtection()
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.headers(cop.Handler(h)))
	}
	handle("GET /{$}", s.signedIn(s.overview))
	handle("GET /checks/{id}", s.signedIn(s.checkPage))
	handle("GET /checks/new", s.signedIn(s.newCheckPage))
	handle("POST /checks/new", s.signedIn(s.addCheck))
	handle("GET /checks/{id}/edit", s.signedIn(s.editCheckPage))
	handle("POST /checks/{id}/edit", s.signedIn(s.saveCheck))
	for _, action := range []string{"pause", "move", "delete", "ping-url"} {
		handle("GET /checks/{id}/"+action, s.signedIn(s.confirmPage(action)))
	}
	handle("POST /checks/{id}/pause", s.signedIn(s.pauseCheck))
	handle("POST /checks/{id}/resume", s.signedIn(s.resumeCheck))
	handle("POST /checks/{id}/move", s.signedIn(s.moveCheck))
	handle("POST /checks/{id}/delete", s.signedIn(s.removeCheck))
	handle("POST /checks/{id}/ping-url", s.signedIn(s.replacePingURL))
	handle("GET /messages", s.signedIn(s.messagesPage))
	handle("GET /log", s.signedIn(s.logPage))
	handle("GET /channels", s.signedIn(s.channelsPage))
	handle("POST /channels/push", s.signedIn(s.savePush))
	handle("POST /channels/email", s.signedIn(s.addEmail))
	handle("POST /channels/{id}/test", s.signedIn(s.testChannel))
	handle("GET /channels/{id}/confirm", s.signedIn(s.confirmChannel))
	handle("POST /channels/{id}/delete", s.signedIn(s.deleteChannel))
	handle("POST /sign-ins/{id}/end", s.signedIn(s.endSignIn))
	handle("POST /sessions/{id}/end", s.signedIn(s.endBrowserSession))
	handle("GET /device", s.signedInToDevice(s.devicePage))
	handle("POST /device", s.signedInToDevice(s.deviceConfirm))
	handle("POST /device/deny", s.signedInToDevice(s.deviceDeny))
	handle("POST /device/begin", s.signedInJSON(s.deviceBegin))
	handle("POST /device/finish", s.signedInJSON(s.deviceFinish))
	handle("POST /oauth/device_authorization", s.deviceAuthorization)
	handle("POST /oauth/token", s.oauthToken)
	handle("GET /health", s.health)
	handle("GET /login", s.loginPage)
	handle("POST /login/begin", s.loginBegin)
	handle("POST /login/finish", s.loginFinish)
	handle("POST /logout", s.logout)
	handle("GET /setup", s.setupPage)
	handle("POST /setup/link", s.setupLink)
	handle("POST /setup/begin", s.setupBegin)
	handle("POST /setup/finish", s.setupFinish)
	static, _ := fs.Sub(staticFS, "static")
	// The service worker lives at the root, so it covers every page.
	mux.Handle("GET /sw.js", s.headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "sw.js")
	})))
	mux.Handle("GET /static/", s.headers(http.StripPrefix("/static/", noListing(http.FileServerFS(static)))))
	s.registerAPI(mux)
}

// headers sets what every dashboard response carries: no inline script or
// style, no framing, no referrer, and nothing cached that belongs to a person.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		static := strings.HasPrefix(r.URL.Path, "/static/")
		if !static {
			h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; "+
				"connect-src 'self'; worker-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if s.secure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if static {
			h.Set("Cache-Control", "no-cache")
		} else {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func noListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cookie names a cookie. Over HTTPS the __Host- prefix keeps it to this host
// and path /, so no other site or subdomain can set or read it.
func (s *Server) cookie(name string) string {
	if s.secure {
		return "__Host-allstill-" + name
	}
	return "allstill-" + name
}

var errSignedOut = errors.New("signed out")

func (s *Server) session(r *http.Request) (store.Session, error) {
	c, err := r.Cookie(s.cookie("session"))
	if err != nil || c.Value == "" {
		return store.Session{}, errSignedOut
	}
	v, err := s.Store.Session(r.Context(), c.Value, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, errSignedOut
	}
	return v, err
}

// viewer is the signed-in person a page is for.
type viewer struct {
	store.Person
	Access store.Access
	Loc    *time.Location
}

func (s *Server) signedIn(next func(http.ResponseWriter, *http.Request, viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.viewer(r)
		if errors.Is(err, errSignedOut) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r, v)
	}
}

func (s *Server) viewer(r *http.Request) (viewer, error) {
	sess, err := s.session(r)
	if err != nil {
		return viewer{}, err
	}
	p, err := s.Store.Person(r.Context(), sess.WorkspaceID, sess.PersonID)
	if errors.Is(err, store.ErrNotFound) {
		return viewer{}, errSignedOut
	}
	if err != nil {
		return viewer{}, err
	}
	a, err := s.Store.AccessOfPerson(r.Context(), sess.WorkspaceID, sess.PersonID)
	if err != nil {
		return viewer{}, err
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	return viewer{Person: p, Access: a, Loc: loc}, nil
}

// page is what every page's layout needs.
type page struct {
	Title   string
	Person  string // the signed-in person's name, or "" on the sign-in pages
	PushKey string // set on signed-in pages, so the page can renew this device's subscription
	Crumbs  []crumb
	Search  string
	Here    string   // the sidebar link this page is: messages or devices
	Nav     *navData // the sidebar's views and projects, on signed-in pages
	Live    bool     // the page refreshes itself
	Data    any
}

type crumb struct{ Name, Href string }

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	if p.Person != "" {
		p.PushKey = s.PushKey
	}
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", p); err != nil {
		s.Log.Error("rendering a page", "page", name, "error", err)
		http.Error(w, "Something went wrong on the server.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	buf.WriteTo(w)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "error", page{Title: "Not found", Data: "There is nothing here, or you may not see it."})
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		s.render(w, r, http.StatusForbidden, "error", page{Title: "You may not change this", Data: "You can look at this project, but not change it. To change it, ask your administrator or its owner for more access."})
		return
	}
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		s.render(w, r, http.StatusUnprocessableEntity, "error", page{Title: "This cannot be saved", Data: sentence(inv.Problem)})
		return
	}
	s.Log.Error("serving a page", "path", r.URL.Path, "error", err)
	s.render(w, r, http.StatusInternalServerError, "error", page{Title: "Something went wrong", Data: "Something went wrong on the server. Try again in a moment."})
}
