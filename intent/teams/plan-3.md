<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: teams, step 3, a team runs itself

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-19
Step 2: [plan-2.md](plan-2.md), running on a real installation since 2026-11-20
Status: accepted; part A built (2026-11-20), still to be tried on a copy of a real database and rolled out
Approved: 2026-11-20, by the maintainer, with the readings of the spec listed below
Risk: higher risk (accounts made away from the server for the first time, an invitation that lets whoever holds it in, deleting a project for the first time, and an API shape that only grows once released)
Codebase: MartijnMul/allstill at 5f81eea
Reviewed: 2026-11-20, by an independent pass against the code, which looked for leaks across the wall, for mechanics the code does not have, and for ways to take a team over; what it found is worked in

This plans the third of the spec's five steps. After it a team's owners no longer need the administrator to run their team. They let people in by handing them an invitation, change what each may do, take them out again, and make, rename and delete their own projects. Everyone chooses for themselves which projects they are told about. All of it works in the dashboard, and all but naming an owner over the API and from the command line.

It covers R3, R7, R19 to R21 and the part of R25 that needs no share. It builds cli-anywhere's R24 to R26 for a team's owners, reads R27 anew, and replaces that spec's R28 way of adding someone with R19's invitation. The spec calls it a link; the code and the words people see call it an invitation, as "link" already means the passkey link everywhere in the code.

## What it gives, played through

Bob owns the team "Bob's", made by the administrator in step 2.

1. Under **People** he chooses **Invite someone**, picks editor, writes "Carol" as who it is for, and gets an invitation that works once, for seven days. He sends it to Carol however he likes. Allstill asks for no address and looks nobody up.
2. Carol opens it and sees that Bob invites her as an editor. She has no account, so she types her name and makes a passkey, then sees where she is invited and accepts. She is in, as an editor of every project of "Bob's". People shows "Carol, editor, invited as 'Carol' by Bob", and Bob has a card on his overview saying so.
3. A customer of Bob's only needs to see one project. On the project's page Bob invites them to that project alone, as viewer. They accept and see that one project, and no team anywhere (R6).
4. Bob makes a project, `Backups`, and gets its ping key once. The project's page shows who can see it and through what: the team for Bob and Carol, a role of their own for the customer, and who of them is told (R7).
5. Carol does not want messages about `Backups` and switches them off on its page. Bob sees that she is not told. If nobody were, he would be shown that, as today.
6. Carol leaves the company. Bob takes her out of the team. The confirmation lists the keys she made in the team's projects and offers to revoke them; her open invitations are withdrawn. She keeps her account and whatever else she is in; the projects of "Bob's" are gone from her overview on her next click (R21).
7. `Backups` turns out to be unneeded. With no checks left in it, Bob deletes it. Its ping key stops working and its name is free again.

## Three parts, each a release

- **Part A, letting people in.** The invitation, the page that accepts it, a team's People page, changing a role and taking someone out, and owners told when the owners change. Store and dashboard.
- **Part B, projects.** A project's own page with who can see it (R7), making, renaming and deleting a project, a role of one's own in a project, and one's own choice to be told. Store and dashboard.
- **Part C, the API and the command line** for all of the above but naming an owner, read as a client by an independent pass before any handler is written, as the API only grows after.

Part A has the one migration, 0020. Parts B and C change no table.

## What changes for an installation with one team

Its administrators own the one team, so they now see what owners see:

- **A People page** in the navigation, for owners only. Nobody else sees the word "team" anywhere they did not before (R6). The step 2 test that no page holds "team" now holds for everyone who owns no team.
- **A page per project,** reached from the project's name on the overview, for everyone who can see the project.
- **Owners are told when the owners change,** and see a card when someone accepts an invitation.
- **An owner can take an administrator out of the team,** like anyone in it. The administrator keeps the installation and can put themselves back only with a command on the server, which tells the owners.
- **The API gains operations and fields;** nothing is removed. 0.15.0, released with part C.

The commands on the server keep working as they do.

## Readings of the spec, accepted by the maintainer

- **An owner may name another owner, in the dashboard only.** R3 lets an owner change roles, and R30 keeps "naming an owner for a team" on the server. This reads R30 as two things. The administrator naming an owner, the first (R9) or any other, stays on the server. An owner of the team may make a member owner, or invite someone as owner, but only in the dashboard, signed in with a passkey, and every owner is told at once. Making an owner less or taking one out is the dashboard's too. The API and the command line give, change and take viewer and editor only, and leave an owner as they are. A sign-in on someone's laptop is a token that lasts; by cli-anywhere C2's reasoning, every step that changes who holds a team takes the passkey. Step 2's test, "no operation names an owner", keeps holding. ADR 0005 says "naming an owner" takes the server in its paragraph on reaching into a team, and is read the same way: it is about the administrator, so it stays true and needs no new record.
- **An administrator is no longer protected inside a team.** cli-anywhere R26 kept an administrator's membership from being changed by an owner. Since step 2 an administrator is in a team as anyone is, and an owner can change their role or take them out. Getting back in is a command on the server, which tells the owners.
- **A team's owners make, rename and delete its projects.** cli-anywhere R24 and R25 gave that to an administrator, and R25 to a project's owner as well. Someone with an owner's role of their own in one project, often a customer outside the team, manages that project's people, invitations, keys and `critical`, but not its name or whether it exists.
- **A project's last owner is the team's.** cli-anywhere R27 kept a project's last owner. A project always has the team's owners; in a team that has none (R32), owners of one project could end each other's roles, and the administrator names an owner on the server.
- **Only an empty project can be deleted,** as cli-anywhere R25 says: one with no checks. A probe's own check is a check, so a project holding one cannot go. A probe that serves the project stops serving it, which is what R25's "no probe" protected: no check of the project is left for it to run. Deleting a project with checks would throw away their history and break every job pinging it, from a laptop, in one step.
- **Leaving a team by oneself waits.** R3 says the last owner cannot leave, which implies others can. Accepting asks first and says what it gives, and an owner takes someone out. An owner can take themselves out, but not as the last.
- **An invitation is accepted in the browser only.** Someone new needs a passkey anyway, and for someone with an account one page shows what is offered. The API and the command line make, list and withdraw invitations.
- **More is told than R28 asks, and less loudly.** Naming, making less or taking out an owner, in the dashboard or by a command on the server, and inviting someone as owner, tell every owner by a notice, as a command on the server does: that is how a stolen sign-in taking a team over is heard. Someone accepting an invitation as viewer or editor is a card on the overview of the team's owners and in the log, without a message: routine arrivals that wake an owner at night would dull the notices that matter.
- **What an invitation shows, and when.** The spec has it show what is offered after signing in. Before, it shows only who invites and as what role ("Bob invites you as editor"), so that someone can decide whether to make an account at all. After signing in or making an account, it shows where: the team's name, or the project's name. The label is never shown to whoever opens it.
- **An invitation says for whom it was made.** The maker writes a few words ("Carol", "the backup contractor"). Whoever accepts chooses their own name, so the label is what lets an owner see that the right person came in.
- **Limits on people and projects still wait (C8).** They were left out when only the administrator made people and projects. Owners now make both. A team's checks are limited since step 2, invitations are bounded below, and an invitation makes at most one account, so this plan leaves people and projects unlimited until someone needs it.

## Part A, letting people in

**The invitation (R19, R20).** A new table, `invites`, holds what is offered. An invitation gives a role in a team, or a role of one's own in one project of the team. Step 4 adds a share as a third kind. Each has:

- the team, and the project when it is for one;
- the role: viewer, editor or owner;
- a label, written by the maker, of up to 100 characters;
- who made it, when, and when it ends: seven days later;
- when it was withdrawn, or when it was accepted and by whom;
- the SHA-256 of its secret, as for every other secret. The maker sees the URL once.

`kind` names no values in the table, as `deliveries.kind` since 0018, so step 4 adds a share without making the table again. `role` keeps its three. An invitation is removed with its team or project row.

Who may make one: for a place in a team, an owner of the team; for one project, an owner of that project by any way in (R4). Owner is given only in the dashboard. Whoever may make a kind of invitation sees the open ones and withdraws them. A team holds at most 50 open invitations to the team, and a project at most 20 to itself, so that owners of one project cannot use up the team's. One that ended or was withdrawn is removed after 30 days by the tidying that already runs; an accepted one is kept, as it says how someone came in.

**When the maker loses the right.** In the transaction that takes away someone's right to make an invitation (taken out of the team, made less than owner, a role of their own ended or made less), their open invitations that need that right are withdrawn. Otherwise an invitation made beforehand would be a way back in.

The URL is `/join#SECRET`. The secret is after `#`, so it is never sent in a request line or kept in a log, and the page clears it from the address bar once read, as `/setup#` does. Every join route goes through the dashboard's cross-origin protection.

**Accepting.** The page reads the secret and asks the server what it offers. An invitation that is unknown, used, withdrawn or ended says which, as a passkey link does, and nothing more. One that is open says who invites and as what, then:

- **Signed in:** where it is for, and "Accept as Dave", with "Not Dave? Sign out and continue" beside it. Accepting gives the role, unless Dave has that role or a higher one in that place already, in which case the page says so and the invitation stays open for whom it was meant.
- **Not signed in, with an account:** the page signs in with a passkey itself, without leaving it, and then shows the above. The secret never goes into a `next` parameter.
- **New:** a name and a passkey. The person's id and name are chosen when the passkey ceremony begins and bound into its signed challenge, as `setupPurpose` binds a passkey link, because the passkey's user handle is the person's id and sign-in finds them by it. The name is trimmed, 1 to 100 characters, and none of the words the log uses for someone it does not name ("the team", "an administrator"); the time zone is the browser's if it is a valid IANA name, else UTC. Finishing is one transaction: it makes the person, without an email address and without being an administrator; registers the passkey; and starts a session. Then the page shows where the invitation is for and asks to accept, so making an account and accepting are two steps. An invitation makes at most one account: it records the person made through it, in the transaction that makes them, and refuses to make another, so signing out and making a second is refused while the first can still accept. Two people accepting at once: one gets it, as `UseLink` does for passkey links.

An administrator accepts as anyone does, and becomes a member.

**Told and logged.** Accepting writes one line, `invite.accepted`, in the team (a team's invitation) or in the project (a project's), with the role, the maker and the label; it is the line of the role given, not a second one beside `team.member_set` or `member.set`. A new person writes `person.added`, the kind that exists, which an administrator reads as one of the installation's lines; its details hold the person's name and nothing of the team, the project or the label (R10). `invite.made` and `invite.withdrawn` are written where the invitation is. The kinds written in a project get their role in `logMinRole`: the project's owners.

A card on the overview of the team's owners says who accepted, as what, and through whose invitation with which label; for a project's invitation, also on that of the project's owners. An owner outside the team reads the team's people as "the team", as below.

**Owners told (R28, the reading above).** `tellOwners` today sends only for a command on the server, and its words say so. It gains a second set of words for a person, and these cases, from a person or a command alike: an owner named, made less or taken out; an invitation as owner made or accepted. Every owner of the team is told, and so is the person made less or taken out. They go as notices do: every channel, whatever the alerts switch, through quiet hours.

`CreatePasskeyLink` keeps not telling about the first link for someone added on the server a moment ago. Someone who comes in by an invitation never has one, so its exception does not reach them.

**People (R3, R20, R21).** A page per team for its owners, named **People** in the navigation, or the team's name when they own more than one. It lists:

- the team's members, with their role and how they came in: an invitation and its label, or a command on the server;
- people outside the team with a role of their own in one of its projects, per project;
- the open invitations, with role, label, maker and end, each with Withdraw;
- **Invite someone**: a role and a label, then the invitation, shown once.

An owner changes a member's role there and takes them out. Only members: someone outside the team is never made a member by a change of role, as that would let them into every project without asking; they come in by an invitation. The last owner cannot be made less or taken out, and the page says to name another first.

Taking someone out (R21) removes their place in the team at once, their choices about being told for its projects with it, as `set-team-member -role none` does, and their open invitations to the team. It keeps their account, sessions, devices, other teams and their name in the log. A confirmation lists what stays and offers to end it too: roles of their own in the team's projects, and keys they made in them, which keep working otherwise, as a key is the project's (R38).

Changes are written as `team.member_set` and `team.member_removed`, the kinds step 2 made, with the person who did it.

**Migration 0020:** the `invites` table with its indexes.

## Part B, projects

**A project's page (R7).** `/projects/{project_id}`, for anyone who can see the project. It shows the reader's own role and through what, the switch to be told about it, and a link to its checks, which are the overview narrowed to the project as today. To the project's owners it also shows who can see it:

- To an owner in the team: each person, their role, through what (the team, a role of their own, or both, the highest counting), and whether they are told, so that "nobody is told" has names behind it.
- To an owner outside the team: the team's people as one line, "the team: 4 people, 2 of them told", as the log and the key list read them (activity-log R22, step 2); people outside the team with a role of their own by name, as today.
- Disabled people are left out of both.
- A role of one's own changed or ended right there; a role through the team is changed on People, which the page links to for a team owner. The open invitations to the project, with the maker read as above.

The team is named only where step 2 already names it: to someone who sees projects of two teams.

**Being told (R25, without shares).** Everyone who can see a project switches it on or off for themselves, on its page, whatever their role. It is kept in `alert_choices` as `settleChoice` does: a row where it differs from what the role gives. Writes `alerts.chosen`, which the project's owners read. Nobody switches it for someone else. A critical check and quiet hours are as they are; who across a share may switch it on is step 4.

**A role of one's own in a project (R4).** Given by an invitation to the project (part A), changed and ended on its page by the project's owners. In the dashboard and the API, unlike `allstill admin set-member`:

- a role is given only where it is higher than what the team gives the person, and anything else is refused with that reason. On the server "viewer" for a team owner still means "do not tell me" (R18); here only the person chooses that;
- ending answers as not found for someone without a role of their own;
- neither touches whether the person is told, if they chose; without a choice, it follows the new role as it does today.

**Making, renaming and deleting.** For a team's owners, from the overview and from the project's page:

- **Make:** a name of 1 to 100 characters, not taken in the team, checked inside the transaction that writes it, which `CreateProjectIn` does not do today. The ping key is shown once, as `create-project` prints it. Written as `project.created`.
- **Rename:** under the same rule; `RenameProject` gains the length. Written as `project.renamed`.
- **Delete:** only a project with no checks, which the page says before anything else. It cannot be undone, and the history of checks deleted from it can no longer be read by anyone, which the confirmation says. One transaction: `deleted_at` is set; the ping key is cleared so pings with it find nothing; its keys are revoked; its invitations, roles of one's own, choices about being told and the probes' rows that serve it are removed, so nothing keeps feeding notices, People or a probe. Written as `project.deleted` in the team, not in the project, so the team's owners can still read it once the project is gone.

`deleted_at` has been in the table since 0001 and never set. So:

- Every write that puts something into a project reads the project as live inside its own transaction: adding a check, moving one in, `create=1`, making a key, an invitation, a role. Today adding a check checks access before its transaction, so a check added at the moment of deleting would keep running.
- Every query that reads `projects`, `project_roles` or `project_alerted` is checked for it, the loop's and a ping's included, and the list is written in the commit. The views stay as step 1 made them: they leave out nobody, and each query says what it must not count.
- A team whose projects are all deleted can be removed. `CountProjectsOfTeam` counts live projects, and `remove-team` removes the rows of its deleted projects for good, with the deleted checks and history still hanging on them, in small batches as tidying does so that no delete holds the write lock long, and the team last; log lines keep the project as text. Otherwise a team that once had a project could never go.

## Part C, the API and the command line

**The API (0.15.0).** All for a person's sign-in. A project's key and a read-only sign-in are refused, as they are for keys today. Owner is refused as a role wherever one is given, and an owner's role is not changed or taken, with a reason that says the dashboard does that.

| Operation | What it does | Who |
| --- | --- | --- |
| `createProject` `POST /projects` | name, `team_id` when the caller owns more than one team; the ping key once | team owner |
| `updateProject` `PATCH /projects/{project_id}` | the name | team owner |
| `deleteProject` `DELETE /projects/{project_id}` | an empty project | team owner |
| `listProjectPeople` `GET /projects/{project_id}/people` | R7, as the page reads it | project owner |
| `setProjectRole` `PUT /projects/{project_id}/roles/{person_id}` | change a role of one's own; never makes one | project owner |
| `removeProjectRole` `DELETE /projects/{project_id}/roles/{person_id}` | ends it | project owner |
| `setProjectAlerts` `PUT /me/projects/{project_id}/alerts` | `{"alerts": true}`, one's own choice | anyone who sees it |
| `getTeam` `GET /teams/{team_id}` | name, members, outsiders per project | team owner |
| `setTeamMember` `PUT /teams/{team_id}/members/{person_id}` | a member's role; never makes one | team owner |
| `removeTeamMember` `DELETE /teams/{team_id}/members/{person_id}` | takes them out; answers with the roles of their own and keys that stay | team owner |
| `listTeamInvites`, `createTeamInvite` `GET`, `POST /teams/{team_id}/invites` | role, label; the URL once | team owner |
| `listProjectInvites`, `createProjectInvite` `GET`, `POST /projects/{project_id}/invites` | role, label; the URL once | project owner |
| `withdrawInvite` `DELETE /invites/{invite_id}` | | who may make it |

- An `Invite` has `kind`, documented as an open set, so step 4 adds a share as a value.
- `Project` and `MeProject` gain `alerts`, whether the caller is told; it is left out for a key.
- A person is named by id only, and only someone the caller can already see there: a member for the team's operations, someone with a role of their own for a project's. A `PUT` never makes anyone a member; for anyone else it answers as not found. No operation takes an email address or tells whether an account exists (R20).
- Answers: 404 for anything of another team or not there; 409 for the last owner, a project that still has checks (with the count), and the most open invitations; 422 for a name taken or too long, a role not higher than the team's, owner as a role or an owner's role changed or taken, and several teams owned with no `team_id`.
- Each operation is in the walk of step 2.

**The command line.** Over the API, as the rest of it, plural like every other command:

- `allstill projects add NAME [-team TEAM]`, `projects rename PROJECT NAME`, `projects delete PROJECT`.
- `allstill projects people PROJECT`, `projects role PROJECT PERSON ROLE|none`, `projects alerts PROJECT on|off`, `projects invite PROJECT -role ROLE -for LABEL`.
- `allstill teams [TEAM]`: members, outsiders and open invitations. `teams role PERSON ROLE`, `teams remove PERSON`, `teams invite -role ROLE -for LABEL`, `teams withdraw INVITE`.

`-team` is needed only by someone who owns more than one team (R18). A person is named by id, or by their name where it is one of a kind there.

**What stays on the server:** making and removing a team, its first owner and any owner the administrator names, its limits and ranges, sign-in links, removing a passkey, ending someone's sign-ins, probes. Naming an owner is otherwise the dashboard's. Step 2's `TestWhatStaysOnTheServer` changes from words in operation names to what operations can do: none makes or removes a team, gives owner, makes a sign-in link, removes a passkey, or accepts an invitation. cli-anywhere's R28 to R30 for an administrator, listing people over the API and removing a person, are not built here and stay open in that spec.

## Files that change

- Part A: `internal/store/migrations/0020_invites.sql` (new); `internal/store/invites.go` (new), `teams.go`, `workspace.go`, `signin.go`, `notices.go`, `logread.go`, `tidy.go`; `internal/store/queries/invites.sql` (new), `teams.sql`; `internal/logtext`; `internal/web` (`join.go`, `people.go` new, `signin.go`, `nav.go`, `pages.go`, `web.go`), templates `join.html`, `people.html` (new), `overview.html`, `static/app.js`, which reads and clears the secret as it does for `/setup#`; `internal/alert` for the notices' words.
- Part B: `internal/store/workspace.go`, `teams.go`, `checks.go`, `manage.go`, `keys.go`, `ping.go`, `probe.go`, `logread.go` and the queries reading `projects` and the two views; `internal/web` (`project.go` new, `pages.go`, `nav.go`), templates `project.html`, `project-new.html`, `project-confirm.html` (new), `overview.html`; `cmd/allstill/teams.go` for `remove-team`.
- Part C: `api/openapi.yaml`, `api/README.md`, both generators' lists (`internal/api/oapi-codegen.yaml`, `internal/apiclient/oapi-codegen.yaml`) and the generated code; `internal/web/api.go`, `apiwrite.go`, `teams_test.go`; `internal/client`, `cmd/allstill/remote.go`, `remotewrite.go`, `main.go`.
- Each part as it lands: `docs/running.md`, `internal/guide/guide.md`, `README.md`, `AGENTS.md` where they say who adds people and projects, and the status of cli-anywhere.

## Order of work

1. **The walk first.** Step 2's walk gains a person who came in by a team's invitation and one by a project's, and the pages and operations below as they are added, failing until each is walled.
2. Part A: the migration and the store, the join page, People, the notices and cards. Tried on a fixture with a real passkey in a browser, including signing out and in again, then on a copy of a real database with `-shadow`. A release candidate.
3. Part B: the store with the list of queries for `deleted_at`, then the pages. Tried on a copy: make, rename and delete a project, remove a team whose project was deleted, switch being told. A release candidate.
4. Part C: the description first, then an independent pass that reads it as a client would, then the handlers and the command line. A release candidate.

Going back is as before: the copy the store keeps from before the migration, for the first minutes only. Parts B and C change no table.

## Tests that prove it

- **The wall (R43).** Every new page and operation in the walk, as each kind of person of step 2 plus the two who came in by an invitation. Another team's invitation, listed, withdrawn or named by id, answers as one that does not exist. The join page shows who invites and as what before sign-in, where only after, and never the label. The maker's name before sign-in is the one name the wall lets through, as the reading above decides, and the walk holds that it is the only one.
- **What an owner outside the team reads.** On the project's page, `listProjectPeople` and the invitations: the team's people as a count, never a name or id; the same reader sees the same in the log and the key list.
- **Invitations (R19, R20).** Works once; makes one account at most, also when its holder signs out and tries again; ends after seven days; withdrawn is withdrawn; two at once give one person; someone with the role already leaves it open; the bounds of 50 and 20; pruned after 30 days unless accepted. A new person has no email and is no administrator, and can sign out and sign in again with the passkey made. Names the log uses are refused. The secret is in no table, log line or response but the one that made it.
- **The maker loses the right.** Taken out, made less, or their own role ended: their open invitations that needed it no longer work.
- **Who may.** A table over person (team owner, editor, viewer, a project's own owner outside the team, an administrator in no team, a key, a read-only sign-in) and action (each row of the API table, and owner given over the API), against the rule written in Go. The last owner cannot be made less or taken out by any of them. Someone outside the team cannot be made a member by a change of role.
- **Roles of one's own.** A role not higher than the team's is refused and nothing about being told changes; an explicit choice survives a change of role.
- **Taken out (R21).** Afterwards the person sees none of the team's projects on the next request, over the dashboard and the API; their own roles and keys stay unless ended, and the confirmation and `removeTeamMember` say which; their account, other teams, sessions and devices are untouched; who is told follows.
- **Told (R25).** Switching on and off as each role, `project_alerted` agreeing with what each person chose. An owner sees who is told; nobody can switch someone else.
- **Notices and cards.** An owner named, made less or taken out, and an owner's invitation made or accepted, tell every owner and the person concerned once, in shadow mode and quiet hours, from a person and from a command. A viewer or editor accepting is a card for the owners and no message. Nobody outside the team is told.
- **A stolen sign-in.** With a command line's sign-in of an owner: no owner can be named, invited, made less or taken out. In the dashboard, each of those tells every owner.
- **Deleting a project.** Refused with checks; a check being added at that moment either lands before and refuses the delete, or is refused itself; afterwards its ping key finds nothing, its keys, invitations, roles, choices and probes' rows are gone, it is in no list, page, operation, notice or `Access`, its name can be used again, and `project.deleted` is read by the team's owners. A team whose only project was deleted can be removed.
- **One team (R6, R18).** A person who owns no team sees no new word; every command on the server and its output are as they were. An owner takes out every administrator: they keep the installation's pages, see no project, and `set-team-member` on the server brings them back and tells the owners.
- **What stays on the server (R30).** As above.

## Risks

- **An invitation lets whoever holds it in.** Bounded: once, seven days, withdrawable, labelled, gone when its maker loses the right, an owner's told at once, and People shows who came in through which. It is the same trust as handing someone a sign-in link, held by the team's owner instead of the administrator.
- **A team taken over by a stolen sign-in.** A token cannot name an owner, and any change to the owners is told to all of them at once. A stolen dashboard session still can, loudly: the same edge the spec accepts for the administrator (C2).
- **Accounts made away from the server.** Every person so far was made by a command. Someone new has no email, and a person without one is checked in every place that sends mail, signs them in, or names them (`WebAuthnName` falls back to the name already). The ceremony binds the id it makes, and the test signs in again.
- **Deleting a project for the first time.** `deleted_at` was never set, so a query that misses it fails silently: a ping that still lands, a project still counted, a notice about a project nobody sees. The writes read the project as live, the list of queries is written down, and the tests walk them.
- **The API's shape.** It only grows after part C, so the independent pass reads the description before any handler is written.
