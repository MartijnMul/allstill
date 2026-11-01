<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: add and change checks without a shell on the server

Author: Claude, confirmed by Martijn Mullié (maintainer)
Status: accepted
Started: 2026-11-01

## Problem

Allstill can watch a lot now, but only someone with a shell on the server can tell it what to watch. A check Allstill runs itself comes from `allstill admin add-check`. A check that jobs ping comes from an import, or from a ping with `create=1`, which leaves it with the project's default schedule. Changing a check's schedule, pausing it or removing it has no page at all.

That is workable for the person who installed it, and for nobody else. The people a project belongs to, who can see that a check is wrong, cannot put it right: an editor has the right to change a check, but no way to use it. A new user has to learn a set of commands before seeing anything useful, where Healthchecks lets them click "add check" and copy a URL. Without this, Allstill does not really replace Healthchecks for anyone who does not run the server themselves.

The JSON API that the menu bar app, the CLI and agents are to use is described but not served, apart from the probe's two endpoints. It needs the same operations, and the spec for checks Allstill runs itself decided that the form and the API follow together.

## Proposed outcome

- From the dashboard, someone who may edit a project can add a check of any kind: one that jobs ping, an HTTP check or a TCP check, and choose where it runs.
- A new ping check immediately shows how to use it: its URL, and a line to copy for `allstill run`.
- The same person can change a check's name, schedule and settings, pause and resume it, move it between projects they may edit, and delete it, with a clear warning of what deleting removes.
- Viewers keep seeing what they see today, and nothing that lets them change anything. Nobody sees or reaches anything of a project they are not in.
- The same operations work through the JSON API with a token, so the menu bar app, the CLI and agents can use them later.
- A mistake is hard to make silently: a schedule that cannot be read, or an address the server may not reach, is refused with what to do.
- Success: a new person, given a passkey link and no shell, adds a check, connects a job and sees it go green, in a few minutes.

## Affected users and systems

- Editors and owners of a project, who get to do what they have only been allowed on paper.
- Viewers, often outsiders who only need a status, who must not notice any of this except fewer surprises.
- Administrators, who keep the admin commands and stop being the only way in.
- The server, which starts taking changes from browsers and tokens rather than only from its own command line.
- The API description, which clients will start to build on.

## Constraints

- The roles already decide who may do what; this adds no new kind of permission. Every change checks the caller's role in that project, as the store's methods already do.
- The dashboard stays server-rendered Go templates with a little of our own JavaScript, under the strict content security policy, and works on a phone (ADR 0001).
- Every rule for a check holds in one place, whichever way the change comes in: the admin commands, the form and the API behave the same.
- A check's URL and ping URL are secrets, and stay as hidden from viewers as they are now. A probe stays within the projects it serves.
- Requests that change something must come from the dashboard itself, or carry a token.
- Calm: changing a check must not cause an alert by itself.

## Out of scope

- Managing projects, members and who is alerted in the dashboard. Projects keep their commands, and members get a small one with this change (spec, C12).
- Rules on values, calendars and maintenance windows, which come with their own features.
- Editing many checks at once.
- Undo. A paused check is the way to stop one without losing it.

## Open questions

- Should deleting keep a check's history for a while, or remove it at once as `deleteCheck` describes?
- Does the first version of the API need tokens for people as well as project keys, or can OAuth wait for the menu bar app?
- Should the form try an address once before saving, as `add-check` does, knowing that makes the server fetch on someone's click?
- Should an editor be able to give a check to a probe, or only an administrator?
- Should someone who may edit be able to see and copy a project's ping key, which today is shown once when the project is made?
