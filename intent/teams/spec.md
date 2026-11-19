<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: teams with their own projects, on one installation

Intent: [intent.md](intent.md)
Status: accepted (2026-11-19, with the recommendations of C1 to C14)
Written: 2026-11-19, by Claude for the maintainer; revised the same day after an independent review against the code
Codebase: MartijnMul/allstill at 47a2dee
Policies in force: ADR 0001 to 0004, AGENTS.md, REVIEW.md, docs/brand, `api/openapi.yaml` 0.12.0, the specs of intent/alerts, intent/http-checks, intent/manage-checks, intent/activity-log and intent/cli-anywhere, the built-in guide (`internal/guide/guide.md`), and needs from real setups (kept privately)

## Summary

An installation holds one or more teams. A team owns projects and has members, and its owners run it without the administrator: they let people in, make projects and share a project with another team. The administrator keeps the server, makes teams and helps people back in, and is no longer shown every project.

Three words stay the whole of it: owner, editor, viewer. A team gives each member one of them for all its projects, a project can still give one to a single person as it does today, and sharing gives one to another team. No role is new, and an installation with one team looks as it does now.

A team is not a second workspace. People, their devices, their quiet hours and incidents stay with the installation, so one person is told once. A team is who owns a project and who is let in by default, which is why sharing can cross it.

Everything that crosses from one team to another, or from outside into a team, is a link that one side makes and hands over, and the other side accepts after signing in. Nobody looks anyone up, so nobody learns who has an account or which teams exist.

It flags that the server's commands reach every team today and would stay an unannounced way in (C1), that the administrator can still become anyone with a sign-in link (C2), what the administrator is left able to see (C3), who hears when nobody in a team does (C4), that a guest's checks can reach the private ranges the administrator opened (C6), that a guest moving from Healthchecks still needs the server (C9), and that a team running itself rests on pages and commands that do not exist yet (C10). It proposes five steps, the first of which changes nothing anyone can see.

## Requirements

**Teams and what they own**
- R1. Every project belongs to exactly one team. A team has a name its owners can change; two teams may share a name, and a command then takes an id, as for projects. (Proposed outcome)
- R2. A team's member has one role in it: owner, editor or viewer. That is their role in every project the team owns, as those words mean today. (Proposed outcome; ADR 0001)
- R3. A team's owner can also let people into the team and take them out, change their roles, make, rename and delete its projects, and share them. The last owner cannot leave or be made less. (Proposed outcome)
- R4. A project can still give a role to a single person, as today, given by an owner of the project. Where someone has several ways into a project, the highest role counts. (Case 3; Constraints)
- R5. A project's name need be told apart only within its team, and a probe's likewise. Where two a person can see share a name, commands and lists say `team/project`. No message about a name ever depends on what another team holds. (Affected: the command line; Constraints: the wall)
- R6. Someone in one team sees no team anywhere they do not manage one: no switcher, no grouping, no extra word. (Proposed outcome: a small installation notices nothing)
- R7. A project's page shows its owners everyone who can see it and through what: the team, a role of their own, or a share. (C12)

**The administrator**
- R8. An administrator is the keeper of the installation and has no role in any project by being one. They see a team's projects only as a member, through a share, or by entering it (R26). (Decided)
- R9. Only an administrator makes or removes a team and names its first owner. (Decided; C7)
- R10. An administrator sees, per team: its name, its owners, and how many people, projects and checks it has. No project's or check's name, state, address or history. (Proposed outcome; C3)
- R11. An administrator sees everyone with an account, which teams they are in, and whether messages to them fail, with the kind of channel and the reason but not what the message said. (Constraints; C3)
- R12. Sending mail, how long history is kept, and whether the installation sends alerts at all stay the administrator's. (Constraints; C11)
- R13. The private ranges an administrator opens are open to the teams they name, and to no new team unless said. After the upgrade they are open to the first team. (Constraints: what the wall is not; C6)

**Upgrading**
- R14. An upgrade makes one team that owns every project, named as the installation is and renameable. Each administrator stays one and becomes an owner of it. Every other person keeps exactly the roles they have in single projects and joins no team. (Constraints: nobody loses or gains access)
- R15. An administrator's own rows in single projects exist today only to say whether they are alerted. They become that choice (R25), not a role, as the role was already an owner's. (Constraints)
- R16. After the upgrade every person sees, may change and is told about exactly what they were before, shown by a test that builds each kind of person, upgrades, and compares: an administrator, one set to viewer in a project, each role, a read-only sign-in, a key, a probe. (Constraints)
- R17. Log lines written before the upgrade read as they did, "an administrator" included. (Constraints; activity-log R22)
- R18. While there is one team, every command works as it does, and `allstill admin create-user` without `-project` still makes an administrator who owns it. With more than one, a command that would have to guess a team asks for `-team`. (Constraints)

**Letting people in**
- R19. An owner lets someone into the team, or into one project, by making a link with a role and handing it over. Whoever opens it signs in, or registers a passkey under a name if they have no account, and accepts. A link works once, lasts seven days, and can be withdrawn. (Proposed outcome)
- R20. The owner sees who accepted, by name, and can take them out again. No address is asked for or looked up, so a link tells its maker nothing until it is accepted. An email address stays what it is today: somewhere to send alerts, confirmed by its holder. (Constraints: the wall)
- R21. A person taken out of a team keeps their account, their other teams, their devices and sign-ins, and at once loses what the team gave them. A role of their own in one of its projects stays, and taking them out says so and offers to end it. (Case 5)

**Sharing**
- R22. A team's owner shares a project by making a link with a role, viewer or editor, never owner, and handing it to the other team. It counts once an owner there accepts it for their team. Either side ends it at once. (Decided; C5)
- R23. A member of the team that accepted has the lower of the shared role and their own role in their team. A shared project is marked with the team it belongs to wherever it is listed. (Proposed outcome)
- R24. A check moves only between projects of one team. A share never lets the other side take a check, with its history and ping URL, into a project of theirs. (Constraints; manage-checks R13)
- R25. Whether a person is alerted for a project is their own choice per project, apart from their role. It starts on for an owner or editor of the team that owns the project and for a single person given such a role, and off for viewers and for everyone who sees the project through a share. Across a share only someone who is owner or editor in their own team can switch it on. A critical check breaks through quiet hours only for people of the team that owns it. (Open question; C5)

**Entering a team**
- R26. An administrator can look into a team with a command on the server, naming an administrator, a reason and a time, an hour unless said otherwise and a day at most. It gives that person a viewer's role in the team's projects and ends by itself. It changes nothing for someone who is a member already. (Decided; C1)
- R27. A command on the server that reads or changes a team's project, check, member or key still works, as whoever has the shell has the database. Each is written to that team's log, named as a command on the server. (C1)
- R28. A team's owners are told at once when someone enters, when a server command gives anyone a role in the team or names an owner, and when a sign-in link is made for one of its members who has a passkey. They are told who, why where a reason was given, and until when. (Constraints: a trace)
- R29. Those notices are sent whether alerts are on or in shadow mode, to every channel the owner has, and stay at the top of the owner's overview until read. (Constraints)
- R30. Making a sign-in link for someone who has a passkey, naming an owner for a team, and removing a team stay commands on the server. (Case 8; C2; cli-anywhere R31)

**Nobody left to hear**
- R31. An administrator is told, on their overview and in their digest, that a team has no owner, or has projects for which nobody is alerted, with the team's name and a count. (Case 7; C4)
- R32. Removing a person who is a team's last owner says so first. A team without an owner keeps running, and its editors are still told. (Case 6)
- R33. A team with projects cannot be removed. Removing one says how many projects still hold it. (Case 6)

**Limits**
- R34. A team has a limit on checks Allstill runs and on checks jobs ping, set by an administrator, with a default for a new team. The first team has none unless set. (Constraints; C8)
- R35. When attempts wait for room, teams take turns, so one team's dead hosts cannot hold up another's checks. Today's share per project stays within a team. (Constraints; C8)
- R36. Reaching a limit refuses the change, saying what the limit is and that the administrator can raise it. It never stops a check that exists. (Constraints)

**What belongs to a team**
- R37. A probe belongs to one team and serves only that team's projects. Making one stays on the server. A team's owner can do with a probe's own check what manage-checks R19 keeps for an administrator. (Affected: probes)
- R38. A project's keys stay the project's, as they are. No key reaches a team as a whole. Turning `critical` on takes an owner of the project, by any way in. (Constraints; manage-checks R7, C4)
- R39. A check may come to depend on a check its editor can see, which sharing makes possible across teams. Dependencies are not built here; their spec decides what each side sees of the other's checks in a joined incident, and what happens when a share ends. (Case 4)

**The log**
- R40. A team's own lines are read by its owners: people let in, taken out and changed, shares made, accepted and ended, limits changed, an entry, a server command. An administrator reads what concerns the installation: teams made and removed, people, probes, settings. An administrator no longer reads every project's lines. (Affected: the log)
- R41. Across a share, people of the other team are never named. A reader sees "someone in" followed by that team's name, both as who acted and as who was reached, whatever the reader's role. (Constraints: the wall; activity-log R22)

**Keeping what holds**
- R42. What a person may see and change is decided in one place, where their access is worked out, and who is alerted in one other, a single definition every query uses. Today the second is the same condition written out in query after query. (Constraints; ADR 0001)
- R43. A test with two teams walks every page and every operation in `apiOperations` as a member of one, and shows that nothing of the other's projects, checks, people or names comes back, in an answer or in an error. The same for messages, digests, the live stream and the log. (Constraints)
- R44. `docs/running.md` says plainly what an administrator and the server's commands can still do and see, and that whoever holds the server holds the data. (Constraints)
- R45. Pings, checks and alerts keep running through the upgrade, and no ping URL changes. (Constraints)

## Design

**The model.** In the one workspace:

```
Installation (one workspace)
├── People        accounts, devices, quiet hours; administrators among them
└── Team          members with a role, limits, probes, private ranges
    └── Project   single-person roles, shares to other teams, keys
        └── Check
```

New tables:
- `teams`: name, limits, which private ranges it may reach.
- `team_members`: team, person, role.
- `project_shares`: project, team, role, who accepted and when.
- `links`: what is being handed over (a place in a team, a role in a project, a share), its role, who made it, its hash, when it ends, who took it.
- `team_entries`: team, person, reason, from and until.
- `alert_choices`: person, project, on or off. A row only where someone chose otherwise than their role gives.

Changed:
- `projects.team_id` and `probes.team_id`, never empty. Names are told apart per team.
- `log_entries.team_id`, for lines about a team and no project.
- `memberships` stays as the role of one person in one project. Its `alerts` flag moves to `alert_choices`.
- `people.workspace_admin` stays, and now means keeper of the installation only.

**Why not a workspace per team.** Every table carries a workspace, and a team could be one. But a person, their passkeys, devices, quiet hours and incidents all hang on a workspace, and the push key is per workspace (ADR 0003). A person in two workspaces would be two people, and a shared project would cross the one line every foreign key is built to hold. The workspace stays what ADR 0001 kept it for: a hosted service with customers who share nothing.

**One place decides who sees.** `AccessOfPerson` builds the list of projects with a role that every read already goes through. Today it is "a membership, or every project for an administrator". It becomes the highest of: the person's role in the owning team, their own role in the project, a share to a team they are in, capped at their role there, and a running entry. Everything that asks `Access.Role` keeps working. `Access.Admin` stops meaning "sees everything". Each of the places that read it is decided one by one as the installation's (people, mail health, teams) or a team owner's (keys, `critical`, a probe's own check, names in the log). `AdminAccess`, which gives the server's commands every project as owner, stays and gains the log line and notice of R27 and R28.

**One place decides who is told.** A view over team roles, single-person roles, shares and `alert_choices` gives, per project, the people to tell. The queries in `incidents.sql` and `channels.sql` that each spell out "a membership with alerts on, or an administrator with no membership" use it.

**Links.** One mechanism for every crossing. The maker gets a URL once; only its hash is kept. Opening it asks for a sign-in or, for someone new, a name and a passkey, then shows what is offered and by whom, and asks to accept. A share's link is accepted by an owner on behalf of their team. A link that is never opened runs out and tells nobody anything.

**Entering.** `allstill admin enter-team TEAM -person ID -reason "..." [-for 1h]` writes a row with an end time. It gives a viewer's sight and nothing to change, so nothing it does outlives it. Changing something in a team takes one of the server's commands, each of which is logged there.

**Names.** "Administrator" keeps its word and loses its view. "Team" is the one new word. The log's signature for a viewer, "the team" today, becomes the team's name.

**What it changes elsewhere.**
- The API gains teams, members, shares and links, and a project says which team it belongs to. `me` gains the person's teams. `me.workspace_admin` keeps its name but no longer means "sees every project"; the description says so and the command line stops reading it that way.
- The command line gains `allstill teams` and `-team` where a project is made.
- intent/cli-anywhere: R24 to R27 (projects and their members, for an administrator) are built for a team's owner. R28's adding a person becomes R19's link. R29 and R30 (removing a person) stay the administrator's, and R30 names teams and counts where it named projects. R23: a team's owner sees the team's probes.
- intent/manage-checks: R19 and C4 name an administrator where this names a team's or a project's owner (R37, R38). R13's move stays within a team (R24).
- intent/activity-log: R22's "an administrator" stays for old lines and for the server's commands; R41 adds the other team's people.
- ADR 0005 replaces parts of ADR 0001's section on workspace, projects, people and keys, and names them: the administrator who sees every project, teams rejected as access, probes and private ranges that belong to the installation, and who is alerted. One workspace per installation and the rejection of hard walls stand. Named groups for a few of a team's projects, which ADR 0001 planned as a shortcut, are not covered by teams and stay planned.

## Cases played through

1. **A guest who is an equal.** Alice, the administrator, runs `allstill admin create-team "Bob's"` on the server and hands Bob the owner's link it prints. Bob registers a passkey, makes a project and a check, and gives a colleague a link as editor. Alice's overview shows "Bob's: 2 people, 1 project, 1 check". Every page of it answers her 404, as for any project she is not in.
2. **One operator for many parties.** Carol's team owns `Customer A`, `Customer B` and `Customer C`. Each customer's contact has a viewer's role in one project, as today, and is in no team. Carol gives a second contact a link to that project. A customer who later runs checks of their own gets a team from the administrator, and Carol shares their project with it.
3. **An outsider with an account.** After the upgrade the contact at `Customer A` has the same one role and sees the same one project. Being in no team, they see no other project of Carol's and none of her people.
4. **Shared infrastructure.** Alice's team owns project `Hosts` with `web-1`, and Bob's jobs run on it. Before and after this spec: `web-1` goes down, Alice's people get a message, Bob's jobs go down as their deadlines pass and his people get theirs. Alice shares `Hosts` with Bob's team as viewer. Bob now sees that `web-1` is down, and as an owner in his own team switches its alerts on for himself (R25). When dependencies are built, his checks can depend on `web-1` because he can see it. Without the share there is nothing of Alice's for his checks to depend on, and each team hears of its own. The wall has a door, and only the owner opens it.
5. **A person in two teams.** Dave is an editor in his employer's team and owner of a team of one. He signs in once and his overview shows both, grouped. His phone is registered once. His employer takes him out: its projects are gone from his overview on the next click, his own team and his sign-in stay, and his name stays in the employer's log.
6. **Someone leaves.** Erin is the only owner of a team of three. Removing her account says first that the team will be left without an owner. Its checks keep running and its two editors are still told. The administrator's overview says the team has no owner, and on the server they name one of the editors, who is told at once (R28).
7. **Nobody is left to hear.** In Bob's team both people switch alerts off for `Backups`. Bob is shown that nobody is alerted for it, as today. Alice's says "Bob's: 1 project nobody is alerted for", without its name. She is not told when a check in it goes down.
8. **The only owner loses their passkey.** Bob asks Alice. She runs `allstill admin passkey-link` for him on the server and sends him the link. His team's log says a sign-in link was made for Bob by a command on the server, and its owners are told, here Bob himself on the devices he still has. Alice could have used the link herself; that is C2.
9. **An outsider who only needs a status.** Unchanged: a status page or a share link, no account. When built, a status page belongs to a team.
10. **One party on its own installation.** They upgrade. There is one team. Nobody sees the word unless they manage people. Every command works as before.
11. **The administrator needs to look.** Bob says his checks stay unknown. Alice runs `allstill admin enter-team "Bob's" -person alice-id -reason "checks unknown since 14:00"`. Bob is told at once, also though alerts are in shadow mode. For an hour Alice sees his projects as a viewer does, without whole addresses or ping URLs, finds the probe is offline, and tells him. The entry ends by itself.
12. **A server command reaches in.** Alice runs `allstill admin set-member` to give herself a role in one of Bob's projects. It works. Bob's team log has the line, and Bob is told at once who was given what. He takes her out again from the project's page (R7).
13. **A stolen sign-in.** Someone takes an administrator's sign-in from a laptop. They see teams' names and counts and people's names. They can still remove a person who is no administrator, a team's owner among them, which says first when it leaves a team without one (R32) and is in the installation's log. They cannot enter a team, name an owner, or make a sign-in link for someone with a passkey, as each takes the server.
14. **A share an editor could abuse.** Alice shares `Hosts` with Bob's team as editor. An editor there can change and pause its checks, which is what was given, and both are logged in Alice's team as "someone in Bob's". They cannot move a check out of it (R24), make a key or turn `critical` on (R38), or see who in Alice's team was told (R41).

## Areas of concern

- **C1. The server's commands are a way in, today and after.**
  - Every `allstill admin` command gets every project as owner. A wall in the dashboard with that left as it is would be a wall with a side door, and nobody told. Closing the door is not real either: whoever has the shell has the database.
  - Recommendation: R26 to R29. The commands keep working and each tells on itself: in the team's log always, and to its owners at once when it lets someone in. Looking around in the dashboard takes `enter-team`, which is announced and ends. The alternative, refusing the commands for other teams, would only move a determined administrator to `sqlite3`, where nothing is logged.
- **C2. The administrator can still become anyone.**
  - A sign-in link for Bob lets whoever holds it register a passkey as Bob. That is how a locked-out person is helped, and it cannot be taken away without leaving them locked out.
  - Recommendation: accept it, make it loud (R28, R29) and say it in `docs/running.md`. Recovery codes, due in phase 1, let most people help themselves, so the link becomes rare. This is the honest edge of "the host is not shown": the product does not show, and it tells on whoever reaches in.
- **C3. What the administrator still sees.**
  - R10 and R11 give counts, people's names, their teams, and why a message failed. Without people and failures, nobody can tell a guest why their mail bounces, or answer for the log as a record of named people. With them, the administrator knows who is in Bob's team.
  - Recommendation: as written. Names of projects and checks never; people and delivery failures yes.
- **C4. Who hears when nobody does.**
  - Today an administrator outside a project is told when its checks go down. Behind the wall that stops, so a team where everyone switched alerts off, or whose owner left, goes quiet.
  - Recommendation: R31. The administrator is told that it is quiet, never what went down. Telling them of the incident itself would break the wall in exactly the case where the team is not looking.
- **C5. Alerts across a share.**
  - A share that alerts the other side by default lets one team put messages on another team's phones, and `critical` would break their quiet hours.
  - Recommendation: R25. Off until each person switches it on for themselves; critical never crosses; and a share counts only once an owner accepts it, so a team cannot be handed projects it did not ask for.
- **C6. A guest's checks run from the administrator's network.**
  - `-allow-private` opens ranges to every project's checks. A guest could add checks on those ranges and read what answers there: status codes, certificates, a few characters of JSON.
  - Recommendation: R13. Ranges are opened per team, and a new team gets none. A guest who needs their own private network uses a probe there, which is what probes are for.
- **C7. Who makes a team.**
  - If anyone can, the installation is a service people sign up to, with the administrator answering for strangers. If only the administrator can, someone who wants a team of one has to ask.
  - Recommendation: only the administrator, as the maintainer described it.
- **C8. One team against another.**
  - Limits are per project and per person: five of twenty attempts for a project, 200 and 1000 checks. A team with four projects takes all twenty. They were set for people an owner chose.
  - Recommendation: R34 to R36. Turns between teams rather than a fixed share, which stops working at three teams. A new team starts at 400 checks Allstill runs and 2000 that jobs ping, a first guess; the first team stays unlimited so an upgrade refuses nothing. Limits on people and projects wait until someone needs them.
- **C9. A guest moving from Healthchecks.**
  - `allstill import healthchecks` runs on the server with the Healthchecks key. A guest would have to give the administrator that key, which shows the administrator every check they have.
  - Recommendation: leave it out of this spec and say so. An import from one's own machine is a small intent of its own, worth doing before a guest arrives who needs it.
- **C10. It rests on what is not built.**
  - A team that runs itself needs pages and commands for people and projects. Today those are commands on the server.
  - Recommendation: build in the order below. After step 2 a second team exists and is walled off, with the administrator still doing the adding on the server, each time announced to the team (R27, R28). Step 3 makes it run itself.
- **C11. Alerts on or in shadow, per team.**
  - `allstill admin set-alerts` is one switch. A new team trying Allstill next to another service would want shadow mode for itself while the first team's alerts are on.
  - Recommendation: one switch for now, and say so. A switch per team is a later change that touches where every message is decided.
- **C12. Is this the permission system ADR 0001 avoided?**
  - It adds a level, and a person can now reach a project four ways. That is more to explain than "members of a project".
  - Recommendation: accept it, held by three rules: the same three words at every level, the highest role counts, and the page of R7 that shows for any project who can see it and why.
- **C13. The word.**
  - "Team" is wrong for a household and for a customer.
  - Recommendation: keep it, as it is the word the maintainer used and the least strange for most. "Group" is vague, "organisation" is heavy and what ADR 0001 rejected as a wall, "space" is close to workspace.
- **C14. A project that changes hands is left out.**
  - The intent's case 11 asks for it. Moving a project carries its keys, its log with the old team's people in it, and its single-person roles into another team, and each needs a rule.
  - Recommendation: not in this spec. With two teams, sharing covers working together, and a project can be made again where it belongs. It gets its own change once a real installation needs it, and the intent's case stays open until then.

**Order of work**, each step a release that is useful by itself:

1. The model and the upgrade: one team, access and alerts worked out the new way, nothing visible changes. R1, R2, R4, R14 to R17, R42, R45.
2. A second team, walled off and announced: `admin create-team`, `-team` on today's commands, the administrator's narrower view, the server's commands telling on themselves, the log, limits, probes, private ranges. R5, R6, R8 to R13, R18, R24, R27 to R30, R34 to R38, R40, R43, R44.
3. A team runs itself: links, people, projects and alert choices, in the dashboard, the API and the command line. R3, R7, R19 to R21, R25.
4. Sharing. R22, R23, R41.
5. Entering, and nobody left to hear. R26, R31 to R33.

## Open questions from the intent

- **How far apart are teams?** They own projects, members and probes, and nothing else. People, devices and incidents are the installation's. See Design.
- **Named groups, already planned.** A team's members are let into all its projects at once. A group for a few of them is not covered and stays planned.
- **What can the administrator do?** R8 to R13, R26 to R30; C1 to C3.
- **Who reads why a message failed?** The administrator, without what it said (R11).
- **Who answers for a team's log?** Whoever runs the installation, as today. No command removes a person from it yet, which this spec does not change.
- **How does the administrator get in?** C1.
- **Is a customer a team?** Not until they run checks of their own (case 2).
- **Who is alerted for a shared project?** C5.
- **Team roles and project roles.** The same three; the highest counts (R2, R4).
- **Sharing without learning who has an account.** Links (R19, R20, R22).
- **What the log shows across a share.** What the reader's role shows them, with the other team's people unnamed (R41).
- **Probes, calendars, shared channels.** Probes to the team (R37). Calendars and shared channels, when built, belong to a team.
- **Whose is a status page?** A team's (case 9), when built.
- **Which settings stay?** R12, R13, C11.
- **Who may make a team?** C7.
- **Names.** C13.
- **Order.** Above.

## Out of scope

- Moving a project from one team to another. Until then a project stays where it was made, and sharing covers working on it together.
- An import from Healthchecks run from one's own machine (C9).
- Alerts on or in shadow per team (C11).
- Letting members make teams (C7), and limits on a team's people and projects (C8).
- Dependencies between checks, status pages, calendars and shared channels: this spec only says where they will belong.
- A key for a whole team.
- Recovery codes, which phase 1 already plans and C2 leans on.
- Everything the intent leaves out: a hosted service, single sign-on, teams inside teams, roles a team defines, protecting a team from whoever holds the server.
