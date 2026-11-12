<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Contributing

Thank you for wanting to help with Allstill. Reports of what goes wrong, ideas from real setups, fixes and documentation are all welcome.

## Before you start

- **Found a bug?** Open an issue with what you did, what you expected and what happened. The bug report form asks for the rest.
- **Found a security weakness?** Do not open an issue: [SECURITY.md](SECURITY.md) explains how to report it privately.
- **Want something new, or to change how something works?** Open an issue first, with the problem it solves, before writing code. Allstill is early, and some ideas are already decided or planned differently; agreeing on the what first saves your time. Big changes go through [`intent/`](intent/README.md): what it is for, then a spec, then code.
- **A small fix,** such as a typo or an obvious bug with a test: a pull request on its own is fine.

## Setting up

You need Go (the version in `go.mod`), `make`, Python 3, Docker (`make check` lints the API description in a container) and [gitleaks](https://github.com/gitleaks/gitleaks) (the hooks scan every commit with it). Then, once per clone:

```sh
make hooks   # git hooks: secrets, private terms, identity, trailers
make check   # everything CI runs
```

The hooks refuse a commit whose author or committer is not your GitHub noreply address (`12345+you@users.noreply.github.com`). Each commit also warns that `.git/info/denylist` is missing: that list of private terms is the maintainer's own, and CI checks every pull request against it, so the warning is expected. `make help` lists the rest. [docs/running.md](docs/running.md) explains how to run Allstill, and `allstill guide` how to use it.

## What a change brings with it

- **Tests** for what it changes. A bug fix starts with a test that fails without it.
- **`make check` passes.** Never skip, weaken or delete a check or a test to make it pass.
- **`make generate`** after changing a query in `internal/store/queries/`, a migration, or the API description, with the generated code in the same commit. A committed migration is never edited: add the next number.
- **The documents it makes untrue,** updated in the same commit: the README, [`internal/guide`](internal/guide/guide.md), [docs/running.md](docs/running.md), the API description in [`api/`](api/README.md), or an ADR. An accepted ADR is superseded by a new one, not rewritten.
- **A licence header.** Every source file starts with its `SPDX-License-Identifier`: `AGPL-3.0-only`, or `Apache-2.0` in `api/` and the generated clients ([ADR 0002](docs/adr/0002-licence-and-contributor-agreement.md)).
- **No new dependency or outside service** that [ADR 0001](docs/adr/0001-stack-and-architecture.md) does not allow, without discussing it in an issue first.

## Writing

Everything people see, in English, follows the voice in [docs/brand](docs/brand/README.md#voice): calm, plain and specific, starting with what something means before the details. Examples are invented (`example.com`, `backup.sh`, `web-1`), never taken from a real setup.

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (`fix: ...`, `feat: ...`), use your GitHub noreply address, and are signed if you can. Work in a fork, on a branch, and open a pull request; one change per pull request.

AI assistants are welcome. [AGENTS.md](AGENTS.md) holds what they need to know about the code; where it describes the maintainer's own way of working, such as committing to `main`, this file is what applies to you. You are responsible for what you submit, whoever wrote it.

## The contributor licence agreement

Before your first contribution is merged, you sign the [contributor licence agreement](CLA.md), once. You keep the copyright; you grant a licence that lets Allstill stay open source and also be offered under other terms. [ADR 0002](docs/adr/0002-licence-and-contributor-agreement.md) explains why.

To sign, add a line to [`.github/cla-signatures.txt`](.github/cla-signatures.txt) in your first pull request, in a commit of its own:

```
your-github-login 2026-11-12 Your Full Name
```

That line says you agree to the agreement as it stands in `CLA.md`. Your name stays in the public history of the repository for good. A check on every pull request looks for its author's login in that file, and refuses a pull request that adds or changes anyone else's line: everyone signs for themselves. Commits by others in your pull request, such as a co-author's, need their own signature first.

## Review

The maintainer reviews every pull request against [REVIEW.md](REVIEW.md), and decides. A "not now" is not a judgement of your work: it may not fit yet. Everyone involved follows the [code of conduct](CODE_OF_CONDUCT.md).
