// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MartijnMul/allstill/internal/store"
	"github.com/MartijnMul/allstill/internal/webpush"
)

type channelsData struct {
	Shadow    bool
	Confirmed bool // just confirmed by opening a test message
	Channels  []channelRow
}

type channelRow struct {
	ID    string
	Name  string
	Kind  string
	State string
	Class string
	Since string
}

// GET /channels: where the person is reached, with a test for each, and a
// way to add the device they are on.
func (s *Server) channelsPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	cs, err := s.Store.Channels(ctx, v.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shadow, err := s.Store.ShadowMode(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := channelsData{Shadow: shadow, Confirmed: r.URL.Query().Has("confirmed")}
	now := s.Now()
	for _, c := range cs {
		row := channelRow{ID: c.ID, Name: c.Name, Kind: "Push to this browser", Since: clock(now, c.CreatedAt, v.Loc)}
		switch {
		case c.Kind == "webpush" && c.Key != s.PushKey:
			row.State, row.Class = "Must subscribe again: open Allstill on this device", "down"
		case !c.FailedAt.IsZero():
			row.State, row.Class = "Failed "+clock(now, c.FailedAt, v.Loc)+"; send a test", "down"
		case c.VerifiedAt.IsZero():
			row.State, row.Class = "Not confirmed: send a test and open it", "late"
		default:
			row.State, row.Class = "Confirmed "+clock(now, c.VerifiedAt, v.Loc), "up"
		}
		d.Channels = append(d.Channels, row)
	}
	crumbs := []crumb{{"Devices", ""}}
	s.render(w, r, http.StatusOK, "channels", page{Title: "Devices", Person: v.Name, Crumbs: crumbs, Data: d})
}

// POST /channels/push: the browser's subscription, sent when a device is
// added and again whenever the dashboard is opened on it, so a changed
// subscription is picked up (ADR 0003).
func (s *Server) savePush(w http.ResponseWriter, r *http.Request, v viewer) {
	var in struct {
		Name     string `json:"name"`
		Key      string `json:"key"` // the key the browser subscribed for
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := readJSON(w, r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, err := webpush.ParseSubscription(in.Endpoint, in.Keys.P256dh, in.Keys.Auth)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "The browser's subscription cannot be used: "+err.Error()+".")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		name = "Browser"
	}
	// The browser says which key it subscribed for; only a subscription for
	// the current one is pushed to (ADR 0003).
	key := in.Key
	if len(key) != len(s.PushKey) {
		key = s.PushKey
	}
	c, err := s.Store.SavePush(r.Context(), v.WorkspaceID, v.ID, name, sub, key)
	if err != nil {
		s.Log.Error("saving a push subscription", "person", v.ID, "error", err)
		jsonError(w, http.StatusInternalServerError, "Something went wrong on the server. Try again in a moment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": c.ID, "confirmed": !c.VerifiedAt.IsZero()})
}

// POST /channels/{id}/test sends a test message, even in shadow mode.
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, v viewer) {
	if err := s.Store.Test(r.Context(), v.ID, r.PathValue("id"), s.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.Wake != nil {
		s.Wake()
	}
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

// GET /channels/{id}/confirm, the link in a test message: the device got it.
func (s *Server) confirmChannel(w http.ResponseWriter, r *http.Request, v viewer) {
	err := s.Store.Confirm(r.Context(), v.ID, r.PathValue("id"), r.URL.Query().Get("token"), s.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, "error", page{Title: "Not confirmed", Person: v.Name,
			Data: "This link is not from the newest test message to one of your devices. Send a new test from Devices."})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/channels?confirmed", http.StatusSeeOther)
}

// POST /channels/{id}/delete removes a channel; the page also unsubscribes
// the browser when it is the one removed.
func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, v viewer) {
	if err := s.Store.DeleteChannel(r.Context(), v.ID, r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}
