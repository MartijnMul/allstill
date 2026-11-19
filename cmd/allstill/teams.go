// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/MartijnMul/allstill/internal/client"
	"github.com/MartijnMul/allstill/internal/logtext"
	"github.com/MartijnMul/allstill/internal/store"
)

// Teams, on the server (intent/teams/spec.md, R9, R10, R13, R18, R34): only
// whoever keeps the installation makes a team, names its first owner and says
// what it may take.

// teamFlag is -team, for a command that names a project: needed only where
// two teams have a project of that name.
func teamFlag(fs *flag.FlagSet) *string {
	return fs.String("team", "", "the team, by name or id; needed only where teams share the name")
}

// teamNamed finds the team -team names, with an error a person can act on.
func teamNamed(ctx context.Context, st *store.Store, ws, team string) (store.Team, error) {
	t, err := st.TeamNamed(ctx, ws, team)
	if errors.Is(err, store.ErrNotFound) {
		return t, fmt.Errorf("no team is named %q; allstill admin teams lists them", team)
	}
	return t, err
}

// projectNamed finds a project by its name, in the team named when one is.
func projectNamed(ctx context.Context, st *store.Store, ws, team, name string) (string, error) {
	var teamID string
	if team != "" {
		t, err := teamNamed(ctx, st, ws, team)
		if err != nil {
			return "", err
		}
		teamID = t.ID
	}
	return st.ProjectByNameIn(ctx, ws, teamID, name)
}

// teamFor is the team a new thing goes in: the one named, or the only one.
func teamFor(ctx context.Context, st *store.Store, ws, team string) (store.Team, error) {
	if team != "" {
		return teamNamed(ctx, st, ws, team)
	}
	teams, err := st.Teams(ctx, ws)
	if err != nil {
		return store.Team{}, err
	}
	if len(teams) != 1 {
		return store.Team{}, fmt.Errorf("this installation has %d teams, so say which with -team; allstill admin teams lists them", len(teams))
	}
	return teams[0], nil
}

func createTeam(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin create-team", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin create-team NAME (-owner EMAIL|ID | -owner-name NAME [-owner-email EMAIL]) [flags]

Makes a team beside the ones there are, and names its first owner: someone who
has an account, or a new person, who gets a link to make a passkey. A team owns
projects, and its owner runs it. As an administrator you keep the installation
and are not shown what is in a team you are not in.

A new team may have 400 checks Allstill runs and 2000 that jobs ping, and
reaches none of the private ranges the server was given: allstill admin
set-team changes that.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	owner := fs.String("owner", "", "its first owner, someone who has an account: their email address or id")
	ownerName := fs.String("owner-name", "", "its first owner, a new person: their name")
	ownerEmail := fs.String("owner-email", "", "where the new person's email alerts and digest go")
	tz := fs.String("time-zone", "UTC", "the time zone the dashboard shows the new person, such as Europe/Amsterdam")
	baseURL := fs.String("base-url", envOr("ALLSTILL_BASE_URL", ""), "the dashboard's address (ALLSTILL_BASE_URL; default: the one the server last ran with)")
	name, rest := nameFirst(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	*ownerName = strings.TrimSpace(*ownerName)
	if name == "" || (*owner == "") == (*ownerName == "") {
		fs.Usage()
		return errors.New("give the team's name, and -owner or -owner-name")
	}
	if _, err := time.LoadLocation(*tz); err != nil || *tz == "" || *tz == "Local" {
		return fmt.Errorf("-time-zone %q is not a time zone such as Europe/Amsterdam", *tz)
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	by := store.Command("create-team")
	var person store.Person
	if *owner != "" {
		if person, err = personByEmailOrID(ctx, st, ws, *owner); err != nil {
			return err
		}
	} else if *ownerEmail != "" {
		if _, err := st.PersonByEmail(ctx, ws, *ownerEmail); err == nil {
			return fmt.Errorf("someone with the email address %s exists already; name them with -owner %s", *ownerEmail, *ownerEmail)
		}
	}
	var base fmt.Stringer
	if *owner == "" {
		if base, err = baseOf(ctx, st, *baseURL); err != nil {
			return err
		}
	}
	teamID, err := st.CreateTeam(ctx, by, ws, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Made the team %s (%s).\n", client.Clean(name), teamID)
	if *owner == "" {
		id, err := st.CreatePerson(ctx, by, ws, store.NewPerson{Name: strings.TrimSpace(*ownerName), Email: *ownerEmail, TimeZone: *tz})
		if err != nil {
			return err
		}
		person = store.Person{ID: id, Name: strings.TrimSpace(*ownerName)}
	}
	if err := st.SetTeamMember(ctx, by, ws, teamID, person.ID, store.Owner); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s (%s) owns it.\n", client.Clean(person.Name), person.ID)
	if *owner == "" {
		token, err := st.CreatePasskeyLink(ctx, by, ws, person.ID, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "They make a passkey with this link, which works once, for 24 hours:\n\n  %s/setup#%s\n", base, token)
	}
	fmt.Fprintln(stdout, "\nMake its first project with: allstill admin create-project -team "+strconv.Quote(name)+" NAME")
	return nil
}

// personByEmailOrID finds someone who has an account.
func personByEmailOrID(ctx context.Context, st *store.Store, ws, who string) (store.Person, error) {
	var p store.Person
	var err error
	if strings.Contains(who, "@") {
		p, err = st.PersonByEmail(ctx, ws, who)
	} else {
		p, err = st.Person(ctx, ws, who)
	}
	if errors.Is(err, store.ErrNotFound) {
		return p, fmt.Errorf("no one with %s, or they are disabled", who)
	}
	return p, err
}

func listTeams(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin teams", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin teams [-people] [-db file]

Lists the teams: who owns each, what it may take and how much it holds. With
-people, everyone with an account and the teams they are in. It names no
project and no check.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	people := fs.Bool("people", false, "list everyone with an account and their teams")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("teams takes no name")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if *people {
		all, err := st.PeopleWithTeams(ctx, ws)
		if err != nil {
			return err
		}
		for _, p := range all {
			what := strings.Join(p.Teams, ", ")
			if what == "" {
				what = "in no team"
			}
			if p.Admin {
				what = "administrator; " + what
			}
			if p.Disabled {
				what += "; disabled"
			}
			fmt.Fprintf(stdout, "%s  %s  %s\n", p.ID, client.Clean(p.Name), client.Clean(what))
		}
		return nil
	}
	teams, err := st.TeamSummaries(ctx, ws)
	if err != nil {
		return err
	}
	limit := func(n int, what string) string {
		if n < 0 {
			return "any number of " + what
		}
		return strconv.Itoa(n) + " " + what
	}
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	for _, t := range teams {
		owners := "no owner"
		if len(t.Owners) > 0 {
			owners = "owned by " + strings.Join(t.Owners, ", ")
		}
		private := "public internet only"
		if t.Private {
			private = "may reach the server's private ranges"
		}
		fmt.Fprintf(stdout, "%s (%s), %s\n  %s, %s, %s, %s\n  may have %s and %s; %s\n",
			client.Clean(t.Name), t.ID, client.Clean(owners), count(t.People, "person", "people"), count(t.Projects, "project", "projects"),
			count(t.Checks, "check", "checks"), count(t.Probes, "probe", "probes"),
			limit(t.MaxActive, "checks Allstill runs"), limit(t.MaxPing, "checks that jobs ping"), private)
	}
	return nil
}

func setTeam(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-team", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-team TEAM [-name NAME] [-active-checks N|none] [-ping-checks N|none] [-private on|off] [-db file]

Changes a team's name, how many checks it may have, or whether its checks may
reach the private ranges the server was started with (-allow-private). A
limit refuses new checks and never stops one that exists.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	newName := fs.String("name", "", "its new name")
	active := fs.String("active-checks", "", "how many checks Allstill runs it may have, or none for no limit")
	ping := fs.String("ping-checks", "", "how many checks that jobs ping it may have, or none for no limit")
	private := fs.String("private", "", "on to let its checks reach the server's private ranges, off to hold them to the public internet")
	name, rest := nameFirst(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	if name == "" || *newName+*active+*ping+*private == "" {
		fs.Usage()
		return errors.New("give the team and what to change")
	}
	var ch store.TeamChange
	if *newName != "" {
		ch.Name = newName
	}
	for flagName, v := range map[string]*string{"active-checks": active, "ping-checks": ping} {
		if *v == "" {
			continue
		}
		n := -1
		if *v != "none" {
			var err error
			if n, err = strconv.Atoi(*v); err != nil || n < 0 {
				return fmt.Errorf("-%s takes a number, or none", flagName)
			}
		}
		if flagName == "active-checks" {
			ch.MaxActive = &n
		} else {
			ch.MaxPing = &n
		}
	}
	switch *private {
	case "":
	case "on", "off":
		on := *private == "on"
		ch.Private = &on
	default:
		return errors.New("-private takes on or off")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := teamNamed(ctx, st, ws, name)
	if err != nil {
		return err
	}
	if err := st.SetTeam(ctx, store.Command("set-team"), ws, t.ID, ch); err != nil {
		return err
	}
	if ch.Name != nil {
		fmt.Fprintf(stdout, "Changed the team %s, now %s.\n", client.Clean(t.Name), client.Clean(*ch.Name))
	} else {
		fmt.Fprintf(stdout, "Changed the team %s.\n", client.Clean(t.Name))
	}
	if ch.Private != nil && *ch.Private {
		fmt.Fprintln(stdout, "Its editors can now have the server fetch from the private ranges it was started with, and read what answers there.")
	}
	return nil
}

func setTeamMember(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin set-team-member", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: allstill admin set-team-member (-email EMAIL | -person ID) -team TEAM -role ROLE [-db file]

Gives someone a role in a team, which is their role in every project it owns:
viewer, editor or owner, or none to take them out of it. The team's owners are
told. Its last owner cannot be taken out or made less: name another first.

Flags:
`)
		fs.PrintDefaults()
	}
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	email := fs.String("email", "", "the person's email address")
	personID := fs.String("person", "", "the person's id, per_…")
	team := fs.String("team", "", "the team, by name or id")
	roleName := fs.String("role", "", "viewer, editor, owner, or none")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*email == "") == (*personID == "") || *team == "" || *roleName == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("give -email or -person, and -team and -role")
	}
	var role store.Role
	if *roleName != "none" {
		var err error
		if role, err = store.ParseRole(*roleName); err != nil {
			return fmt.Errorf("-role: %w", err)
		}
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := personNamed(ctx, st, ws, *email, *personID)
	if err != nil {
		return err
	}
	t, err := teamNamed(ctx, st, ws, *team)
	if err != nil {
		return err
	}
	err = st.SetTeamMember(ctx, store.Command("set-team-member"), ws, t.ID, p.ID, role)
	switch {
	case errors.Is(err, store.ErrLastOwner):
		return fmt.Errorf("%s is the last owner of %s: name another owner first", p.Name, t.Name)
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%s is not in %s", p.Name, t.Name)
	case err != nil:
		return err
	case role == "":
		fmt.Fprintf(stdout, "%s is no longer in the team %s. A role of their own in one of its projects stays: allstill admin set-member ends one.\n", client.Clean(p.Name), client.Clean(t.Name))
	default:
		fmt.Fprintf(stdout, "%s is now %s of the team %s, and of every project it owns.\n", client.Clean(p.Name), role, client.Clean(t.Name))
	}
	return nil
}

func removeTeam(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin remove-team", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr("ALLSTILL_DB", "allstill.db"), "the database file (ALLSTILL_DB)")
	name, rest := nameFirst(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	if name == "" {
		return errors.New("usage: allstill admin remove-team TEAM [-db file]")
	}
	st, ws, err := openStore(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := teamNamed(ctx, st, ws, name)
	if err != nil {
		return err
	}
	if err := st.RemoveTeam(ctx, store.Command("remove-team"), ws, t.ID); err != nil {
		if holds, ok := errors.AsType[*store.TeamHolds](err); ok {
			return fmt.Errorf("%s still holds %s and %s, and a team that holds any stays", t.Name,
				logtext.Plural(holds.Projects, "project", "projects"), logtext.Plural(holds.Probes, "probe", "probes"))
		}
		return err
	}
	fmt.Fprintf(stdout, "Removed the team %s. Its people keep their accounts.\n", client.Clean(t.Name))
	return nil
}
