<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: tell people when something is wrong, calmly

Author: Martijn Mullié (maintainer)
Status: accepted
Started: 2026-10-21

## Problem

Allstill sees when a job is late, fails or stops, but only someone looking at the dashboard finds out. So it cannot replace Healthchecks yet: today Healthchecks is what sends the email when a backup does not run. Every check stays at Healthchecks until Allstill can be trusted to say so itself.

Alerts are only worth something when they are rare enough to be taken seriously. Users of other monitors switch alerts off because of noise, and then real failures reach nobody. A host that serves several projects going down should not turn into a message per check behind it.

## Proposed outcome

- When a check goes down, the right people hear about it once, at the right speed: on their phone within about five minutes when it needs action now, in a digest when it can wait. When it comes back, they hear that too, if their project wants it.
- Before anyone relies on it, Allstill runs for a few weeks recording when it would have alerted, so that can be compared with the alerts Healthchecks sends for the same checks.
- Someone can test that an alert reaches them, and see what was sent and whether it arrived.
- Success: the first checks at Healthchecks are switched off, starting with the nightly backups, and nothing is missed afterwards.

## Affected users and systems

- People who run checks for themselves, customers and a home lab, often on shared infrastructure.
- The phone (Web Push to the dashboard added to the home screen) and email, as ADR 0001 sets out; other channels later.
- The menu bar app and the dashboard show the same incidents, but are not alert channels.

## Constraints

- Calm: no message for a successful run, no repeats for the same problem, no flapping, nothing for Allstill's own outage.
- Green is proven: when Allstill cannot tell (after its own downtime), checks are unknown, not down, and nobody is alerted for that.
- Alerts belong to people (ADR 0001): their own channels, quiet hours and digest time across every project they follow.
- A person only learns what they may see; an incident that touches other projects shows them only their part.
- One binary and one SQLite file. External services only where an alert must leave the machine (a browser's push service, an SMTP server).

## Out of scope

- Dependencies between checks, flapping suppression beyond the basics, counting in a window, and going quiet after N messages (phase 2).
- Telegram, Pushover, ntfy and webhooks (phase 2).
- Calendars and maintenance windows, beyond leaving room for them.
- Status pages and outside viewers.

## Open questions

- Which comes first, Web Push or email? Email needs an SMTP server, which not every self-hoster has.
- Is a recovery message wanted by default?
- How are the would-be alerts best compared with what Healthchecks sent?
- How fast must a "now" alert arrive, and what if the phone is unreachable?
