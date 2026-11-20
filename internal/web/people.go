// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"net/http"
	"slices"

	"github.com/MartijnMul/allstill/internal/store"
)

// A team's People page, for its owners (intent/teams/plan-3.md, part A): who is
// in it and how they came in, who has a role of their own in its projects, the
// open invitations, and inviting someone. Only an owner sees the word "team"
// here (R6).

type peopleData struct {
	TeamID, TeamName string
	Members          []memberRow
	Outsiders        []outsiderRow
	Invites          []inviteRow
	Roles            []choice
	// Made is an invitation just made: its URL, shown this once.
	Made    *madeInvite
	Problem string
	Said    *notice
}

type memberRow struct {
	ID, Name, Role, CameIn string
	You                    bool
}

type outsiderRow struct {
	Name, Role, Project string
}

type inviteRow struct {
	ID, Role, Label, Where, MadeBy, Ends string
}

type madeInvite struct {
	URL, Label, Role, Ends string
}

var roleChoices = []choice{{Value: "viewer", Label: "Viewer"}, {Value: "editor", Label: "Editor"}, {Value: "owner", Label: "Owner"}}

func peopleHref(teamID string) string { return "/teams/" + teamID }

// ownedTeam is the team of the request's path, for one of its owners.
func ownedTeam(r *http.Request, v viewer) (store.TeamAccess, error) {
	for _, t := range v.Access.Teams {
		if t.ID == r.PathValue("team") {
			if t.Role != store.Owner {
				return t, store.ErrForbidden
			}
			return t, nil
		}
	}
	return store.TeamAccess{}, store.ErrNotFound
}

// GET /teams/{team}
func (s *Server) peoplePage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderPeople(w, r, v, http.StatusOK, nil, "")
}

func (s *Server) renderPeople(w http.ResponseWriter, r *http.Request, v viewer, code int, made *madeInvite, problem string) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.Now()
	tp, err := s.Store.PeopleOfTeam(r.Context(), v.Access, t.ID, now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := peopleData{TeamID: t.ID, TeamName: t.Name, Roles: roleChoices, Made: made, Problem: problem, Said: s.noticeFor(r, "team:"+t.ID)}
	for _, m := range tp.Members {
		row := memberRow{ID: m.ID, Name: m.Name, Role: string(m.Role), You: m.ID == v.ID, CameIn: "Added on the server"}
		if m.CameIn != "" {
			row.CameIn = "Invited by " + m.InvitedBy + ", for “" + m.CameIn + "”"
		}
		d.Members = append(d.Members, row)
	}
	for _, o := range tp.Outsiders {
		d.Outsiders = append(d.Outsiders, outsiderRow{Name: o.Name, Role: string(o.Role), Project: o.ProjectName})
	}
	for _, i := range tp.Invites {
		where := "The team"
		if i.ProjectID != "" {
			where = "The project " + i.ProjectName
		}
		d.Invites = append(d.Invites, inviteRow{ID: i.ID, Role: string(i.Role), Label: i.Label, Where: where, MadeBy: i.MadeBy.Name, Ends: clock(now, i.Ends, v.Loc)})
	}
	nav, err := s.nav(r.Context(), v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav.markPeople(t.ID)
	title := "People"
	if len(nav.People) > 1 {
		title = t.Name
	}
	s.render(w, r, code, "people", page{Title: title, Person: v.Name, Crumbs: []crumb{{title, ""}}, Here: "people", Nav: nav, Data: d})
}

// POST /teams/{team}/invites: an invitation to the team, shown once.
func (s *Server) inviteToTeam(w http.ResponseWriter, r *http.Request, v viewer) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	role, err := store.ParseRole(r.PostFormValue("role"))
	if err != nil {
		s.renderPeople(w, r, v, http.StatusUnprocessableEntity, nil, "Choose a role.")
		return
	}
	inv, secret, err := s.Store.CreateInvite(r.Context(), v.Access, t.ID, "", role, r.PostFormValue("label"), s.Now())
	if inv2, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderPeople(w, r, v, http.StatusUnprocessableEntity, nil, sentence(inv2.Problem))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	made := &madeInvite{URL: s.base.String() + "/join#" + secret, Label: inv.Label, Role: roleWords(inv.Role), Ends: clock(s.Now(), inv.Ends, v.Loc)}
	s.renderPeople(w, r, v, http.StatusOK, made, "")
}

// POST /teams/{team}/invites/{invite}/withdraw
func (s *Server) withdrawInvite(w http.ResponseWriter, r *http.Request, v viewer) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.Store.WithdrawInvite(r.Context(), v.Access, r.PathValue("invite"), s.Now())
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderPeople(w, r, v, http.StatusUnprocessableEntity, nil, sentence(inv.Problem))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.say(r, "team:"+t.ID, notice{Class: "up", Title: "The invitation was withdrawn: it no longer works."})
	http.Redirect(w, r, peopleHref(t.ID), http.StatusSeeOther)
}

// POST /teams/{team}/members/{person}: a member's new role.
func (s *Server) setTeamRole(w http.ResponseWriter, r *http.Request, v viewer) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	role, err := store.ParseRole(r.PostFormValue("role"))
	if err != nil {
		s.renderPeople(w, r, v, http.StatusUnprocessableEntity, nil, "Choose a role.")
		return
	}
	_, err = s.Store.SetTeamMemberAs(r.Context(), v.Access, t.ID, r.PathValue("person"), role)
	if errors.Is(err, store.ErrLastOwner) {
		s.renderPeople(w, r, v, http.StatusConflict, nil, "This is the team's only owner. Make someone else owner first.")
		return
	}
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderPeople(w, r, v, http.StatusUnprocessableEntity, nil, sentence(inv.Problem))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.PathValue("person") == v.ID && role != store.Owner {
		// They no longer own the team, so its People page is not theirs.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.say(r, "team:"+t.ID, notice{Class: "up", Title: "The role was changed."})
	http.Redirect(w, r, peopleHref(t.ID), http.StatusSeeOther)
}

type removeData struct {
	TeamID, TeamName, PersonID, Name string
	Roles                            []store.Outsider
	Keys                             []store.KeyInTeam
	Problem                          string
}

// memberName is the name of a member of a team, for one of its owners.
func (s *Server) memberName(r *http.Request, v viewer, teamID, personID string) (string, error) {
	tp, err := s.Store.PeopleOfTeam(r.Context(), v.Access, teamID, s.Now())
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(tp.Members, func(m store.Member) bool { return m.ID == personID })
	if i < 0 {
		return "", store.ErrNotFound
	}
	return tp.Members[i].Name, nil
}

// GET /teams/{team}/members/{person}/remove: what they keep, and taking them out.
func (s *Server) removeMemberPage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderRemove(w, r, v, http.StatusOK, "")
}

func (s *Server) renderRemove(w http.ResponseWriter, r *http.Request, v viewer, code int, problem string) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	personID := r.PathValue("person")
	name, err := s.memberName(r, v, t.ID, personID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stays, err := s.Store.WhatStays(r.Context(), v.Access, t.ID, personID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav, err := s.nav(r.Context(), v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	nav.markPeople(t.ID)
	d := removeData{TeamID: t.ID, TeamName: t.Name, PersonID: personID, Name: name, Roles: stays.Roles, Keys: stays.Keys, Problem: problem}
	crumbs := []crumb{{"People", peopleHref(t.ID)}, {"Take " + name + " out", ""}}
	s.render(w, r, code, "people-remove", page{Title: "Take " + name + " out", Person: v.Name, Crumbs: crumbs, Here: "people", Nav: nav, Data: d})
}

// POST /teams/{team}/members/{person}/remove, with what else to end.
func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, v viewer) {
	t, err := ownedTeam(r, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	personID := r.PathValue("person")
	name, err := s.memberName(r, v, t.ID, personID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	stays, err := s.Store.RemoveTeamMemberAs(r.Context(), v.Access, t.ID, personID)
	if errors.Is(err, store.ErrLastOwner) {
		s.renderRemove(w, r, v, http.StatusConflict, "This is the team's only owner. Make someone else owner first.")
		return
	}
	if inv, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderRemove(w, r, v, http.StatusUnprocessableEntity, sentence(inv.Problem))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// What else the owner chose to end, of what stays in this team only.
	ended := 0
	for _, role := range stays.Roles {
		if !slices.Contains(r.PostForm["end-role"], role.ProjectID) {
			continue
		}
		if err := s.Store.RemoveMember(r.Context(), v.Access.Actor, v.WorkspaceID, role.ProjectID, personID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		ended++
	}
	for _, k := range stays.Keys {
		if !slices.Contains(r.PostForm["revoke-key"], k.ID) {
			continue
		}
		if err := s.Store.RevokeAPIKey(r.Context(), v.Access, k.ID, s.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		ended++
	}
	said := notice{Class: "up", Title: name + " is out of the team."}
	if left := len(stays.Roles) + len(stays.Keys) - ended; left > 0 {
		said.Lines = []string{"What you left them in the team's projects stays, until you end it there."}
	}
	if personID == v.ID {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.say(r, "team:"+t.ID, said)
	http.Redirect(w, r, peopleHref(t.ID), http.StatusSeeOther)
}
