<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Security

Allstill knows when your jobs run, holds the keys they ping with, and decides who gets told when something breaks. A weakness in it matters, so thank you for looking.

## Reporting a weakness

Report it privately, through [a security advisory on GitHub](https://github.com/MartijnMul/allstill/security/advisories/new). Please do not open an issue, a pull request or a discussion for it: those are public from the first minute.

Say what you found, how to reproduce it, and what someone could do with it. A proof of concept against your own installation helps; please do not test against an installation that is not yours.

What happens next:

- You hear back within 7 days that the report has arrived, and whether it is a weakness in Allstill.
- While it is being fixed, you hear how it is going, at least every 14 days.
- The fix comes in a release with an advisory that names the weakness and credits you, unless you would rather not be named.

## What is covered

Everything in this repository: the server, the dashboard, the API, the command line, the probe, and the release artifacts with their signatures.

Not covered: how someone has set up their own installation, such as a proxy in front of it, and weaknesses in Go or a dependency that Allstill does not reach. `make vulncheck` shows which known weaknesses Allstill's code reaches.

## Versions

Allstill is in early development. Until 1.0, only the latest release gets security fixes.
