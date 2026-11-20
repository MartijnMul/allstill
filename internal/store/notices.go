// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A command on the server works on every team, as whoever has the shell has
// the database. So it tells on itself (intent/teams/spec.md, R27 to R29): what
// lets someone into a team, or lets someone become one of its people, is told
// to the team's owners at once, whatever the alerts switch says, and stays at
// the top of their overview until they have read it. A change to who holds a
// team is told the same way when a person makes it (plan-3.md), and someone
// accepting an invitation is a card on the owners' overview.

// The kinds of notice.
const (
	noticeRoleGiven     = "role.given"      // a role in one of the team's projects
	noticeRoleTaken     = "role.taken"      // a project's owner of their own no longer is
	noticeTeamRoleGiven = "team.role_given" // a role in the team, an owner's among them
	noticeTeamRoleTaken = "team.role_taken" // taken out of the team
	noticeLinkMade      = "signin.link_made"
	noticePasskeyGone   = "passkey.removed"
	noticeSignInsEnded  = "signins.ended"
	// Invitations (plan-3.md): one as owner was made, and one was accepted.
	noticeOwnerInvite    = "invite.owner_made"
	noticeInviteAccepted = "invite.accepted"
)

// NoticeKind is the kind of a delivery that carries a notice.
const NoticeKind = "notice"

// Notice is what an owner of a team was told about a command on the server.
type Notice struct {
	ID    string
	Title string
	At    time.Time
	Read  bool
}

// noticeTitle says a notice in one line, from its facts: who did it is a
// command on the server, or a person by name.
func noticeTitle(kind string, d map[string]any) string {
	s := func(key string) string { v, _ := d[key].(string); return v }
	by := "A command on the server (" + s("command") + ")"
	if s("command") == "" && s("by") != "" {
		by = s("by")
	}
	switch kind {
	case noticeOwnerInvite:
		where := "the team " + s("team")
		if s("project") != "" {
			where = "the project " + s("project")
		}
		return fmt.Sprintf("%s made an invitation as owner of %s, for %q. Whoever holds it can become an owner.", by, where, s("label"))
	case noticeInviteAccepted:
		where := "the team " + s("team")
		if s("project") != "" {
			where = "the project " + s("project")
		}
		if s("by") == "" {
			return fmt.Sprintf("%s accepted an invitation and is now %s of %s.", s("person"), article(s("role")), where)
		}
		return fmt.Sprintf("%s accepted the invitation %s made for %q and is now %s of %s.", s("person"), s("by"), s("label"), article(s("role")), where)
	case noticeRoleGiven:
		return fmt.Sprintf("%s made %s %s of %s.", by, s("person"), s("role"), s("project"))
	case noticeRoleTaken:
		return fmt.Sprintf("%s ended the role %s had of their own in %s.", by, s("person"), s("project"))
	case noticeTeamRoleGiven:
		return fmt.Sprintf("%s made %s %s of the team %s.", by, s("person"), s("role"), s("team"))
	case noticeTeamRoleTaken:
		return fmt.Sprintf("%s took %s out of the team %s.", by, s("person"), s("team"))
	case noticeLinkMade:
		return fmt.Sprintf("%s made a sign-in link for %s. Whoever holds it can sign in as them.", by, s("person"))
	case noticePasskeyGone:
		return fmt.Sprintf("%s took away a passkey of %s.", by, s("person"))
	case noticeSignInsEnded:
		return fmt.Sprintf("%s ended every sign-in of %s.", by, s("person"))
	}
	return by + " reached into your team."
}

// tellOwners tells the owners of teams what a command on the server did, within
// the transaction that does it. Anything that is not a command tells nobody: a
// person acting in the dashboard is in the log under their name. Someone who
// owns several of the teams is told once. also names people who are told as
// well, such as an owner who was made less a moment ago.
func tellOwners(ctx context.Context, q *db.Queries, by Actor, workspaceID string, teamIDs, also []string, kind string, details map[string]any, at time.Time) error {
	if by.Kind != "command" {
		return nil
	}
	details["command"] = by.Name
	return noticeOwners(ctx, q, workspaceID, teamIDs, also, kind, details, at, true)
}

// noticeOwners tells the owners of teams, and the people in also, what happened.
// A loud notice goes to every channel they have confirmed, whatever the alerts
// switch says and through quiet hours; a quiet one is only the card on their
// overview, for what they should see but need not be woken for.
func noticeOwners(ctx context.Context, q *db.Queries, workspaceID string, teamIDs, also []string, kind string, details map[string]any, at time.Time, loud bool) error {
	told := map[string]bool{}
	for _, teamID := range teamIDs {
		owners, err := q.OwnersOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		people := slices.Clone(also)
		for _, o := range owners {
			people = append(people, o.ID)
		}
		people = slices.DeleteFunc(people, func(p string) bool { return told[p] })
		for _, p := range people {
			told[p] = true
		}
		if err := noticePeople(ctx, q, workspaceID, teamID, people, kind, details, at, loud); err != nil {
			return err
		}
	}
	return nil
}

// noticePeople tells these people, once each, about something in a team.
func noticePeople(ctx context.Context, q *db.Queries, workspaceID, teamID string, people []string, kind string, details map[string]any, at time.Time, loud bool) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	items, err := json.Marshal([]Item{{What: NoticeKind, Title: noticeTitle(kind, details)}})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, personID := range people {
		if seen[personID] {
			continue
		}
		seen[personID] = true
		err := q.CreateNotice(ctx, db.CreateNoticeParams{
			ID: id.New(id.Notice), WorkspaceID: workspaceID, PersonID: personID, TeamID: teamID, Kind: kind, Details: string(raw), CreatedAt: at.UnixMilli(),
		})
		if err != nil {
			return err
		}
		if !loud {
			continue
		}
		// To every way they have confirmed, not the first that works: it is
		// about who can get in, and they should see it wherever they look.
		channels, err := q.ConfirmedChannelsOfPerson(ctx, nullString(personID))
		if err != nil {
			return err
		}
		message := id.New(id.Delivery)
		for i, c := range channels {
			d := message
			if i > 0 {
				d = id.New(id.Delivery)
			}
			err := q.CreatePendingDelivery(ctx, db.CreatePendingDeliveryParams{
				ID: d, WorkspaceID: c.WorkspaceID, PersonID: personID, ChannelID: nullString(c.ID), ChannelKind: nullString(c.Kind),
				Kind: NoticeKind, Items: string(items), CreatedAt: at.UnixMilli(), NextAt: nullMs(at), MessageID: nullString(message),
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// tellOwnersAbout tells the owners of every team a person is in, or has a role of
// their own in a project of.
func tellOwnersAbout(ctx context.Context, q *db.Queries, by Actor, workspaceID, personID, kind string, details map[string]any, at time.Time) error {
	if by.Kind != "command" {
		return nil
	}
	teams, err := q.TeamsConcerning(ctx, personID)
	if err != nil {
		return err
	}
	return tellOwners(ctx, q, by, workspaceID, teams, nil, kind, details, at)
}

// Notices returns what a person was told about commands on the server, newest first.
func (s *Store) Notices(ctx context.Context, personID string, limit int) ([]Notice, error) {
	rows, err := db.New(s.r).NoticesOfPerson(ctx, db.NoticesOfPersonParams{PersonID: personID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	notices := make([]Notice, len(rows))
	for i, r := range rows {
		var details map[string]any
		json.Unmarshal([]byte(r.Details), &details)
		notices[i] = Notice{ID: r.ID, Title: noticeTitle(r.Kind, details), At: time.UnixMilli(r.CreatedAt).UTC(), Read: r.ReadAt.Valid}
	}
	return notices, nil
}

// ReadNotices marks what a person was told up to a moment as read: what they
// were shown, and not what arrived since.
func (s *Store) ReadNotices(ctx context.Context, personID string, upTo time.Time) error {
	return db.New(s.w).MarkNoticesRead(ctx, db.MarkNoticesReadParams{ReadAt: nullMs(time.Now()), PersonID: personID, UpTo: upTo.UnixMilli()})
}

// LogCommandRead writes that a command on the server read a project, in that
// project's log. It tells nobody: nothing changed (spec, R27).
func (s *Store) LogCommandRead(ctx context.Context, workspaceID, command, projectID string) error {
	return writeLog(ctx, db.New(s.w), logEntry{
		WorkspaceID: workspaceID, At: time.Now(), ProjectID: projectID, Kind: "command.read",
		AboutKind: "project", AboutID: projectID, By: Command(command),
	})
}

// article puts "a" or "an" before a role: an editor, a viewer.
func article(role string) string {
	if role == string(Editor) || role == string(Owner) {
		return "an " + role
	}
	return "a " + role
}
