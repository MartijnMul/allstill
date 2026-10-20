# AGENTS.md

Orientation and rules for AI coding assistants in this repository, and for anyone else who changes it.

## What this is

**Allstill**: a self-hosted dead man's switch and check service that replaces Healthchecks.io and does more (values with rules, HTTP checks, calendars, incidents, calm alerts), with a native macOS menu bar app. See `README.md` for the goal and [ADR 0001](docs/adr/0001-stack-and-architecture.md) for the stack and architecture.

**Current status: design.** There is no code yet. Read ADR 0001 before writing any, and don't add a dependency or an external service it doesn't allow without a new ADR. The API is described in [`api/openapi.yaml`](api/openapi.yaml); its conventions and change rules are in [`api/README.md`](api/README.md).

## Working on the code

- `make help` lists the commands. `make api-lint` lints the API description; run it before committing a change to it.
- The OpenAPI document is the source of truth for the JSON API. Change it first, then the code generated from it. Within `/api/v1`, changes are additive only.

## Written to be public

This repository is private now and will be published as open source. Git history cannot be unpublished, so everything committed, including commit messages, branch names, and PR and issue text, is written as if it were public today.

- **No secrets**, keys or certificates, not even for tests: generate test keys at runtime or use clearly fake fixtures.
- **No real data.** Test data, fixtures, screenshots and examples are invented: `example.com`, `backup.sh`, `web-1`.
- **No names from other work**: no companies, customers, partners, colleagues, internal hostnames or addresses, and no board or ticket references. A commit message explains the change on its own.
- **Trailers**: a `Co-Authored-By:` trailer is fine. No `Claude-Session:` trailers or claude.ai session links in commits or PR descriptions.
- **Identity**: commits use the GitHub noreply address and are signed.
- **English** for everything public: code, comments, commits, docs.
- Anything specific to one person's setup stays behind configuration, so Allstill works for anyone.
- Licences ([ADR 0002](docs/adr/0002-licence-and-contributor-agreement.md)): everything is `AGPL-3.0-only` except the API description and generated clients, which are `Apache-2.0` with their own `LICENSE` in their directory. Every source file starts with its `SPDX-License-Identifier`. `LICENSE` holds the AGPL text unchanged.
- Dependencies need a licence compatible with the part they are used in. Check before adding one.
- Contributions from others are merged only after they have signed the [CLA](CLA.md).
- Every page the server serves links to the source of the running version (AGPL section 13); keep that on any new layout.

## Private notes

`.notes/` holds private working notes: the plan, the needs gathered from real setups, transcripts. It is its own git repository without a remote, excluded through `.git/info/exclude` (not `.gitignore`, which would name it). Nothing in it is copied into the repository without rereading it as an outsider and removing everything the list above forbids. Real infrastructure details stay there, and only as needs; they become generic features with invented examples here.

## Working agreements

- Keep it properly designed and documented, without overkill: add structure, tooling or docs when there is a concrete need, not in advance.
- `docs/adr/` holds the why behind decisions that are hard to reverse. An accepted record is never rewritten; a new one supersedes it.
- Commit directly to `main` while one person works on it; switch to branches and pull requests once others contribute. Commit messages follow Conventional Commits.
- Keep the docs current with the code: a change that makes a sentence here, in the README or in an ADR untrue updates that sentence in the same commit.
- Anything outward-facing needs explicit approval first: pushing, changing the repository's visibility, opening PRs or issues.
