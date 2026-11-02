// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/MartijnMul/allstill/internal/store"
)

// Signing in from the command line: the device grant of RFC 8628, for one client,
// with no client secret, no other grant and no registration
// (intent/cli-anywhere/spec.md). Someone approves a code on /device, in any
// browser, with a passkey ceremony of its own: unlike signing in, it takes only
// the signed-in person's own passkey, is bound to one grant and scope, and opens no
// session. The sign-in is made when it is approved, so its person sees it from then
// on and can end it, and whoever asked has a minute to fetch it. The code is typed and never read from the address, so a link cannot
// arrive with it filled in (C3).

const (
	// clientID is the one client there is: `allstill login`.
	clientID = "allstill"
	// deviceGrantType is RFC 8628's name for what a poll asks for.
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	maxOAuthBody    = 4 << 10
	maxDeviceName   = 100
	maxTypedCode    = 32
	// unnamedDevice is what a command line that gave no name is called.
	unnamedDevice = "unnamed"
)

func oauthError(w http.ResponseWriter, code int, name, description string) {
	body := map[string]string{"error": name}
	if description != "" {
		body["error_description"] = description
	}
	writeJSON(w, code, body)
}

// oauthForm reads a small form body, from the body alone. It says false when it
// answered, and so did the caller.
func oauthForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "The request cannot be read.")
		return nil, false
	}
	if r.PostForm.Get("client_id") != clientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "Only `allstill login` can sign in here.")
		return nil, false
	}
	return r.PostForm, true
}

// cleanDeviceName keeps what a command line says it is called to one short line,
// without what could steer a terminal or turn text around on a page. It is still
// only its own claim.
func cleanDeviceName(name string) string {
	name = strings.Map(func(c rune) rune {
		if unicode.In(c, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co) {
			return -1
		}
		return c
	}, strings.ToValidUTF8(name, ""))
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxDeviceName {
		name = strings.TrimSpace(string([]rune(name)[:maxDeviceName]))
	}
	if name == "" {
		return unnamedDevice
	}
	return name
}

// POST /oauth/device_authorization: a command line asks to sign someone in. Anyone
// can ask, and it writes nothing.
func (s *Server) deviceAuthorization(w http.ResponseWriter, r *http.Request) {
	form, ok := oauthForm(w, r)
	if !ok {
		return
	}
	scope := form.Get("scope")
	switch scope {
	case "":
		scope = store.ScopeManage
	case store.ScopeRead, store.ScopeManage:
	default:
		oauthError(w, http.StatusBadRequest, "invalid_scope", "A sign-in is read or manage.")
		return
	}
	gr, ok := s.grants.ask(cleanDeviceName(form.Get("device_name")), scope, s.Now())
	if !ok {
		w.Header().Set("Retry-After", "60")
		oauthError(w, http.StatusTooManyRequests, "too_many_requests", "Too many sign-ins are being asked for. Try again in a moment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      gr.deviceCode,
		"user_code":        showUserCode(gr.userCode),
		"verification_uri": s.base.String() + "/device",
		"expires_in":       int(grantLifetime / time.Second),
		"interval":         int(pollInterval / time.Second),
	})
}

// POST /oauth/token: a command line asks what became of its code.
func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	form, ok := oauthForm(w, r)
	if !ok {
		return
	}
	if form.Get("grant_type") != deviceGrantType {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
		return
	}
	gr, err := s.grants.poll(form.Get("device_code"), s.Now())
	if err != nil {
		oauthError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access_token": gr.token, "token_type": "Bearer", "scope": gr.scope})
}

type deviceData struct {
	Problem  string
	Code     string // as it was typed or shown
	Name     string
	Asked    string
	ReadOnly bool
	Denied   bool
}

// signedInToDevice is signedIn for the approval page, which someone signed out comes
// back to after signing in.
func (s *Server) signedInToDevice(next func(http.ResponseWriter, *http.Request, viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.viewer(r)
		if errors.Is(err, errSignedOut) {
			http.Redirect(w, r, "/login?next=device", http.StatusSeeOther)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r, v)
	}
}

// signedInJSON is signedIn for what a page's script posts.
func (s *Server) signedInJSON(next func(http.ResponseWriter, *http.Request, viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.viewer(r)
		if errors.Is(err, errSignedOut) {
			jsonError(w, http.StatusUnauthorized, "You are no longer signed in here. Sign in and enter the code again.")
			return
		}
		if err != nil {
			s.Log.Error("reading who is signed in", "error", err)
			jsonError(w, http.StatusInternalServerError, msgTryAgain)
			return
		}
		next(w, r, v)
	}
}

func (s *Server) renderDevice(w http.ResponseWriter, r *http.Request, code int, d deviceData) {
	s.render(w, r, code, "device", page{Title: "Sign in from elsewhere", Data: d})
}

// GET /device. Whatever the address holds is not read, so a link cannot arrive with
// a code filled in (C3).
func (s *Server) devicePage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderDevice(w, r, http.StatusOK, deviceData{})
}

var (
	errTooManyCodes = errors.New("too many codes")
	errNoSuchCode   = errors.New("no such code")
)

const (
	msgTooManyCodes = "That was too many tries. Wait a minute, then enter the code again."
	msgNoSuchCode   = "That code does not work. It may have expired or been used: run allstill login again for a new one."
)

// enteredCode finds the grant a person's code names, within their tries: a wrong
// code counts, and past the limit even a right one is refused (R3).
func (s *Server) enteredCode(v viewer, typed string) (grant, error) {
	var gr grant
	var found bool
	room := s.withinUnless("device|"+v.ID, wrongCodesPerMinute, func() bool {
		gr, found = s.grants.waiting(typed, s.Now())
		return found
	})
	switch {
	case !room:
		return grant{}, errTooManyCodes
	case !found:
		return grant{}, errNoSuchCode
	}
	return gr, nil
}

// typedCode reads a code from a form's body alone, never the address, kept to what a
// code could be, as the page shows it again.
func typedCode(w http.ResponseWriter, r *http.Request) string {
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	typed := strings.ToValidUTF8(r.PostFormValue("code"), "")
	if len(typed) > maxTypedCode {
		return strings.ToValidUTF8(typed[:maxTypedCode], "")
	}
	return typed
}

func codeProblem(err error) (status int, message string) {
	if errors.Is(err, errTooManyCodes) {
		return http.StatusTooManyRequests, msgTooManyCodes
	}
	return http.StatusBadRequest, msgNoSuchCode
}

// POST /device: what the code stands for, before anyone says yes to it.
func (s *Server) deviceConfirm(w http.ResponseWriter, r *http.Request, v viewer) {
	typed := typedCode(w, r)
	gr, err := s.enteredCode(v, typed)
	if err != nil {
		status, message := codeProblem(err)
		s.renderDevice(w, r, status, deviceData{Problem: message, Code: typed})
		return
	}
	s.renderDevice(w, r, http.StatusOK, deviceData{
		Code: showUserCode(gr.userCode), Name: gr.name, Asked: ago(s.Now(), gr.asked), ReadOnly: gr.scope == store.ScopeRead,
	})
}

// POST /device/deny: nobody signs in with this code.
func (s *Server) deviceDeny(w http.ResponseWriter, r *http.Request, v viewer) {
	typed := typedCode(w, r)
	if _, err := s.enteredCode(v, typed); err != nil {
		status, message := codeProblem(err)
		s.renderDevice(w, r, status, deviceData{Problem: message})
		return
	}
	// It may just have been approved elsewhere, and then this must not say it was not.
	if !s.grants.deny(typed, s.Now()) {
		s.renderDevice(w, r, http.StatusBadRequest, deviceData{Problem: msgNoSuchCode})
		return
	}
	s.renderDevice(w, r, http.StatusOK, deviceData{Denied: true})
}

// devicePurpose binds a passkey's answer to one grant, its scope and the person
// approving it, so an answer for one code cannot approve another.
func devicePurpose(gr grant, personID string) string {
	h := sha256.Sum256([]byte(gr.id + "\x00" + gr.scope + "\x00" + personID))
	return "device " + hex.EncodeToString(h[:])
}

// POST /device/begin
func (s *Server) deviceBegin(w http.ResponseWriter, r *http.Request, v viewer) {
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	gr, err := s.enteredCode(v, req.Code)
	if err != nil {
		status, message := codeProblem(err)
		jsonError(w, status, message)
		return
	}
	u, err := s.loadUser(r, v.Person)
	if err != nil {
		s.Log.Error("reading a person's passkeys", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	if len(u.creds) == 0 {
		jsonError(w, http.StatusForbidden, "You have no passkey for this site's address, so you cannot approve this here. Ask your administrator for a new link.")
		return
	}
	ch := s.ceremonies.challenge(devicePurpose(gr, v.ID), s.nonce(w), s.Now())
	options, _, err := s.wa.BeginLogin(u, webauthn.WithChallenge(ch))
	if err != nil {
		s.Log.Error("beginning an approval", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	writeJSON(w, http.StatusOK, options)
}

// POST /device/finish
func (s *Server) deviceFinish(w http.ResponseWriter, r *http.Request, v viewer) {
	nonce := s.readNonce(w, r)
	var req struct {
		Code       string          `json:"code"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	gr, err := s.enteredCode(v, req.Code)
	if err != nil {
		status, message := codeProblem(err)
		jsonError(w, status, message)
		return
	}
	at := s.Now()
	ch, err := base64.RawURLEncoding.DecodeString(parsed.Response.CollectedClientData.Challenge)
	if err != nil || !s.ceremonies.valid(ch, devicePurpose(gr, v.ID), nonce, at) {
		jsonError(w, http.StatusBadRequest, "This took too long or began elsewhere. Try again.")
		return
	}
	u, err := s.loadUser(r, v.Person)
	if err != nil {
		s.Log.Error("reading a person's passkeys", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	_, session, err := s.wa.BeginLogin(u, webauthn.WithChallenge(ch))
	if err != nil {
		jsonError(w, http.StatusForbidden, "That is not a passkey of yours. Approve this with your own.")
		return
	}
	cred, err := s.wa.ValidateLogin(u, *session, parsed)
	if err != nil {
		s.Log.Info("an approval failed", "error", err)
		jsonError(w, http.StatusForbidden, "That is not a passkey of yours. Approve this with your own.")
		return
	}
	if cred.Authenticator.CloneWarning {
		// As with signing in: the counter went backwards, so a copy may exist. Keep the
		// record as it was, so the real one keeps working.
		s.Log.Warn("a passkey's sign counter went backwards; the approval was refused", "person", v.ID)
		if err := s.Store.PasskeyLooksCopied(r.Context(), v.WorkspaceID, v.ID, at); err != nil {
			s.Log.Error("recording a passkey that looks copied", "error", err)
		}
		jsonError(w, http.StatusForbidden, "This passkey looks copied, so it was refused. Use another passkey or ask for a new link.")
		return
	}
	if !s.ceremonies.answer(ch, at) {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	var passkeyID string
	for i, c := range u.creds {
		if bytes.Equal(c.ID, cred.ID) {
			passkeyID = u.keys[i].ID
		}
	}
	record, err := json.Marshal(cred)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	if !s.grants.claim(gr.id, at) {
		jsonError(w, http.StatusBadRequest, msgNoSuchCode)
		return
	}
	token, err := s.Store.CreatePersonToken(r.Context(), store.NewPersonToken{
		WorkspaceID: v.WorkspaceID, PersonID: v.ID, PasskeyID: passkeyID, Record: record, Name: gr.name, Scope: gr.scope,
	}, at)
	if err != nil {
		s.grants.unclaim(gr.id)
		s.Log.Error("making a sign-in", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	s.grants.approved(gr.id, token, at)
	writeJSON(w, http.StatusOK, map[string]bool{"approved": true})
}
