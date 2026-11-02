<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: see what happened, when and by whom

Intent: [intent.md](intent.md)
Status: accepted (2026-11-02, with the recommendations of C1 to C13)
Written: 2026-11-02, by Claude for the maintainer; revised the same day after an independent review against the code, with queries measured on synthetic data
Codebase: MartijnMul/allstill at 02d79a3
Policies in force: no skills yet; ADR 0001 to 0004, AGENTS.md, REVIEW.md, docs/brand, the specs of intent/alerts, intent/http-checks and intent/manage-checks, the built-in guide (`internal/guide/guide.md`), and needs from real setups (kept privately)
Prompt: the requirements-and-design prompt of the AI-native SDLC playbook, run on intent.md

## Summary

One new page, **Log**, lists what happened, newest first: what happened to checks, who was told, and what people, jobs, probes and commands did. It can be narrowed to one check, project, person or kind of thing, and a check's page shows its own recent lines.

Behind it is one new table that only ever gets new rows. Allstill writes a row at the moment something happens, in the same transaction that makes it true. Today's tables cannot answer the question later: they hold a check's current state and not its past states, one line per person on what they were last told, and nothing on who did what.

It flags what this newly shows to whom: customers, colleagues and staff (C1, C2, C3). It also flags what the log can and cannot prove (C5), a limit on how much one check can write (C7), and where the record gives way to the intent's rule that it must not cost a ping (C9). It recommends that recording starts before the page is finished, because a day without recording cannot be recovered (C10).

## Requirements

**What is written down**
- R1. Every change in a check's state is an entry: from which state to which, with a reason from a fixed list of Allstill's own wording. A reason holding a value, such as what a JSON field said, is written as its kind: "a JSON field was not as expected". (Proposed outcome; Constraints; C4)
- R2. A run or ping that changes no state is not an entry; runs stay on the check's page. (Open questions; Constraints: calm)
- R3. A message reaching a person is an entry per check it mentions: who, which check, what it said about it (down, recovered, still open, a warning and so on), at once or in the digest, and the kind of channel that took it. It is written when the first delivery succeeds, holding only what the message still held. (Proposed outcome)
- R4. In shadow mode the entry of R3 says the message was recorded and not sent. (Proposed outcome)
- R5. A person who could not be reached is an entry, once per person and incident until a message reaches them, also when they have no channel at all. A message dropped because it was no longer true is not an entry. (Proposed outcome)
- R6. Every action on a check is an entry: added, changed, paused, resumed, moved, deleted, ping URL replaced, alerting changed. An action that also changes the state is one entry holding both, and one form is one entry. (Proposed outcome)
- R7. Every action around checks is an entry: a project made or renamed; a person added; a role given, changed or taken away; a passkey link made; a passkey added; a sign-in; a device or email address confirmed, handed on or removed, by its owner or because it stopped working; a probe made or given a new key; alerts turned on or back to shadow mode. (Proposed outcome)
- R8. An action that changed nothing writes nothing: resuming what is not paused, saving a form unchanged, giving the role someone already has. (Constraints: calm)
- R9. A failed sign-in is not an entry, as anyone on the internet can cause one; it stays in the server's log. A passkey whose sign counter went backwards is an entry, as that takes a valid passkey. (Open questions)
- R10. Each entry says by what: a person by name, a key by name, a probe by name, a job, a command on the server with the command's name, or Allstill. A probe's own check reporting in is by that probe. (Proposed outcome; C6)
- R11. An entry keeps its own copy of the names it mentions, as they were then, so it stays readable after a check, project or probe has gone or been renamed. (Proposed outcome)
- R12. A change says which fields changed, and shows before and after only for fields on a short list of values the check's page already shows, such as name, schedule, grace, time zone and tags. A field not on the list is named without its value, so a new setting starts out hidden. (Constraints; Open questions)
- R13. Allstill itself writes no URL, location, header, token, certificate, ping body, job output, email address, IP address or user agent into an entry. Names are copied as typed, so a check named after a secret keeps it until its entry's age limit. (Constraints; C3, C4)
- R14. An entry holds when it happened and when it was recorded. The page lists entries in the order they were recorded, and a line that arrived more than a minute late says so, as a probe's run can be hours old. (Constraints)
- R15. Allstill writes an entry for every action from every way in: the dashboard, the commands and later the API. Every writer takes who is acting as a required argument, and a test per kind of entry shows it is written. (Constraints)
- R16. A check whose state changes more than 30 times in an hour writes one line saying it is changing too often, and no more state entries until it has been steady for an hour, when one line says what it did in that time. Messages and actions are always written. (Constraints: calm; C7)

**Staying calm**
- R17. Going into `unknown`, and coming out of it with nothing wrong, is written, but shows only when narrowed to that check. Allstill not being able to judge is not news about the check, so a restart, a silent probe or a lost network does not bury the page. Coming out of it to `down`, or to up with an incident open, shows as usual, so a real outage or recovery is never hidden. (Constraints: calm; C8)
- R17a. A check going late, or back to up from late, is written but shows only when asked for, also when narrowed to that check, and does not count towards R16. A job that pings a moment after its time does this on every run, which on a real installation made up 98 of the first 104 lines. Going on to down, or a recovery, shows as usual. A check that jobs ping and that does only this, with no incident for an hour, counts as steady for R16. (Constraints: calm; C7)
- R18. A restart or clock jump that left checks unjudged writes one line per project, with the count and the times: "Nothing was judged between 03:10 and 03:40: 12 checks." (Constraints: calm)
- R19. A message mentioning several checks shows as one line per project, which opens to its checks: "Ada's digest covered 4 checks." (Constraints: calm)

**Who sees what**
- R20. Entries are shown by this table, per project, judged by the reader's access today. Someone added now can read back; someone removed no longer can. (Constraints; C1, C2, C3)

| What | Viewer | Editor | Owner | Administrator |
|---|---|---|---|---|
| A check's state changes, with reasons | Yes | Yes | Yes | Yes |
| Actions on a check, and values on the list of R12 | Yes, by "the team" | Yes | Yes | Yes |
| A project renamed | Yes | Yes | Yes | Yes |
| Who was reached | No | Yes | Yes | Yes |
| Why a message failed | No | No | No | Yes |
| Roles in the project | No | No | Yes | Yes |
| Their own sign-ins, passkeys and devices | Yes | Yes | Yes | Yes |
| Others' sign-ins, passkeys and devices; people, new projects, probes, alert mode | No | No | No | Yes |

- R21. To a viewer, a person, a key and a command all read "the team"; a job, a probe and Allstill read as they are. (Constraints; C1)
- R22. To an editor or owner, an administrator who was not a member of the project when the entry was written reads "an administrator", with no name, both as who acted and as who was reached. (Affected users: one operator running checks for many parties; C2)
- R23. A person can be narrowed to only by following a name on a line. There is no list of people. Narrowing to a person leaves out every project where the reader sees "the team", and every name R22 hides. (Constraints; C1)
- R24. Narrowing to a check or project someone cannot see gives the same empty page as one that does not exist. (Constraints)
- R25. An entry stays with the project it happened in. A move writes one entry in each project, naming the other only to someone who can see it. The one in the new project says what state the check arrived in and whether an incident was open. A message about a check that moved after it was queued lands in the check's project, and someone who is no member there is not named to its people. (Open questions; C11)

**The page**
- R26. The sidebar has a link to the log for everyone signed in. The page lists entries newest first with their time, what happened and by what, a link to a check that still exists and is visible, and a way to older entries. (Proposed outcome)
- R27. It can be narrowed by check, project and kind: what happened, what was done, who was reached, sign-ins. A choice lives in the address, so it can be shared, and works without JavaScript. (Proposed outcome; Constraints)
- R28. A deleted check can be found by its name, along with any later check of the same name. (Proposed outcome)
- R29. A check's page shows its last few entries, with a link to its full log. (Proposed outcome)
- R30. The page says where the log starts and how long entries are kept, and never suggests that nothing happened before that. (Constraints)
- R31. Times are in the person's own time zone with the date. The page follows docs/brand, works at 375 px wide and in dark mode, and states keep their shapes. It does not refresh itself, so a line does not move while being read. (Constraints)
- R32. Any page of the log, however narrowed, reads in well under a second with 3 million entries, shown by a test against a table of that size. (Constraints)

**Keeping the record**
- R33. An entry is written in the transaction that makes its thing true, so an action is not done unless it is recorded. Nothing in the table can refuse a row, and building a row cannot fail, so a state change or message fails over its entry only when the database itself is failing, when it would fail anyway. (Constraints; C9)
- R34. No page or command edits or removes an entry, and the database refuses an update to one. Only the age limit of R35 removes entries. (Open questions; C5)
- R35. Entries are kept for 400 days by default, set with `-keep-log` (`ALLSTILL_KEEP_LOG`), at least 30, and removed in batches so that pings do not wait. (Open questions; C7)
- R36. Deleting a check leaves its entries as they are. (Proposed outcome)
- R37. `docs/running.md` says what the log records, who sees it, how long it stays, that it holds names of people, and what it cannot prove. The guide says the same in short. (Constraints; C3, C5)
- R38. The line each dashboard change writes to the server's log stays as it is (manage-checks R29). (Out of scope in the intent)
- R39. Someone without a shell answers "who removed this check?" and "did anyone touch this check before it went down?" in under a minute, tried with a person who has not seen the page. (Proposed outcome)

## Design

**One table that holds facts.** A new table, for instance `log_entries`, with a number that only goes up, so pages can be read by "older than this one" however long the log gets:

- when it happened and when it was recorded; the workspace; the project and its name then, empty for what concerns the whole installation;
- the kind, as a stable word such as `check.state` or `check.deleted`;
- what it is about: its kind, id and name then;
- by what: the kind (`person`, `key`, `probe`, `job`, `command`, `allstill`), its id and its name then;
- the person it concerns, such as who was reached or whose role changed, and the incident it belongs to, each with an index, as neither can be added afterwards;
- for who acted and for the person it concerns, whether they were an administrator with no membership of the project then, which R22 needs and `memberships`, which keeps no history, cannot say later;
- whether it is hidden from the default list (R17, R19), and a group id linking a line of R18 or R19 to what it opens;
- details as JSON, written only from named fields, never by copying settings.

It has no foreign keys and no checks, as `incident_checks` has no foreign key to `checks`, so it outlives what it mentions and cannot refuse a row. It stores facts and not sentences: the page writes the sentence when it shows an entry, per reader, so wording can improve and old entries benefit, and R20 cannot be defeated by text stored too early. An entry of a kind the page does not know yet shows as what it has. A trigger refuses updates (R34), which keeps a mistake in the code from rewriting history. It does not stop someone with the database in hand (C5), nor `DELETE`, which tidying needs.

**Why not read it from what exists.** `runs` are cleaned up by count and go with a deleted check. `checks` holds only the current state. `recipients` holds only what a person was last told, and `deliveries` cascades with its person. `events` is the live stream's position: seven days, no person, no details, and it stays as it is. Merging all of those at read time would still lack who did what. Past incidents therefore get their answer through the log, without a page of their own.

**Where entries are written.** All in the transaction that makes the thing true:

- *State changes:* `saveStatusAt` in the store, past its return for an unchanged state, as every state change already passes through it. Its callers say the cause: a ping is by a job, a passed deadline or an attempt by Allstill, a probe's run and a probe's own check reporting in by that probe. A probe's own check going down because the probe fell silent is by Allstill. An action that passes through it (pausing, resuming, changing a schedule) says the action writes the entry, so there is one (R6). Whether R17 hides an entry follows from its new state and whether an incident is open, which `incidentStep` reads anyway.
- *What restarts and steady checks write:* R18 in `Recover`, which has both times. R16's closing line has no event to hang on, as nothing fires when a check stays steady, so the regular pass that tidies writes it.
- *Messages:* not where `Told` is recorded, which happens when a message is queued and can still be taken back, turned to shadow or trimmed. R3 is written at the first `Sent` of a `message_id`, which then needs the delivery and not only its id, and gets a transaction. A message's `Item` holds only an incident, a kind and a title of free text. A line with an incident finds its checks through `incident_checks`; warnings have no incident, so `Item` gains the check's id, name and project for those. The title is not copied, as it can hold a reason (R1). A line queued before the upgrade with neither is one line for the person, with no check. R4 is written at `Shadowed`, and at `Record` and `RecordDigest` for a shadow delivery. R5 is written where `fallBack` runs out of ways and where a person has no channel, both of which try again every ten minutes, so a person's entry is written once, until a message reaches them.
- *Actions:* the store's methods. `Access` gains who is acting, set where it is made: a person from `AccessOfPerson`, a command from `AdminAccess` with the command's name. The actions that take no `Access` today (`MoveCheck`, `SetAlerting`, `CreateProject`, `RenameProject`, `SetMember`, `RemoveMember`, `CreateMember`, `CreatePerson`, `CreatePasskeyLink`, `SetAlerts` and the channel and sign-in methods) take who is acting as an argument, so the compiler finds every caller. Several of them run without a transaction today (`CreateCheck`, `CreatePerson`, `CreateProject`, `RemoveMember`, `RekeyProbe`, `DeleteChannel`, `Confirm`), and get one.
- *A ping that acts:* a ping with `create=1` that makes a check is an entry by a job, and so is a ping that resumes a paused check. They are written in the ping's transaction.
- *The backwards sign counter (R9):* a new store method, as today the web handler only writes a log line and refuses.

**Reading.** One query per project the reader can see and one for their own entries, each taking the newest rows by number, merged in Go, as `Checks` already works per project. An administrator's query has no project condition. Which kinds a role sees comes from one tested function, so narrowing can never reach past it. Each way of narrowing is its own static query with its own index, since sqlc's queries are static and a condition like `(:check = '' OR about = :check)` was measured to scan. What a reader may not see of an entry (R21, R22) is dropped before the template gets it, tested per role and kind. The store runs `PRAGMA optimize` on a schedule, as it keeps no statistics today.

**The page.** `GET /log`, server-rendered, not `Live`. Filters are a plain form using GET. A line reads as a sentence with the check's state shape first:

- `09:12` `backup.sh` went down: no ping by 09:00. By Allstill.
- `09:12` Ada was reached at once by push.
- `09:05` Ada changed `backup.sh`: schedule from every 5 minutes to every hour.
- `08:40` `web-1` was paused by the team. (as a viewer sees it)
- `03:40` Nothing was judged between 03:10 and 03:40: 12 checks.

The check's page gets a short "History" section reading the same way. **Messages** stays as it is, as it answers a different question: what was sent to me, and did it arrive.

**Where the log starts.** The migration stores the date under `installation`, as the oldest row cannot say it after tidying (R30).

**Tidying.** `Tidy` removes entries past their age at the end of a round, in batches, using an index on when they were recorded, without which the search for what to delete was measured to scan inside the write lock.

## Cases played through

1. **Who removed this check?** Bob misses `backup.sh`, opens the log, types its name, and reads "Ada deleted `backup.sh`" at 14:02 on Tuesday. The check, its runs and its pings have gone; the line has not. If a job with `create=1` has since made a new check of that name, its lines come up too.
2. **Did anyone touch it first?** `web-1` went down at 09:12. Its history shows the schedule changed at 09:05, by whom, and from what to what.
3. **An outsider.** Carol is a viewer of a customer's project. She sees that `site` went down at 09:12 and came back at 09:30, and that the team paused it at 03:00. She sees no names, nobody's messages, and nothing of any other project. The times still show when the team works. Typing another project's id into the address gives an empty page.
4. **One operator, two customers.** Dan administers both and sees all of it. Customer A's owner sees only A, and reads "an administrator paused `site`" for Dan's action. A shared probe's new key is an entry for administrators only, and neither customer learns the other exists.
5. **A person in two projects.** Eve is an editor in A and a viewer in B. She reads Ada's name in A and "the team" in B. Following Ada's name shows only A's lines, so B's cannot be pinned on Ada by comparing them.
6. **Moved to another customer.** A check moves from A to B while down. A's people read "moved to another project". B's read "arrived from another project, down, with an incident open", so the recovery that follows makes sense, and nothing from before. The check's runs did go with it (manage-checks R13). Someone in both sees both.
7. **A command on the server.** `allstill admin delete-check` runs over a shell. The entry reads "by a command on the server: delete-check", which says how but not who (C6). A viewer reads "the team".
8. **Importing 200 checks.** 200 lines, one per check, by the command. It happens once, and the kind filter gets past them.
9. **The server was away.** It restarts after half an hour and 12 checks in a project were left unjudged. That project's people see one line. The 12 going unknown and coming back fine show only on each check's own log; one that really went down meanwhile shows as usual. The same holds for a clock jump, which is why the line does not say the server was down.
10. **A probe goes silent.** Its own check goes down, which is a normal line with a message behind it. The checks it runs go unknown one by one, which do not show on the page until narrowed to one of them.
11. **A flapping check.** A broken script flips it every minute. Past 30 changes in an hour there is one line saying so, and when it settles, one line saying what it did. Whatever people did in between still shows.
12. **An incident closes before its message goes.** Ada is queued a message at 09:12, the push service answers 503, and someone pauses the check at 09:13, which closes the incident at once. The message is dropped as no longer true. No line says Ada was reached, because she was not.
13. **A device that keeps failing.** Ada's only device answers errors every time. One line says she could not be reached for that incident, not six an hour.
14. **Alerts in shadow mode.** The line says the message was recorded and not sent, so nobody reads it as Ada having been told.
15. **A digest.** Ada's morning digest covers four checks, two of them warnings. One line for that project, which opens to the four. Each also shows on its check's log.
16. **A late report.** A probe that was cut off sends a run from 08:50 at 09:10. It lands above the 09:05 schedule change and says "reported at 09:10, happened at 08:50", so nobody reads the order as the cause.
17. **Restoring a backup.** The log goes back with the database; what happened since is gone from it, and the page has no way to tell (C5).
18. **The disk is full.** Pings and actions already fail then, so nothing new goes wrong.

## Areas of concern

- **C1. What viewers learn.**
  - Today a viewer sees a check's state and runs, and nothing of who works on it. Outsiders reading "Ada paused this at 03:00" would learn the operator's staff. The times alone still show working hours.
  - Recommendation: as in R20 and R21, viewers see what happened and what was done, by "the team". Values on the list of R12 are not hidden, as viewers already read them on the check's page. The alternative, viewers seeing state changes only, is simpler, but then a paused check has no explanation for them.
- **C2. Staff shown to customers, and who is on call.**
  - Who was reached is new to everyone, and shows who is on call. Editors at a customer are not staff: an administrator who is no member follows every project and acts in it, so without a mask each customer's editors read the operator's names. Nothing in the dashboard lists people today, so names among members are new too.
  - Recommendation: names among a project's members are fine; an administrator who is no member reads "an administrator" (R22). Who was reached shows the name and kind of channel only, never the address, device or why the digest was chosen. Viewers are not told about checks, so that row is simply no. Why a message failed stays with administrators, as it does today.
- **C3. A record about people.**
  - The log is a lasting record of what named people did and when they signed in, which an administrator can read. For a company in the EU that is personal data, and in some places monitoring of staff, and whoever runs the installation answers for it. No command removes a person today, so nothing asks for removal yet, but someone can ask.
  - Recommendation: no IP address or user agent (R13), sign-ins for administrators and the person only, the age limit of R35, and plain words in `docs/running.md`. When removing people arrives, it replaces their name in old entries with "a former member", as one narrow exception to R34, with a migration dropping the trigger then. Not built now.
- **C4. Reasons, shortened on purpose.**
  - A reason can hold what a JSON field said and what was expected, or up to 500 characters of a probe's own text. A check's page and its recent attempts show one only for as long as that run is kept, and deleting a check clears its settings at once to get rid of any token. A copy in the log would keep it for 400 days, readable by viewers.
  - Recommendation: the log keeps the kind of reason, and the check's page and runs keep the detail for as long as they exist. The cost: an old line says "a JSON field was not as expected" without saying what it said.
- **C5. What the log proves.**
  - It stops mistakes and people in the dashboard. It does not stop someone with the database, and restoring a backup rewinds it unnoticed. Signing or chaining entries would add real machinery for a need nobody has shown yet.
  - Recommendation: say so plainly in `docs/running.md` (R37), and do not call it tamper-proof anywhere. Sending entries off the server is what would make it stronger, later.
- **C6. Who is a command on the server?**
  - A shell has no person in the dashboard's sense, and inside a container the user is usually root. A name typed with the command would be unverified text in a record meant to be trusted.
  - Recommendation: "a command on the server" with the command's name, and no more. With several people holding a shell, it does not tell them apart; that belongs to how the server is logged in to.
- **C7. How much, and a limit on one check.**
  - Runs and pings, the history a job writes most of, are limited per check; this one is limited by age. Each alternation of a success and a fail flips the state, and pings are allowed one a second, so one holder of a ping URL or one broken script could write 31 million rows a year, about 10 GB.
  - Normal use is far below that. An estimate, not a measurement: a thousand checks with a few state changes a week, five people following each, and messages make around a million rows a year, at about 375 bytes each including indexes as measured, so about 375 MB. A check flapping through a whole year under R16 writes at most about 260,000 rows.
  - Recommendation: one age limit of 400 days for everything, so a year can be compared with the one before, and the ceiling of R16. The cost of the ceiling is a gap, which its line states. Keeping `late` follows the intent. It proved noisy on the first real installation, so it left the default view (R17a), and is still stored. Those lines have no ceiling of their own: an always-late job writes two a run, which is about 200,000 rows and 80 MB a year at every 5 minutes, and at most a million rows and 400 MB at every minute.
- **C8. Hiding `unknown`.**
  - An early draft folded each cause into one line, but a silent probe's checks go unknown one at a time at their own deadlines and the store does not know the cause, and after a restart the checks come back singly, which is the part that buries the page.
  - Recommendation: hide entries into `unknown`, and out of it with nothing wrong, from the default list (R17), with R18's line to explain. The cost: someone not narrowed to a check does not see a stretch of a check's unknown unless a restart's line explains it, though a probe's own check going down does show.
- **C9. Where the record gives way to the intent.**
  - The intent says a problem with the record must not lose a ping. Writing an entry beside the ping, in its own transaction, would keep that literally, but a crash between the two leaves a gap, and every state change costs a second write. A savepoint inside the transaction would catch only a bug, as an insert into a table with no constraints fails only on a full disk, an I/O error or corruption, and sqlc cannot express one.
  - Recommendation: one transaction (R33), with a table and a builder that cannot refuse. A ping is then lost over its entry only when the database is failing, when it would be lost anyway, or through a bug in the insert itself, which tests cover. This is narrower than the intent's wording, so it is the maintainer's to accept.
- **C10. Recording first.**
  - Every day without recording is lost for good, and the page takes longer than the recording. The columns of the design cover what the reviews found could not be added later (the person, the incident, both times, and who was an administrator with no membership).
  - Recommendation: release the recording as soon as it is sound, the page after it. This is for the plan to order; it is flagged here because it decides what the first release candidate holds.
- **C11. A moved check's history.**
  - Runs and output follow a move (manage-checks C8), but entries do not, so the new project's check page shows runs older than its first log line. That looks inconsistent, but moving entries would show B's people what A's people did and whom A told, and take it away from A.
  - Recommendation: as in R25.
- **C12. No backfill.**
  - Past incidents could be copied in as entries by the migration, with times and names, so the log does not start empty. But they would be partial, with no state changes in between and no reliable record of who was told, and would read as more complete than they are.
  - Recommendation: start empty and say so (R30).
- **C13. The word.**
  - "Log" is short and is what the maintainer called it, though it is also what server people call their server's log. "Activity" and "History" each cover only half.
  - Recommendation: "Log" in the sidebar, and "History" for the section on a check's page.

## Open questions from the intent

- **Is every run an entry?** Answered: no, only what changes a state (R2).
- **How long is it kept?** Answered: 400 days by default, set per installation; one limit for everything, so actions do not outlast states, and both outlast runs that are cleaned up by count (R35, C7).
- **May anyone remove or edit an entry?** Answered: no, apart from the age limit (R34), with one narrow exception when removing people arrives (C3). No command deletes a project today, and entries would outlast it if one came.
- **Who sees what?** Answered: the table in R20, with R21 to R23 (C1, C2, C3).
- **Who is "a command on the server"?** Answered: just that, with the command's name (C6).
- **What do people see after a move?** Answered: each project keeps what happened in it (R25, C11).
- **A failed sign-in?** Answered: not an entry; the backwards sign counter is (R9).
- **What becomes of Messages?** Answered: it stays, as a person's own view. The log adds who was reached.
- **Export, or sending it elsewhere?** Still open, and left out for now. Nobody has needed it yet, and the stable kinds and JSON details keep the door open for the API and a command.
- **Should changes show what they were?** Answered: yes for values on a short list the check's page already shows (R12). This goes further than the server's log line, which stays without values (R38).

## Out of scope

- **Inherited from the intent:** the server's own diagnostic log; undoing a change from the screen; being told about an entry.
- **Also left out:** export and forwarding; the API for reading the log; failed sign-ins; signing or chaining entries; a backfill; removing a person's name from old entries; a page for a single incident; recording why someone was not told; sign-outs and sessions that end; test messages; `allstill backup`, which changes nothing and may run every night; changes to server settings such as `-keep-log` or `-allow-private`; folding an import or `move-checks`, which run once and each check in its own transaction.
