// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/MartijnMul/allstill/internal/store"
)

// A ceremony is one passkey registration or sign-in: the server hands out a
// challenge, the browser has the passkey sign it, and the server checks the
// signature. The challenge carries its own state: 16 random bytes, when it
// expires, and a MAC over those, what the ceremony is for and a nonce in a
// cookie of the browser that began it, with a key that lives only in this
// process. Nothing is stored until a challenge is answered, so the endpoints
// that begin a ceremony, which anyone may call, cost no memory, and an answer
// only counts in the browser that asked for it.
type ceremonies struct {
	key  [32]byte
	mu   sync.Mutex
	used map[string]time.Time // answered challenges, until they expire
}

const (
	ceremonyLifetime = 5 * time.Minute
	macSize          = 16
	challengeSize    = 16 + 8 + macSize
)

func newCeremonies() *ceremonies {
	c := &ceremonies{used: map[string]time.Time{}}
	rand.Read(c.key[:])
	return c
}

func (c *ceremonies) challenge(purpose string, nonce []byte, at time.Time) []byte {
	b := make([]byte, 24, challengeSize)
	rand.Read(b[:16])
	binary.BigEndian.PutUint64(b[16:24], uint64(at.Add(ceremonyLifetime).UnixMilli()))
	return append(b, c.mac(b, purpose, nonce)...)
}

func (c *ceremonies) mac(b []byte, purpose string, nonce []byte) []byte {
	m := hmac.New(sha256.New, c.key[:])
	m.Write(b[:24])
	m.Write([]byte{0})
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write(nonce)
	return m.Sum(nil)[:macSize]
}

// valid says whether this process made the challenge for this purpose and
// browser, and it has not expired.
func (c *ceremonies) valid(ch []byte, purpose string, nonce []byte, at time.Time) bool {
	return len(ch) == challengeSize && len(nonce) > 0 &&
		hmac.Equal(ch[24:], c.mac(ch, purpose, nonce)) &&
		at.UnixMilli() < int64(binary.BigEndian.Uint64(ch[16:24]))
}

// answer marks a challenge as answered, and says false when it was before.
func (c *ceremonies) answer(ch []byte, at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, until := range c.used {
		if !at.Before(until) {
			delete(c.used, k)
		}
	}
	k := string(ch)
	if _, ok := c.used[k]; ok {
		return false
	}
	c.used[k] = time.UnixMilli(int64(binary.BigEndian.Uint64(ch[16:24])))
	return true
}

func withChallenge(ch []byte) webauthn.RegistrationOption {
	return func(o *protocol.PublicKeyCredentialCreationOptions) error {
		o.Challenge = ch
		return nil
	}
}

// user is a person as WebAuthn sees them. Their user handle is their id.
type user struct {
	store.Person
	keys  []store.Passkey
	creds []webauthn.Credential
}

func (u *user) WebAuthnID() []byte                         { return []byte(u.ID) }
func (u *user) WebAuthnName() string                       { return cmp(u.Email, u.Name) }
func (u *user) WebAuthnDisplayName() string                { return u.Name }
func (u *user) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// loadUser returns a person with the passkeys made for this host name.
func (s *Server) loadUser(r *http.Request, p store.Person) (*user, error) {
	keys, err := s.Store.Passkeys(r.Context(), p.WorkspaceID, p.ID)
	if err != nil {
		return nil, err
	}
	u := &user{Person: p}
	for _, k := range keys {
		if k.RPID != s.wa.Config.RPID {
			continue
		}
		var c webauthn.Credential
		if err := json.Unmarshal(k.Record, &c); err != nil || !bytes.Equal(c.ID, k.CredentialID) {
			s.Log.Error("a passkey's record cannot be read", "passkey", k.ID)
			continue
		}
		u.keys = append(u.keys, k)
		u.creds = append(u.creds, c)
	}
	return u, nil
}

// The ceremony nonce lives in its own short cookie.
func (s *Server) nonce(w http.ResponseWriter) []byte {
	n := make([]byte, 16)
	rand.Read(n)
	http.SetCookie(w, &http.Cookie{
		Name: s.cookie("ceremony"), Value: base64.RawURLEncoding.EncodeToString(n), Path: "/",
		MaxAge: int(ceremonyLifetime / time.Second), HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode,
	})
	return n
}

func (s *Server) readNonce(w http.ResponseWriter, r *http.Request) []byte {
	c, err := r.Cookie(s.cookie("ceremony"))
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure})
	n, _ := base64.RawURLEncoding.DecodeString(c.Value)
	return n
}

func setupPurpose(token string) string {
	h := sha256.Sum256([]byte(token))
	return "setup " + hex.EncodeToString(h[:])
}

// jsonError answers a script with a message for the person.
func jsonError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// readJSON reads a small JSON request body.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v)
}

const (
	msgTryAgain    = "That did not work. Try again."
	msgUnknownLink = "This link does not work. Ask for a new one."
)

func linkError(err error) string {
	switch {
	case errors.Is(err, store.ErrLinkUsed):
		return "This link has been used. Ask for a new one if you need to add another passkey."
	case errors.Is(err, store.ErrLinkExpired):
		return "This link has expired. Ask for a new one."
	}
	return msgUnknownLink
}

// GET /login. It can return to the page that approves a sign-in from elsewhere, and
// to nowhere else, so it cannot be used to send someone to another site.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	next := ""
	if r.URL.Query().Get("next") == "device" {
		next = "/device"
	}
	if _, err := s.session(r); err == nil {
		http.Redirect(w, r, cmp(next, "/"), http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", page{Title: "Sign in", Data: next})
}

// POST /login/begin
func (s *Server) loginBegin(w http.ResponseWriter, r *http.Request) {
	ch := s.ceremonies.challenge("login", s.nonce(w), s.Now())
	options, _, err := s.wa.BeginDiscoverableLogin(webauthn.WithChallenge(ch))
	if err != nil {
		s.Log.Error("beginning a sign-in", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	writeJSON(w, http.StatusOK, options)
}

// POST /login/finish
func (s *Server) loginFinish(w http.ResponseWriter, r *http.Request) {
	nonce := s.readNonce(w, r)
	parsed, err := protocol.ParseCredentialRequestResponseBody(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	at := s.Now()
	ch, err := base64.RawURLEncoding.DecodeString(parsed.Response.CollectedClientData.Challenge)
	if err != nil || !s.ceremonies.valid(ch, "login", nonce, at) {
		jsonError(w, http.StatusBadRequest, "This sign-in took too long or began elsewhere. Try again.")
		return
	}
	_, session, err := s.wa.BeginDiscoverableLogin(webauthn.WithChallenge(ch))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	var who *user
	var lookupErr error // a failure of the database, not an unknown person
	find := func(rawID, userHandle []byte) (webauthn.User, error) {
		p, err := s.Store.Person(r.Context(), s.workspace, string(userHandle))
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				lookupErr = err
			}
			return nil, err
		}
		if who, err = s.loadUser(r, p); err != nil {
			lookupErr = err
			return nil, err
		}
		return who, nil
	}
	_, cred, err := s.wa.ValidatePasskeyLogin(find, *session, parsed)
	if lookupErr != nil {
		s.Log.Error("looking up a passkey's person", "error", lookupErr)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	if err != nil {
		s.Log.Info("a sign-in failed", "error", err)
		jsonError(w, http.StatusForbidden, "This passkey is not known here. Sign in with a passkey you made for this site.")
		return
	}
	if cred.Authenticator.CloneWarning {
		// The passkey's counter went backwards: a copy of it may exist. Keep
		// the record as it was, so the real one keeps working.
		s.Log.Warn("a passkey's sign counter went backwards; the sign-in was refused", "person", who.ID)
		if err := s.Store.PasskeyLooksCopied(r.Context(), who.WorkspaceID, who.ID, at); err != nil {
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
	for i, c := range who.creds {
		if bytes.Equal(c.ID, cred.ID) {
			passkeyID = who.keys[i].ID
		}
	}
	record, err := json.Marshal(cred)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	token, err := s.Store.SignIn(r.Context(), who.WorkspaceID, who.ID, passkeyID, record, r.UserAgent(), at)
	if err != nil {
		s.Log.Error("opening a session", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	s.startSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]string{"location": "/"})
}

// startSession replaces the browser's session, if any, with a new one.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, token string) {
	if old, err := r.Cookie(s.cookie("session")); err == nil {
		if err := s.Store.EndSession(r.Context(), old.Value); err != nil {
			s.Log.Error("ending a session", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookie("session"), Value: token, Path: "/", MaxAge: int(store.SessionMax / time.Second),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

// POST /logout
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookie("session")); err == nil {
		if err := s.Store.EndSession(r.Context(), c.Value); err != nil {
			s.Log.Error("ending a session", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookie("session"), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// GET /setup. The link's token is in the URL's fragment, which browsers
// never send, so it stays out of access logs; the page's script posts it.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "setup", page{Title: "Add a passkey"})
}

type setupRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// POST /setup/link: who the link is for, and who is signed in here now.
func (s *Server) setupLink(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgUnknownLink)
		return
	}
	p, err := s.Store.PasskeyLink(r.Context(), req.Token, s.Now())
	if err != nil {
		jsonError(w, http.StatusNotFound, linkError(err))
		return
	}
	answer := map[string]string{"name": p.Name}
	if v, err := s.session(r); err == nil && v.PersonID != p.ID {
		if other, err := s.Store.Person(r.Context(), v.WorkspaceID, v.PersonID); err == nil {
			answer["signed_in_as"] = other.Name
		}
	}
	writeJSON(w, http.StatusOK, answer)
}

// POST /setup/begin
func (s *Server) setupBegin(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgUnknownLink)
		return
	}
	p, err := s.Store.PasskeyLink(r.Context(), req.Token, s.Now())
	if err != nil {
		jsonError(w, http.StatusNotFound, linkError(err))
		return
	}
	u, err := s.loadUser(r, p)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	ch := s.ceremonies.challenge(setupPurpose(req.Token), s.nonce(w), s.Now())
	options, _, err := s.wa.BeginRegistration(u, withChallenge(ch), webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()))
	if err != nil {
		s.Log.Error("beginning a passkey registration", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	writeJSON(w, http.StatusOK, options)
}

// POST /setup/finish, with the link's token, the passkey's name and the new
// credential.
func (s *Server) setupFinish(w http.ResponseWriter, r *http.Request) {
	nonce := s.readNonce(w, r)
	var req struct {
		setupRequest
		Credential json.RawMessage `json:"credential"`
	}
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	token, name := req.Token, strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 100 {
		name = strings.ToValidUTF8(name[:100], "")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	at := s.Now()
	ch, err := base64.RawURLEncoding.DecodeString(parsed.Response.CollectedClientData.Challenge)
	if err != nil || !s.ceremonies.valid(ch, setupPurpose(token), nonce, at) {
		jsonError(w, http.StatusBadRequest, "This took too long or began elsewhere. Try again.")
		return
	}
	p, err := s.Store.PasskeyLink(r.Context(), token, at)
	if err != nil {
		jsonError(w, http.StatusNotFound, linkError(err))
		return
	}
	u, err := s.loadUser(r, p)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	_, session, err := s.wa.BeginRegistration(u, withChallenge(ch))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	cred, err := s.wa.CreateCredential(u, *session, parsed)
	if err != nil {
		s.Log.Info("a passkey registration failed", "error", err)
		jsonError(w, http.StatusBadRequest, "Your device's passkey was not accepted. Try again, or try another device.")
		return
	}
	if !s.ceremonies.answer(ch, at) {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	record, err := json.Marshal(cred)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	sessionToken, err := s.Store.AddPasskey(r.Context(), token, store.NewPasskey{
		CredentialID: cred.ID, RPID: s.wa.Config.RPID, Name: name, Record: record,
	}, r.UserAgent(), at)
	switch {
	case errors.Is(err, store.ErrExists):
		jsonError(w, http.StatusConflict, "This passkey is registered already.")
		return
	case errors.Is(err, store.ErrLinkUsed), errors.Is(err, store.ErrLinkExpired), errors.Is(err, store.ErrNotFound):
		jsonError(w, http.StatusNotFound, linkError(err))
		return
	case err != nil:
		s.Log.Error("storing a passkey", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	s.startSession(w, r, sessionToken)
	writeJSON(w, http.StatusOK, map[string]string{"location": "/"})
}
