// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/MartijnMul/allstill/internal/api"
	"github.com/MartijnMul/allstill/internal/store"
)

// A team's owners running their team over the API (intent/teams/plan-3.md, part C).
// The rules are the store's, the same as for the dashboard: what is here reads a
// request and says what came of it. An owner is never named, made less or taken out
// here, which the store holds to for a sign-in from a command line.

// roleToGive reads a role given over the API: viewer or editor. Owner is refused with
// the reason, as the dashboard is where owners change.
func roleToGive(r api.RoleToGive) (store.Role, error) {
	switch r {
	case api.RoleToGiveViewer, api.RoleToGiveEditor:
		return store.Role(r), nil
	case "owner":
		return "", &store.InvalidError{Field: "role", Problem: "an owner is named, invited, made less or taken out in the dashboard, signed in with a passkey"}
	}
	return "", &store.InvalidError{Field: "role", Problem: "give viewer or editor"}
}

// conflict is what the store refuses as being full or used already, which the API
// answers with 409 rather than 422: the request is right, the state is not.
func conflict(err error) (api.Problem, bool) {
	if inv, ok := errors.AsType[*store.InvalidError](err); ok && inv.Field == "invite" {
		return problem(http.StatusConflict, sentence(inv.Problem), ""), true
	}
	return api.Problem{}, false
}

// freshProject reads a project as the caller sees it now, after a change.
func (a apiServer) freshProject(ctx context.Context, projectID string) (api.Project, error) {
	c := callerOf(ctx)
	access, err := a.s.Store.AccessOfPerson(ctx, c.Token.WorkspaceID, c.Token.PersonID)
	if err != nil {
		return api.Project{}, err
	}
	for _, p := range access.Projects {
		if p.ID != projectID {
			continue
		}
		checks, err := a.s.Store.CheckStates(ctx, access)
		if err != nil {
			return api.Project{}, err
		}
		told, err := a.s.Store.ToldAbout(ctx, access, c.Token.PersonID)
		if err != nil {
			return api.Project{}, err
		}
		return projectOf(access, p, checks, told), nil
	}
	return api.Project{}, store.ErrNotFound
}

// CreateProject makes a project in a team the caller owns.
func (a apiServer) CreateProject(ctx context.Context, req api.CreateProjectRequestObject) (api.CreateProjectResponseObject, error) {
	access := callerOf(ctx).Access
	teamID := ""
	if req.Body.TeamId != nil {
		teamID = *req.Body.TeamId
	} else {
		var owned []string
		for _, t := range access.Teams {
			if t.Role == store.Owner {
				owned = append(owned, t.ID)
			}
		}
		switch len(owned) {
		case 0:
			if callerOf(ctx).scope() != store.ScopeManage {
				return nil, store.ErrForbidden
			}
			return api.CreateProject403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: api.ForbiddenApplicationProblemPlusJSONResponse(
				problem(http.StatusForbidden, "You own no team, so you make no project.", "A team's owners make its projects."))}, nil
		case 1:
			teamID = owned[0]
		default:
			return nil, &store.InvalidError{Field: "team_id", Problem: "you own several teams: say which with team_id"}
		}
	}
	id, pingKey, err := a.s.Store.CreateProjectAs(ctx, access, teamID, req.Body.Name)
	if err != nil {
		return nil, err
	}
	p, err := a.freshProject(ctx, id)
	if err != nil {
		return nil, err
	}
	out, err := as[api.ProjectCreated](p)
	if err != nil {
		return nil, err
	}
	out.PingKey = pingKey
	return api.CreateProject201JSONResponse{Body: out, Headers: api.CreateProject201ResponseHeaders{Location: a.s.base.String() + "/api/v1/projects/" + id}}, nil
}

// UpdateProject renames a project of a team the caller owns.
func (a apiServer) UpdateProject(ctx context.Context, req api.UpdateProjectRequestObject) (api.UpdateProjectResponseObject, error) {
	if req.Body.Name == nil {
		return nil, &store.InvalidError{Field: "name", Problem: "say what to change: name is the one field"}
	}
	if err := a.s.Store.RenameProjectAs(ctx, callerOf(ctx).Access, req.ProjectId, *req.Body.Name); err != nil {
		return nil, err
	}
	p, err := a.freshProject(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return api.UpdateProject200JSONResponse(p), nil
}

// DeleteProject deletes a project without checks, of a team the caller owns.
func (a apiServer) DeleteProject(ctx context.Context, req api.DeleteProjectRequestObject) (api.DeleteProjectResponseObject, error) {
	err := a.s.Store.DeleteProjectAs(ctx, callerOf(ctx).Access, req.ProjectId, a.s.Now())
	if holds, ok := errors.AsType[*store.ProjectHolds](err); ok {
		return api.DeleteProject409ApplicationProblemPlusJSONResponse(problem(http.StatusConflict, "The project still holds checks.",
			"It holds "+plural(holds.Checks, "check", "checks")+": delete them, or move them to another project, first.")), nil
	}
	if err != nil {
		return nil, err
	}
	return api.DeleteProject204Response{}, nil
}

func projectPersonOf(p store.ProjectPerson) api.ProjectPerson {
	out := api.ProjectPerson{Id: p.ID, Name: p.Name, Role: api.Role(p.Role), Alerts: p.Told}
	if p.TeamRole != "" {
		r := api.Role(p.TeamRole)
		out.TeamRole = &r
	}
	if p.OwnRole != "" {
		r := api.Role(p.OwnRole)
		out.OwnRole = &r
	}
	return out
}

// ListProjectPeople says who can see a project, for one of its owners.
func (a apiServer) ListProjectPeople(ctx context.Context, req api.ListProjectPeopleRequestObject) (api.ListProjectPeopleResponseObject, error) {
	pp, err := a.s.Store.PeopleOfProject(ctx, callerOf(ctx).Access, req.ProjectId, a.s.Now())
	if err != nil {
		return nil, err
	}
	out := api.ListProjectPeople200JSONResponse{Items: []api.ProjectPerson{}}
	for _, p := range pp.People {
		out.Items = append(out.Items, projectPersonOf(p))
	}
	if pp.Unnamed {
		out.Team = &struct {
			Alerted int `json:"alerted"`
			People  int `json:"people"`
		}{Alerted: pp.TeamTold, People: pp.Team}
	}
	return out, nil
}

// SetProjectRole changes someone's role of their own in a project.
func (a apiServer) SetProjectRole(ctx context.Context, req api.SetProjectRoleRequestObject) (api.SetProjectRoleResponseObject, error) {
	role, err := roleToGive(req.Body.Role)
	if err != nil {
		return nil, err
	}
	access := callerOf(ctx).Access
	if err := a.s.Store.SetProjectRoleAs(ctx, access, req.ProjectId, req.PersonId, role); err != nil {
		return nil, err
	}
	pp, err := a.s.Store.PeopleOfProject(ctx, access, req.ProjectId, a.s.Now())
	if err != nil {
		return nil, err
	}
	for _, p := range pp.People {
		if p.ID == req.PersonId {
			return api.SetProjectRole200JSONResponse(projectPersonOf(p)), nil
		}
	}
	return nil, store.ErrNotFound
}

// RemoveProjectRole ends someone's role of their own in a project.
func (a apiServer) RemoveProjectRole(ctx context.Context, req api.RemoveProjectRoleRequestObject) (api.RemoveProjectRoleResponseObject, error) {
	if err := a.s.Store.SetProjectRoleAs(ctx, callerOf(ctx).Access, req.ProjectId, req.PersonId, ""); err != nil {
		return nil, err
	}
	return api.RemoveProjectRole204Response{}, nil
}

// SetProjectAlerts is the caller's own choice to be told about a project. A sign-in
// that can only read cannot make it, though a viewer may.
func (a apiServer) SetProjectAlerts(ctx context.Context, req api.SetProjectAlertsRequestObject) (api.SetProjectAlertsResponseObject, error) {
	c := callerOf(ctx)
	if _, sees := c.Access.Role(req.ProjectId); !sees {
		return nil, store.ErrNotFound
	}
	if c.scope() != store.ScopeManage {
		return nil, store.ErrForbidden
	}
	if err := a.s.Store.SetOwnAlerts(ctx, c.Access, req.ProjectId, req.Body.Alerts, a.s.Now()); err != nil {
		return nil, err
	}
	return api.SetProjectAlerts204Response{}, nil
}

func outsiderOf(o store.Outsider) api.ProjectOutsider {
	return api.ProjectOutsider{Id: o.ID, Name: o.Name, Role: api.Role(o.Role), ProjectId: o.ProjectID, ProjectName: o.ProjectName}
}

// GetTeam returns a team and its people, for one of its owners.
func (a apiServer) GetTeam(ctx context.Context, req api.GetTeamRequestObject) (api.GetTeamResponseObject, error) {
	tp, err := a.s.Store.PeopleOfTeam(ctx, callerOf(ctx).Access, req.TeamId, a.s.Now())
	if err != nil {
		return nil, err
	}
	out := api.GetTeam200JSONResponse{Id: tp.Team.ID, Name: tp.Team.Name, Members: []api.TeamMember{}, Outsiders: []api.ProjectOutsider{}}
	for _, m := range tp.Members {
		mem := api.TeamMember{Id: m.ID, Name: m.Name, Role: api.Role(m.Role)}
		if m.CameIn != "" {
			mem.InvitedAs = &m.CameIn
			by := api.LogParty{Kind: "former_member"}
			if m.InvitedByID != "" {
				by = api.LogParty{Kind: "person", Id: optional(m.InvitedByID), Name: optional(m.InvitedBy)}
			}
			mem.InvitedBy = &by
		}
		out.Members = append(out.Members, mem)
	}
	for _, o := range tp.Outsiders {
		out.Outsiders = append(out.Outsiders, outsiderOf(o))
	}
	return out, nil
}

// SetTeamMember changes a member's role in a team, and says which roles of their own
// ended with it.
func (a apiServer) SetTeamMember(ctx context.Context, req api.SetTeamMemberRequestObject) (api.SetTeamMemberResponseObject, error) {
	role, err := roleToGive(req.Body.Role)
	if err != nil {
		return nil, err
	}
	access := callerOf(ctx).Access
	ended, err := a.s.Store.SetTeamMemberAs(ctx, access, req.TeamId, req.PersonId, role)
	if err != nil {
		return nil, err
	}
	tp, err := a.s.Store.PeopleOfTeam(ctx, access, req.TeamId, a.s.Now())
	if err != nil {
		return nil, err
	}
	out := api.SetTeamMember200JSONResponse{Id: req.PersonId, Role: api.Role(role)}
	out.EndedRoles = make([]struct {
		ProjectId   api.Id   `json:"project_id"`
		ProjectName string   `json:"project_name"`
		Role        api.Role `json:"role"`
	}, len(ended))
	for i, e := range ended {
		out.EndedRoles[i].ProjectId, out.EndedRoles[i].ProjectName, out.EndedRoles[i].Role = e.ProjectID, e.ProjectName, api.Role(e.Role)
	}
	for _, m := range tp.Members {
		if m.ID == req.PersonId {
			out.Name, out.Role = m.Name, api.Role(m.Role)
		}
	}
	return out, nil
}

// RemoveTeamMember takes someone out of a team, and says what they keep there.
func (a apiServer) RemoveTeamMember(ctx context.Context, req api.RemoveTeamMemberRequestObject) (api.RemoveTeamMemberResponseObject, error) {
	stays, err := a.s.Store.RemoveTeamMemberAs(ctx, callerOf(ctx).Access, req.TeamId, req.PersonId)
	if err != nil {
		return nil, err
	}
	out := api.RemoveTeamMember200JSONResponse{Roles: []api.ProjectOutsider{}}
	out.Keys = make([]struct {
		Id          api.Id `json:"id"`
		Name        string `json:"name"`
		ProjectId   api.Id `json:"project_id"`
		ProjectName string `json:"project_name"`
	}, len(stays.Keys))
	for i, k := range stays.Keys {
		out.Keys[i].Id, out.Keys[i].Name, out.Keys[i].ProjectId, out.Keys[i].ProjectName = k.ID, k.Name, k.ProjectID, k.ProjectName
	}
	for _, o := range stays.Roles {
		out.Roles = append(out.Roles, outsiderOf(o))
	}
	return out, nil
}

func inviteOf(inv store.Invite) api.Invite {
	out := api.Invite{
		Id: inv.ID, Kind: inv.Kind, Role: api.Role(inv.Role), Label: inv.Label, MadeBy: logPartyOf(inv.MadeBy),
		CreatedAt: inv.Created, ExpiresAt: inv.Ends,
	}
	if inv.ProjectID != "" {
		out.ProjectId, out.ProjectName = optional(inv.ProjectID), optional(inv.ProjectName)
	}
	return out
}

func inviteList(invites []store.Invite) api.InviteList {
	out := api.InviteList{Items: []api.Invite{}}
	for _, inv := range invites {
		out.Items = append(out.Items, inviteOf(inv))
	}
	return out
}

// madeInviteOf is an invitation that was just made, with its link, which is never
// shown again.
func (a apiServer) madeInviteOf(inv store.Invite, secret string) (api.InviteMade, error) {
	out, err := as[api.InviteMade](inviteOf(inv))
	if err != nil {
		return api.InviteMade{}, err
	}
	out.Url = a.s.base.String() + "/join#" + secret
	return out, nil
}

// ListTeamInvites lists a team's open invitations, to it and to its projects.
func (a apiServer) ListTeamInvites(ctx context.Context, req api.ListTeamInvitesRequestObject) (api.ListTeamInvitesResponseObject, error) {
	invites, err := a.s.Store.OpenInvites(ctx, callerOf(ctx).Access, req.TeamId, a.s.Now())
	if err != nil {
		return nil, err
	}
	return api.ListTeamInvites200JSONResponse(inviteList(invites)), nil
}

// CreateTeamInvite makes an invitation into a team.
func (a apiServer) CreateTeamInvite(ctx context.Context, req api.CreateTeamInviteRequestObject) (api.CreateTeamInviteResponseObject, error) {
	role, err := roleToGive(req.Body.Role)
	if err != nil {
		return nil, err
	}
	inv, secret, err := a.s.Store.CreateInvite(ctx, callerOf(ctx).Access, req.TeamId, "", role, req.Body.Label, a.s.Now())
	if p, ok := conflict(err); ok {
		return api.CreateTeamInvite409ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	out, err := a.madeInviteOf(inv, secret)
	if err != nil {
		return nil, err
	}
	return api.CreateTeamInvite201JSONResponse(out), nil
}

// ListProjectInvites lists a project's open invitations, for one of its owners.
func (a apiServer) ListProjectInvites(ctx context.Context, req api.ListProjectInvitesRequestObject) (api.ListProjectInvitesResponseObject, error) {
	pp, err := a.s.Store.PeopleOfProject(ctx, callerOf(ctx).Access, req.ProjectId, a.s.Now())
	if err != nil {
		return nil, err
	}
	return api.ListProjectInvites200JSONResponse(inviteList(pp.Invites)), nil
}

// CreateProjectInvite makes an invitation into one project, with a role of one's own.
func (a apiServer) CreateProjectInvite(ctx context.Context, req api.CreateProjectInviteRequestObject) (api.CreateProjectInviteResponseObject, error) {
	role, err := roleToGive(req.Body.Role)
	if err != nil {
		return nil, err
	}
	inv, secret, err := a.s.Store.CreateInvite(ctx, callerOf(ctx).Access, "", req.ProjectId, role, req.Body.Label, a.s.Now())
	if p, ok := conflict(err); ok {
		return api.CreateProjectInvite409ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	out, err := a.madeInviteOf(inv, secret)
	if err != nil {
		return nil, err
	}
	return api.CreateProjectInvite201JSONResponse(out), nil
}

// WithdrawInvite withdraws an open invitation.
func (a apiServer) WithdrawInvite(ctx context.Context, req api.WithdrawInviteRequestObject) (api.WithdrawInviteResponseObject, error) {
	err := a.s.Store.WithdrawInvite(ctx, callerOf(ctx).Access, req.InviteId, a.s.Now())
	if p, ok := conflict(err); ok {
		return api.WithdrawInvite409ApplicationProblemPlusJSONResponse(p), nil
	}
	if err != nil {
		return nil, err
	}
	return api.WithdrawInvite204Response{}, nil
}
