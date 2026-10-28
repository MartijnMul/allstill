// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"

	"github.com/MartijnMul/allstill/internal/store"
)

// listedMessages is how many of a person's newest messages the page shows.
const listedMessages = 100

type messagesData struct {
	Shadow   bool
	Nobody   []string // projects the person may see where nobody gets alerts
	Messages []messageRow
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
	"silent_check":    "Cannot alert, never pinged or its schedule unreadable",
	"warning":         "Warning",
}

var kindLabel = map[string]string{"alert": "Alert", "digest": "Digest", "test": "Test"}

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
			row.State, row.Class = "Would have been sent", "new"
		case "sent":
			row.State, row.Class = "Sent", "up"
		case "pending":
			row.State, row.Class = "Sending", "late"
		case "failed":
			row.State, row.Class = "Failed: "+m.Error, "down"
			if m.Error == store.NoLongerTrue {
				row.State, row.Class = "Left out: no longer true when it could go", "quiet"
			}
		default:
			row.State, row.Class = "Failed: "+m.Error, "down"
		}
		for _, it := range m.Items {
			row.Lines = append(row.Lines, whatLabel[it.What]+": "+it.Title)
		}
		d.Messages = append(d.Messages, row)
	}
	crumbs := []crumb{{"Messages", ""}}
	s.render(w, r, http.StatusOK, "messages", page{Title: "Messages", Person: v.Name, Crumbs: crumbs, Data: d})
}
