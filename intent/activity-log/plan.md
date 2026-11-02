<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: see what happened, when and by whom

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-02
Status: accepted
Approved: 2026-11-02, by the maintainer; risk: higher risk (a table that is hard to change once it holds a record, every writer in the store touched, and a lasting record about people)
Codebase: MartijnMul/allstill at 061ada2

Three steps, each a release candidate that runs on a real installation before the next. Recording comes first, as a day without it cannot be recovered (spec C10): after step 1 what happens to checks and what people do is kept, after step 2 who was reached is too, and step 3 makes it all readable.

## Files that change

Step 1, recording states and actions:
- `internal/store/migrations/0011_log.sql` (new): `log_entries` as the spec's design says, no foreign keys or checks; an index on project, on what it is about and its name (R28), on by what, on the person, on the incident, on the kind (R27), on the group and on when recorded; the trigger refusing updates; the log's start date under `installation`.
- `internal/store/queries/log.sql` (new), `internal/store/internal/db` (generated): writing entries, the counts R5 and R16 need, removing old ones.
- `internal/id/id.go` (changed): a prefix for the group that ties R18's line to what it opens.
- `internal/store/log.go` (new): `Actor` (a person, key, probe, job, command or Allstill, with whether a person is an administrator with no membership of the project); a builder that writes named fields and cannot fail, dropping details it cannot encode; the list of fields whose values may show (R12); the mapping of a reason to its fixed wording, which returns only its own text and a neutral line for anything it does not know (R1, C4); R16's ceiling.
- `internal/store/ping.go` (changed): `saveStatus` and `saveStatusAt` take a cause, and write the entry past the return for an unchanged state; `applyPing` takes who a ping is by, so a probe's own check is by that probe; `create=1` and a ping that resumes write theirs; `Recover` writes R18's line per project, with its group.
- `internal/store/incidents.go` (changed): `SetAlerting` and `SetAlerts` take who is acting.
- `internal/store/manage.go`, `checks.go`, `probe.go`, `workspace.go`, `signin.go`, `channels.go` (changed): an entry from each action, a required actor where there is no `Access`, one transaction around each action that lacks one (`CreateCheck`, `CreatePerson`, `CreateProject`, `RemoveMember`, `RekeyProbe`, `DeleteChannel`, `Confirm`), nothing written for an action that changed nothing (R8), the method for R9.
- `internal/store/workspace.go` (changed): `Access` carries the actor; `AccessOfPerson` sets it, and `AdminAccess` takes the command's name.
- `internal/store/tidy.go` (changed): removing old entries in batches, R16's closing line, `PRAGMA optimize`; `Retention` gains the log's age, with a floor of 30 days in `Check`.
- `internal/web/manage.go` (changed): adding a check with its alerting becomes one entry (R6), by `NewCheck` taking the alerting, which also makes it one transaction.
- `internal/web/signin.go` (changed): recording a passkey that looks copied (R9).
- `cmd/allstill/admin.go`, `checks.go`, `probe.go`, `compare.go`, `main.go` (changed): each command names itself; `-keep-log` and `ALLSTILL_KEEP_LOG`. An import gets its name through the `Access` it is given.
- `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md` (changed): that actions and state changes are now recorded, for how long, and that they hold people's names (R37), with no page yet.

Step 2, recording who was reached:
- `internal/store/incidents.go`, `queries/incidents.sql`, `internal/store/internal/db` and `internal/alert/sender.go` (changed): `Item` gains the check's id, name and project for lines with no incident, such as warnings, which `SilentChecks` and `WarningChecks` then return. A line with an incident finds its checks through `incident_checks` when it is written, not `Item`: that also serves messages already queued, a check that moved since, and an incident of several checks, so `DueRecipients` and `DigestIncidents` stay as they are. The additions to JSON leave messages already queued readable.
- `internal/store/channels.go` (changed): `Sent` takes the delivery, gets a transaction and writes R3 on a message's first success; `Shadowed` and `fallBack` write theirs (R4, R5), and R19's line per project.
- `internal/store/incidents.go` (changed): `Record` and `RecordDigest` write R4 for a shadow delivery, and R5 where a person has no channel. The sender records a failure with no channel only once a day and otherwise calls `Record` with no delivery, so it must say which incidents had no way to reach the person, or a second incident that day writes nothing.
- `internal/alert/push.go`, `mail.go`, `sender.go` (changed): passing the delivery as trimmed, not its id, and what `Record` needs for R5.
- `internal/store/log.go`, `queries/log.sql` (changed): writing these lines, and why a message failed in Allstill's own words, as an error from sending can hold a device's push address.
- `docs/running.md` (changed): that this is recorded too, and who may see it.

Step 3, the page:
- `internal/store/logread.go` (new) and `queries/log.sql` (changed): reading: one static query per way of narrowing, one per project a reader is in, merged in Go, with each entry judged by one function: whether the reader may see it, and what of it (R20 to R25). Because that judging is in Go, narrowing cannot reach past it, and the queries are all one equality on one index. A page reads at most 50,000 entries, and links on from where it stopped.
- `internal/store/migrations/0012_log_names.sql` (new): the index on names ignores case (R28), and an index on what concerns a person outside any project.
- `internal/web/log.go` (new): `GET /log`, its filters from the address and writing each kind as a sentence; a kind it does not know shows what it has.
- `internal/web/templates/log.html` (new), `templates/layout.html`, where the sidebar's fixed links and icons live, `check.html`, `active.html`, `parts.html`, `static/app.css` (changed): the page, the "History" section, states with their shapes, 375 px and dark mode.
- `internal/web/web.go`, `pages.go`, `active.go` (changed): the route, and the section on a check's page.
- `Makefile` (changed): the timed test at 3 million rows.
- `docs/running.md`, `internal/guide/guide.md`, `README.md` if it lists the pages, `AGENTS.md` (changed): the page, who sees what, and what the log cannot prove.

## Order of work

1. **Recording states and actions** (release candidate).
   1. The migration, tried on a copy of a real installation's database in a temporary volume. Measure its size and how long it takes, though it only adds a table.
   2. `log.go`, test-first: the builder, the list of R12, the reason mapping, with a table over every reason `internal/check`, `internal/active` and `internal/store` can write.
   3. State changes: `saveStatusAt` and its seven callers, then R16, R17 and R18.
   4. Actions on a check: `Access` gains the actor, then the methods one at a time.
   5. Actions around checks: the actor as a required argument, and the missing transactions.
   6. Tidying, `-keep-log` and the docs. Wait for CI before rolling out, because of the migration.
   7. On the real installation: make a change, pause, resume and sign in, wait for a state change, then read a copy from `allstill backup` locally with `sqlite3`, as there is no page yet. Each must be there as expected, without a secret.
2. **Recording who was reached** (release candidate).
   1. `Item`'s new fields, while messages already queued keep working.
   2. `Sent`, `Shadowed`, `fallBack`, `Record` and `RecordDigest`, one at a time, with spec cases 12 to 15 as tests.
   3. On the real installation: a test incident, and read the copy as in step 1.
3. **The page** (release candidate).
   1. Reading in the store, and who sees what, test-first against R20 to R25 and spec cases 3 to 6.
   2. The page, then the section on a check's page, seen in a browser at both widths and in dark mode, with the preview fixture.
   3. The test at 3 million rows.
   4. The docs and the guide.
   5. R39: someone who has not seen the page answers the two questions in under a minute.

## Tests that prove it

- **Every kind is written (R15):** a table test with one row per kind that performs the action through the store and asserts its entry, including who, and that no second entry appears. Actor as a required argument makes a new writer fail to compile without one.
- **Every way in:** the commands and dashboard routes each reach an entry, by name, in `cmd/allstill` and `internal/web` tests.
- **No secrets (R13):** a test creates a check with a token in its URL, location and settings, and a probe reason and JSON reason holding a marker string, runs each action and state change, and asserts the marker is in no row of the table.
- **No values beyond the list (R12):** a change to every field of the form, asserting which appear with values.
- **Nothing written for no change (R8):** one test per case the spec names.
- **Calm:** `testing/synctest` or set times for R16's ceiling and closing line; R17's rule per state pair, including unknown to down and down to unknown to up; R18 after a restart and a clock jump.
- **Messages (R3 to R5):** a table over what can happen to a message: sent at once, sent after retry, trimmed, dropped as no longer true, turned to shadow, falling back to email, no channel, no channel for a second incident the same day, always failing. Each asserts its entries, and that repeated tries write one line.
- **Who sees what (R20 to R25):** one table per row of the spec's table, per role, in the page's output as well as the store's result, with a viewer in one project and an editor in another, a non-member administrator, access removed, and each filter; a marker name must never appear where it is masked.
- **Speed (R32):** query plans in `make check` must show no scan or temporary sort for any page. The timed test at 3 million rows takes too long for every run, so it is a make target run before each release candidate of step 3, and its figures go in the commit. Measured on 2026-11-02 on a laptop, 375 bytes per entry with its indexes: the slowest page, one of an editor or viewer asking for a kind they have none of, took about 130 ms, as it reads its limit of 50,000 entries; every other page took under 15 ms. A first run found two pages over a second, reading a person's lines in other projects, which is why the query for what is their own has an index and a viewer's search for someone else stops at once.
- **The record (R33 to R35):** an update is refused; an action is rolled back with its entry when the entry's insert is made to fail through a test hook; tidy removes only what is past its age; `Check` refuses under 30 days.

## Risks

- **The riskiest step is 1.3,** as it changes the path every ping takes. An error there loses pings, which is the one thing the intent forbids. It writes only when a state changes, keeping the ordinary ping's cost as it is; R16's count runs on those writes only. Test: `make check` with the ping path's tests unchanged apart from the new argument of `saveStatus`, and a comparison of pings per second before and after with a benchmark. Measured on 2026-11-02 in six alternating rounds of 3000 pings on a laptop, against the commit before: pings that change no state took about 510 µs before and 580 µs after, and pings that each change the state about 875 µs and 840 µs. The rounds vary by a factor of two or more and overlap, so no difference shows above the noise.
- **Wrapping existing actions in transactions (1.5)** could change what a failure leaves behind. `create-user` makes a person and then a link in separate steps, and stays that way, with an entry for each.
- **`Sent` changes shape (2.2),** and a mistake there can send a message twice or lose track of one. Only the entry is new: when and how it sends do not change, and the sender's tests stay unchanged.
- **`Item` is stored JSON in `deliveries`,** so old rows have no new fields at the moment of the upgrade. Spec design: such a message writes one line for the person with no checks.
- **The table is hard to change once it holds a record:** adding a column stays possible (the migration's test shows the trigger allows it), but what was not written cannot be recovered. So step 1 writes every column from the start, also those only step 3 reads.
- **Secrets through names:** R13 cannot stop someone naming a check after a secret. `docs/running.md` says so.
- **What can break for others:** a person added today can read back up to 400 days once step 3 is out, including who was reached before they joined. That is what R20 says, and it goes in the docs.

## Options not taken

- **A separate transaction for entries**, beside the ping's: a crash could leave a gap and every state change would write twice (spec C9).
- **A savepoint around the entry's insert:** it catches only a bug, sqlc cannot express one, and the transaction would have to be passed through every caller (spec C9).
- **Rewriting `events` to carry this:** it has a different job, the stream's position, and must not have R16's gaps.
- **Its own package:** entries are written in the store's transactions by the store's methods, so they sit in `internal/store`. The page's sentences are in `internal/web`.
- **A command to read the log, as a way to check step 1:** it would be export under another name, which the spec left out. A local copy from `allstill backup` serves.
- **Building the page first,** as it shows progress sooner: every day until recording starts is lost for good (spec C10).
