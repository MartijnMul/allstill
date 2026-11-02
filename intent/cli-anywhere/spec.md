<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: use the command line to create, manage and do things, from anywhere

Intent: [intent.md](intent.md)
Status: accepted (2026-11-02, with the recommendations of C1 to C11)
Written: 2026-11-02, by Claude for the maintainer; revised the same day after an independent review against the code
Codebase: MartijnMul/allstill at be2f2e4
Policies in force: no skills yet; ADR 0001 to 0004, AGENTS.md, REVIEW.md, docs/brand, `api/openapi.yaml` 0.9.0 and `api/README.md`, the specs of intent/manage-checks and intent/activity-log, the built-in guide (`internal/guide/guide.md`), and needs from real setups (kept privately)
Prompt: the requirements-and-design prompt of the AI-native SDLC playbook, run on intent.md

## Summary

From their own machine, someone types `allstill login https://allstill.example.com`, confirms a short code with their passkey in any browser, even on their phone, and from then on works with commands such as `allstill status`, `allstill checks add` and `allstill projects create`. They can do what their role allows, nothing more, and the log says it was them. A script gets a key of its own project instead of a person's sign-in.

The commands talk to the JSON API, which is served at last, for people as well as for keys. `allstill admin` stays for the server, but no longer creates a database by accident. A few things stay on the server on purpose, so that a stolen laptop cannot take the installation for good.

It flags that this reverses the order the manage-checks spec chose (C1), what stays on the server and what a stolen laptop can still do (C2), how someone could be tricked into approving a stranger's code (C3), how long a sign-in lasts (C4), that removing a person becomes essential, and changes what the log's spec decided about their name (C6), and that it holds back from owners something ADR 0001 gives them (C10).

## Requirements

**Signing in**
- R1. `allstill login URL` prints an address and a short code, and finishes by itself once someone has approved that code in a browser on any device. (Proposed outcome; ADR 0001)
- R2. Approving takes a signed-in person, the code, and that person's own passkey again on the approval page, which says what is asking, when, with which scope, and that they should only go on if they started this themselves. (Constraints; C3)
- R3. A code works once and is gone after 10 minutes, a person gets a few wrong tries a minute, and a denied or expired code ends `login` with a message saying so. (Constraints)
- R4. A sign-in has the scope `manage` unless asked for `-read-only`, and acts with the person's role as it is at that moment, so a change of role counts at once. (Proposed outcome; Constraints)
- R5. A credential with the scope `read` acts as a viewer in every project its holder can see, also for an administrator, so it never reads a ping URL, a whole address or a token, or anything only an editor, owner or administrator may read. (Constraints; http-checks R12)
- R6. A sign-in ends after 30 days without use and after a year at most, as a browser session does. (Open question; C4)
- R7. A person sees their sign-ins and browser sessions on **Devices**, with name, what they may do, when made and last used, and can end any of them; `allstill logout` ends the one in use, also a read-only one, and that is all a read-only one can end. Something ended stops working on the next request. (Constraints; ADR 0001)
- R8. The credential is kept in the user's configuration folder, readable only by them, per installation; the last one signed in to is used unless `-server` or `ALLSTILL_SERVER` says otherwise. (Open questions; C5)
- R9. The command line sends a credential only over `https`, or `http` on this machine, the same rule the server has for its own address, and it follows no redirect with one. (Constraints)
- R10. Signing in works where there is no browser, as the code can be approved anywhere. (Open question)

**Keys for scripts**
- R11. An owner or administrator can make, list and revoke a project's keys with `allstill keys`, with a scope of `read` or `manage` as manage-checks R21 says; a key is shown once and stored as a hash. (Constraints; ADR 0001; C1)
- R12. `ALLSTILL_SERVER` and `ALLSTILL_TOKEN` make a command use a key without signing in, and a key never appears in output after it is made. (Affected users: scripts)
- R13. A key belongs to its project and keeps working when the person who made it has gone; the log names the key on what it does, and who made it on the line saying it was made. (Open question; ADR 0001)

**Seeing what exists**
- R14. `allstill status` shows, per project the caller can see, how many checks are in each state, and every check needing attention, with a reason. (Proposed outcome)
- R15. `allstill projects`, `allstill checks`, `allstill checks show` and `allstill checks runs` list and show what the caller can see, with the same limits on addresses as the dashboard. (Proposed outcome; http-checks R12)
- R16. `allstill log` shows the log with the same narrowing and the same rules for each reader as the page, including a deleted check by its name. (Problem)
- R17. `allstill whoami` says who is asking, on which installation, with which scope and which roles. (Proposed outcome)
- R18. Output is readable text by default, and `-json` gives the API's own answer, for scripts. (Proposed outcome)
- R19. Printed text never carries control characters from names, reasons or anything else that came from outside, so a monitored site or a typed name cannot steer the terminal. (Constraints)

**Creating, changing and removing**
- R20. An editor or owner can add a job's check, a site or a port, change, pause, resume, move and delete a check, as in the form and under the same rules. (Proposed outcome; manage-checks R24)
- R21. Adding a job's check or replacing a ping URL prints it, with a line to copy for `allstill run`; `allstill checks ping-url` shows it again, for those who may see it; nothing else, `-json` included, ever carries it. (Problem; Constraints)
- R22. Adding or changing an address tries it once and says what it found, as the form does, under the same limits, per credential. (manage-checks R4, R30)
- R23. An editor can see which probes serve their projects, to choose one for a check, and an administrator sees all probes. (manage-checks R7)
- R24. An administrator can create and rename a project; the ping key is printed once. (Proposed outcome; ADR 0001)
- R25. An owner or administrator can delete a project with no checks and no probe, and otherwise it says what still holds it. (Open question; ADR 0001; C7)
- R26. An owner or administrator can list a project's members, change a role and take someone out, but an owner cannot change an administrator's membership. Adding someone who is not yet a member takes an administrator. (Constraints; C10)
- R27. An owner cannot take away or demote the project's last owner, though an administrator can; an administrator who holds no membership does not count as one. (Constraints)
- R28. An administrator can list people, add a person limited to chosen projects, make a passkey link for someone who has no passkey yet and is not an administrator, and end all sign-ins and sessions of someone who is not an administrator. (Proposed outcome; C2)
- R29. An administrator can remove someone who is not an administrator: they can no longer sign in, every sign-in, session, passkey and channel of theirs ends, and they are no longer told anything. (Open question; C2; C6)
- R30. Removing a person says which keys they made, and the projects where they were the only member being told, with who is told from then on, or that nobody is, so that someone can act on what they could still hold and on who hears what. (Constraints)
- R31. Making a probe and giving one a new key stay on the server, so that a stolen sign-in cannot leave behind a probe that runs checks and reports on them. (Constraints; C2)

**What stays on the server**
- R32. `serve`, `backup`, `import healthchecks`, `compare healthchecks`, `admin test-email`, `admin set-alerts`, `admin create-probe`, `admin rekey-probe`, and all of these for an administrator stay on the server, where the API offers none of them: making one, removing one, a passkey link, ending their sessions. So does a passkey link for anyone who already has a passkey. (Open question; C2)
- R33. On the server, `admin remove-person`, `admin end-sessions` and `admin remove-passkey` work on anyone, as a lost device may be an administrator's; the last administrator cannot be removed. (Constraints; C2)
- R34. Every command that opens a database refuses a path holding none and creates nothing, saying what it looked for and pointing to `allstill login`; only `serve` creates one. (Problem)
- R35. `allstill run` and `allstill probe` take no sign-in and use nothing of this. (Constraints; ADR 0001)

**Keeping what holds**
- R36. Every operation goes through the store's rules with the caller's access, so the dashboard, `allstill admin` and the API refuse the same things, apart from what R32 and R33 keep for the server, in the terms of manage-checks R24 and R25: 403 for what the role forbids, 404 for what they cannot see. (Constraints)
- R37. A key is held to what a person is held to, not to what the server's own commands are: the counts, the limits on tries and the rules on a description. (Constraints; manage-checks R30)
- R38. The API accepts only a bearer credential and never a browser's cookie, so a web page cannot use someone's session against it. (Constraints)
- R39. A person's action is logged as theirs, with the name of the sign-in it came through for those who may read the name, and a key's as the key. (Constraints; activity-log R15, R21, R22)
- R40. Sign-ins made and ended, a browser session ended by someone from elsewhere or by an administrator, a passkey removed, keys made and revoked, a project deleted, a person removed and each membership that goes with them write a log line, and a failed attempt does not, as a failed sign-in does not. (Constraints; activity-log)
- R41. No credential, ping URL or address with a token is ever in a log line, in the server's log, or in an error. (Constraints)
- R42. Each credential has a limit on requests per minute, answered with 429 and `Retry-After`, and asking for a code and polling for it are limited too, so that neither a loop in a script nor someone outside can slow pings down or fill the server. (Open question; Constraints)
- R43. Pings and alerts behave as before, shown by `make load` no worse once it also sends API requests. (Constraints)

**The API description**
- R44. What is served follows `api/openapi.yaml`, and what the description promises and is not built either leaves it, as allowed while it has no clients, or says it is not served yet. (Constraints; C9)
- R45. Asking a server for something it does not know ends in a message saying that server and command line are of different versions, with the command line's version and the server's where it can tell. (Constraints)

**How it feels**
- R46. Commands are named `allstill <thing> <what to do>`, take a project or check by name as `allstill admin` does, with an id for a name that two share, which they list, and a person by email or id. (Proposed outcome; manage-checks C10)
- R47. Messages follow docs/brand: plain words, what went wrong, what to do. A refusal says who to ask, not a status code. (Constraints)
- R48. Someone with a passkey and no shell on the server signs in, creates or picks a project, adds a nightly job's check, runs it with its ping URL and sees it go up in a few minutes without the dashboard, tried with someone new. (Proposed outcome)

## Design

**One way in, three kinds of credential.** Next to the probe's key there are now a person's sign-in and a project's key. A bearer value resolves to a `store.Access`, the same type the dashboard uses. A person's holds their roles as they are now, with `Actor` set to them and the sign-in's name. A key's holds one project, with a role of viewer for `read` and editor for `manage`, as manage-checks designed. A `read` credential is built with `Admin` off and every project at viewer, whoever holds it (R5), so what it may read is decided where the role is read, and no handler has to remember the scope. Values start with their kind (`alt_`, `alk_`, as `alp_` does for probes), so the server knows which table to look in and a leaked one can be recognised by secret scanners. Each is stored as its SHA-256. Last use is written at most once an hour, as for sessions.

**What the store has to gain (R36, R37).** Most of what this needs is there for checks and missing for the rest.
- `SetMember`, `RemoveMember`, `CreateProject`, `RenameProject`, `CreatePerson`, `CreateMember` and `CreatePasskeyLink` today take an `Actor` and check nothing, as only a shell could call them. They gain an `Access` and the rule, with `allstill admin` passing an administrator's. What only the server may do tests that `Actor.Kind` is a command, as an administrator's access is otherwise the same. `CreateProbe` and `RekeyProbe` already refuse anyone but an administrator, and gain that test.
- `viaPerson` reads `PersonID` to tell someone from the server's own commands, and a key has none, so it would be treated as the server. It tests `Actor.Kind` instead.
- The role an administrator reads as (owner, in `ProjectsOfPerson`) is no membership: R26 and R27 count real rows.
- Which of a check's fields are secret is decided in the web layer today (`whole`). It moves to where both can use it, and an API answer goes through it.
- The limits on tries are per person (`internal/web/manage.go`) and become per credential.

**Signing in.** This is the device grant of RFC 8628, as ADR 0001 says, for one client only (`client_id` is a constant). No client secret, no other grant, no registration.

```
allstill login https://allstill.example.com
  POST /oauth/device_authorization   -> device code, user code, /device, 600 s, every 5 s
  Open https://allstill.example.com/device and enter WDJB-MJHT
  POST /oauth/token (polls)          -> authorization_pending ... then a token
Signed in as Ada on allstill.example.com.
```

The grants waiting for approval live in memory, not in the database, since anyone on the internet can ask for one and they last 10 minutes: asking costs no write, a restart just means asking again, and there is one server process by design. Asking is limited to 60 a minute over the whole installation, as the server cannot tell who asks (below), which also bounds how many are held; polling is limited per device code with `slow_down`. A device code redeems once, atomically. The answers are RFC 8628's own: `authorization_pending`, `slow_down`, `access_denied`, `expired_token`. A user code ignores case and dashes.

The approval page lives at `GET /device`, behind sign-in. The sign-in page gains a way back to it, as today it always ends at `/`. It takes the code in a form and never from the address, so a link cannot arrive with the code filled in, which is the usual way this grant is abused. The next page shows what was asked: the name the command line gave for its machine, marked as its own claim, how long ago, and the scope. It does not show an address: nothing in the server knows a caller's real one behind the proxy `docs/running.md` asks for, there is no setting for a trusted proxy, and the log's spec keeps no addresses (activity-log R13).

"Allow" is a passkey ceremony of its own, not `/login/begin`. Sign-in's is discoverable, accepts any person's passkey, and ends in a new session. This one has its own purpose in the challenge, bound to that grant and scope so that an answer for one code cannot approve another, checks that the passkey is the signed-in person's own, updates the counter, refuses and writes `passkey.looks_copied` on a counter that went backwards, as sign-in does, and opens no session. "Deny" ends the grant.

The sign-ins are one new table, `person_tokens`: person, the passkey it was approved with, name, scope, hash, made, last used, expires, ended. Removing a passkey ends what was approved with it, as with sessions. `tidy` clears what is old.

Only a token comes back, with no refresh token. OAuth has refresh tokens because its access tokens are short and checked without a lookup. Here each request reads the row and the person's roles anyway, and ending one counts at once (C4).

**The API.** The strict generated server of the probe grows to the tags in use, each operation choosing its kind of credential, as manage-checks designed. `/oauth/*` is described in the same document under a tag of its own, with RFC 8628's form bodies, left out of the generated handlers as `/health` is, since a standard defines it. The list:

| Tag | Operations | Who |
|---|---|---|
| Account | `getMe`, with the server's version; `endThisToken` | Anyone signed in, read-only too; a key gets `getMe` |
| Status | `getStatus`, holding counts and what needs attention | Any credential |
| Projects | `listProjects`, `getProject` | Any credential |
| | `createProject`, `updateProject` | Administrator |
| | `deleteProject` | Owner, administrator |
| Checks | `listChecks`, `getCheck`, `listRuns` | Any credential |
| | `createCheck`, `updateCheck`, `deleteCheck`, `pauseCheck`, `resumeCheck`, `moveCheck`, `rotatePingUrl`, `getPingUrl` | Editor, under the store's rules |
| Members | `listMembers`, `setMember`, `removeMember` | Owner, administrator; a person's sign-in only |
| Keys | `listKeys`, of one project or of all the caller is an owner of, which for an administrator is every project; `createKey`, `revokeKey` | Owner, administrator; a person's sign-in only |
| People | `listPeople`, `createPerson`, `removePerson`, `createPasskeyLink`, `endSessionsOfPerson` | Administrator; a person's sign-in only |
| Probes | `listProbes` | Editors see those of their projects, as the form does; administrators see all |
| Log | `listLog` | Any credential, by the log's rules |

A key can never make a key or a person, or touch anything outside its project, so a leaked key stays a leak of one project. A key's row keeps its own copy of who made it and the sign-in's name, and `listKeys` shows them by the log's rules: an owner reads "an administrator" or "a former member" where the log would say so, and only an administrator or the maker reads the sign-in's name. Other sign-ins are listed and ended on **Devices** alone, so a read-only sign-in can neither list nor end them. An owner reading `listMembers` gets names, roles and ids, and only an administrator gets email addresses. `createCheck` and `updateCheck` answer with what the try found (R22), which the description gains. `listLog` returns facts and not sentences, and the wording moves from `internal/web/log.go` to where the page and the command line both use it. An older command line says only what kind of entry it does not know.

**What leaves the description (R44).** `Check.ping_url` (only `createCheck`, `rotatePingUrl` and `getPingUrl` answer with one), `Idempotency-Key`, the fields of features not built, `Status`'s `event_id`, `open_incidents` and `upcoming`, `getRun`, and for now `listIncidents`, `getIncident`, `acknowledgeIncident`, `snoozeIncident` and everything under `Calendars`. `/events`, `Alerts` and `setAlertSettings` stay in it and are not served, and each says so at the operation, since a client generated from it should not be misled. `api/README.md` stops saying the dashboard manages projects and members. The command line's client is generated from the same document, so it cannot drift.

**The commands.** A new package holds the API client, the credentials file and the printing; `cmd/allstill` only wires them, as ADR 0001 asks.

```
allstill login URL [-read-only] [-name NAME]
allstill logout
allstill whoami
allstill status
allstill log [-project P] [-check C] [-name N] [-person ID] [-what W] [-late]
allstill projects [create NAME | rename NAME NEW | delete NAME]
allstill checks [-project P] [show | runs | add | change | pause | resume | move | delete | ping-url] ...
allstill members -project P [set PERSON ROLE | remove PERSON]
allstill keys [-project P] [make NAME -scope read|manage | revoke NAME]
allstill people [add NAME ... | remove PERSON | link PERSON | sign-out PERSON]
allstill probes
```

`checks add` and `checks change` take the flags of `admin add-check` and `admin set-check`, parsed by the same code so the two cannot disagree, plus what the form also changes: for a job's check `-every`, and for all `-new-name`, `-slug`, `-description`, `-tags`, `-urgency` and `-critical`. A person is an email address or an id, resolved by the server. For an owner it looks only among the project's members, with one answer for someone who does not exist and someone who is not a member, so it tells them nothing of anyone else. Names are resolved by the command line against the list, and a project can be left out when the caller has just one. A write is never retried by itself, as `Idempotency-Key` is gone. Exit status is 0 when the command did what it said and not otherwise; `status` reports state, not health, and is 0 however bad things are.

**`allstill admin` stays (C8).** It keeps working on the server, with its log lines. The store's `Open` gains a form that will not create a file, used by everything but `serve` (R34). The guide's warning about running these elsewhere can then go, and the tests' fixtures make their database first.

**Removing a person (R29, R30; C6).** Using `people.disabled_at`, which migration 0001 keeps for this and which the sign-in and alert queries already honour, including who follows a project and who is told. Nothing sets it yet. The row stays, as it holds foreign keys and the log's links. Everything that grants or reaches goes in one transaction: sessions, sign-ins, passkeys, passkey links, channels and memberships, each membership with its own `member.removed` line, so that an owner reads that a member left. The email is cleared, as `people_email` ignores `disabled_at`, so the same address can come back as someone new. The command then prints the keys they made, and the projects where they had alerts on and no other member has, with who is told from then on: the administrators who are not members, or nobody.

In the log, no entry changes (C6). `ReadLog` reads the ids of removed people once per call, a small list, and `readLogEntry` takes it. The rules that exist come first: a viewer still reads "the team", and someone who reads "an administrator" still does. Only a name the reader would otherwise read becomes "a former member", in the fields that can hold it (`By`, `Person` and `About` of a member's line), with its id left out so that it cannot be narrowed to. An administrator reads everything as before.

**Deleting a project (R25; C7).** Using `projects.deleted_at`, as for a check. A project with no check that is not deleted and no probe serving it: a check waiting for tidy does not hold it up. A probe's own check, or a probe serving it, does, and since probes cannot be deleted or re-served yet (C11), such a project stays. Its keys and members go, each key with its `key.revoked` line, its ping key stops working, and its log entries stay.

**Limits (R42).** A token bucket per credential, as the ping limit is, checked after the credential is looked up, so that made-up values do not fill it. A restart resets it, which is fine for a brake. Lists are paged, with at most 200 to a page, and bodies are capped as the probe's are.

**What the log gains.** A kind not in `logMinRole` is for administrators only, so each new one gets its reader on purpose: `token.made`, `token.ended`, `session.ended` and `passkey.removed` for the person themself and administrators, through `logOwnKinds`, `key.made` and `key.revoked` for owners and administrators, `project.deleted` and `person.removed` for administrators. The sign-in's name goes in `details`, shown as "Ada, with laptop-1". `readLogEntry` today masks `By` and keeps `details`, so it removes the name wherever a person reads as "the team", "an administrator" or "a former member", with a test per role. It is what `-name` says, by default the machine's host name, and is copied as given, as all names are, so `docs/running.md` lists it among what the log keeps. Activity-log R15's promise that the API uses the same record is met by `Access.Actor`, without new columns.

**The guide.** "Not here yet" and "On the server" change, with every new command in its examples, so `TestGuideCommandsExist` holds them to their flags. For AI assistants: they use a key or a read-only sign-in they were given, they never run `allstill login` or approve a code themselves, and a read-only one cannot read a ping URL, so for that they ask.

## Cases played through

1. **Daily work.** Ada is an administrator, at her laptop. `allstill status` shows two checks down. `allstill log -check backup` shows Bob changed its schedule an hour ago. `allstill checks change -check backup -cron "0 3 * * *"` puts it right. No ssh.
2. **The success test.** `allstill login`, approve on the phone, `allstill projects create Backups`, `allstill checks add -project Backups -name nightly -cron "0 3 * * *"`. It prints the ping URL and a line for `allstill run`. She adds it to the crontab, and `allstill status` shows it new, then up.
3. **An editor without a shell.** Bob edits one project. `allstill checks` shows only that project. `allstill projects create Other` says only an administrator can, and who to ask. Asking for a check elsewhere by id says it does not exist, as it would for one that really does not.
4. **A viewer.** Carol is a customer. `allstill status` and `allstill checks` show what the dashboard shows her: scheme and host for an address, no ping URLs, "the team" in the log. `allstill checks ping-url` is refused.
5. **A read-only administrator.** Ada signs in with `-read-only` for an AI assistant. It sees every project as a viewer: no ping URL, no whole address, "the team" in the log, no people. She keeps her full sign-in for herself.
6. **A stolen laptop.** Ada ends that sign-in and that browser session on **Devices** on her phone. The thief's next command gets 401. Her passkey may still be on the laptop, and it stays good until `admin remove-passkey` on the server. If the thief made a person or a key, the log says so: she removes the person, and `allstill keys` lists every key with who made it and the sign-in it came through, for her to revoke. The thief could not make or re-key a probe (C2).
7. **A lost laptop with the only passkey.** Ada can no longer sign in anywhere. On the server, `admin passkey-link` gives her a new passkey, `admin remove-passkey` removes the old one, and with it what was approved with it. Someone who is not an administrator and has lost theirs needs the same on the server, as a link from a laptop would let a thief plant a passkey on a colleague's account (C2).
8. **A tricked approval.** A message says "enter WDJB-MJHT to keep your account". The page shows a machine calling itself `backup-server` asked a minute ago, and asks for her passkey. A code cannot be sent to her in a link. It can still fool someone careless (C3).
9. **A script.** An owner runs `allstill keys make monitor -project Shop -scope read` and puts the key in the script's secrets. The script runs `ALLSTILL_TOKEN=... allstill status -json`. It sees Shop alone and is refused any change. Revoked, it gets 401.
10. **Someone leaves.** Dan owned a project and made its key. `allstill people remove dan@example.com`: his sign-ins, sessions, passkeys and channels end, and he is told nothing more. The command lists the key he made, as he may have kept it, and says that he was the only member being told, and that from now on only the administrators who are not members hear of that project, or nobody if there are none. The key keeps running until an owner replaces it, as ADR 0001 wants. The log says he left the project. Old lines read "a former member" to another owner, "the team" to a viewer as ever, and Dan to an administrator.
11. **The wrong place.** On a laptop, `allstill admin create-project Backups` says there is no database at `allstill.db`, that these commands work on the server, and to use `allstill login` from anywhere else. No file appears.
12. **No browser.** On a small server, `allstill login` prints the address and code. Ada approves on her phone, and the server is signed in. A key is usually better there, and the guide says so.
13. **Several installations.** A consultant signs in to two. The last is the default; `-server` or `ALLSTILL_SERVER` picks the other. Nothing merges them.
14. **Different versions.** An older server is asked for `listLog`: 404 without a problem body, and the command says the server is older and that it should be upgraded. A newer server with an older command line just works, as `/api/v1` only grows after its first client.
15. **Plain http.** `allstill login http://allstill.lan` refuses, saying a sign-in would travel unprotected. Passkeys need https there too, so such an installation could not sign anyone in anyway. `http://localhost:8000` works.
16. **A first installation.** `serve`, then `admin create-user` on the server for the first administrator, who makes a passkey, as today. From there it is `allstill login`.
17. **Shared infrastructure.** One operator runs a probe for two customers. Each customer's owner sees members and keys of their own project, no people list, and only the probe that serves them. They cannot add the operator's staff or a person from the other customer, so they cannot wake them by adding them (C10). The operator's administrators follow every project they are not members of, so an owner's `critical` still reaches them, as manage-checks C4 accepted.
18. **An administrator who is also a member.** Ada is an administrator and also a member of Shop. Bob, an owner of Shop, cannot change her membership. Erin is an administrator with no membership, and she does not count as an owner of Shop, so if Bob is its last real owner, he cannot take himself out.
19. **A hostile name.** A site's JSON field holds terminal escape codes, and a reason repeats them. `allstill status` prints them harmlessly.
20. **Two with one name.** `allstill checks show -check nightly`, and two projects hold it. It lists both with their projects and ids, and changes nothing.
21. **An AI assistant writing a crontab line.** Given a `manage` key of the project, it adds the check and keeps the ping URL with the project's secrets. Given nothing, it writes the commands for the person to run, as today.
22. **Alerts in shadow mode.** Nothing differs: changes apply, messages stay recorded, and switching alerts on stays on the server.
23. **A poll that never stops.** A script runs `status` in a tight loop. It gets 429 with `Retry-After`, and pings go on as before.
24. **Someone hammering `/oauth/device_authorization`.** No database write. After 60 in a minute others get 429 too, and `login` says to try again in a moment, for as long as it goes on (C3). Anyone signing in by passkey in the dashboard is untouched.

## Areas of concern

- **C1. This reverses the order manage-checks chose.**
  - Its C1 said keys and the API come when the menu bar app or an agent is about to use them, and that "OAuth for people waits for that app". Its R22 had owners make keys in the dashboard, on a small project page.
  - The command line is now that client, and the intent asks for a person's own sign-in. Keys alone would not do: they reach one project, so an administrator or anyone with several projects would need one per project, and the log could not name them.
  - Recommendation: build the sign-in now, with only the device grant, leaving the app's authorisation code flow until the app. Make keys with commands instead of a page (R11), leaving that page for the dashboard intent. Manage-checks step 4 is replaced by this spec; R21 and R23 of that spec hold.
- **C2. What a stolen laptop can do.**
  - An administrator's sign-in on a laptop is a new, much easier target than a shell on a server. If it can make administrators or passkey links for them, a thief can stay after being found out. It could also switch alerts off, or lock out every other administrator.
  - Recommendation: R32 keeps those on the server. A passkey link from a laptop is only for someone with no passkey yet, as one for an existing colleague would let a thief plant a passkey on their account. A thief can still read everything, delete checks, add a non-administrator with a role in every project, and make a `manage` key in every project, and both of those survive the sign-in being ended. The log shows them. Removing the person is one command, and each key has to be revoked, which `allstill keys` lists with the sign-in they came through. Making a probe or giving one a new key stays on the server (R31), as a thief's own probe would receive its checks' whole URLs, report on them for as long as it runs, and could not even be deleted yet.
  - That does not stop a thief faking green. A stolen sign-in that may edit can read ping URLs and keep pinging them until each is replaced, leaving no trace in the log, as reading is not logged, and can point a site's check at an address of their own, which the log does show. So after a stolen sign-in with such rights, `docs/running.md` says to treat its projects' ping URLs as leaked, and to read the log for changed addresses. Keeping every ping URL from a laptop would defeat the success test, so this stays. A stricter option is a separate scope for an administrator's work, asked for at sign-in and lasting hours, at the cost of signing in again whenever a project is created. Worth it only once more than a few people administer an installation, so not now.
  - The cost: losing every device still takes a shell, for anyone, as does removing anyone's passkey. So the shell stays necessary in rare cases, against the intent's wish to stop connecting for daily work, which this still meets. Passkeys can be listed and removed on **Devices** with phase 1's recovery codes.
- **C3. Someone tricked into approving a code.**
  - The device grant's known weakness is an attacker starting a sign-in and getting someone to enter the code. A signed-in browser would make it a single click.
  - Recommendation: as designed: the code is typed and never in a link (no `verification_uri_complete`), the page says what asked, and a passkey is needed again, which is a pause and proves presence but does not check the story. No address can be shown, which weakens the page a little (see Design). The log line and **Devices** make it visible afterwards.
  - The limit on asking is one bucket for everyone, so someone hammering it can keep new `login`s waiting for as long as they keep at it. They gain nothing else, and existing sign-ins, keys and the dashboard go on. A proxy can limit by address, which Allstill cannot. Accepted until it happens.
- **C4. How long a sign-in lasts.**
  - Longer is easier, and worse when lost. A token that a cron job or an unattended machine depends on would fail at a bad moment, but that is what keys are for.
  - Recommendation: as a browser session, 30 days idle and a year at most, and no refresh token (see Design). The cost is that MCP clients, which ADR 0001 expects later, will want refresh tokens; those can be added without changing this.
- **C5. Where the credential lives.**
  - A file readable only by its owner can be read by anything running as that user. The macOS Keychain and its Linux counterparts protect better, but need platform code or cgo, which ADR 0001's single static binary resists, and fail on servers with no session.
  - Recommendation: a file, as `gh` falls back to and `ssh` keys are. `-read-only` and ending a sign-in are what limit the damage. A keychain can come later for macOS only.
- **C6. Removing a person, and their name in the log.**
  - The intent asks whether removing is part of this. Until now someone who left kept a passkey nobody could take; with a sign-in on their own machine they also keep a credential nobody can see. So it is needed, with R28 and R29.
  - The log's accepted spec (its C3) says that when removing people arrives, it replaces their name in old entries with "a former member", dropping the trigger that keeps entries from being edited. But "who removed this check, in the months before they left?" is exactly what the log is for, and a record that can be edited proves less.
  - Recommendation: change that decision. No entry is edited. At reading, administrators see the name, anyone else who would have read it reads "a former member", and entries go at the age limit as all do. Whoever runs the installation can then answer a question about what happened, and others no longer read a former colleague's name. A person asking to be forgotten before the age limit is a different change, not met here.
- **C7. Deleting a project.**
  - Deleting one with checks would throw away a lot in one step, from a laptop, and break every job pinging it.
  - Recommendation: only an empty one (R25). Its checks are moved or deleted first, one command each and each logged. ADR 0001 gives owners deleting, so they can do this. A project a probe serves cannot be deleted until probes can change, as C11 leaves them.
- **C8. Two sets of commands.**
  - `allstill admin set-check` and `allstill checks change` do the same. Pointing `admin` at the local server through the API would make one set, but the server needs a way in with nothing signed in, and it has to be the shell.
  - Recommendation: keep both for now. `admin` gets only what the API deliberately does not have (R33), and the guide leads with the new ones. Whether most of `admin` retires is a question for after real use.
- **C9. Changing the description before anyone uses it.**
  - Taking out `acknowledgeIncident`, `listIncidents` and the rest is not additive, and the menu bar app's planned client is generated from these. Keeping them promises what is not there.
  - Recommendation: take them out now, as `api/README.md` allows while there are no clients outside this repository, and note it there. After this ships, `/api/v1` only grows, as the command line is its first real client. So the shape of this document becomes hard to change, which is why it had an independent critical pass with this spec.
- **C10. This holds back from owners something ADR 0001 gives them.**
  - ADR 0001 gives owners "members, keys, deleting", and administrators people. But adding an existing person is more than a name in a list: a non-viewer starts getting alerts for open incidents at once, and an owner can turn `critical` on, so the owner of customer A could wake customer B's people or the operator's staff. It also tells an owner who else is on the installation.
  - Recommendation: an owner manages who is already in the project, and an administrator brings someone in. If a real owner often needs this, an invitation the other person accepts is the right answer. No ADR changes: this is a limit on one operation until that exists, noted in `docs/running.md`.
- **C11. Common things left out of this spec.**
  - The intent names acknowledging and snoozing an incident, and deleting a probe. Neither acknowledging nor snoozing exists anywhere yet: the store has columns and nothing else, and what they do to alerts needs deciding. A deleted probe has its own check and may serve several projects.
  - Recommendation: leave these for their own changes, so this one can finish. Reading incidents waits with them, since ADR 0001 wants them rendered per person and no store read takes an `Access` yet; `status` and `log` answer what is wrong and what happened. Changing a person's quiet hours or the digest's time is also left out.

## Open questions from the intent

- **Which commands come first?** Answered: reading everything and managing checks first, then members and keys, then projects and people. The order of work is for the plan.
- **What stays on the server only?** Answered in R32, R33 and C2.
- **Should an administrator's most dangerous actions need something more from a laptop?** Answered: those stay on the server instead (C2).
- **What becomes of `allstill admin`?** Answered: it stays, for the server (C8).
- **Should `allstill admin` refuse to create a new database by accident?** Answered: yes, R34, and it goes first.
- **Is a project's key enough for scripts, or does one working across projects need a workspace key?** Answered: project keys for now. A script of an administrator's working across projects uses a key per project. A workspace key would be the most dangerous credential on the installation, and no real case has needed it.
- **What does a key in the log's "by whom" mean when its maker has left?** Answered: R13. The key is the actor, with its own name, and R30 points it out when they leave.
- **Can someone sign in where there is no browser, using a phone?** Answered: yes, R10. A passkey is only ever used in the browser that approves, for the server's host name, wherever the command line runs.
- **How long does a sign-in last, and where is it kept?** Answered: R6 and R8; C4 and C5.
- **Can one person work with several installations?** Answered: yes, R8.
- **Where do people see and revoke what is signed in?** Answered: on **Devices** (R7). Recovery codes and managing passkeys stay with phase 1, not here, except `admin remove-passkey` on the server.
- **Are deleting a project and removing a person part of this?** Answered: yes, in a limited form (C6, C7).
- **What must the API description gain and lose?** Answered: in the Design's table and "What leaves the description".
- **How does the command line find the installation?** Answered: from sign-in, or `-server` and `ALLSTILL_SERVER` (R8).
- **How much can the API be asked before the server suffers?** Answered: R42 and R43, with limits to be measured against `make load`. Still open is what number to pick: start at 120 requests a minute per credential, and change it if real use says so.

## Out of scope

- **Inherited from the intent:** dashboard pages for managing projects, people and probes, except what **Devices** gains for R7; the menu bar app; an MCP tool; new alert channels; calendars and maintenance windows.
- **Also left out:** the authorisation code flow, refresh tokens and third-party clients; a workspace key; a setting for a trusted proxy; reading, acknowledging and snoozing incidents, and deleting a probe (C11); a run's output, `/events`; changing alert settings, quiet hours or the digest's time from the command line; the late margin, which is a separate small change; erasing a person from the log before the age limit.
