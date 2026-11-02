<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: add and change checks without a shell on the server

Intent: [intent.md](intent.md)
Status: accepted (2026-11-01, with the recommendations of C1 to C12)
Written: 2026-11-01, by Claude for the maintainer; revised the same day after an independent review against the code
Codebase: MartijnMul/allstill at dc71f3f
Policies in force: no skills yet; ADR 0001 to 0004, AGENTS.md, REVIEW.md, docs/brand, `api/openapi.yaml` 0.8.0, the specs of intent/alerts and intent/http-checks, the built-in guide (`internal/guide/guide.md`), and needs from real setups (kept privately)
Prompt: the requirements-and-design prompt of the AI-native SDLC playbook, run on intent.md

## Summary

Someone who may edit a project can add, change, pause, move and delete its checks in the dashboard, and a project's API key can do the same through `/api/v1`. Every way in runs the same rules in the store, so the commands, the form and the API cannot disagree.

The roles already say who may do what, but until now only a shell could use them. Opening them to editors is what this spec is mostly about: what an editor can learn and cause through the server, how a change stays calm, and what deleting means.

It flags that today everyone is an administrator, so no editor exists yet (C12), that one editor can starve every other project's checks (C3), what an editor can read and reach through the server (C2), who may wake people at night (C4), and how a new check should behave before it has ever worked (C5). It proposes building the forms first and the API after (C1).

## Requirements

**Adding**
- R1. An editor or owner of a project can add a ping, HTTP or TCP check to it from the dashboard. (Proposed outcome)
- R2. A form offers only the projects where the person is an editor or owner. Someone with none sees no way to add one, and an empty overview tells them whom to ask, not a shell command. (Constraints)
- R3. A new ping check opens on its page, showing its ping URL with a line to copy for `curl` first and `allstill run` second, and is `new` until its first ping. (Proposed outcome)
- R4. An HTTP or TCP check is tried once when it is saved with a new or changed address, and the page says what the try found and by what time its first real attempt comes. The try does not count as an attempt. On a probe it is not tried. (Proposed outcome; C3)
- R5. A form that is refused keeps what was typed and says, at each field, what is wrong and what to do. (Proposed outcome)
- R6. A schedule's time zone defaults to the person's, is shown, and the page says when the check is next expected after saving. (Proposed outcome)

**Changing**
- R7. An editor or owner can change a check's name, slug, description, tags, schedule, alerting and settings, and who runs it among the probes serving its project. A check's kind cannot change, and turning `critical` on takes an owner or an administrator. (Proposed outcome; C4, C7)
- R8. A change saves only what its form shows: settings of no field on the form, such as those kept from an import, stay. A box left unticked on the form does turn its setting off. (Constraints)
- R9. A change saved from a page that was out of date is refused, listing the fields that changed, and keeps what was typed. A ping does not make a page out of date. (Constraints)
- R10. A change never itself moves a check to late or down. Only a change of schedule or grace touches when a check is next expected, so renaming cannot postpone a late job. (Constraints; C6)
- R11. An attempt that started before a check's settings changed does not count, and a changed address starts its failures over. (Constraints)
- R12. An editor or owner can pause and resume a check. Pausing closes an open incident without a recovery message, and the page says a job's next ping resumes it. Resuming makes it `new`, for both kinds. (Proposed outcome; alerts spec)
- R13. Someone who may edit both projects can move a check between them, within `MoveCheck`'s rules. The page says a ping by slug needs the new project's key, that a job with `create=1` makes a new check in the old project, and that its runs and output go with it, visible to the new project's viewers. (Proposed outcome; C8)
- R14. An editor or owner can replace a check's ping URL, after a warning that the old one stops working at once. (Constraints: a ping URL is a secret, and a leaked one needs a way out)

**Deleting**
- R15. An editor or owner can delete a check, after a page saying what goes: its runs, pings and output, with the number of runs. (Proposed outcome)
- R16. A deleted check is gone from every list at once, its ping URL answers 404, its open incident closes without a recovery message, and its settings, with any token in them, are cleared at once. Its runs, pings and row are removed in the background. Past incidents keep its name. (Proposed outcome)
- R17. Someone told a check was down hears in the next digest that it was closed by being deleted or paused, as there is no other way to learn it. (Proposed outcome; alerts spec)
- R18. A digest that goes out anyway lists checks that are paused, except those that never alert; a pause alone sends no message, which the maintainer decided on 2026-11-01 over a weekly reminder. (Constraints: calm; alerts spec)

**A probe's own check**
- R19. A probe's own check can be renamed, but not deleted, paused or rescheduled, and only an administrator can move it within the projects the probe serves or set it to anything but alerting now. That would silence the sign of life of a probe other projects may rely on. (Constraints; http-checks R7)

**The API**
- R20. `listProjects`, `getProject`, `listChecks`, `getCheck`, `createCheck`, `updateCheck`, `deleteCheck`, `pauseCheck`, `resumeCheck` and `rotatePingUrl` are served as the API description says, on code generated from it. (Proposed outcome; ADR 0001)
- R21. A project's API key has a scope of `read` or `manage` (ADR 0001's `ping` scope is not needed yet, as a ping needs no key): `read` acts as a viewer of that project, `manage` as an editor, and it reaches no other project. A probe's key opens only the probe's endpoints, and an API key does not open those. (Constraints; ADR 0001)
- R22. An owner can make and revoke a project's API keys in the dashboard. A key is stored as a hash and shown once. (Proposed outcome; C1)
- R23. The API's write schema holds only what is built. A field it does not know, or a change of `kind`, is refused with 422 and the field's name, rather than ignored. (Constraints)

**Keeping what holds**
- R24. Every rule for a check is enforced in the store, or in one function all callers share, so the commands, the forms and the API reject the same things. Two differences are deliberate: someone at a shell is told more of why an address is refused (R27), and commands and imports are not held to the counts (R30). (Constraints)
- R25. Viewers see no form, no button and no address beyond what they see today, and a change of theirs is refused with 403, also when made by hand. A person or key gets 404 for a check or project they cannot see. (Constraints; http-checks R12)
- R26. A form posts only from the dashboard itself, and works without JavaScript. (Constraints; ADR 0001)
- R27. A person is never told what a host name resolves to, that an address is the server's own, or anything about a check in a project they cannot see, including another check on the same host. (Constraints; C2)
- R28. A check follows a redirect without telling the next address where it came from: no `Referer`, which today carries the previous URL with any token in it. (Constraints; ADR 0004)
- R29. Each change is written to the server's log with who made it, a person or a key, which check and what kind of change, without any URL or setting's value. (Constraints; C9)
- R30. At most 5 of the 20 attempts running at once belong to one project, tries are limited per person per minute, and a project holds at most 200 checks Allstill runs and 1000 ping checks when added this way, also on a move. Commands and imports are not held to the counts. (Constraints; C3)

**How it feels**
- R31. Pages follow docs/brand: sentence case, plain words, states with their shapes, usable at 375 px wide and in dark mode. A button says what it does: "Add check", "Pause", "Delete check". (Constraints)
- R32. Live refresh never replaces a form that is being filled in. (Constraints)
- R33. Someone with a passkey link and no shell can add a check, connect a job and see it go up in a few minutes, tried with a person who has not seen the dashboard. (Proposed outcome)

**People with a role**
- R34. An administrator can add a person limited to chosen projects with a role, and change their role, from the command line. Someone added this way sees no other project and is not an administrator. (Proposed outcome; C12)
- R35. A failed attempt on a check that has never passed opens no incident: it stays `new`, with the reason on its page and in the digest. (Constraints: calm; C5)

## Design

**One way in for the rules.** The store gets what is missing: pausing, resuming, deleting, replacing a ping URL, changing a ping check, a move taking a caller's access, and `SetAlerting` taking one, each checking the role as `CreateCheck` does and bumping the check's `version`. What the commands do today that is really a rule moves down with them: checking an address the server may reach, and clearing a TCP check's certificate settings when TLS goes off. `description` gets the 2000 characters the API says. The admin commands keep working, with an administrator's rights. In the web layer, `ErrForbidden` and `InvalidError` become a refusal, not the 500 they are today.

**Where things go in the dashboard.**
- The sidebar and the overview's head get "Add check", for those who may. On an empty overview it is the invitation.
- `GET /checks/new?project=` asks what to watch, in plain words: "a job that reports in", "a website or API", "a port". Then the fields for that kind, with the common case filled in and the rest under "More settings".
- A schedule is "every N minutes, hours or days", or a cron expression, with its zone and grace beside it, and defaults that fit a nightly job.
- A check's page gets "Edit" and "Pause" or "Resume" in its head. `GET /checks/{id}/edit` holds the form, and below it the project and a link to delete. Moving, deleting and replacing a ping URL each have a page saying what will happen.
- These pages are not `Live`, so refresh cannot wipe a form (R32). A refused form is rendered again; an accepted post redirects, so reloading sends nothing twice.
- The overview says "not checked yet" rather than "not pinged yet" for a check Allstill runs.

**Telling a page is out of date (R9).** The form carries the check's `version`, which migration 0001 keeps for this: it changes with settings, not with pings. The API's `ETag` and `If-Match` use the same value, with `If-Match` optional, as a merge patch only touches what it sends. As a check's state changes without it, the ETag names the settings: it is never used to answer 304.

**Calm (R10; C6).** A new pure function in `internal/check` decides what a change of schedule or grace does to a status, tested for each state:

| State | After the change |
|---|---|
| new, paused | No deadline, as before. |
| up, warning | Next expected counts from now. A run still open that is past the new grace is let go without an alert, the way a run from a time that did not count is. |
| late | Becomes up, with the reason "schedule changed, no ping since", next expected from now. |
| down | Stays down, with its incident, until a ping. The page says so. |
| unknown after a restart | A new grace from now. |
| unknown, schedule unreadable | Becomes new, or up with next expected from now if it has pinged: this is the very case a form must be able to fix. |
| run by Allstill | Waits at most one new interval and its grace, as `SetActiveCheckOn` does. |

Whoever saves tells the scheduler (`Nudge`), which has one timer: a shorter deadline is otherwise missed. What can still alert after a change is deliberate: a new judgement under new settings, such as a stricter `fail_days`; a move making the new project's people due for an incident that is open; and a change of urgency making people due who were not told.

**A check that has never worked (R35; C5).** A failed attempt on a check that has never passed does not open an incident: it stays `new`, with its reason on its page and in the digest. A check that passed before, including a resumed one, goes down after its failures in a row as usual. So a typo costs nobody their sleep, and a check nobody ever got working is not green.

**Attempts in flight (R11).** The loop's check carries its `version`, and an attempt whose version is not the check's any more is dropped, as one by the wrong runner already is.

**Deleting (R15 to R17).** Setting `checks.deleted_at` is respected by every read that matters, frees the slug through the partial index, and the incident closes with the reason `deleted`, which the sender already leaves silent. New is that the row's `settings` are cleared at once, and that tidy removes a deleted check's pings and runs in batches, and then the row, which also frees its ping UUID for a later import. Foreign keys allow that; `probes.check_id` restricts, which R19 covers. A job that still pings a slug with `create=1` makes a new check, as with Healthchecks, and the delete page says so.

**Trying an address (R4; C3).** After saving, the same request makes one attempt through the outbound rules, for at most 15 seconds, within the server's 30-second write limit. The result lives for a minute in memory, keyed by session and check, for the page the post redirects to. It is stored nowhere else. The time of the first real attempt comes from the check's slot, which can be up to an interval away.

**Fair shares (R30; C3).** The loop runs at most 5 attempts of one project at once, and its queue goes on past those that wait. A project of dead hosts then waits on itself, and others keep their slots.

**API keys (R21, R22; C1).** A new table: id, workspace, project, name, hash, scope, who made it, created, last used, revoked. A key resolves to an `Access` holding that one project with the matching role, with its id for the log. Owners get the section on a small project page, which does not exist yet: name, scope, "Make key", the list, "Revoke". Handlers are generated by adding the `Checks` and `Projects` tags to `internal/api`, each operation choosing its kind of key rather than one middleware for all. The description loses `Idempotency-Key`, which would mean storing responses holding ping URLs, and the fields of features not built; both can come back additively.

**A secret stays one.** The form shows a URL or `location` whole only to those who may see it (http-checks R12), and a page's address never holds it: posts carry it in the body.

**What a person is told of a refusal (R27; C2).** An editor is told "this address is not on the public internet, and this server may not reach it; ask your administrator, or use a probe", also for the server's own address. `add-check`'s message, with the address and the range to allow, stays for the shell. Its note that another check watches the same certificate looks only within the project.

## Cases played through

1. **A new person.** Ada follows a passkey link, sees an empty overview and "Add check", chooses "a job that reports in", names it, keeps the daily schedule in her own time zone, and lands on a page with a `curl` line to copy. She runs it; the page refreshes to up. No shell.
2. **An outsider.** Bob is a viewer of a customer's project. He sees its checks as a viewer does today, no buttons, and a hand-made post gets 403 there and 404 anywhere else. A viewer of a project with no checks yet is not told to add one.
3. **A nightly job moved to 05:00.** An editor changes the cron at 04:50, and the job last pinged yesterday at 03:00. The page says it is next expected today at 05:00: if the crontab has not been changed by then, it goes down after its grace, and that is a true alarm.
4. **Moved the other way.** Changed to 04:00 at 04:50: it does not alert for the 04:00 it just missed; next expected is tomorrow at 04:00.
5. **Grace cut while a run is open.** A run is 40 minutes old and grace goes from an hour to 30 minutes. The run is let go, nobody is alerted, and a run that ends later still records its time.
6. **A mistyped address.** A new site check for a typo host: it saves, the try says "the host name does not exist", and the editor fixes it on the next page, which tries again. No incident: it has never passed. Failures from the old address, still on their way, do not count.
7. **An internal host name.** An editor tries `intranet.example.com`, which resolves to a private address. They are told it may not be reached and to use a probe, not what it resolved to.
8. **Two editors.** Both open the edit form. The second to save is told which fields changed, and keeps what they typed.
9. **Shared infrastructure.** One operator runs a probe serving two customers. An editor of customer A picks it from a list of probes serving A, and could reach B's network through it, as C7 says. An editor of a third project never sees it listed, and a hand-made post is refused.
10. **One editor's dead hosts.** A project's 30 checks all time out for a minute each. They queue among themselves at 5 at a time; other projects' checks keep their slots.
11. **Deleting during an outage.** The check is down, with an open incident. Delete closes it. Nobody hears "recovered". The person paged at night reads in the next digest that it was closed by being deleted.
12. **A deleted slug comes back.** A job with `create=1` pings a deleted slug: a new check appears with the project's defaults. The delete page warned of it.
13. **A probe's own check.** Its page has no delete, move, pause or schedule, and alerting is fixed for editors. A hand-made post is refused with why.
14. **Moved to another customer.** The page warns that its runs and output become visible to that project's viewers. It must be confirmed.
15. **A job that keeps running through a pause.** It pings; the check resumes, as Healthchecks does, and the pause page said so. A fail ping alerts, which is true.
16. **An import with settings the form does not know.** Editing the name leaves them as they were.
17. **A key for a script.** An owner makes a `manage` key for a project. It creates a check there, gets 404 for one elsewhere, and is refused on the probe's endpoints. Revoked, it gets 401.
18. **Alerts in shadow mode.** Nothing here differs: changes apply, messages stay recorded.

## Areas of concern

- **C1. Forms and API together, and keys that do not exist.**
  - The earlier spec (C10) said the form and the API follow together. But the API needs a table of keys and a page to make them, the person the intent is about never touches it, and it has no real client yet to test its shape against.
  - Recommendation: one spec, two release candidates. Forms first, on store operations the API will use unchanged. Then keys and the API, when the menu bar app or an agent is about to use them, not necessarily straight after. OAuth for people waits for that app. Changed by [intent/cli-anywhere](../cli-anywhere/spec.md) (C1): the command line is that client, with a person's sign-in, and keys are made with commands.
- **C2. What an editor can reach and read through the server.**
  - Until now, nobody with fewer rights than a shell could ask the server anything. Within `-allow-private` and a probe's network, an editor can now learn whether a port is open, read up to 40 characters of any JSON field ("status is x instead of y"), and fire any GET every minute. Their own URL receives the server's address and version. Reasons are shown to viewers too. The earlier spec (R10) accepted this for "every editor of every project", but on paper: no editor could reach it.
  - Recommendation: keep the rule, as editors are people an owner chose; say all of this plainly in `docs/running.md`; hide resolved and own addresses (R27). If customers become editors, limiting it needs a flag on the check read as it dials, as a check when saving is passed by a name that changes what it resolves to. That is a later change.
- **C3. One editor against everyone else.**
  - Twenty checks on dead hosts with long timeouts fill every running slot, and other projects' checks then go unknown. That blinds every party on the installation, well under any count. Each check is also a request a minute from the server's address to any public host, under the installation's name.
  - Recommendation: the share per project and the limit on tries of R30. The count of 200 is only a coarse brake on outward requests, as deleting and adding again passes it. The alternative, only administrators adding checks Allstill runs, would leave the intent's main person unable to add a site.
- **C4. Who may wake people at night.**
  - `critical` breaks through quiet hours for everyone following a check, including workspace administrators without a membership. ADR 0001 gives editors routing, so this is a choice; an editor of a small project could otherwise wake them at 03:00, and each change of urgency makes untold people due at once.
  - Recommendation: only owners and administrators can turn `critical` on. Urgency, including `none`, stays with editors, as it is logged and visible, with R19's exception.
- **C5. A check that has never worked.**
  - Today a new check on a typo goes down after two failures, about two minutes, and alerts. "`new` until first ping" is what a ping check already means.
  - Recommendation: no incident before its first pass, as in the design, with the reason on its page and in the digest. The cost: a site that is really down when it is added does not alert until it has been up once.
- **C6. A check that is late when its schedule changes.**
  - It needs some answer: staying late shows "overdue" with a future next time, and going down would be an alert caused by the edit.
  - Recommendation: up, with the reason shown, close to what Healthchecks does. The cost is green without proof for one interval, which is why the reason stays on the page.
- **C7. An editor and a probe.**
  - A probe can reach inside someone's network. The earlier spec (C6) let any editor of a served project assign checks and made the choice of served projects the boundary, but that too was only reachable from a shell. With a shared probe, an editor of project A can reach anything it reaches, including B's network, and whoever runs the probe receives the URLs of every project it serves.
  - Recommendation: keep it, and say both when a probe is made, as `create-probe` and the docs will. One probe per party is the safe setup.
- **C8. Moving a check between customers.**
  - Runs and output go with it, and viewers can read output, so B's viewers read what A's job printed.
  - Recommendation: the warning of R13, confirmed on its page. Clearing output on a move is the stricter option, if real use asks for it.
- **C9. Who changed what.**
  - With several people able to delete, "who removed this?" has no answer: `events` has no person.
  - Recommendation: the log line of R29 now; an audit page when a real installation needs one.
- **C10. Duplicate names.**
  - `set-check -check NAME` already refuses a name two checks share, and imports, `create=1` and moves are exempt from any rule anyway.
  - Recommendation: a warning on the form, not a refusal, which would also trap someone retrying a save that timed out.
- **C11. How a project's first owner gets there.**
  - The intent keeps projects with their commands, so a fresh installation needs a shell once. R33 starts from a passkey link someone with a shell made.
  - Recommendation: accept it for now. Making projects and inviting people from the dashboard is the natural next intent.
- **C12. Today, nobody can be an editor or a viewer.**
  - `create-user` makes every person a workspace administrator, nothing in the program calls `SetMember`, and the guide says "everyone is an administrator" and warns against adding an outside party. So the roles exist in the store and its tests, but on a real installation the form would only ever be used by administrators, who already have a shell, and the outsider of case 2 cannot exist.
  - The intent assumed otherwise, in saying members "keep their commands". Scope is the maintainer's to change.
  - Recommendation: add the small command of R34 with this change, `create-user -project P -role editor` and a way to change a role, and leave managing people in the dashboard for the next intent. It is a little code on what exists, and it is what makes the rest of this spec real. The alternative is to build the forms for administrators only, and leave R1's editor and every concern about editors for later, which is simpler now but means building these checks twice.

## Open questions from the intent

- **Should deleting keep a check's history for a while?** Answered: no. Its settings go at once and the rest in the background, without undelete; pausing is the way to stop without losing, for a job that stops pinging too. It is a recovery from a real mistake only through a backup, which keeps it for a while anyway.
- **Does the first API need tokens for people as well as project keys?** Answered: project keys; OAuth with the menu bar app (C1).
- **Should the form try an address before saving?** Answered: after saving, once, on the same request, not stored (C3).
- **May an editor give a check to a probe?** Answered: an editor of a project the probe serves (C7).
- **Should an editor see a project's ping key?** Answered: nobody can, as only its hash is kept. A check's own ping URL needs no key, and that is what a new check shows. Replacing a project's key from the dashboard is left out; `<ping key>` stays a placeholder on the page.

## Out of scope

- **Inherited from the intent:** managing projects, members and who is alerted in the dashboard; rules on values, calendars and maintenance windows; editing many checks at once; undo.
- **Also left out:** managing people in the dashboard; managing probes in the dashboard, and removing one; replacing a project's ping key; OAuth; `/me` and the rest of the API; server-sent events; an audit page; limiting how much output a project's checks may store.
