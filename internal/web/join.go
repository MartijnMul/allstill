// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store"
)

// Accepting an invitation (intent/teams/plan-3.md, part A). The secret is in
// the URL's fragment, which browsers never send, so it stays out of access
// logs; the page's script posts it. Before signing in, whoever opens it is told
// who invites and as what; where to, once they are signed in or have made an
// account. The label is never shown here: it is for the owners.

type joinRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	TimeZone string `json:"time_zone"`
	Person   string `json:"person"`
}

func inviteError(err error) string {
	switch {
	case errors.Is(err, store.ErrLinkUsed):
		return "This invitation has been accepted already. Ask whoever sent it for a new one."
	case errors.Is(err, store.ErrLinkExpired):
		return "This invitation has expired. Ask whoever sent it for a new one."
	case errors.Is(err, store.ErrInviteWithdrawn):
		return "This invitation was withdrawn. Ask whoever sent it for a new one."
	}
	return "This invitation does not work. Copy the whole link, or ask for a new one."
}

// roleWords is a role with its article: an editor.
func roleWords(r store.Role) string {
	if r == store.Viewer {
		return "a viewer"
	}
	return "an " + string(r)
}

// GET /join
func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "join", page{Title: "An invitation"})
}

// POST /join/offer: what the invitation offers, and, to someone signed in,
// where to and whether they have it already.
func (s *Server) joinOffer(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, inviteError(store.ErrNotFound))
		return
	}
	at := s.Now()
	offer, err := s.Store.InviteOffer(r.Context(), req.Token, at)
	if err != nil {
		jsonError(w, http.StatusNotFound, inviteError(err))
		return
	}
	answer := map[string]any{
		"offer":        offer.MadeBy + " invites you as " + roleWords(offer.Role) + ".",
		"account_made": offer.MadeAccount != "",
	}
	if v, err := s.viewer(r); err == nil {
		answer["signed_in_as"] = v.Name
		if offer.Kind == store.InviteToProject {
			answer["where"] = "This makes you " + roleWords(offer.Role) + " of the project " + offer.ProjectName + "."
		} else {
			answer["where"] = "This makes you " + roleWords(offer.Role) + " of every project of the team " + offer.TeamName + "."
		}
		has, err := s.Store.HasOffered(r.Context(), req.Token, v.ID, at)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLinkUsed) || errors.Is(err, store.ErrLinkExpired) || errors.Is(err, store.ErrInviteWithdrawn) {
			jsonError(w, http.StatusNotFound, inviteError(err))
			return
		}
		if err != nil {
			s.Log.Error("reading an invitation", "error", err)
			jsonError(w, http.StatusInternalServerError, msgTryAgain)
			return
		}
		answer["has"] = has
	}
	writeJSON(w, http.StatusOK, answer)
}

// POST /join/accept, signed in.
func (s *Server) joinAccept(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	v, err := s.viewer(r)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "Sign in first, then accept.")
		return
	}
	err = s.Store.AcceptInvite(r.Context(), req.Token, v.WorkspaceID, v.ID, s.Now())
	switch {
	case errors.Is(err, store.ErrAlreadyHas):
		jsonError(w, http.StatusConflict, "You have this already, so the invitation stays open for whom it was meant.")
		return
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrLinkUsed), errors.Is(err, store.ErrLinkExpired), errors.Is(err, store.ErrInviteWithdrawn):
		jsonError(w, http.StatusNotFound, inviteError(err))
		return
	case err != nil:
		s.Log.Error("accepting an invitation", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"location": "/"})
}

// joinPurpose binds a new account's passkey ceremony to the invitation, the id
// the account will have, and the name it was begun with: the passkey's user
// handle is that id, and signing in finds the person by it.
func joinPurpose(token, personID, name string) string {
	h := sha256.Sum256([]byte(token + "\n" + personID + "\n" + name))
	return "join " + hex.EncodeToString(h[:])
}

// POST /join/begin: the options for a new account's passkey, and the id it will have.
func (s *Server) joinBegin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	name, err := store.NewPersonName(req.Name)
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		jsonError(w, http.StatusUnprocessableEntity, sentence(inv.Problem))
		return
	}
	offer, err := s.Store.InviteOffer(r.Context(), req.Token, s.Now())
	if err != nil {
		jsonError(w, http.StatusNotFound, inviteError(err))
		return
	}
	if offer.MadeAccount != "" {
		jsonError(w, http.StatusConflict, "An account was made with this invitation already. Sign in with its passkey.")
		return
	}
	personID := id.New(id.Person)
	u := &user{Person: store.Person{ID: personID, Name: name}}
	ch := s.ceremonies.challenge(joinPurpose(req.Token, personID, name), s.nonce(w), s.Now())
	options, _, err := s.wa.BeginRegistration(u, withChallenge(ch))
	if err != nil {
		s.Log.Error("beginning a passkey registration", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": options, "person": personID})
}

// POST /join/finish: the new account, its passkey and a session, all or nothing.
// What the invitation offers is accepted after, once they have seen where to.
func (s *Server) joinFinish(w http.ResponseWriter, r *http.Request) {
	nonce := s.readNonce(w, r)
	var req struct {
		joinRequest
		Credential json.RawMessage `json:"credential"`
	}
	if err := readJSON(w, r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	name, err := store.NewPersonName(req.Name)
	if err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		jsonError(w, http.StatusBadRequest, msgTryAgain)
		return
	}
	at := s.Now()
	ch, err := base64.RawURLEncoding.DecodeString(parsed.Response.CollectedClientData.Challenge)
	if err != nil || !strings.HasPrefix(req.Person, id.Person+"_") || !s.ceremonies.valid(ch, joinPurpose(req.Token, req.Person, name), nonce, at) {
		jsonError(w, http.StatusBadRequest, "This took too long or began elsewhere. Try again.")
		return
	}
	u := &user{Person: store.Person{ID: req.Person, Name: name}}
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
	token, err := s.Store.CreatePersonByInvite(r.Context(), req.Token, req.Person, name, req.TimeZone, store.NewPasskey{
		CredentialID: cred.ID, RPID: s.wa.Config.RPID, Name: "Passkey", Record: record,
	}, r.UserAgent(), at)
	switch {
	case errors.Is(err, store.ErrInviteMadeAccount):
		jsonError(w, http.StatusConflict, "An account was made with this invitation already. Sign in with its passkey.")
		return
	case errors.Is(err, store.ErrExists):
		jsonError(w, http.StatusConflict, "This passkey is registered already. Sign in with it instead.")
		return
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrLinkUsed), errors.Is(err, store.ErrLinkExpired), errors.Is(err, store.ErrInviteWithdrawn):
		jsonError(w, http.StatusNotFound, inviteError(err))
		return
	case err != nil:
		if inv, ok := errors.AsType[*store.InvalidError](err); ok {
			jsonError(w, http.StatusUnprocessableEntity, sentence(inv.Problem))
			return
		}
		s.Log.Error("making an account by invitation", "error", err)
		jsonError(w, http.StatusInternalServerError, msgTryAgain)
		return
	}
	s.startSession(w, r, token)
	writeJSON(w, http.StatusOK, map[string]string{})
}
