// SPDX-License-Identifier: AGPL-3.0-only

package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MartijnMul/allstill/internal/apiclient"
)

// A team's owners running their team (intent/teams/plan-3.md, part C). An owner is
// never named, made less or taken out from here: the server refuses it, and says the
// dashboard does that.

// answered is what every call here does with an answer: the value it expects, or what
// the server refused.
func answered[T any](c *Client, ctx context.Context, resp *http.Response, body []byte, got *T) (*T, []byte, error) {
	if got == nil {
		return nil, nil, c.refusal(ctx, resp, body, true)
	}
	return got, body, nil
}

// done is answered for a call that answers with nothing.
func (c *Client) done(ctx context.Context, resp *http.Response, body []byte) error {
	if resp.StatusCode != http.StatusNoContent {
		return c.refusal(ctx, resp, body, true)
	}
	return nil
}

// MakeProject makes a project, in the team given or, with "", the one the caller owns.
func (c *Client) MakeProject(ctx context.Context, name, teamID string) (*apiclient.ProjectCreated, []byte, error) {
	in := apiclient.CreateProjectJSONRequestBody{Name: name}
	if teamID != "" {
		in.TeamId = &teamID
	}
	r, err := c.api.CreateProjectWithResponse(ctx, in)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON201)
}

// RenameProject gives a project a new name.
func (c *Client) RenameProject(ctx context.Context, projectID, name string) (*apiclient.Project, []byte, error) {
	r, err := c.api.UpdateProjectWithResponse(ctx, projectID, apiclient.UpdateProjectJSONRequestBody{Name: &name})
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// DeleteProject deletes a project without checks.
func (c *Client) DeleteProject(ctx context.Context, projectID string) error {
	r, err := c.api.DeleteProjectWithResponse(ctx, projectID)
	if err != nil {
		return c.unreachable(err)
	}
	return c.done(ctx, r.HTTPResponse, r.Body)
}

// ProjectPeople says who can see a project.
func (c *Client) ProjectPeople(ctx context.Context, projectID string) (*apiclient.ProjectPeople, []byte, error) {
	r, err := c.api.ListProjectPeopleWithResponse(ctx, projectID)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// SetProjectRole changes someone's role of their own in a project, or with "" ends it.
func (c *Client) SetProjectRole(ctx context.Context, projectID, personID, role string) ([]byte, error) {
	if role == "" {
		r, err := c.api.RemoveProjectRoleWithResponse(ctx, projectID, personID)
		if err != nil {
			return nil, c.unreachable(err)
		}
		return r.Body, c.done(ctx, r.HTTPResponse, r.Body)
	}
	r, err := c.api.SetProjectRoleWithResponse(ctx, projectID, personID, apiclient.SetProjectRoleJSONRequestBody{Role: apiclient.RoleToGive(role)})
	if err != nil {
		return nil, c.unreachable(err)
	}
	_, body, err := answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
	return body, err
}

// SetAlerts is the caller's own choice to be told about a project.
func (c *Client) SetAlerts(ctx context.Context, projectID string, on bool) error {
	r, err := c.api.SetProjectAlertsWithResponse(ctx, projectID, apiclient.SetProjectAlertsJSONRequestBody{Alerts: on})
	if err != nil {
		return c.unreachable(err)
	}
	return c.done(ctx, r.HTTPResponse, r.Body)
}

// Team returns a team and its people.
func (c *Client) Team(ctx context.Context, teamID string) (*apiclient.TeamPeople, []byte, error) {
	r, err := c.api.GetTeamWithResponse(ctx, teamID)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// SetTeamMember changes a member's role, and says which roles of their own ended.
func (c *Client) SetTeamMember(ctx context.Context, teamID, personID, role string) (*apiclient.TeamMemberSet, []byte, error) {
	r, err := c.api.SetTeamMemberWithResponse(ctx, teamID, personID, apiclient.SetTeamMemberJSONRequestBody{Role: apiclient.RoleToGive(role)})
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// RemoveTeamMember takes someone out of a team, and says what they keep there.
func (c *Client) RemoveTeamMember(ctx context.Context, teamID, personID string) (*apiclient.WhatStays, []byte, error) {
	r, err := c.api.RemoveTeamMemberWithResponse(ctx, teamID, personID)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// Invites lists the open invitations of a team, or with a project's id, of that project.
func (c *Client) Invites(ctx context.Context, teamID, projectID string) (*apiclient.InviteList, []byte, error) {
	if projectID != "" {
		r, err := c.api.ListProjectInvitesWithResponse(ctx, projectID)
		if err != nil {
			return nil, nil, c.unreachable(err)
		}
		return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
	}
	r, err := c.api.ListTeamInvitesWithResponse(ctx, teamID)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON200)
}

// MakeInvite makes an invitation into a team or, with a project's id, into that project,
// with what the server said, which holds its link, once.
func (c *Client) MakeInvite(ctx context.Context, teamID, projectID, role, label string) (*apiclient.InviteMade, []byte, error) {
	in := apiclient.InviteInput{Role: apiclient.RoleToGive(role), Label: label}
	if projectID != "" {
		r, err := c.api.CreateProjectInviteWithResponse(ctx, projectID, in)
		if err != nil {
			return nil, nil, c.unreachable(err)
		}
		return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON201)
	}
	r, err := c.api.CreateTeamInviteWithResponse(ctx, teamID, in)
	if err != nil {
		return nil, nil, c.unreachable(err)
	}
	return answered(c, ctx, r.HTTPResponse, r.Body, r.JSON201)
}

// WithdrawInvite withdraws an open invitation.
func (c *Client) WithdrawInvite(ctx context.Context, id string) error {
	r, err := c.api.WithdrawInviteWithResponse(ctx, id)
	if err != nil {
		return c.unreachable(err)
	}
	return c.done(ctx, r.HTTPResponse, r.Body)
}

// FindTeam finds a team the caller is in, by name or id. With "", it is the one team
// they own, and otherwise it says which there are (R18).
func (c *Client) FindTeam(ctx context.Context, nameOrID string) (apiclient.MeTeam, error) {
	me, err := c.Me(ctx)
	if err != nil {
		return apiclient.MeTeam{}, err
	}
	var teams []apiclient.MeTeam
	if me.Teams != nil {
		teams = *me.Teams
	}
	if nameOrID != "" {
		for _, t := range teams {
			if t.Id == nameOrID || t.Name == nameOrID {
				return t, nil
			}
		}
		return apiclient.MeTeam{}, noSuch(fmt.Sprintf("you are in no team named %q", Clean(nameOrID)))
	}
	var owned []apiclient.MeTeam
	for _, t := range teams {
		if t.Role == apiclient.RoleOwner {
			owned = append(owned, t)
		}
	}
	switch len(owned) {
	case 0:
		return apiclient.MeTeam{}, errors.New("you own no team: its owners run it")
	case 1:
		return owned[0], nil
	}
	names := make([]string, len(owned))
	for i, t := range owned {
		names[i] = Clean(t.Name)
	}
	return apiclient.MeTeam{}, fmt.Errorf("give -team: you own %s", strings.Join(names, ", "))
}

// oneOf picks the one thing a name or id stands for among what can be seen there, or
// says there is none, or which there are when two share the name (R46).
func oneOf[T any](things []T, nameOrID, what, where string, id, name func(T) string) (T, error) {
	var found []T
	for _, t := range things {
		if id(t) == nameOrID || name(t) == nameOrID {
			found = append(found, t)
		}
	}
	var none T
	switch len(found) {
	case 0:
		return none, noSuch(fmt.Sprintf("there is no %s %q %s", what, Clean(nameOrID), where))
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of them are named %q %s. Use one of these ids:", len(found), Clean(nameOrID), where)
	for _, t := range found {
		fmt.Fprintf(&b, "\n  %s", Clean(id(t)))
	}
	return none, errors.New(b.String())
}

// FindMember finds someone in a team, by name or id.
func FindMember(team *apiclient.TeamPeople, nameOrID string) (apiclient.TeamMember, error) {
	return oneOf(team.Members, nameOrID, "one", "in "+Clean(team.Name),
		func(m apiclient.TeamMember) string { return m.Id }, func(m apiclient.TeamMember) string { return m.Name })
}

// FindPerson finds someone who can see a project, by name or id.
func FindPerson(people *apiclient.ProjectPeople, nameOrID, project string) (apiclient.ProjectPerson, error) {
	return oneOf(people.Items, nameOrID, "one", "who can see "+Clean(project),
		func(p apiclient.ProjectPerson) string { return p.Id }, func(p apiclient.ProjectPerson) string { return p.Name })
}

// FindInvite finds an open invitation, by id or by whom it is for.
func FindInvite(invites *apiclient.InviteList, idOrLabel string) (apiclient.Invite, error) {
	return oneOf(invites.Items, idOrLabel, "open invitation for", "here",
		func(i apiclient.Invite) string { return i.Id }, func(i apiclient.Invite) string { return i.Label })
}
