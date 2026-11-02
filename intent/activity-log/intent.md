<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: see what happened, when and by whom

Author: Claude, confirmed by Martijn Mullié (maintainer)
Status: draft
Started: 2026-11-02

## Problem

There is no place to look up what happened. Two kinds of question have no good answer today:

- **History: what happened to this check?** A check's page shows its recent runs and an incident that is still open, but a past incident has no page, and Messages shows only what was sent to you. "It went down at 09:12, who was told, and when did it come back?" cannot be answered from the dashboard, not even by an administrator.
- **Audit: who did this?** A person who changes a check in the dashboard leaves one line in the server's log, which only someone with a shell can read. The admin commands leave no line at all, and neither does a successful sign-in. The events Allstill stores for its live stream name no person and are gone after a week. With several people able to delete, "who removed this check?" has no answer that anyone in the dashboard can reach.

The two questions often come together: "why did this go down, and did someone touch it just before?" Keeping them apart would split that answer in half.

Every day without this also loses something for good. Part of what happened to a check can be read back later from what is stored, though old runs are cleaned up and a deleted check's runs go with it. Who changed what cannot be read back at all.

The spec for managing checks left an audit page out until a real installation needed one (C9). The maintainer now misses it on a real installation.

## Proposed outcome

- In the dashboard, someone can see what happened, when and by what, newest first.
- It covers both kinds of question: what happened to checks (down, up, late, who was told) and what was done to them and around them (added, changed, paused, moved, deleted, signed in).
- "By what" is always answered: a person, a key, a job, a probe, a command on the server, or Allstill itself.
- It can be narrowed to what someone is looking for, such as one check, one project or one person.
- An entry stays readable after the thing it is about has gone, so a deleted check can still be asked about.
- The maintainer's suggestion: one screen for audit and history together, rather than two.
- Success: someone without a shell answers "who removed this check?" and "did anyone touch this check before it went down?" in under a minute.

## Affected users and systems

- Administrators, who answer for the whole installation and today have to read a server log.
- Owners and editors of a project, who share its checks and want to know who changed what.
- Viewers, often outsiders who only need a status. Anything this shows them beyond what they see today is decided on purpose, not by accident.
- One operator running checks for many parties: each party sees its own project and nothing of another's.
- Jobs, probes and keys, which act without a person behind them and need a clear name in the answer.
- The server, which starts keeping a lasting record of actions, next to the history it has kept until now and cleans up.

## Constraints

- Nobody but an administrator sees anything of a project they are not in, and the roles decide what they see within it.
- A check's URL and ping URL are secrets. So are settings that carry a token. An entry can say that a URL changed, but never what it is or was, for anyone. The log line in the manage-checks spec (R29) goes further and leaves out every setting's value; the last open question asks whether this should.
- Writing down what happened must not make the thing itself fail: a problem with the record must not lose a ping, or delay an alert.
- Calm: a check that runs every minute must not bury the few things that matter.
- It stays on what ADR 0001 allows: one SQLite database and server-rendered pages. Like the rest of the dashboard, it works under the strict content security policy and on a phone.
- Every action goes through the same record, whichever way it comes in: the dashboard, the commands on the server, and later the API.
- What was done before this exists cannot be recovered, and the screen must not suggest otherwise.

## Out of scope

- The server's own diagnostic log, which stays what it is, for whoever runs the server.
- Undoing a change from the screen.
- Being told about an entry: alerts stay about checks, not about the record.

## Open questions

- Is every run an entry, or only what changed? A successful run each minute is history too, but it is already on the check's page.
- How long is it kept, and can it be set per installation? Should actions outlast the history of runs? Does a limit have to hold for an installation with thousands of checks?
- May anyone ever remove or edit an entry? Deleting a project, or a person asking to be removed, may ask for it; a record that can be edited proves little.
- Who sees what? In particular: do viewers see the history of their checks, do they see which person made a change, who may see who was told, which nobody can see today, and who sees sign-ins and changes to people and probes?
- Who is "a command on the server"? A shell gives no person in the dashboard's sense. Is "someone on the server" enough, or does a command ask for a name?
- When a check moves from one project to another, what do the people of each project see of what happened before and after the move?
- A failed sign-in: an entry, given that anyone on the internet can cause one?
- What becomes of the Messages page: part of this, or its own page linked from it?
- Is seeing it in the dashboard enough, or do some installations need to export it or send it elsewhere, for instance to prove what happened to a customer?
- Should changes show what they were, as in "every 5 minutes became every hour", where the value is not a secret?
