// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/MartijnMul/allstill/internal/store"
)

// A project's own page (intent/teams/plan-3.md, part B): the reader's role and
// through what, whether they are told, and for its owners who can see it (R7),
// the roles of their own in it and its invitations. A team's owners make,
// rename and delete its projects.

type projectData struct {
	ID, Name string
	// Where is the team, for someone who sees projects of two teams (spec, R5).
	Where    string
	Role     string
	Through  string
	Told     bool
	ChecksAt string // the overview narrowed to it
	Checks   int
	// MayRun is an owner of its team: renames and deletes it.
	MayRun bool
	// People is who can see it, for its owners.
	MayPeople bool
	People    []projectPersonRow
	// TeamLine is the team's people as a count, to an owner from outside it.
	TeamLine   string
	NobodyTold bool
	Seeing     int    // how many can see it, the team's people counted too
	PeopleAt   string // the team's People page, for an owner of the team
	Invites    []inviteRow
	Roles      []choice
	Made       *madeInvite
	PingKey    string // a project just made: its ping key, shown this once
	Problem    string
	Said       *notice
}

type projectPersonRow struct {
	ID, Name, Role, Through string
	Told, You               bool
	// Choices are the roles of their own they may be given here: higher than
	// what the team gives them. None for someone only in through the team.
	Choices []choice
	Own     string
}

func projectHref(projectID string) string { return "/projects/" + projectID }

// through says through what someone sees a project.
func through(team, own store.Role) string {
	switch {
	case team != "" && own != "":
		return "the team, and a role of their own here"
	case own != "":
		return "a role of their own here"
	}
	return "the team"
}

// higherRoles are the roles of one's own that give more than a team's role.
func higherRoles(team store.Role) []choice {
	var c []choice
	for _, r := range roleChoices {
		if team == "" || !store.RoleAtLeast(team, store.Role(r.Value)) {
			c = append(c, r)
		}
	}
	return c
}

// teamsSeen counts the teams whose projects someone sees.
func teamsSeen(a store.Access) int {
	teams := map[string]bool{}
	for _, p := range a.Projects {
		teams[p.TeamID] = true
	}
	return len(teams)
}

// GET /projects/{project}
func (s *Server) projectPage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderProject(w, r, v, http.StatusOK, projectData{})
}

// renderProject renders a project's page with what extra says: something just
// made, or a problem.
func (s *Server) renderProject(w http.ResponseWriter, r *http.Request, v viewer, code int, extra projectData) {
	ctx, now := r.Context(), s.Now()
	info, err := s.Store.Project(ctx, v.Access, r.PathValue("project"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := extra
	d.ID, d.Name, d.Role, d.Told, d.Checks = info.ID, info.Name, string(info.Role), info.Told, info.Checks
	d.ChecksAt, d.Roles = overviewLink("", info.ID), roleChoices
	switch {
	case info.TeamRole != "" && info.OwnRole != "":
		d.Through = "You see it through the team, and a role of your own here."
	case info.OwnRole != "":
		d.Through = "You see it through a role of your own here."
	default:
		d.Through = "You see it through the team."
	}
	if info.TeamName != "" && teamsSeen(v.Access) > 1 {
		d.Where = info.TeamName
	}
	d.MayRun = v.Access.OwnsTeamOf(info.ID)
	d.Said = s.noticeFor(r, "project:"+info.ID)
	if info.Role == store.Owner {
		pp, err := s.Store.PeopleOfProject(ctx, v.Access, info.ID, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.MayPeople = true
		told := pp.TeamTold
		for _, p := range pp.People {
			row := projectPersonRow{ID: p.ID, Name: p.Name, Role: string(p.Role), Through: through(p.TeamRole, p.OwnRole), Told: p.Told, You: p.ID == v.ID, Own: string(p.OwnRole)}
			if p.OwnRole != "" {
				row.Choices = higherRoles(p.TeamRole)
			}
			if p.Told {
				told++
			}
			d.People = append(d.People, row)
		}
		if pp.Unnamed && pp.Team > 0 {
			d.TeamLine = "The team: " + plural(pp.Team, "person", "people") + ", " + fmt.Sprintf("%d of them told", pp.TeamTold)
		}
		d.NobodyTold = told == 0
		d.Seeing = len(d.People) + pp.Team
		if d.MayRun {
			d.PeopleAt = peopleHref(info.TeamID)
		}
		for _, i := range pp.Invites {
			by := i.MadeBy.Name
			if i.MadeBy.Kind == "team" {
				by = "the team"
			}
			d.Invites = append(d.Invites, inviteRow{ID: i.ID, Role: string(i.Role), Label: i.Label, MadeBy: by, Ends: clock(now, i.Ends, v.Loc)})
		}
	}
	nav, err := s.nav(ctx, v, info.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	title := v.Access.Label(info.ID)
	s.render(w, r, code, "project", page{Title: title, Person: v.Name, Crumbs: []crumb{{title, ""}}, Nav: nav, Data: d})
}

// POST /projects/{project}/alerts: whether the reader is told about it.
func (s *Server) setOwnAlerts(w http.ResponseWriter, r *http.Request, v viewer) {
	project := r.PathValue("project")
	on := r.PostFormValue("alerts") == "on"
	if err := s.Store.SetOwnAlerts(r.Context(), v.Access, project, on, s.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	said := notice{Class: "up", Title: "You are told about this project again."}
	if !on {
		said.Title = "You are no longer told about this project."
		said.Lines = []string{"Its checks stay on your overview. Switch it on here whenever you like."}
	}
	s.say(r, "project:"+project, said)
	http.Redirect(w, r, projectHref(project), http.StatusSeeOther)
}

// POST /projects/{project}/invites: an invitation to the project, shown once.
func (s *Server) inviteToProject(w http.ResponseWriter, r *http.Request, v viewer) {
	project := r.PathValue("project")
	role, err := store.ParseRole(r.PostFormValue("role"))
	if err != nil {
		if _, ok := v.Access.Role(project); !ok {
			s.fail(w, r, store.ErrNotFound)
			return
		}
		s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: "Choose a role."})
		return
	}
	inv, secret, err := s.Store.CreateInvite(r.Context(), v.Access, "", project, role, r.PostFormValue("label"), s.Now())
	if bad, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: sentence(bad.Problem)})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	made := &madeInvite{URL: s.base.String() + "/join#" + secret, Label: inv.Label, Role: roleWords(inv.Role), Ends: clock(s.Now(), inv.Ends, v.Loc)}
	s.renderProject(w, r, v, http.StatusOK, projectData{Made: made})
}

// POST /projects/{project}/invites/{invite}/withdraw
func (s *Server) withdrawProjectInvite(w http.ResponseWriter, r *http.Request, v viewer) {
	project := r.PathValue("project")
	if role, ok := v.Access.Role(project); !ok {
		s.fail(w, r, store.ErrNotFound)
		return
	} else if role != store.Owner {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	err := s.Store.WithdrawInvite(r.Context(), v.Access, r.PathValue("invite"), s.Now())
	if bad, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: sentence(bad.Problem)})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.say(r, "project:"+project, notice{Class: "up", Title: "The invitation was withdrawn: it no longer works."})
	http.Redirect(w, r, projectHref(project), http.StatusSeeOther)
}

// POST /projects/{project}/roles/{person}: a role of their own changed, or with
// "none" ended.
func (s *Server) setProjectRole(w http.ResponseWriter, r *http.Request, v viewer) {
	project, person := r.PathValue("project"), r.PathValue("person")
	if _, ok := v.Access.Role(project); !ok {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	var role store.Role
	if value := r.PostFormValue("role"); value != "none" {
		var err error
		if role, err = store.ParseRole(value); err != nil {
			s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: "Choose a role."})
			return
		}
	}
	err := s.Store.SetProjectRoleAs(r.Context(), v.Access, project, person, role)
	if errors.Is(err, store.ErrLastOwner) {
		s.renderProject(w, r, v, http.StatusConflict, projectData{Problem: "This is the project's only owner. Make someone else owner first."})
		return
	}
	if bad, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: sentence(bad.Problem)})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if person == v.ID {
		// What they may do here has changed, so the page may no longer be theirs.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	said := "The role was changed."
	if role == "" {
		said = "Their role of their own here was ended."
	}
	s.say(r, "project:"+project, notice{Class: "up", Title: said})
	http.Redirect(w, r, projectHref(project), http.StatusSeeOther)
}

// POST /projects/{project}/rename
func (s *Server) renameProject(w http.ResponseWriter, r *http.Request, v viewer) {
	project := r.PathValue("project")
	err := s.Store.RenameProjectAs(r.Context(), v.Access, project, r.PostFormValue("name"))
	if bad, ok := errors.AsType[*store.InvalidError](err); ok {
		s.renderProject(w, r, v, http.StatusUnprocessableEntity, projectData{Problem: sentence(bad.Problem)})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.say(r, "project:"+project, notice{Class: "up", Title: "The project was renamed."})
	http.Redirect(w, r, projectHref(project), http.StatusSeeOther)
}

type newProjectData struct {
	Teams   []choice // the teams the person owns, when more than one
	Team    string
	Name    string
	Problem string
}

// ownedTeams are the teams someone owns, as a form lists them.
func ownedTeams(v viewer) []choice {
	var teams []choice
	for _, t := range v.Access.Teams {
		if t.Role == store.Owner {
			teams = append(teams, choice{t.ID, t.Name})
		}
	}
	return teams
}

// GET /projects/new, with ?team= for a team chosen already.
func (s *Server) newProjectPage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderNewProject(w, r, v, http.StatusOK, newProjectData{Team: r.URL.Query().Get("team")})
}

func (s *Server) renderNewProject(w http.ResponseWriter, r *http.Request, v viewer, code int, d newProjectData) {
	teams := ownedTeams(v)
	if len(teams) == 0 {
		s.fail(w, r, store.ErrNotFound)
		return
	}
	if d.Team != "" {
		if !owns(teams, d.Team) {
			s.fail(w, r, store.ErrNotFound)
			return
		}
	} else if len(teams) == 1 {
		d.Team = teams[0].Value
	}
	if len(teams) > 1 {
		d.Teams = teams
	}
	nav, err := s.nav(r.Context(), v, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, code, "project-new", page{Title: "New project", Person: v.Name, Crumbs: []crumb{{"New project", ""}}, Nav: nav, Data: d})
}

func owns(teams []choice, teamID string) bool {
	for _, t := range teams {
		if t.Value == teamID {
			return true
		}
	}
	return false
}

// POST /projects/new: the project, and its ping key once.
func (s *Server) addProject(w http.ResponseWriter, r *http.Request, v viewer) {
	d := newProjectData{Team: r.PostFormValue("team"), Name: r.PostFormValue("name")}
	teams := ownedTeams(v)
	if d.Team == "" && len(teams) == 1 {
		d.Team = teams[0].Value
	}
	if d.Team == "" && len(teams) > 1 {
		d.Problem = "Choose the team it is for."
		s.renderNewProject(w, r, v, http.StatusUnprocessableEntity, d)
		return
	}
	projectID, pingKey, err := s.Store.CreateProjectAs(r.Context(), v.Access, d.Team, d.Name)
	if bad, ok := errors.AsType[*store.InvalidError](err); ok {
		d.Problem = sentence(bad.Problem)
		s.renderNewProject(w, r, v, http.StatusUnprocessableEntity, d)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The access was read before it existed.
	if v.Access, err = s.Store.AccessOfPerson(r.Context(), v.WorkspaceID, v.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	r.SetPathValue("project", projectID)
	s.renderProject(w, r, v, http.StatusCreated, projectData{PingKey: pingKey})
}

type deleteProjectData struct {
	ID, Name, Problem string
	Checks            int
	ChecksAt          string
}

// GET /projects/{project}/delete: what deleting it means, for an owner of its team.
func (s *Server) deleteProjectPage(w http.ResponseWriter, r *http.Request, v viewer) {
	s.renderDeleteProject(w, r, v, http.StatusOK, "")
}

func (s *Server) renderDeleteProject(w http.ResponseWriter, r *http.Request, v viewer, code int, problem string) {
	info, err := s.Store.Project(r.Context(), v.Access, r.PathValue("project"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !v.Access.OwnsTeamOf(info.ID) {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	nav, err := s.nav(r.Context(), v, info.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := deleteProjectData{ID: info.ID, Name: info.Name, Problem: problem, Checks: info.Checks, ChecksAt: overviewLink("", info.ID)}
	title := "Delete " + info.Name
	crumbs := []crumb{{v.Access.Label(info.ID), projectHref(info.ID)}, {"Delete", ""}}
	s.render(w, r, code, "project-confirm", page{Title: title, Person: v.Name, Crumbs: crumbs, Nav: nav, Data: d})
}

// POST /projects/{project}/delete
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request, v viewer) {
	project := r.PathValue("project")
	name := v.Access.Label(project)
	err := s.Store.DeleteProjectAs(r.Context(), v.Access, project, s.Now())
	if holds, ok := errors.AsType[*store.ProjectHolds](err); ok {
		s.renderDeleteProject(w, r, v, http.StatusConflict, sentence(holds.Error()))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.say(r, "", notice{Class: "up", Title: "The project " + name + " was deleted.", Lines: []string{"Its ping key and its keys no longer work, and its name is free again."}})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
