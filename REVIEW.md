# Review instructions

The review policy for Allstill, read by the `reviewer` agent before every commit and by any review integration later. Findings inform; approval is the maintainer's.

## Passes

Run five passes over the staged diff and tag each finding with its pass:

- **Bugs**: logic errors, broken edge cases, subtle regressions, races, time zone and daylight-saving mistakes, off-by-one in schedules and windows.
- **Security**: injection, authentication and authorisation gaps (a caller reading another project's data), secrets or ping URLs in logs, SSRF in anything the server fetches, unsafe handling of untrusted input (pings, bodies, headers, uploaded files).
- **Compliance**: the change matches ADR 0001 and 0002, the API description in `api/openapi.yaml` and its change rules in `api/README.md`, and, when there is one, the change's `intent/<slug>/` plan. A new dependency or external service without an ADR is Important.
- **Public**: anything that must not be public (see "Written to be public" in AGENTS.md): secrets, real data, names from other work, private paths, session links. Always Important.
- **Docs**: the change makes a sentence in AGENTS.md, README.md, an ADR, `api/README.md` or `internal/guide/guide.md` untrue, leaves something a user of Allstill can now do out of that guide or still under its "Not here yet", or adds behaviour a user would notice without a `## Unreleased` line in `CHANGELOG.md` once that file exists.

## What Important means here

Important: would break behaviour, lose or leak data, weaken a check, or breach a rule in AGENTS.md or an ADR. A fix that edits or deletes a test to make it pass is Important unless the test itself was wrong and the commit says why. Style and naming are nits.

## Cap the nits

At most five nits per review; summarise the rest as a count.

## Do not report

Generated code, formatting that `gofmt` or `swift format` fixes, and anything `make check` already enforces.

## Second time

A finding that flags a mistake for the second time adds the correction under "Things Claude gets wrong" in AGENTS.md in the same change. A false finding produced twice adds a line under "Do not report" here.

## Output

Findings ranked by severity, Important first, each with its pass, `file:line`, what is wrong and the evidence. Then the nit count. Then what was not reviewed and why.
