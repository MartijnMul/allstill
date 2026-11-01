<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: add and change checks without a shell on the server

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-11-01
Status: accepted
Approved: 2026-11-01, by the maintainer; risk: higher risk (the state machine shared by every check, removing data, and rights opened to more people)
Codebase: MartijnMul/allstill at 867d058

Four steps, each a release candidate that runs on the real installation before the next, and each worth having on its own: after step 1 people can be limited to projects; step 2 changes nothing anyone sees but what is safe; step 3 is what the intent asks for; step 4 waits until something is about to use it (C1).

## Files that change

Step 1, people with a role, and the bug that cannot wait (it also keeps delivery errors, a way to learn what an address resolves to, from those who are not administrators):
- `internal/outbound/checks.go` (changed): no `Referer` when following a redirect (R28).
- `internal/store/workspace.go` (changed): adding a person limited to projects, changing a role.
- `cmd/allstill/admin.go` (changed): `create-user -project P -role R` without administrator rights, `set-member`.
- `docs/running.md`, `internal/guide/guide.md`, `AGENTS.md` (changed): that people can be limited, and to what.

Step 2, the rules, with no new page:
- `internal/store/migrations/0010_ever_passed.sql` (new): when a check last passed, so "has never passed" survives retention and a resume; filled from existing runs and states.
- `internal/check/check.go` (changed): a function for a change of schedule or grace, for every state of the spec's table; a resume; a failure on a check that has never passed opens nothing (R35).
- `internal/store/checks.go`, `probe.go`, `incidents.go`, `tidy.go`, `queries/` (changed): changing a ping check, pausing, resuming, deleting, replacing a ping URL, a move and `SetAlerting` with a caller's access, each bumping `version`; refusing what R19 protects; tidy removing a deleted check's pings, runs and row; a check's limits (R30); `sameCertificate`'s rule scoped to a project; rules moved down from `cmd` (the address check, TLS off clearing certificate settings).
- `internal/active/loop.go` (changed): the share per project (R30); `version` on a check, so an attempt from before a change is dropped (R11).
- `internal/alert/sender.go`, `decide.go`, `mail.go`, `push.go` (changed): the digest says a check was closed by deleting or pausing (R17), and lists paused ones (R18).
- `cmd/allstill/checks.go` (changed): the commands on the same rules, and changing a ping check's schedule, which has no command today.

Step 3, the forms:
- `internal/web/manage.go` (new): add, edit, pause, resume, move, delete, replace a ping URL; refusals by field; the try with its short-lived result; the log line (R29).
- `internal/web/templates/` (new `check-new.html`, `check-edit.html`, `check-confirm.html`; changed `overview.html`, `check.html`, `active.html`, `layout.html`, `parts.html`) and `static/app.css`.
- `internal/web/web.go`, `pages.go` (changed): routes; `ErrForbidden` and `InvalidError` as refusals; empty overview by role; "not checked yet"; for a viewer, who cannot act and gets no alerts, "5 checks need attention" rather than "need you".
- `docs/running.md`, `internal/guide/guide.md`, `README.md`, `AGENTS.md` (changed): what an editor can reach and read (C2), and "Not here yet".

Step 4, the API, when something is about to use it:
- `internal/store/migrations/0011_api_keys.sql` (new), `internal/store/keys.go` (new): keys as an `Access` holding one project.
- `internal/web/api.go` (new), `internal/web/templates/project.html` (new): the handlers, and a small project page for owners.
- `internal/api/` (changed): the `Checks` and `Projects` tags, with authentication per operation.
- `api/openapi.yaml` (changed, its next version), `api/README.md` (changed): `Idempotency-Key` and the fields of features not built leave, noted as allowed while there are no clients.

## Order of work

1. **People with a role** (release candidate).
   1. The `Referer` fix, test-first with a chain of real redirects.
   2. The commands, with a viewer and an editor tried on the real dashboard: what they see, and what they are refused.
2. **The rules** (release candidate).
   1. The state machine, test-first: every row of the spec's table, cases 3 to 6, and every existing test unchanged.
   2. Migration 0010, tried first on a copy of the real database.
   3. The store's operations, each with its role test, and R19's refusals.
   4. Deleting: settings cleared, tidy removing the rest in batches.
   5. The share per project, with `testing/synctest` (case 10), and `make load` showing no loss.
   6. The digest's two new lines.
3. **The forms** (release candidate).
   1. Add and edit, for each kind, with refusals and the out-of-date check.
   2. The try, within its limits, and the result it shows.
   3. Pause, resume, move, delete and replacing a URL, each with its page.
   4. Every page at 375 and 1440 px, light and dark, and by role, in the local preview.
   5. On the real installation: someone who has not seen the dashboard gets a passkey link as an editor, and does R33 unaided, while it is timed and watched.
4. **The API** (release candidate), when the menu bar app or an agent is about to use it.
   1. Keys, the project page and the description.
   2. Generated handlers, on the same store operations.

## Risks

- **The state machine (step 2)** is what every check depends on, and the never-passed rule changes when an incident opens. Every existing state test runs unchanged. A check that has passed behaves exactly as before, shown by a test for each state, and migration 0010 marks every check with any passed run as having passed, so nothing on a running installation goes quiet.
- **A real outage that stays quiet (step 2).** The cost accepted in C5: a site down when it is added does not alert until it has been up once. Its page and the digest say so, and a test holds that.
- **Removing data for good (step 2).** Tidy deleting runs and rows is new. It touches only checks with `deleted_at` set, in batches, a probe's own check cannot get there, and a test deletes one check among others and counts what is left.
- **Rights opened to more people (steps 1 and 3).** No editor has been able to reach this before. Each store operation gets a test for each role and for another project; a security pass goes through the forms before the release candidate, including by hand-made posts as a viewer.
- **Starving other projects (step 2).** The share changes the loop's queue. Load tests before and after.
- **A half-filled form lost (step 3).** Edit pages are not live, and a refused form renders again with what was typed.

## Options not taken

- **Everything in one release.** Rules, forms and API each have their own way to go wrong, and the rules need a real installation before people can reach them through a form.
- **Forms before the rules.** The forms would work, and editing a check would alert people.
- **JavaScript for the forms.** The dashboard stays server-rendered and works without it (R26); a little may prefill a time zone.

## Proof

- Step 1: a redirect test sees no `Referer`; a person made with `-role viewer` sees one project on the real dashboard and is refused a change.
- Step 2: `go test ./internal/check` with every row of the table; every earlier test unchanged; migration 0010 on a copy of the real database, with `foreign_key_check` empty and every check that has passed marked so; a deleted check's pings, runs and row gone after tidy with others untouched; case 10 under `testing/synctest`; `make load` no worse.
- Step 3: `internal/web` tests per role and per form, including R8's unknown settings and R9's out-of-date refusal; screenshots at both widths; the person of R33 succeeding unaided, in a few minutes.
- Step 4: the API's lint and `generated code is current`; a key reaching its project alone, refused on the probe's endpoints, and 401 when revoked.
- Every step: `make check` clean, and the reviewer agent on the staged diff before each commit.

## Departures

- Step 2 keeps `SetActiveCheck` and `SetActiveCheckOn` for existing callers, on the new `UpdateCheck`. Before, any `set-check` of a check Allstill runs moved its deadline; now a change of schedule, of who runs it or of what it fetches does, and a rename does not (R10).
- R11 uses a revision named after a check's settings, not its `version`: a rename must not drop the attempt in flight, which could make a check briefly unknown. It travels with a probe's checks and runs too (API 0.9.0), as a probe reads its checks every 30 seconds. A change of settings also gives a check Allstill runs until its next attempt.
- The spec says pausing, resuming and replacing a ping URL bump `version`. They do not: it names the settings, and a form need not be out of date for them.
- The address check moved to `outbound` rather than the store, as a shared function that R24 allows: it needs the server's ranges and the network.
- R18, decided by the maintainer on 2026-11-01: a digest going out anyway lists paused checks, except those that never alert, but a pause alone sends no message. As first written, one paused check meant a message every day, forever. A forgotten pause on a quiet installation shows only in the dashboard.
- Fixing an unreadable schedule of a check with an open incident makes it down, not up: it was down, and no ping has said otherwise.
- Tests of checks Allstill runs that went down from new now pass once first, for C5. "Every earlier test unchanged" in the Proof holds otherwise.
- The scheduler now looks at deadlines on its heartbeat too, as a command run beside the server can bring one closer and cannot wake it.
- Step 2 adds `pause-check`, `resume-check` and `delete-check`, which the plan left implicit, so the rules can be used on a real installation before the forms exist.
- Step 3 keeps the checks of R27 in the web layer, and the attempts' reason now says the same of the server's own address as of a private one: reasons are shown to people. A person who adds a check is told nothing of the same certificate watched elsewhere in the project, which only `add-check` says.
- Saving a form and its alerts is one transaction, on `UpdateCheck`: an editor's refused `critical` must not leave half a change. A new check's alerts are a second step after it is added, with `critical` judged before.
- A try's own timeout is held to 15 seconds as well, so its reason names the limit it had.
- The tick boxes of a form say they were on it, as a post does not mention one left unticked, and a form's fields left out of a post stay as they are: a hand-made post is judged on what it says, not on what it leaves out.
- A save with new addresses is limited as well, to 30 a minute for a person: each says whether a name is private, and a refused post stores nothing for R30's counts to hold. The limit on tries alone does not cover that.
- R27 holds for what is said, not for all that can be inferred: with a private range allowed, one host in it that is the server's own is refused while its neighbours answer. Runs from before this step keep the reason they had, until retention removes them.
- ADR 0001 names "deleting" under the owner's role. That is read as deleting a project, as R15, accepted with this spec, lets editors delete checks.
