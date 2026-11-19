<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: teams, step 2, a second team, walled off and announced

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-19
Step 1: [plan.md](plan.md), running on a real installation since 2026-11-19
Status: accepted
Approved: 2026-11-19, by the maintainer's word to carry out step 2; the changes to the spec listed below are put to them with the first part
Risk: higher risk (the wall between parties who must not see each other, three migrations that cannot be edited once committed, and messages that go out whatever the alerts switch says)
Codebase: MartijnMul/allstill at 2523cb4
Reviewed: 2026-11-19, by an independent pass against the code, which looked for leaks and tried the migrations' mechanics on a copy of the schema; what it found is worked in

This plans the second of the spec's five steps. After it an administrator can make a second team on the server. Its people see their own projects and nothing of anyone else's, the administrator is not shown them, and every command on the server that reaches into a team says so to that team.

It covers R5, R6, R8 to R13, R18, R24, R27 to R30, R34 to R38, R40, R43 and R44, and the half of R15 that step 1 left.

## Three parts, each a release

A second team must not exist before the whole wall does. So the command that makes one comes last, and the first two parts change little that a one-team installation can see.

- **Part A, the wall.** Access, the log, keys, probes and names know about teams. Tests make a second team through the store and walk every page and API operation. No command makes a team yet.
- **Part B, commands tell on themselves.** A notice to a team's owners, sent whatever the alerts switch says, when a command on the server lets someone in or makes a sign-in link for a member who has a passkey.
- **Part C, a second team.** Limits and turns per team, private ranges per team, and the commands: `create-team`, `teams`, `set-team`, `set-team-member`, `remove-team`, and `-team` where a command would have to guess.

Each part has its own migration, 0017 to 0019, and each is tried on a copy of a real database before it is rolled out.

## What changes for an installation with one team

Nothing in what anyone sees or may do, with these exceptions, each on purpose:

- **Owners are told about commands on the server (part B).** `create-user`, `set-member`, `passkey-link`, `remove-passkey` and `end-sessions` now send a message to the owners of the teams the person is in, who on a one-team installation are its administrators. The command line has no person, so Allstill cannot know the owner ran it themselves. It is what makes a stolen shell, or one administrator becoming another, loud.
- **`allstill whoami` no longer says an administrator "can see and change every project".** It says they keep the installation. `allstill admin set-member` says of an administrator that they have their role from the team that owns the project, where it said they see every project.
- **"Only an owner or administrator" becomes "only an owner"** where `critical` is turned on, and "an owner of its team" for a probe's own check. With one team these are the same people.
- **An administrator who was given a role of their own in a project loses that row (part A).** To an editor or owner of that project who is not in the team, their new lines read "an administrator" where they read the name. Who sees what and who is told does not change.
- **`project.created` becomes a line a project's owners read.** Today only an administrator does.
- **Narrowing the log by a name or a person** reads the lines the reader may see, not every line of that name, so it can be slower on a long log and never tells of lines elsewhere.
- **The API gains fields** (`team` on a project, `teams` on `me`, later the message kind `notice`). Nothing is removed. 0.13.0 with part A, 0.14.0 with part B.
- **Texts that say an administrator sees every project,** in the commands' help and the server's start-up warning about private ranges, are rewritten in the part that makes them untrue.

## Changes to the spec, for the maintainer to accept with this plan

- **The log's signature for a viewer stays "the team".** The spec makes it the team's name. A team is named as its installation is, "Allstill" unless renamed, and "Allstill paused `backup`" reads as the program acting. "The team" is the team of the project the line is in, which is never ambiguous. The name comes with step 4, where a line must say "someone in" another team.
- **Private ranges are opened per team as a whole, not range by range.** The server's `-allow-private` stays the list. A team may reach all of it or none, kept so that naming ranges later needs no new table. R13 holds: the first team may, a new team may not until said. Naming ranges per team waits for an installation that opens two networks to different parties.
- **What R11 gives an administrator comes through the log.** A message that reached someone by no way at all is a line they can read with the person and the reason, once per person and incident, and without the project, the check or the incident. The line is written when every channel failed, so it has no one kind of channel; the reason names it. A list of people with their teams is `allstill admin teams -people` on the server. A page for it comes with managing people away from the server (cli-anywhere R28).
- **Removing an empty team is built now** (R9), with R33's refusal for one that holds projects or probes. Nothing deletes a project yet, so only a team that never got one can go.
- **Teams in the API wait for step 3,** but for the two fields above, which R5 needs to tell two projects of one name apart. A project's `team` is given only to a member of that team: someone with a role of their own in one project does not learn the team from it (R6, activity-log R21).
- **A notice is held back by `allstill serve -shadow`,** though not by the alerts switch. That flag is how a copy of a database is served, and a copy must never message real people (R29 says "in shadow mode", which this reads as the switch).
- **More is told than R28 lists.** Taking someone's passkey away and ending their sign-ins from the server are told too, and a sign-in link is told for anyone who has a role anywhere, with or without a passkey. Otherwise removing the last passkey and then making a link becomes someone silently.

## Part A, the wall

**Who is in a team.** `Access` gains the caller's teams with their role in each, and each project in it says which team owns it. `Access.Admin` keeps its name and now only means keeper of the installation. `AdminAccess`, for commands on the server, holds every team and project as owner, as it does today.

**Every reader of `Access.Admin`, decided:**

| Where | Today | After |
| --- | --- | --- |
| Why a message to you failed (`messages.go`) | administrator | administrator: mail and push are the installation's (R11, R12) |
| Sender behind, mail cannot be sent (`health.go`) | administrator | administrator (R12) |
| "There is no project yet", the import hint (`pages.go`) | administrator | an owner of a team that has no project |
| A probe's own check: alerting, moving (`web/manage.go`, `store/manage.go`) | administrator | an owner of the team the check's project is in (R37) |
| Turning `critical` on (`store/manage.go`) | owner of the project, or administrator | owner of the project by any way in (R38) |
| "You get no alerts" on Devices (`channels.go`) | not an administrator, and viewer everywhere | viewer everywhere, or in no project |
| Who made a key, and through which sign-in (`keys.go`) | administrator reads all | someone in the project's team reads the name; the sign-in is for the team's owners and the maker |
| Making a probe, a new key for one (`probe.go`) | administrator | unchanged: only commands on the server (R37, R30) |
| Reading the log (`logread.go`) | administrator reads everything | see "The log" |
| `me.workspace_admin` (API) | "sees every project" | keeper of the installation; the description says so, and `whoami` stops reading it as more |
| A read-only sign-in (`tokens.go`) | viewer everywhere, no administrator | the same, and viewer in every team |

**The rest of R15.** Migration 0017 removes a person's own row in a project where their role in the project's team is as high or higher: the row gives nothing, and it would turn into a real role the day they leave the team. Their choice about alerts is in `alert_choices` since step 1 and stays. After it:

- `setMember` keeps a row of one's own only where it gives more than the team does, and removes one that no longer does. Where no row is kept it sets the choice: told or not as today's rule would have it, and no choice at all where that is what their role gives anyway. So `allstill admin set-member -role viewer` still stops an administrator's alerts for a project and `-role none` still gives them back (R18), also when there is a choice and no row.
- That change is logged as what it is, `alerts.chosen`, for the project's owners to read, not as a role that does not exist. The command says that a role below the team's cannot be given in one project, and what it did instead.
- The role test of step 1, which plays every run of role changes against the statements from before teams, keeps holding who is told. Which row exists is no longer compared for someone in the team. One thing is given up: an administrator's row that only an old database could hold, an editor's with alerts off or a viewer's with alerts on, cannot be told from the rows the code makes once it has gone, so the next change of their role tells them by the new role alone.

**The log (R40).** One function still decides what a reader may read of a line. Its rule becomes:

- **A line in a project:** the reader's role in that project decides, as for everyone but an administrator today. An administrator has no way past it. Every kind written in a project has a role that reads it, `project.created` an owner's, and a test fails on one that has none.
- **A line of a team and no project** (new column `log_entries.team_id`): the team's owners read it. An administrator reads the kinds that concern the installation: a team made, changed or removed, and its probes. Lines about probes written before this step have no team; an administrator reads them and a team's owners do not.
- **A line of neither:** an administrator reads it, and the person it concerns reads what is their own, as today.
- **A message that reached someone by no way:** also read by an administrator, stripped before anything is narrowed, so that no filter by check, name or project can confirm what it was about (R11). One line is shown per person and incident. An incident holds one check today; once incidents join several, which dependencies will bring, one line must be written per person and incident, or the lines' numbers would count its checks.

The new kinds, which cannot be renamed later: `alerts.chosen`, `team.created`, `team.changed`, `team.removed`, `team.member_set`, `team.member_removed`, and `command.read` for a command on the server that only reads.

Names follow the reason activity-log R22 gave, that a customer's editors are not staff:

- Someone in a project without a row of their own is recorded on the line, as today: an administrator as 1, and from now someone in through the team who is no administrator as 2. The column has no constraint, so no table changes.
- A reader who is owner or editor in the project's team reads the name, on old lines and new. Every administrator an old line hides became an owner of that team in step 1, so this names nobody to a stranger. It replaces "an administrator reads everything".
- To an editor or owner of the project who is not in its team, 1 reads "an administrator", as it does today, and 2 reads "the team".
- A viewer reads "the team" for every person, in the team or not, as today.
- A probe's line no longer lists the projects it serves to a reader who cannot see them.

Reading changes with it: an administrator's query has no "everything" any more. They read their projects one by one as everyone does, plus the installation's lines and the failed messages, each through an index that exists. Narrowing by a name or by a person no longer reads every line with that value in the installation: it reads what the reader may see and matches there, so a busy name in another team cannot be felt as short pages. A log line's number counts for the whole installation, so its gaps say roughly how much happens elsewhere; `docs/running.md` says so.

**Keys.** Who made a key reads as the log does. Listing "every project I own" follows the caller's roles, which for an administrator now means their team's.

**Probes (R37).** A probe serves projects of its own team only, checked when it is made. Its name need be told apart only within its team, which the table has held since step 1. Lookups by name take the team.

**Names (R5).** A project's name is checked within its team, on making and renaming; the commands that make one look the name up first, as they do today, so nothing starts refusing. Where two projects a person can see share a name, the dashboard and the command line show `Team / Project` for a team they are in, and the command line takes the id, which it lists. No message about a name depends on what another team holds: a name taken in another team is simply free.

**Checks stay in their team (R24).** Moving a check to a project of another team is refused, for a person, a key and a command alike.

**The API.** `Project` and `MeProject` gain `team` (id and name) for a member of it, `Me` gains `teams` (id, name, role). Version 0.13.0.

**Nothing left to guess.** A project is made in the team its caller says, and without one in the only team, refused when there are several. An administrator is made owner of the only team, or in no team when the caller says so. With one team the commands need say nothing (R18).

**Migration 0017:** `log_entries.team_id` with its index; the rows of R15; and `api_keys` made again, as its constraint on who made a key allows only the two values it had.

## Part B, commands tell on themselves

**What is told (R27, R28).** A team's owners are told at once when a command on the server:
- gives someone a role in the team or in one of its projects that lets them in or lets them do more, names an owner, or takes someone out of the team;
- makes a sign-in link for someone in the team;
- takes away a passkey of someone in the team, or ends their sign-ins.

"In the team" is anyone with a role in it or a role of their own in one of its projects, and a sign-in link is told whether or not they have a passkey. The one link that is not told is the first for someone added in the last ten minutes: adding them was told, and the link is how they come in at all. Someone named an owner is among the owners who are told. Making an administrator who owns the team is told as naming an owner. Each notice says what was done, to whom, and by which command. Entering a team (R26) is step 5 and will use the same notice.

A command that only reads writes `command.read` and sends nothing: `compare` in the project it read. `teams -people`, `set-alerts`, which prints who cannot be reached, and `backup` read across teams by their nature, write nothing, and `docs/running.md` says so (R44).

**How (R29).** A notice is a message of a new kind `notice`, queued to every confirmed channel of each owner, by its own path: an alert picks one channel and falls back, a notice goes to all. Like a test message it goes out whatever the alerts switch says, and unlike an alert it does not wait for quiet hours to end: it is about the account, not about a check. `serve -shadow` holds it back, as it holds everything. It has its own words for a push and for mail, is recorded under Messages, and counts there as a message does. Until the owner has opened it, it is a card at the top of their overview.

- "At once" means within the sender's ten seconds, while the server runs: a command is another process and cannot wake it.
- A new table `notices` holds one row per owner told: what, about whom, by what, when, and when it was read.
- `deliveries.kind` gains `notice`. Its allowed values are a constraint on the table, so migration 0018 makes the table again with all its columns and four indexes and copies its rows as they are. Nothing points to it. The new table has no constraint on the kind, so the next kind needs no rebuild.
- The log line and the notices are written in the transaction that makes the change, as every log line is.
- The API's `Message.kind` gains `notice`: version 0.14.0.
- Trying this on a copy of a real database would message real people, so part B is tried on a fixture, and on a copy only with `-shadow`.

**What stays on the server (R30):** a sign-in link for someone who has a passkey, naming an owner, removing a team. None gets an API operation, and a test holds that.

## Part C, a second team

**Limits (R34, R36).** `teams.max_active_checks` and `teams.max_ping_checks`, empty for no limit. The first team has none. A new team starts at 400 checks Allstill runs and 2000 that jobs ping. Reaching one refuses a new check, a move into the team is not possible anyway, and a ping with `create=1` makes nothing, as at a project's limit today. The message says what the limit is and that the administrator can raise it. A check that exists is never stopped. Commands on the server are not held to it, as they are not held to a project's.

**Turns (R35).** When more attempts are due than may run at once, the next to start is from the team with the fewest running, in today's order within a team and with today's five per project as the ceiling. With one team the order is what it is now. The loop learns each check's team from the query that feeds it. A probe's checks run on the probe and are not part of this. Two limits stay and are said: nothing is held free, so with twenty attempts hanging another team's check starts when the first of them ends; and one host is tried by one attempt at a time for the whole installation, so two teams checking one host wait for each other.

**Private ranges (R13).** `teams.private_ranges`, text: empty for none, `*` for all the server opened, so that naming ranges per team later needs no new table. Migration 0019 sets `*` for every team that exists and a new team starts empty. A check of a team without them is held to the public internet wherever the server dials for it: where an address is looked up on saving, where it is tried once from the form, in the loop, and in `add-check` and `set-check` on the server. The ranges are passed with each attempt, and an attempt that names no team gets none. The message when one is refused no longer says a range is open "to every project's checks".

**Commands.**
- `allstill admin create-team NAME (-owner EMAIL|ID | -owner-name NAME [-owner-email EMAIL])` makes a team and names its first owner (R9). For a new person it prints their passkey link.
- `allstill admin teams [-people]` lists each team's name, id, owners, limits, and how many people, projects and checks it has (R10), and with `-people` everyone with an account and their teams (R11).
- `allstill admin set-team TEAM [-name N] [-active-checks N|none] [-ping-checks N|none] [-private on|off]`.
- `allstill admin set-team-member (-email E | -person ID) -team TEAM -role owner|editor|viewer|none`. The last owner cannot be taken out or made less; name another first. Someone who becomes owner or editor is told of what is open in the team's projects, as `set-member` does for one project.
- `allstill admin remove-team TEAM`, refused with a count while it holds projects or probes (R33).
- `-team` on `create-project`, `create-probe`, `rekey-probe` and wherever `-project NAME` matches projects of two teams. With one team it is never needed (R18).
- `create-user` without `-project` and without `-team` makes an administrator who owns the one team, as today. With several teams it asks: `-team TEAM -role ROLE` for a member of one, `-admin` for an administrator who is in none, or both.

**What an administrator sees (R10).** With more than one team, their overview lists each team they are not in as a line: name, owners, people, projects, checks. No link leads further.

**Docs (R44).** `docs/running.md` says what an administrator and the server's commands can still see and do, that whoever holds the server holds the data, and how to give someone a team. The built-in guide and `AGENTS.md` follow.

## Files that change

- Part A: `internal/store/migrations/0017_*.sql` (new); `internal/store/workspace.go`, `teams.go`, `log.go`, `logread.go`, `keys.go`, `probe.go`, `manage.go`, `checks.go`, `tokens.go` and their queries; `internal/logtext`; `internal/web` (`pages.go`, `manage.go`, `channels.go`, `nav.go`, `log.go`, `api.go`, `apiread.go`); `api/openapi.yaml` and both generated clients; `internal/client`, `cmd/allstill/remote.go`.
- Part B: `0018_*.sql` (new); `internal/store/notices.go` (new), `channels.go`, `incidents.go`, `signin.go`, `workspace.go`; `internal/alert` (`push.go`, `mail.go`, `sender.go`); `internal/web/health.go`, `messages.go`; `cmd/allstill/compare.go`.
- Part C: `0019_*.sql` (new); `internal/store/teams.go`, `manage.go`, `ping.go`, `checks.go`; `internal/active/loop.go`, `http.go`, `tcp.go`; `internal/web/manage.go`, `apiwrite.go`, `pages.go`; `cmd/allstill/admin.go`, `teams.go` (new), `checks.go`, `probe.go`, `main.go`; `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md`, `README.md` where it says what an administrator sees.

## Order of work

1. **The two-team fixture and the walk (R43), first.** A fixture with two teams, each with an owner, an editor, a viewer, a project with checks of each kind, a key, a probe, an incident and messages; an administrator who owns the first and is in neither's second; and one person with a role of their own in one project. A test walks every page and every operation in `apiOperations` as each person and fails when a name, an id or an address of the other team comes back, in an answer or an error. It fails until part A is done.
2. Part A in the order written above, the migration first.
3. Part A on a copy of a real database, then a release candidate.
4. Part B: the migration and the store, the sender, the card, the commands.
5. Part B on a copy, with a notice really sent to a test device, then a release candidate.
6. Part C: limits, turns, ranges, then the commands, `create-team` last.
7. Part C on a copy: make a second team there, walk it by hand as its owner and as the administrator, then a release candidate.

Going back is as in step 1: the copy the store keeps from before each migration, for the first minutes only.

## Tests that prove it

- **The wall (R43).** The walk of step 1 above, over pages, API operations, the log with every way of narrowing, messages, digests, cards and errors, as each team's owner, editor and viewer, an administrator in no team, a viewer, editor and owner of one project who are in no team, a project's key, and a read-only sign-in. What names something of another team must answer exactly as asking for something that does not exist. Each walker must also read something of their own, and nothing may have changed afterwards. It fails when an operation is added to `apiOperations` and not to the walk.
- **The administrator's view (R8, R10, R11).** As an administrator who is in no team: every page of another team's project answers as one that does not exist; the log gives the installation's lines and failed messages without a project's or check's name; `me` lists no project.
- **One team, nothing new (R6, R18).** Step 1's upgrade test runs through 0017 too and holds what each person sees and is told. No page of a one-team installation holds the word "team" beyond today's "the team". The commands' own tests hold their output, which changes only where "What changes" says.
- **The rows of R15.** After 0017 on the reference database of step 1, who is told is the same for every person, and the role-change test holds through the store.
- **The log's rule.** A table over reader (administrator in no team, team owner, team editor, team viewer, own role in the project, nobody) and line (project, team, installation, own, failed message; written before and after this step), against the rule written out in Go.
- **Names (R5).** Two teams with a project of one name: both can be made and renamed to it; each owner's lists show one; a person with a role in both sees `Team / Project`; a command names which.
- **Probes and moves (R24, R37).** A probe cannot serve another team's project; a check cannot move to one; both refusals say why to someone who can see both, and nothing to someone who cannot.
- **Notices (R28, R29).** Each command that should tell does, to every owner and nobody else, in shadow mode and during quiet hours, once; a command that should not, does not. The card goes when the notice is read. A failed notice is retried as a message is.
- **Limits and turns (R34 to R36).** At the limit a person, a key and `create=1` are refused and a command is not; no check stops. With one team holding twenty dead hosts and another one check due, the other's check starts next.
- **Private ranges (R13).** A check of a team without them is refused on saving and unjudged in the loop for a private address, and a team with them is not.
- **What stays on the server (R30).** No operation in the API description makes a team, names an owner, or makes a sign-in link.
- **Pings and checks keep running (R45)** through each of the three migrations, as step 1 showed for 0016.
