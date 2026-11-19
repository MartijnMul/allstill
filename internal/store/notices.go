// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MartijnMul/allstill/internal/id"
	"github.com/MartijnMul/allstill/internal/store/internal/db"
)

// A command on the server works on every team, as whoever has the shell has
// the database. So it tells on itself (intent/teams/spec.md, R27 to R29): what
// lets someone into a team, or lets someone become one of its people, is told
// to the team's owners at once, whatever the alerts switch says, and stays at
// the top of their overview until they have read it.

// The kinds of notice.
const (
	noticeRoleGiven     = "role.given"      // a role in one of the team's projects
	noticeTeamRoleGiven = "team.role_given" // a role in the team, an owner's among them
	noticeTeamRoleTaken = "team.role_taken" // taken out of the team
	noticeLinkMade      = "signin.link_made"
	noticePasskeyGone   = "passkey.removed"
	noticeSignInsEnded  = "signins.ended"
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

// noticeTitle says a notice in one line, from its facts.
func noticeTitle(kind string, d map[string]any) string {
	s := func(key string) string { v, _ := d[key].(string); return v }
	by := "A command on the server (" + s("command") + ")"
	switch kind {
	case noticeRoleGiven:
		return fmt.Sprintf("%s made %s %s of %s.", by, s("person"), s("role"), s("project"))
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
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	items, err := json.Marshal([]Item{{What: NoticeKind, Title: noticeTitle(kind, details)}})
	if err != nil {
		return err
	}
	told := map[string]bool{}
	for _, teamID := range teamIDs {
		owners, err := q.OwnersOfTeam(ctx, teamID)
		if err != nil {
			return err
		}
		people := also
		for _, o := range owners {
			people = append(people, o.ID)
		}
		for _, personID := range people {
			o := struct{ ID string }{personID}
			if told[o.ID] {
				continue
			}
			told[o.ID] = true
			err := q.CreateNotice(ctx, db.CreateNoticeParams{
				ID: id.New(id.Notice), WorkspaceID: workspaceID, PersonID: o.ID, TeamID: teamID, Kind: kind, Details: string(raw), CreatedAt: at.UnixMilli(),
			})
			if err != nil {
				return err
			}
			// To every way they have confirmed, not the first that works: it is
			// about who can get in, and they should see it wherever they look.
			channels, err := q.ConfirmedChannelsOfPerson(ctx, nullString(o.ID))
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
					ID: d, WorkspaceID: c.WorkspaceID, PersonID: o.ID, ChannelID: nullString(c.ID), ChannelKind: nullString(c.Kind),
					Kind: NoticeKind, Items: string(items), CreatedAt: at.UnixMilli(), NextAt: nullMs(at), MessageID: nullString(message),
				})
				if err != nil {
					return err
				}
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
