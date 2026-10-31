// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/url"

	"github.com/MartijnMul/allstill/internal/check"
	"github.com/MartijnMul/allstill/internal/store"
)

// navData is the sidebar every signed-in page shares: the views of the
// overview and the projects, each with how many checks it holds, and the
// worst state among a project's checks as its shape.
type navData struct {
	Views     []navItem
	Projects  []navItem
	Attention int    // checks that need someone, so the view can stand out
	Urgency   string // the worst look among them: warn or bad
}

type navItem struct {
	Name, Href, Class string
	Count             int
	Current           string // its aria-current: page, or location for the project of a check's page
}

// current is what an item's aria-current says: what when on, else nothing.
func current(on bool, what string) string {
	if on {
		return what
	}
	return ""
}

// overviewLink is the overview with a view or a project chosen.
func overviewLink(show, project string) string {
	q := url.Values{}
	if show != "" {
		q.Set("show", show)
	}
	if project != "" {
		q.Set("project", project)
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

// navOf builds the sidebar from the checks the person may see. show and
// project are what the page shows, to mark it; here is "" outside the
// overview, so no view is marked there.
func navOf(checks []store.CheckView, v viewer, here, show, project string) *navData {
	attention, paused := 0, 0
	urgency := look{Class: "warn"}
	perProject := map[string][]store.CheckView{}
	for _, c := range checks {
		perProject[c.ProjectID] = append(perProject[c.ProjectID], c)
		if needsAttention(c.Status.State) {
			attention++
			urgency = worse(urgency, lookOf(c.Status.State))
		}
		if c.Status.State == check.Paused {
			paused++
		}
	}
	overview := here == "overview"
	n := &navData{Attention: attention, Urgency: urgency.Class}
	n.Views = []navItem{
		{Name: "All checks", Href: overviewLink("", ""), Count: len(checks), Current: current(overview && show == "" && project == "", "page")},
		{Name: "Needs attention", Href: overviewLink("attention", ""), Count: attention, Current: current(overview && show == "attention" && project == "", "page"), Class: "attention"},
		{Name: "Paused", Href: overviewLink("paused", ""), Count: paused, Current: current(overview && show == "paused" && project == "", "page"), Class: "paused"},
	}
	where := "location"
	if overview {
		where = "page"
	}
	for _, p := range v.Access.Projects {
		worst := look{Class: "up"}
		for _, c := range perProject[p.ID] {
			worst = worse(worst, lookOf(c.Status.State))
		}
		if len(perProject[p.ID]) == 0 {
			worst.Class = "quiet"
		}
		n.Projects = append(n.Projects, navItem{
			Name: p.Name, Href: overviewLink("", p.ID), Class: worst.Class, Count: len(perProject[p.ID]), Current: current(project == p.ID, where),
		})
	}
	return n
}

// nav reads the checks for the sidebar of a page other than the overview.
func (s *Server) nav(ctx context.Context, v viewer, project string) (*navData, error) {
	checks, err := s.Store.Checks(ctx, v.Access)
	if err != nil {
		return nil, err
	}
	return navOf(checks, v, "", "", project), nil
}
