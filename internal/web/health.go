// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/http"
	"time"
)

// BehindAfter is how far the sender may lag before it counts as behind: a
// few passes, and less than the gather period's wait (R11).
const BehindAfter = 2 * time.Minute

// GET /health: whether Allstill can alert and judge, for a monitor outside
// it to watch. 200 when the sender keeps up, 503 when it is behind, the
// database cannot be read, or the server is offline for the checks it runs
// (its canary does not answer). It says nothing about any check, so it needs
// no sign-in.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	type answer struct {
		Status       string `json:"status"` // ok, behind, offline or error
		SenderBehind int64  `json:"sender_behind"`
	}
	var behind time.Duration
	var err error
	if s.Behind != nil {
		behind, err = s.Behind(r.Context())
	}
	offline := false
	if s.Offline != nil {
		_, offline = s.Offline()
	}
	switch {
	case err != nil:
		s.Log.Error("checking health", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, answer{Status: "error"})
	case behind > BehindAfter:
		writeJSON(w, http.StatusServiceUnavailable, answer{Status: "behind", SenderBehind: int64(behind / time.Second)})
	case offline:
		writeJSON(w, http.StatusServiceUnavailable, answer{Status: "offline", SenderBehind: int64(behind / time.Second)})
	default:
		writeJSON(w, http.StatusOK, answer{Status: "ok", SenderBehind: int64(behind / time.Second)})
	}
}

// alertCards are what may keep alerts from reaching the person: a sender
// that is behind, messages to them that failed, devices that need them, and
// projects they see where nobody would be told (R11).
func (s *Server) alertCards(ctx context.Context, v viewer, now time.Time) ([]card, error) {
	var cards []card
	if s.Behind != nil {
		behind, err := s.Behind(ctx)
		if err != nil {
			return nil, err
		}
		if behind > BehindAfter {
			cards = append(cards, card{Href: "/messages", Class: "bad",
				Title: "Alerts are " + span(behind) + " behind", Line: "Allstill has not kept up with sending; messages may come late."})
		}
	}
	failed, err := s.Store.FailedSince(ctx, v.ID, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	if failed > 0 {
		cards = append(cards, card{Href: "/messages", Class: "bad",
			Title: plural(failed, "message", "messages") + " to you failed", Line: "In the last day · see Messages"})
	}
	channels, err := s.Store.Channels(ctx, v.ID)
	if err != nil {
		return nil, err
	}
	emails := false
	for _, c := range channels {
		emails = emails || c.Kind == "email"
		if c.Kind == "email" {
			if !c.FailedAt.IsZero() && s.MailOn {
				cards = append(cards, card{Href: "/channels", Class: "warn",
					Title: "Email to " + c.Address + " failed", Line: "The mail server refused it · check it and send a test"})
			}
			continue
		}
		if c.Kind == "webpush" && c.Key != s.PushKey {
			cards = append(cards, card{Href: "/channels", Class: "warn",
				Title: c.Name + " must subscribe again", Line: "Open Allstill on that device"})
		} else if !c.FailedAt.IsZero() {
			cards = append(cards, card{Href: "/channels", Class: "warn",
				Title: "Alerts to " + c.Name + " failed", Line: "Since " + clock(now, c.FailedAt, v.Loc) + " · send it a test"})
		}
	}
	if emails && !s.MailOn {
		cards = append(cards, card{Href: "/channels", Class: "warn",
			Title: "Email is not set up on this server", Line: "Alerts to your email addresses cannot go out"})
	}
	if s.MailProblem != nil && v.Admin {
		if p := s.MailProblem(); p != nil {
			cards = append(cards, card{Href: "/channels", Class: "bad",
				Title: "Email cannot be sent as the server is set up", Line: "Since " + clock(now, p.At, v.Loc) + " · " + p.Error})
		}
	}
	nobody, err := s.Store.NobodyFollows(ctx, v.WorkspaceID)
	if err != nil {
		return nil, err
	}
	for _, p := range nobody {
		if _, ok := v.Access.Role(p.ID); ok {
			cards = append(cards, card{Href: "/messages", Class: "warn",
				Title: "Nobody would be told about " + p.Name, Line: "No member gets alerts for this project"})
		}
	}
	return cards, nil
}
