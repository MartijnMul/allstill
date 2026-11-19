<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: teams, step 1, the model and the upgrade

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-19
Status: accepted
Approved: 2026-11-19, by the maintainer, with the change to the spec's order below; risk: higher risk (a migration that rebuilds two tables and cannot be edited once committed, and the one place that decides what everyone sees and who is told)
Codebase: MartijnMul/allstill at 49145f7
Reviewed: 2026-11-19, by an independent pass against the code, which tried the migration's mechanics on a copy of the schema; what it found is worked in

This plans the first of the spec's five steps, and only that one. Each later step gets its plan when the one before runs on a real installation.

After this step an installation has one team that owns every project. Access and alerts are worked out the new way, and nobody sees a difference: no page, command, API answer, message or log line changes. It covers R1, R2, R4, R14, R16, R17, R42 and R45, and the half of R15 that can be done unseen.

## What is built, and what waits

Built now:
- `teams` and `team_members`, and a team on every project and probe.
- `alert_choices`, which takes over from the `alerts` flag on a membership.
- One definition of a person's role in a project, and one of who is told, which every query uses.

Left for the step that needs it, each in its own migration:
- `project_shares` and `links` (steps 3 and 4), `team_entries` (step 5).
- A team's limits and private ranges, `log_entries.team_id`, and every place that reads `Access.Admin` (step 2). With one team its administrators own it, so "administrator" and "owner of the team" still see the same.
- Anything in the API, the command line's words, or the dashboard.

## One change to the spec's order

The spec puts all of R15 in this step: an administrator's own rows in single projects become an alert choice and stop being a role. This plan moves the removal of those rows to step 2 and keeps the rest.

- **Why.** Whether an administrator has a row of their own in a project decides more than their alerts today. It decides whether a log line they wrote, or one about a message that reached them, names them or says "an administrator", whether a key shows who made it, and what `allstill admin set-member` prints and logs for them. Removing the rows changes each of those for new lines, on every installation, in a step that promises to change nothing.
- **What stays.** The `alerts` flag still moves to `alert_choices` now. The rows stay, and since the highest role counts they give an administrator nothing they do not have as owner of the team.
- **Where it goes.** Step 2 decides what the log and key lists show about a team's owners, with the other readers of `Access.Admin`, and removes the rows then.

## Design of this step

**The migration,** `0016_teams.sql`, in this order:
- `teams`: id, workspace, name, when made, with `UNIQUE (workspace_id, id)` as every table that others point to has. `team_members`: workspace, team, person, role, when added, one row per person per team.
- `projects` and `probes` are rebuilt, so that `team_id` is never empty and is held by a foreign key on workspace and team together. Adding a column cannot do that in SQLite.
  - `projects` keeps `UNIQUE (workspace_id, id)`, which memberships, checks and keys point to, and its unique ping key hash with its check.
  - `probes` keeps its unique key hash and its own check. "One name per workspace" becomes "one name per team", which is the same thing while there is one team. Step 2 needs that, and a second rebuild of the same table would be the price of waiting.
  - Ids, ping keys and every other column are copied as they are.
- Where a workspace exists, one team is made, named as the workspace is. Every administrator becomes an owner of it, disabled ones too, as a disabled person has no access either way.
- `alert_choices`: workspace, person, project, on or off, removed with its person or its project as a membership is. A row is written only where a membership's flag differs from what the person's highest role gives. In a database the code made, that is an administrator set to viewer in a project, who gets "off".
- `memberships.alerts` is dropped, before the views are made: SQLite lets a column go that a view still reads.
- Two views, neither of which leaves out disabled people or deleted projects, as the queries that use them differ on that today and keep their own conditions:
  - `project_roles`: per project and person, the highest of their role in the project's team and their own role in the project.
  - `project_alerted`: per project, the people to tell: those whose role in `project_roles` is owner or editor, unless their `alert_choices` says otherwise, and a viewer whose choice says to tell them.
  - Both are written flat, as joins on primary keys with the role picked by a `CASE`, so every query that uses them looks rows up as it does today. A share in step 4 adds a second row per pair, and the views are rewritten then.

**Access.** `ProjectsOfPerson` reads `project_roles`. It no longer gives an administrator every project by being one; they get them as owners of the team. `Access`, `Access.Admin` and `AdminAccess` keep their shape.

**Who is told.** Eight queries change:
- Seven spell out "a membership with alerts on, or an administrator with no membership" and join `project_alerted` instead: `AddFollowers`, `DueRecipients`, `DigestIncidents`, `SilentChecks`, `WarningChecks` and `ProjectsNobodyFollows` in `incidents.sql`, and `StillFollowed` in `channels.sql`.
- `PeopleWhoGetAlerts` asks whether a person is told about any project, and is rewritten on the same view. Today it counts a deleted project for a member and not for an administrator. No code deletes a project yet, so one rule for both changes nothing. For the same reason it changes nothing that the view needs the project's row to exist, where today an administrator follows an incident whose project's row is gone.

**Writers keep the model whole:**
- `Workspace` makes the first team with the workspace, in one transaction.
- `CreatePerson` for an administrator adds them to the only team as owner (R18).
- `CreateProject` puts a project in the installation's only team, and refuses with an error naming `-team` where there are several, which step 2 makes possible and then lets a caller say. Its callers do not change.
- A probe gets its team from the project its own check is in, and a project made by `move-checks` from the project the checks come from.
- `setMember` works out what today's rule would make the flag: on when someone becomes more than a viewer, off when they become one, kept otherwise. It then writes a choice only where that differs from what their highest role gives, and removes the choice where it does not. `RemoveMember` removes the choice with the row. So `allstill admin set-member -role viewer` still stops an administrator's alerts, `-role none` still gives them back, and both print and log what they do today.
- `RoleInProject` keeps meaning a person's own row in the project, which the log, key lists and `set-member` ask for.

## Files that change

- `internal/store/migrations/0016_teams.sql` (new): as above.
- `internal/id/id.go` (changed): a prefix for a team.
- `internal/store/queries/teams.sql` (new), `workspace.sql`, `incidents.sql`, `channels.sql`, `probes.sql` (changed), `internal/store/internal/db` (generated).
- `internal/store/teams.go` (new): finding the only team.
- `internal/store/workspace.go` (changed): `Workspace`, `CreatePerson`, `CreateProject`, `setMember`, `RemoveMember`, `AccessOfPerson`'s comment.
- `internal/store/probe.go`, `incidents.go`, `channels.go` (changed): the team on a probe, and the readers of who is told.
- `internal/store/store_test.go`, `workspace_test.go` (changed): five tests insert a project with plain SQL and need a team, and `TestAlertsFollowTheRole` reads the dropped flag and asks the view instead. No other existing test changes, and none is weakened.
- `docs/running.md` and `AGENTS.md` (changed): that an upgrade makes one team, how to go back, and that nothing else is different yet.

## Order of work

1. **The reference test, first.** A test builds a database with the migrations up to 0015 and fills it with plain SQL. It works out with today's own queries, copied into the test, what each person sees and for which projects they are told. It then applies 0016 and compares. It fails until the migration exists. It also proves, in the SQLite the program is built with, what the review tried in the command-line one: that the flag can be dropped, that two rebuilt tables leave no orphan, and that `sqlc` reads the views.
2. **The migration,** then `make generate`.
3. **Access:** `ProjectsOfPerson` on the view.
4. **Who is told:** the eight queries, one at a time, each with its existing tests.
5. **The writers,** in the order under "Design".
6. **The docs.**
7. **On a copy of a real installation's database,** in a temporary directory: apply the migration, note how long it takes, compare counts of every table before and after, and compare each person's projects and alerts before and after with the reference test's queries. Then send a ping to an existing URL and by project key.
8. **A release candidate** on the real installation, after CI is green. Then: every person's overview shows what it did, a test incident reaches who it reached before, a ping arrives, and the log names an administrator where it did and not where it did not.

Going back: the store keeps a copy of the database from before the migration. Stopping the server, putting that copy back and starting the release before it loses everything since the upgrade: pings, state changes, incidents, messages, log lines, sign-ins and changes to checks. It is for the first minutes, not the next day. The store does not replace a copy that exists, so the copy is moved away before a second try.

## Tests that prove it

- **Nobody loses or gains (R16).** The reference test, with: an administrator with no membership; one set to viewer in a project, alerts off; one with a membership as owner; an owner, an editor and a viewer; an editor whose alerts are off and a viewer whose alerts are on, which the code cannot make and an old database might hold; someone in two projects; a disabled person; a disabled administrator; a deleted project; a read-only sign-in; a project's key; a probe with its projects. For each: the projects and roles, and the projects they are told about, the same before and after.
- **The writers afterwards.** One table of steps run twice, against the migrations up to 0015 with plain SQL as today's statements write it, and against the store after 0016: viewer to editor and back, removed and added again, an administrator set to viewer and then to none, an administrator set to editor and then to none, the same role given twice. After every step: who is told, what `set-member` prints, and which log lines exist with their "from" and "to".
- **One definition (R42).** A test fails when a query in `internal/store/queries/` reads `alert_choices` or `team_members`, or names `workspace_admin` next to `memberships`, outside the views. The queries that write a choice or a member are named in it.
- **The rule itself.** A table over team role, own role and choice, each also absent, against the rule written out in Go: the highest role counts; told when owner or editor unless chosen otherwise; a viewer told only by choice. And a test that reads both views after every migration, so a later one cannot break them unseen.
- **Fresh and whole.** A first start makes a workspace and a team. A new administrator owns the team. A new project and a new probe are in it. Two teams, made directly in the test, make `OnlyTeam` refuse. Every table has a workspace, as the existing test asks.
- **The log and keys (R17).** Lines and keys from before the migration, and new ones after it, read the same to an administrator, an owner, an editor and a viewer as they do today, for an administrator with a row of their own and for one without.
- **Speed.** The query plans of `DueRecipients`, `AddFollowers`, `DigestIncidents` and `StillFollowed` in `make check` show no scan and no view built in memory.
- **Pings and checks keep running (R45).** After the migration: a ping by URL, a ping by project key and slug, `create=1`, and a probe fetching its work, each against rows made before it.
