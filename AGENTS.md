# AGENTS.md

Orientation and rules for AI coding assistants in this repository, and for anyone else who changes it. Keep it under one page: detail goes in the document that owns it, with a pointer here.

## What this is

**Allstill**: a self-hosted dead man's switch and check service that replaces Healthchecks.io and does more, with a native macOS menu bar app. Goal: `README.md`. Stack and architecture: [ADR 0001](docs/adr/0001-stack-and-architecture.md); licences: [ADR 0002](docs/adr/0002-licence-and-contributor-agreement.md); the API: [`api/`](api/README.md); the look and the voice of everything people see: [docs/brand](docs/brand/README.md). Don't add a dependency or an external service ADR 0001 doesn't allow without a new ADR.

**Current status: phase 0**, the foundation. The server receives pings and applies deadlines, `allstill run` reports jobs, `allstill import healthchecks` copies checks over, and the dashboard shows them to people who sign in with a passkey (`allstill admin create-user` prints their first link). Pings are limited per check and history is kept per check (`-keep-runs`, `-keep-output`). Phase 1 has begun with alerts ([intent/alerts](intent/alerts/spec.md)): incidents, what each person is told, quiet hours and the digest run in shadow mode, recorded under Messages and not sent until an administrator runs `allstill admin set-alerts on`; people add devices (Web Push) and email addresses (SMTP) under Devices and get test messages; `allstill compare healthchecks` lines them up with Healthchecks. Checks Allstill runs itself have begun too ([intent/http-checks](intent/http-checks/spec.md)): HTTP and TCP checks with their certificates, from the server, added with `allstill admin add-check -http` or `-tcp`; probes follow. Releases are built by GoReleaser from a tag ([docs/running.md](docs/running.md)), without the Homebrew tap ADR 0001 names, which needs its own repository; the first release comes when phase 0 holds up in daily use; recovery codes and managing passkeys in the dashboard follow with phase 1 (until then a lost device means a new link from `allstill admin passkey-link`). Logic lives in `internal/`; `cmd/allstill` only wires it together.

## Commands

- `make check`: everything CI runs. Healthy: the API lint reports `Your API description is valid`, the hook tests print only `ok` lines, the Go tests print only `ok` (and `no test files`) lines, `generated code is current`, and govulncheck reports `Your code is affected by 0 vulnerabilities`.
- `make generate` after changing a query in `internal/store/queries/` or a migration; commit the generated code with it. Migrations in `internal/store/migrations/` are never edited once committed: add the next number.
- `make hooks`: once per clone; installs the git hooks (secrets, names from other work, commit identity and trailers). The terms live in `.git/info/denylist`, outside the repository; CI checks the whole history against the same list, kept as the `DENYLIST` secret.
- Claude Code hooks (`.claude/hooks/hooks.py`) keep Claude from skipping those checks, ask before outward-facing commands, and protect licence texts, committed migrations and the guards themselves from Edit and Write. They are a convenience, not the boundary: the git hooks and CI are. Every case they judge is in `test_hooks.py`; a hook change adds its case there.
- `make help` lists the rest.

## Done means verified

- Run `make check` before saying a task is done, and show its output. Never skip, weaken or delete a check or a test to make it pass.
- Before every commit, run the `reviewer` agent on the staged diff with [REVIEW.md](REVIEW.md). Fix every Important finding first; report the nits.
- Before a decision that is hard to reverse (an ADR, a data model, an API shape), test it against real uses and ask for an independent critical pass, then write it down.

## Written to be public

Private now, open source later, and git history cannot be unpublished. Everything committed, commit messages included, is written as if it were public today:

- No secrets, keys or certificates, not even in tests. No real data: examples are invented (`example.com`, `backup.sh`, `web-1`).
- No names from other work: companies, customers, colleagues, internal hosts, board or ticket references.
- `Co-Authored-By:` trailers are fine; no `Claude-Session:` trailers or claude.ai session links. Commits use the GitHub noreply address and are signed.
- English for everything public. Anything specific to one setup stays behind configuration.
- Licences per ADR 0002: `AGPL-3.0-only`, except the API description and generated clients (`api/` and each client's own directory), which are `Apache-2.0`. Every source file starts with its `SPDX-License-Identifier`. `LICENSE` files are never edited. Contributions from others need the [CLA](CLA.md).

`.notes/` holds private notes (the plan, needs from real setups). It is its own repository, excluded through `.git/info/exclude`. Nothing moves from it into this repository without being reread as an outsider.

## Working agreements

- Properly designed and documented, without overkill: add structure when there is a concrete need.
- `docs/adr/` holds the why behind hard-to-reverse decisions; an accepted record is superseded, not rewritten. `intent/<slug>/` holds the intent and the spec of a big change ([intent/README.md](intent/README.md)).
- Commit to `main` while one person works on it; Conventional Commits. A change that makes a sentence here, in the README or in an ADR untrue updates it in the same commit.
- Outward-facing actions need approval first: changing visibility, force pushes, PRs and issues. Pushing to `origin main` is approved.

## Things Claude gets wrong

When Claude gets something wrong a second time, the correction becomes a line here, in the same commit.

- Designing around one person's own servers and tools. Allstill is a general product; real setups only tell us which features it needs.
- Settling a model before playing it through real cases: shared infrastructure, one operator for many parties, outsiders who only need a status.
- Explaining in technical terms first. Start with what it does and what it gives, in plain words, then the details.
