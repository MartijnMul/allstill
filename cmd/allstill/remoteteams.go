// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MartijnMul/allstill/internal/apiclient"
	"github.com/MartijnMul/allstill/internal/client"
)

// A team's owners running their team from a command line, over the API
// (intent/teams/plan-3.md, part C). An owner is named, invited, made less or taken
// out only in the dashboard, signed in with a passkey; the server says so when asked.

// projectsSubcommands are what `allstill projects` does beside listing.
var projectsSubcommands = map[string]func(context.Context, []string, io.Writer, io.Writer) error{
	"add": projectsAdd, "rename": projectsRename, "delete": projectsDelete, "people": projectsPeople,
	"role": projectsRole, "alerts": projectsAlerts, "invite": projectsInvite, "invites": projectsInvites,
	"withdraw": projectsWithdraw,
}

// teamsSubcommands are what `allstill teams` does beside showing a team.
var teamsSubcommands = map[string]func(context.Context, []string, io.Writer, io.Writer) error{
	"role": teamsRole, "remove": teamsRemove, "invite": teamsInvite, "withdraw": teamsWithdraw,
}

// subcommand is one subcommand's flags and usage, read the same way for each.
type subcommand struct {
	fs *flag.FlagSet
	r  reading
}

func newCommand(name, usageLine, help string, stderr io.Writer) subcommand {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: allstill %s\n\n%s\n\nFlags:\n", usageLine, help)
		fs.PrintDefaults()
	}
	return subcommand{fs, readingFlags(fs)}
}

// args parses the flags, wherever they are, and wants exactly n arguments.
func (c subcommand) args(args []string, n int, what string) ([]string, error) {
	rest, err := parseAround(c.fs, args)
	if err != nil {
		return nil, err
	}
	if len(rest) != n {
		c.fs.Usage()
		return nil, errors.New(what)
	}
	return rest, nil
}

func projectsAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects add", "projects add NAME [-team TEAM] [-json] [-server URL]",
		`Makes a project in a team you own, with you its owner through the team and every other
member at their role there. Its ping key, for pings by slug, is printed once.`, stderr)
	team := c.fs.String("team", "", "the team, by name or id; needed only when you own more than one")
	rest, err := c.args(args, 1, "give the project one name")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	teamID := ""
	if *team != "" {
		t, err := api.FindTeam(ctx, *team)
		if err != nil {
			return said(api, err)
		}
		teamID = t.Id
	}
	made, raw, err := api.MakeProject(ctx, rest[0], teamID)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	in := ""
	if made.Team != nil {
		in = " in " + client.Clean(made.Team.Name)
	}
	fmt.Fprintf(stdout, "Made the project %s%s (%s). Its ping key, for pings by slug, is shown only now:\n\n  %s\n\nA job pings %s/ping/KEY/SLUG with it, and with ?create=1 makes the check the first time.\n",
		client.Clean(made.Name), in, client.Clean(made.Id), client.Clean(made.PingKey), api.Server)
	return nil
}

func projectsRename(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects rename", "projects rename PROJECT NAME [-json] [-server URL]",
		`Gives a project of a team you own a new name. Its ping key stays, so jobs that ping by
slug keep working.`, stderr)
	rest, err := c.args(args, 2, "give the project and its new name")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	renamed, raw, err := api.RenameProject(ctx, p.Id, rest[1])
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "Renamed %s to %s.\n", client.Clean(p.Name), client.Clean(renamed.Name))
	return nil
}

func projectsDelete(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects delete", "projects delete PROJECT [-server URL]",
		`Deletes a project of a team you own, for good. Only a project without checks can be
deleted: delete or move its checks first. Its ping key and keys stop working, its open
invitations and the roles of their own in it end, and its name can be used again.`, stderr)
	rest, err := c.args(args, 1, "give one project")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	if err := api.DeleteProject(ctx, p.Id); err != nil {
		return said(api, err)
	}
	fmt.Fprintf(stdout, "Deleted the project %s.\n", client.Clean(p.Name))
	return nil
}

func projectsPeople(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects people", "projects people PROJECT [-json] [-server URL]",
		`Lists who can see a project you own, through what, and whether they are told. An owner
from outside the project's team reads the team's people as a count.`, stderr)
	rest, err := c.args(args, 1, "give one project")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	people, raw, err := api.ProjectPeople(ctx, p.Id)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	printProjectPeople(stdout, people)
	return nil
}

func printProjectPeople(w io.Writer, people *apiclient.ProjectPeople) {
	if people.Team != nil {
		fmt.Fprintf(w, "The team: %s, %d of them told.\n", countOf(people.Team.People, "person", "people"), people.Team.Alerted)
		if len(people.Items) == 0 {
			return
		}
		fmt.Fprintln(w)
	}
	if len(people.Items) == 0 {
		fmt.Fprintln(w, "Nobody else.")
		return
	}
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "NAME\tROLE\tTHROUGH\tTOLD\tID")
	for _, p := range people.Items {
		var through []string
		if p.TeamRole != nil {
			through = append(through, "the team, as "+string(*p.TeamRole))
		}
		if p.OwnRole != nil {
			through = append(through, "their own, as "+string(*p.OwnRole))
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", client.Clean(p.Name), client.Clean(string(p.Role)), client.Clean(strings.Join(through, "; ")), yesNo(p.Alerts), client.Clean(p.Id))
	}
	t.Flush()
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func projectsRole(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects role", "projects role PROJECT PERSON viewer|editor|none [-server URL]",
		`Changes someone's role of their own in a project you own, or with none ends it. It never
gives one: someone new comes in by an invitation (allstill projects invite). The role
must be more than the team gives them. A person is named by name, where it is one of a
kind there, or by id. An owner's role changes only in the dashboard.`, stderr)
	rest, err := c.args(args, 3, "give the project, the person and the role")
	if err != nil {
		return err
	}
	role := rest[2]
	if role != "viewer" && role != "editor" && role != "none" {
		return errors.New("the role is viewer, editor or none; an owner is named in the dashboard")
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	people, _, err := api.ProjectPeople(ctx, p.Id)
	if err != nil {
		return said(api, err)
	}
	who, err := client.FindPerson(people, rest[1], p.Name)
	if err != nil {
		return err
	}
	if role == "none" {
		role = ""
	}
	if _, err := api.SetProjectRole(ctx, p.Id, who.Id, role); err != nil {
		return said(api, err)
	}
	if role == "" {
		fmt.Fprintf(stdout, "Ended the role %s had of their own in %s.\n", client.Clean(who.Name), client.Clean(p.Name))
		return nil
	}
	fmt.Fprintf(stdout, "%s is now %s of %s.\n", client.Clean(who.Name), article(role), client.Clean(p.Name))
	return nil
}

func article(role string) string {
	if role == "viewer" {
		return "a viewer"
	}
	return "an " + role
}

func projectsAlerts(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects alerts", "projects alerts PROJECT on|off [-server URL]",
		`Chooses whether you are told when a check of a project goes down, and when it comes
back. It is yours alone to choose, with any role in the project.`, stderr)
	rest, err := c.args(args, 2, "give the project, and on or off")
	if err != nil {
		return err
	}
	if rest[1] != "on" && rest[1] != "off" {
		return errors.New("say on or off")
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	if err := api.SetAlerts(ctx, p.Id, rest[1] == "on"); err != nil {
		return said(api, err)
	}
	if rest[1] == "on" {
		fmt.Fprintf(stdout, "You are told about %s.\n", client.Clean(p.Name))
	} else {
		fmt.Fprintf(stdout, "You are no longer told about %s. Its checks stay on your overview.\n", client.Clean(p.Name))
	}
	return nil
}

// inviteFlags are what making an invitation asks.
func inviteFlags(fs *flag.FlagSet) (role, label *string) {
	return fs.String("role", "viewer", "viewer or editor; an owner is invited in the dashboard"),
		fs.String("for", "", "for whom it is, such as Carol or the backup contractor, so you can tell the right person came in")
}

func printMadeInvite(w io.Writer, made *apiclient.InviteMade, where string) {
	fmt.Fprintf(w, "An invitation for %s, as %s of %s. Send this link to them however you like. It works once, until %s, and is shown only now:\n\n  %s\n",
		client.Clean(made.Label), article(string(made.Role)), where, client.Clock(time.Now(), made.ExpiresAt, time.Local), client.Clean(made.Url))
}

func projectsInvite(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects invite", "projects invite PROJECT -for LABEL [-role viewer|editor] [-json] [-server URL]",
		`Makes an invitation into a project you own, and prints its link once. Whoever opens it
signs in, or makes an account with a passkey, and gets the role in this project alone.
Nobody is looked up by name or address.`, stderr)
	role, label := inviteFlags(c.fs)
	rest, err := c.args(args, 1, "give one project")
	if err != nil {
		return err
	}
	if *label == "" {
		c.fs.Usage()
		return errors.New("give -for: for whom it is")
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	made, raw, err := api.MakeInvite(ctx, "", p.Id, *role, *label)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	printMadeInvite(stdout, made, client.Clean(p.Name))
	return nil
}

func printInvites(w io.Writer, invites []apiclient.Invite) {
	if len(invites) == 0 {
		fmt.Fprintln(w, "No open invitations.")
		return
	}
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "FOR\tROLE\tIN\tMADE BY\tWORKS UNTIL\tID")
	for _, inv := range invites {
		in := "the team"
		if inv.ProjectName != nil {
			in = *inv.ProjectName
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", client.Clean(inv.Label), client.Clean(string(inv.Role)), client.Clean(in),
			partyName(inv.MadeBy), client.Clock(time.Now(), inv.ExpiresAt, time.Local), client.Clean(inv.Id))
	}
	t.Flush()
}

// partyName says who someone is, as far as this reader may read it.
func partyName(p apiclient.LogParty) string {
	switch p.Kind {
	case "team":
		return "the team"
	case "administrator":
		return "an administrator"
	case "former_member":
		return "someone no longer in the team"
	}
	if p.Name != nil {
		return client.Clean(*p.Name)
	}
	return "someone"
}

func projectsInvites(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects invites", "projects invites PROJECT [-json] [-server URL]",
		`Lists the open invitations into a project you own.`, stderr)
	rest, err := c.args(args, 1, "give one project")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	invites, raw, err := api.Invites(ctx, "", p.Id)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	printInvites(stdout, invites.Items)
	return nil
}

func projectsWithdraw(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("projects withdraw", "projects withdraw PROJECT INVITATION [-server URL]",
		`Withdraws an open invitation into a project you own, named by whom it is for or by id.
Its link stops working at once.`, stderr)
	rest, err := c.args(args, 2, "give the project and the invitation")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	p, err := api.FindProject(ctx, rest[0])
	if err != nil {
		return said(api, err)
	}
	invites, _, err := api.Invites(ctx, "", p.Id)
	if err != nil {
		return said(api, err)
	}
	return withdraw(ctx, api, invites, rest[1], stdout)
}

func withdraw(ctx context.Context, api *client.Client, invites *apiclient.InviteList, which string, stdout io.Writer) error {
	inv, err := client.FindInvite(invites, which)
	if err != nil {
		return err
	}
	if err := api.WithdrawInvite(ctx, inv.Id); err != nil {
		return said(api, err)
	}
	fmt.Fprintf(stdout, "Withdrew the invitation for %s. Its link no longer works.\n", client.Clean(inv.Label))
	return nil
}

func teamsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		if sub, ok := teamsSubcommands[args[0]]; ok {
			return sub(ctx, args[1:], stdout, stderr)
		}
	}
	c := newCommand("teams", `teams [TEAM] [-json] [-server URL]
       allstill teams role PERSON viewer|editor [-team TEAM]
       allstill teams remove PERSON [-team TEAM]
       allstill teams invite -for LABEL [-role viewer|editor] [-team TEAM]
       allstill teams withdraw INVITATION [-team TEAM]`,
		`Shows a team you own: its members, the people outside it with a role of their own in
one of its projects, and its open invitations. Without TEAM, the one you own. A team's
owners let people in with an invitation, and change or take away what they may do. An
owner is named, invited, made less or taken out in the dashboard.`, stderr)
	rest, err := parseAround(c.fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		c.fs.Usage()
		return fmt.Errorf("unknown teams command %q", client.Clean(rest[0]))
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	t, err := api.FindTeam(ctx, name)
	if err != nil {
		return said(api, err)
	}
	team, raw, err := api.Team(ctx, t.Id)
	if err != nil {
		return said(api, err)
	}
	invites, rawInvites, err := api.Invites(ctx, t.Id, "")
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, []byte(fmt.Sprintf(`{"team":%s,"invites":%s}`, raw, rawInvites)))
	}
	printTeam(stdout, team, invites.Items)
	return nil
}

func printTeam(w io.Writer, team *apiclient.TeamPeople, invites []apiclient.Invite) {
	fmt.Fprintf(w, "%s (%s)\n\n", client.Clean(team.Name), client.Clean(team.Id))
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "MEMBER\tROLE\tCAME IN\tID")
	for _, m := range team.Members {
		came := "by a command on the server"
		if m.InvitedAs != nil {
			came = fmt.Sprintf("invited as %q", client.Clean(*m.InvitedAs))
			if m.InvitedBy != nil {
				came += " by " + partyName(*m.InvitedBy)
			}
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", client.Clean(m.Name), client.Clean(string(m.Role)), came, client.Clean(m.Id))
	}
	t.Flush()
	if len(team.Outsiders) > 0 {
		fmt.Fprintln(w)
		t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(t, "OUTSIDE THE TEAM\tROLE\tIN\tID")
		for _, o := range team.Outsiders {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", client.Clean(o.Name), client.Clean(string(o.Role)), client.Clean(o.ProjectName), client.Clean(o.Id))
		}
		t.Flush()
	}
	fmt.Fprintln(w)
	printInvites(w, invites)
}

// teamAndMember reads the team a command is about and finds someone in it.
func teamAndMember(ctx context.Context, api *client.Client, team, person string) (*apiclient.TeamPeople, apiclient.TeamMember, error) {
	t, err := api.FindTeam(ctx, team)
	if err != nil {
		return nil, apiclient.TeamMember{}, said(api, err)
	}
	people, _, err := api.Team(ctx, t.Id)
	if err != nil {
		return nil, apiclient.TeamMember{}, said(api, err)
	}
	m, err := client.FindMember(people, person)
	return people, m, err
}

func teamsRole(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("teams role", "teams role PERSON viewer|editor [-team TEAM] [-json] [-server URL]",
		`Changes a member's role in a team you own, in every project the team owns. It never
makes anyone a member: someone new comes in by an invitation (allstill teams invite). A
role of their own in a project that no longer gives them more ends, and is listed. An
owner's role changes only in the dashboard.`, stderr)
	team := c.fs.String("team", "", "the team, by name or id; needed only when you own more than one")
	rest, err := c.args(args, 2, "give the person and the role")
	if err != nil {
		return err
	}
	if rest[1] != "viewer" && rest[1] != "editor" {
		return errors.New("the role is viewer or editor; an owner is named in the dashboard, and allstill teams remove takes someone out")
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	people, m, err := teamAndMember(ctx, api, *team, rest[0])
	if err != nil {
		return err
	}
	set, raw, err := api.SetTeamMember(ctx, people.Id, m.Id, rest[1])
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "%s is now %s of %s.\n", client.Clean(set.Name), article(string(set.Role)), client.Clean(people.Name))
	for _, e := range set.EndedRoles {
		fmt.Fprintf(stdout, "Their role of their own in %s, as %s, ended: it gave no more.\n", client.Clean(e.ProjectName), client.Clean(string(e.Role)))
	}
	return nil
}

func teamsRemove(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("teams remove", "teams remove PERSON [-team TEAM] [-json] [-server URL]",
		`Takes someone out of a team you own. From their next request they see none of its
projects through it. Their account, other teams, sign-ins and devices stay, and so do
roles of their own in its projects and keys they made there: those are listed, to end
them too if that was meant. An owner is taken out only in the dashboard.`, stderr)
	team := c.fs.String("team", "", "the team, by name or id; needed only when you own more than one")
	rest, err := c.args(args, 1, "give one person")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	people, m, err := teamAndMember(ctx, api, *team, rest[0])
	if err != nil {
		return err
	}
	stays, raw, err := api.RemoveTeamMember(ctx, people.Id, m.Id)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	fmt.Fprintf(stdout, "Took %s out of %s.\n", client.Clean(m.Name), client.Clean(people.Name))
	for _, r := range stays.Roles {
		fmt.Fprintf(stdout, "They are still %s of %s, by a role of their own: allstill projects role %q %s none ends it.\n",
			article(string(r.Role)), client.Clean(r.ProjectName), client.Clean(r.ProjectName), client.Clean(m.Id))
	}
	for _, k := range stays.Keys {
		fmt.Fprintf(stdout, "The key %s they made in %s keeps working: allstill keys revoke %s ends it.\n",
			client.Clean(k.Name), client.Clean(k.ProjectName), client.Clean(k.Id))
	}
	return nil
}

func teamsInvite(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("teams invite", "teams invite -for LABEL [-role viewer|editor] [-team TEAM] [-json] [-server URL]",
		`Makes an invitation into a team you own, and prints its link once. Whoever opens it
signs in, or makes an account with a passkey, and gets the role in every project the
team owns. Nobody is looked up by name or address.`, stderr)
	role, label := inviteFlags(c.fs)
	team := c.fs.String("team", "", "the team, by name or id; needed only when you own more than one")
	if _, err := c.args(args, 0, "teams invite takes no arguments: give -for"); err != nil {
		return err
	}
	if *label == "" {
		c.fs.Usage()
		return errors.New("give -for: for whom it is")
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	t, err := api.FindTeam(ctx, *team)
	if err != nil {
		return said(api, err)
	}
	made, raw, err := api.MakeInvite(ctx, t.Id, "", *role, *label)
	if err != nil {
		return said(api, err)
	}
	if *c.r.json {
		return client.PrintJSON(stdout, raw)
	}
	printMadeInvite(stdout, made, client.Clean(t.Name))
	return nil
}

func teamsWithdraw(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	c := newCommand("teams withdraw", "teams withdraw INVITATION [-team TEAM] [-server URL]",
		`Withdraws an open invitation of a team you own, into the team or one of its projects,
named by whom it is for or by id. Its link stops working at once.`, stderr)
	team := c.fs.String("team", "", "the team, by name or id; needed only when you own more than one")
	rest, err := c.args(args, 1, "give one invitation")
	if err != nil {
		return err
	}
	api, err := c.r.client()
	if err != nil {
		return err
	}
	t, err := api.FindTeam(ctx, *team)
	if err != nil {
		return said(api, err)
	}
	invites, _, err := api.Invites(ctx, t.Id, "")
	if err != nil {
		return said(api, err)
	}
	return withdraw(ctx, api, invites, rest[0], stdout)
}
