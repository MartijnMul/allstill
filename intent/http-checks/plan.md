<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: watch sites, ports and certificates

Intent: [intent.md](intent.md)
Spec: [spec.md](spec.md), accepted 2026-10-28
Status: accepted
Approved: 2026-10-28, by the maintainer; risk: higher risk (a rebuild of the checks table, and requests the server makes to addresses people choose)
Codebase: MartijnMul/allstill at 5e2e943

Four steps, each a release candidate that runs on a real installation before the next, and each worth having on its own: after step 2 sites and certificates are watched; step 3 adds ports; step 4 reaches networks the server cannot.

## Files that change

Step 1, groundwork:
- `internal/store/migrations/0007_active_checks.sql` (new): rebuilds `checks` without the `kind` constraint and with `fail_streak`; adds `runs.reason`.
- `internal/check/check.go` (changed): `Config` learns the check's kind; `OnAttempt`; for an active check, `OnDeadline` and `OnRestart` make it unknown without a new deadline instead of late or down; unknown from an unjudged attempt.
- `internal/store/ping.go`, `incidents.go`, `queries/` (changed): runs with a reason; an HTTP or TCP check's unknown holds its incident open (the settle query); a ping to such a check is refused.
- `internal/ingest` (changed): the 409 for a ping to an active check.
- `internal/web/pages.go`, `templates/check.html` (changed): viewers see no ping URL (spec C9).

Step 2, HTTP checks from the server:
- `internal/active` (new; not `internal/runner`, which wraps jobs for `allstill run`): one attempt of an HTTP check, judged (status codes, the JSON field, the redirect's `Location`), the certificate read from the verified chain with `ca` and `no_verify`, the reason in plain words without any URL or `Location`, the kind of error (target or own network); `User-Agent: Allstill/<version>` with the optional contact; at most 1 MB of a body, read only for a JSON field.
- `internal/active/loop.go` (new): keeps the active checks of the server or a probe, one timer for the earliest attempt, offsets from the check's id, one attempt per host at a time, at most 20 at once, the canary.
- `internal/outbound` (changed): a transport for checks without kept connections, redirects followed when asked with every hop checked, the server's own addresses refused (ADR 0004).
- `internal/store/checks.go`, `queries/checks.sql` (changed): active checks, their settings, recording a tick's attempts in one transaction.
- `internal/alert/sender.go` (changed): the digest lists checks in warning (spec R9).
- `internal/web` (changed): an HTTP check's page and overview row (last runs with duration and reason, the certificate's days left), its URL by role (spec R12); `health.go` and the overview's attention cards say when the server is offline for its checks (spec R7, C4).
- `cmd/allstill/main.go`, `admin.go` (changed): the loop in `serve`, `-allow-private`, `-canary`, `-contact` for the User-Agent, `admin add-check -http` and `set-check` with `-expect`, `-location`, `-json`, `-follow`, `-every`, `-fail-after`, `-warn-days`, `-fail-days`, `-ca`, `-no-verify`; `add-check` refuses an address the server may not reach, saying what to do (allow the range, or use a probe), says when another check already reads that host and port's certificate, and warns when a HEAD check gets 405.
- `api/openapi.yaml` (changed, 0.6.0): `http` checks, their settings and runs, `CheckKind` without `certificate` and open to new kinds, URLs by scope and role.
- `api/README.md` (changed): the removal of `certificate` noted as the exception the spec accepts (no clients yet), as the alerts spec's C9 was.
- `docs/running.md` (changed): HTTP checks, the canary, `-allow-private` for every project, tokens in check URLs among the database's secrets (ADR 0004).

Step 3, TCP checks:
- `internal/active` (changed): a TCP attempt, with TLS and its certificate when asked.
- `cmd/allstill/admin.go`, `internal/web`, `api/openapi.yaml` (0.7.0), `docs/running.md` (changed): `add-check -tcp host:port [-tls]` and its page.

Step 4, probes:
- `internal/store/migrations/0008_probes.sql` (new): probes (name, key hash, own check, protocol version, last heard), the projects each serves, a check's probe.
- `internal/probe` (new): the probe's side: poll with an ETag every 30 seconds, run with `internal/active`'s loop, post runs in batches at least every 30 seconds with ids and ages, check its clock and version.
- `internal/api` (new, generated): types and a server interface for the `Probe` tag, generated from `api/openapi.yaml` with `oapi-codegen` (a `tool` directive in `go.mod`, part of `make generate` and the check that generated code is current), as ADR 0001 and `api/README.md` say; the first `/api/v1` routes served.
- `internal/web/probe.go` (new): `GET /api/v1/probe/checks` and `POST /api/v1/probe/runs` on that interface, with the probe's key.
- `internal/store` (changed): probes, assignment within the projects a probe serves, runs from a probe (stamped, deduplicated, ordered), a quiet probe's checks unknown.
- `cmd/allstill/probe.go` (new) and `admin.go` (changed): `allstill probe`, `admin create-probe`, `rekey-probe`, `set-check -probe`.
- `api/openapi.yaml` (0.8.0, changed): a `Probe` tag with the probe's endpoints.
- `api/README.md` (changed): the generator options for the `Probe` tag alone at first, and "probes" off the list of what is not in v1 yet.
- `docs/running.md` (changed): how to run a probe.

## Order of work

1. **Groundwork** (release candidate; nothing new to see, apart from what viewers no longer see).
   1. Migration 0007, tried first on a copy of a real installation's database (made with `allstill backup` by its maintainer).
   2. `check.OnAttempt`, and `OnDeadline` and `OnRestart` for active checks, test-first in `internal/check`.
   3. The settle rule for active checks, and runs with a reason.
   4. The 409 for a ping to an active check, and ping URLs hidden from viewers.
2. **HTTP checks from the server** (release candidate; worth having on its own).
   1. `internal/active`: judging an answer and a certificate, with table tests for every reason in the spec, short-lived and self-signed certificates, and no URL in any reason.
   2. The outbound transport for checks: no kept connections, redirects with every hop checked, the server's own addresses refused.
   3. The loop, with `testing/synctest`: offsets, one per host, the cap, the canary and the kinds of error (spec cases 5 to 8), and the server offline on `/health` and the overview.
   4. Storing attempts, the commands, the pages, the digest's warnings.
   5. The API description and the docs.
   6. On the real installation: checks for its own dashboard, a site's www and apex, and a health endpoint, run next to whatever watches them now for a week.
3. **TCP checks** (release candidate): the attempt, the command, the page, the API and docs; on the real installation, a mail server's port with TLS.
4. **Probes** (release candidate).
   1. Migration 0008 and the store, with assignment refused outside the projects a probe serves.
   2. The probe's endpoints, with ids, ages, the server's time and the version.
   3. `oapi-codegen` brought in for the `Probe` tag, then `allstill probe`, reusing the loop, with `-allow` and `-ca`.
   4. The commands, the API and docs.
   5. On a real network the server cannot reach: one probe, one internal service, then its network unplugged on purpose to see its checks turn unknown and the probe alert once.

## Risks

- **Rebuilding `checks` (step 1)** is the riskiest change: every other table hangs off it. The migration copies the database before it runs and checks the foreign keys before it commits; it is tried on a copy of a real database first, and a release candidate on the real installation before step 2 builds on it.
- **The state machine (step 1)** is shared with ping checks. `OnAttempt` is new, but `OnDeadline` and `OnRestart` branch on the kind now: every existing state test runs unchanged, and new ones prove a ping check behaves as before.
- **Requests to addresses people choose (steps 2 to 4).** Every dial goes through the address checks, including redirects; the server's own addresses are refused; a probe only reaches the ranges given on its command line, for the projects it serves. A test tries each way in that the spec names.
- **Alerting for Allstill's own outage (step 2).** Without a canary, the server's network dropping alerts for its checks, as the spec accepts; the docs and `add-check` say so until a canary is set.
- **Load (step 2).** One transaction per tick, at most 20 attempts at once; a load test with 1000 checks a minute against a local test server before the release.
- **Generated code (step 4).** `oapi-codegen` is a new tool, allowed by ADR 0001; it generates only the `Probe` tag at first, and the check that generated code is current covers it.

## Options not taken

- **One release for everything.** The probe is the biggest and least certain part; HTTP checks are useful without it and should not wait.
- **A separate binary for the probe.** ADR 0001 has one binary with `allstill probe`; the runner is the same code on both sides.
- **The probe pushed to by the server.** The probe would need an open port, which ADR 0001 rules out.
- **TCP before HTTP.** HTTP covers the incidents that led to this; TCP is a small step once the runner exists.

## Proof

- Step 1: `go test ./internal/check` covers `OnAttempt`, the active check's deadline and unknown, and an active check across a restart staying unknown without a deadline; `internal/store` tests show an active check's unknown keeps its incident open and a ping to it answered 409; `internal/web` shows a viewer no ping URL; migration 0007 runs on a copy of a real database, and `foreign_key_check` stays empty.
- Step 2: `internal/active` table tests for each reason (none holding a URL), certificate thresholds (90-day and 6-day), a self-signed certificate with `no_verify` still read (spec case 12), redirects with a hop into a private range refused; loop tests for spec cases 1, 2, 5, 6, 7 and 8 under `testing/synctest`; a load test of 1000 checks a minute in which every attempt starts within 5 seconds of its slot and ping ingest answers as fast as without it; on the real installation, a week of the new checks agreeing with the existing watch.
- Step 3: a TCP attempt against a test listener, with and without TLS; on the real installation, a mail server's TLS port up with its certificate's days left shown, and a closed port down.
- Step 4: generated code current; a probe and a server in one test process: assignment refused outside its projects, a replayed batch ignored, a wrong clock reported, a quiet probe alerting once while its checks turn unknown; on a real network, the unplugging in step 4.5.
- Every step: `make check` clean, and the reviewer agent on the staged diff before each commit.

## Departures

- Step 1 already has `Store.RecordAttempt` and `NewCheck.Kind`, which the plan put in step 2: the incident rules for active checks could not be tested without a way to record an attempt. Step 1 also lets a check in warning start its incident's settle period, as the alerts spec's R3 always said; until now only up did.
- Step 1 checks an active check's settings when it is made, so one Allstill cannot read is never made: the digest does not list an active check that is unknown, so it would never alert and never be listed. `set-check` in step 2 runs the same validation.
