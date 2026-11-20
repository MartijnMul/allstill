// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"
	"slices"

	"github.com/MartijnMul/allstill/internal/store"
)

// listedMessages is how many of a person's newest messages the page shows.
const listedMessages = 100

// listedNotices is how many of the newest notices the page and the overview read.
const listedNotices = 20

type messagesData struct {
	Notices  []noticeRow // who was let in or out where they own, and by what
	Shadow   bool
	Nobody   []string // projects the person may see where nobody gets alerts
	Messages []messageRow
}

type noticeRow struct {
	When, Title string
	New         bool
}

type messageRow struct {
	When  string
	Kind  string
	State string
	Class string
	Lines []string
}

var whatLabel = map[string]string{
	"down":            "Down",
	"recovered":       "Resolved",
	"still_open":      "Still open",
	"resolved_unseen": "Resolved before you heard of it",
	"closed_deleted":  "Closed, its check was deleted",
	"closed_paused":   "Closed, its check was paused",
	"silent_check":    "Cannot alert, never pinged or never worked, or its schedule unreadable",
	"paused_check":    "Paused",
	"warning":         "Warning",
	store.NoticeKind:  "Who can get in",
}

var kindLabel = map[string]string{"alert": "Alert", "digest": "Digest", "test": "Test", store.NoticeKind: "Notice"}

// GET /messages: every message to the person, sent or, in shadow mode, only
// recorded.
func (s *Server) messagesPage(w http.ResponseWriter, r *http.Request, v viewer) {
	ctx := r.Context()
	msgs, err := s.Store.Messages(ctx, v.ID, listedMessages)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shadow, err := s.Store.ShadowMode(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nobody, err := s.Store.NobodyFollows(ctx, v.WorkspaceID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := messagesData{Shadow: shadow}
	notices, err := s.Store.Notices(ctx, v.ID, listedNotices)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, n := range notices {
		d.Notices = append(d.Notices, noticeRow{When: clock(s.Now(), n.At, v.Loc), Title: n.Title, New: !n.Read})
	}
	for _, p := range nobody {
		if _, ok := v.Access.Role(p.ID); ok {
			d.Nobody = append(d.Nobody, p.Name)
		}
	}
	now := s.Now()
	for _, m := range msgs {
		row := messageRow{When: clock(now, m.At, v.Loc), Kind: kindLabel[m.Kind]}
		switch m.State {
		case "shadow":
			row.State, row.Class = "Would have been sent", "quiet"
		case "sent":
			row.State, row.Class = "Sent", "up"
		case "pending":
			row.State, row.Class = "Sending", "warn"
		case "failed":
			row.State, row.Class = failedState(v, m.Error), "bad"
			if m.Error == store.NoLongerTrue {
				row.State, row.Class = "Left out: no longer true when it could go", "quiet"
			}
		default:
			row.State, row.Class = failedState(v, m.Error), "bad"
		}
		for _, it := range m.Items {
			row.Lines = append(row.Lines, whatLabel[it.What]+": "+it.Title)
		}
		d.Messages = append(d.Messages, row)
	}
	nav, err := s.nav(ctx, v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crumbs := []crumb{{"Messages", ""}}
	s.render(w, r, http.StatusOK, "messages", page{Title: "Messages", Person: v.Name, Crumbs: crumbs, Here: "messages", Nav: nav, Data: d})
	// Shown, so read: they leave the top of the overview. A failure here only
	// leaves them there.
	if r.Method == http.MethodGet && slices.ContainsFunc(d.Notices, func(n noticeRow) bool { return n.New }) {
		if err := s.Store.ReadNotices(ctx, v.ID, notices[0].At); err != nil {
			s.Log.Warn("marking notices as read", "error", err)
		}
	}
}

// failedState says a message could not go. Why is for an administrator: the error
// can hold what an address resolves to and how a server answers, and anyone
// signed in can have a test sent to an address of their choosing.
func failedState(v viewer, why string) string {
	if v.Access.Admin {
		return "Failed: " + why
	}
	return "Failed: it could not be delivered. Your administrator can see why in the server's log."
}
