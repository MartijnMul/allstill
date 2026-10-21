<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: tell people when something is wrong, calmly

Intent: [intent.md](intent.md)
Status: accepted (2026-10-21, with the recommendations of C2 to C9)
Written: 2026-10-21, by Claude for the maintainer; revised the same day after an independent review
Codebase: MartijnMul/allstill at b888ba6
Policies in force: no skills yet; ADR 0001 and 0002, AGENTS.md, REVIEW.md, `api/openapi.yaml` 0.2.0 at b888ba6, and needs from real setups (kept privately)
Prompt: the requirements-and-design prompt of the AI-native SDLC playbook, run on intent.md

## Summary

The intent asks that Allstill tell the right people once, at the right speed, when a check goes down, and first prove for a few weeks that it would have done so correctly.

This spec turns check states into incidents. For each incident and each person, it keeps one record of what that person has been told. A sender compares that record with the current state and the current settings whenever something falls due, and sends only the difference. Because every message is recorded, a "shadow" switch can keep everything except the sending itself.

It flags which channel comes first, the default urgency, two short waits that buy calm, and where the secrets of Web Push live.

## Requirements

Functional
- R1. A check that counts and enters `down` opens an incident, or joins the one it is still in; `late`, `warning`, `unknown`, `paused` and `new` never do. (Problem, Constraints)
- R2. A check has at most one open incident. (Constraints: no repeats)
- R3. An incident resolves when its checks have all stayed up (or `warning`, `late` or `unknown`, but not down again) for the settle period, or have been paused or deleted. Its reason is kept: recovered, paused or deleted. A check that goes down again within the settle period stays in the open incident. (Proposed outcome; Constraints: no flapping)
- R4. For each incident and each person who gets alerts for a project of the incident, Allstill keeps what that person has been told: nothing, down or recovered. (Constraints: no repeats)
- R5. A person is told "down" once, at the speed of the check's urgency (`now`, `digest`, `none`) and their quiet hours, which only a critical check breaks through. The decision uses the settings at the moment it is due, not those when the incident opened. (Proposed outcome; ADR 0001 "two clocks")
- R6. A person is told "recovered" only if they were told "down", the project wants recovery messages, and the reason is recovered. It goes the same way they heard the "down", and never during quiet hours. (Proposed outcome)
- R7. A person is never sent two messages less than the gather period apart. The second waits and carries whatever came due in the meantime. (Problem: one host, many checks)
- R8. A digest goes out once a day at the person's digest time when there is something to say. It says what opened, what is still open, and what resolved without them being told. It also lists checks that cannot be judged (an unreadable schedule) and checks that have never pinged. One missed while Allstill was down is sent at start. (Proposed outcome)
- R9. A person can send a test message to each of their channels and see every message sent to them, with the channel, the time and the outcome. (Proposed outcome)
- R10. In shadow mode every step runs and is recorded, but only test messages are sent. The dashboard lists what would have been sent. Incidents opened during shadow mode are not alerted when it is switched off; the next digest lists them. (Proposed outcome)
- R11. Silence is visible: a project with nobody who would be told, a delivery that failed, and a sender that has fallen behind each show as `attention` in the overall status. A new health endpoint reports how far the sender is behind, so a monitor outside Allstill can watch it. (Intent: nothing missed)

Quality
- R12. A failed push is retried once. A `now` message that no push device accepted then goes by email. Email retries with backoff, then the delivery is reported as failed (R11). (Constraints)
- R13. Allstill's own downtime alerts nobody. After a restart, or a forward jump of the clock larger than a minute, checks become `unknown`; the scheduler compares the wall clock with the time actually elapsed. A due message whose incident has resolved since it fell due is not sent. (Constraints: green is proven)
- R14. Delivery is at least once. A crash during a send may repeat that one message with the same content, and never loses it.

Compliance and privacy
- R15. A message is rendered per recipient, from the projects they may see at the moment it is sent. It carries no job output, only the check, its state, the reason and a link. A Web Push message stays within the 4 KB a push service accepts, and sums up when many checks are involved. (Constraints; ADR 0001 "incidents across projects")
- R16. Nothing leaves the machine except to the browser's push service and the configured SMTP server. (Constraints; ADR 0001)
- R17. The API description gains what this adds, in the change that builds it:
  - an incident urgency of `none`, the resolve reason and "recovering";
  - channels and their test, and messages to a person;
  - a membership's alert setting, the digest time and the shadow setting;
  - the health endpoint;
  - `attention` also covering failed deliveries and a sender that is behind. (ADR 0001: the API description is the contract)

## Design

**Where it sits.**
- The store already saves a check's new status and writes an event in one transaction, in `saveStatus`. That covers pings, missed deadlines, recovery after a restart and unreadable schedules. The incident step runs there.
- Pausing, resuming and deleting a check do not exist in the code yet (only in the API description). When they are built, they call it explicitly, since they will not go through `saveStatus`.
- The incident step only records: incidents, their checks, and for each person what is due. A sender works outside the transaction, like the scheduler. It wakes for the earliest due time, decides and sends, and records the delivery.

**Incidents.**
- An incident belongs to the workspace, not to a project, so that phase 2's dependencies can join checks from several projects.
- It records:
  - its checks, in `incident_checks`, with when each joined and left and a copy of the check's name. That row has no cascading foreign key, so history outlives a deleted check.
  - a state (`open`, `acknowledged`, `resolved`, as in the API), an urgency and a cause check;
  - when it opened and resolved, and why it resolved;
  - `merged_into`, empty until dependencies merge incidents.
- One open incident per check comes from a copy of the open flag on `incident_checks`, under a partial unique index.
- While its checks are up but not yet settled, the incident stays open and shows "recovering". The live stream then does not flip at every flap.
- Acknowledging records who is on it and shows it to everyone in the incident. It survives a check going down again within the incident. Reminders, which it would stop, come later.
- Calendars and maintenance windows will add states in which a check does not count. R1 already leaves them out.

**What each person is told.** One row per incident and recipient holds:
- the last thing told (`nothing`, `down`, `recovered`);
- when, and on which channel kind;
- the next time the sender must look.

The recipient is a person in this phase; the row allows a shared channel instead, for phase 2.

At its due time, the sender works out what should be true for that person now:
- **Down:** if the incident is open and the person still gets alerts for a project of it, they should know it is down. That happens:
  - now for `now`;
  - at the end of quiet hours, unless critical;
  - in the digest for `digest`;
  - never for `none`.
- **Recovered:** if the incident resolved as recovered and they were told down, they should hear it recovered (R6).

The sender compares that with what was told and sends only the difference. So settings changed while a message waits are followed (R5). A down that recovered before it was due is never sent (R13), and a flap within the settle period sends nothing at all.

**Who is told, and how fast.**
- A membership gets an alert setting, on by default for owners and editors. A workspace administrator sees every project as owner, so gets alerts for every project unless a membership of theirs turns them off.
- Who follows an incident is worked out from each check's current project, so a check moved to another project during an incident is followed by the new project's people. Those who now follow it are added as recipients when it moves.
- `projects.recovery_messages` exists already.
- A check gets the urgency and critical flag the API describes.
- A person gets quiet hours and a digest time. The quiet hours carry their own time zone (as in the API), defaulting to the person's. A period that crosses midnight belongs to the day it starts. An end inside a daylight-saving gap moves on by the length of the gap (02:30 becomes 03:30).

**Channels.**
- One `Channel` interface, as in ADR 0001. Send returns whether to retry, whether the address is gone (a Web Push subscription that answers 404 or 410 is removed) or whether the failure is permanent.
- **Web Push:**
  - RFC 8030, 8291 and 8292, on the standard library.
  - Sent with a `TTL` and with `Urgency: high` for `now`.
  - Usable once the device has opened the test message: Web Push gives no delivery receipt, so the link in the test confirms it.
  - The VAPID key pair is made at first start.
- **Email:** through an SMTP server set with `-smtp-url` and `-smtp-from`, as environment variables, never in the database.
- **Routing:** `now` goes to every push device, and to email when none accepts it. `digest` goes by email, or by a short push when there is no email. A push still respects quiet hours.

**Delivery.**
- One delivery is one message to one channel. It holds a fixed set of what it reports, so a retry sends the same content.
- Before every attempt, the sender checks two things:
  - that each item is still true for that person (R13, R15);
  - whether shadow mode is on. If it is, it marks the delivery `shadow` instead of sending.

**The comparison.** `allstill compare healthchecks -project NAME -since DATE` reads, for each imported check, when Healthchecks saw it go down and come back. It uses the same API as the importer (its status changes, the "flips"), and prints where Allstill would have alerted differently: missed, extra, or more than a few minutes apart. It also lists the checks Allstill has never heard from, since they can never alert.

**Screens.**
- An incident on its check's page and in a list, with "recovering" and who acknowledged it.
- A person's channels with a test button.
- "Messages to you": every delivery with its outcome, and in shadow mode what would have been sent.
- On a project and on an incident: "nobody would be told", when that is so.
- The shadow setting, changed by a workspace admin.

**What exists.**
- In the code: the state machine, the scheduler, the event log, people with an email address, memberships with roles, and `projects.recovery_messages`.
- In the API description: incidents, acknowledge, snooze, urgency, critical and quiet hours.

Everything else is new: one migration with incidents, incident checks, recipients, channels and deliveries, plus the new columns; and the API additions in R17.

## Cases played through

1. **A nightly backup does not start.** Cron 03:00, grace 1 h, urgency `now`, quiet hours until 07:00. It goes down at 04:00, and the person's row says "down" is due at 07:00.
   - It runs late at 05:00 and succeeds. The incident is recovering until 05:10, then resolved. At 07:00 there is nothing to send, since they were never told down. The digest says it was late and recovered.
   - It is up at 06:55 and down again at 07:02: still the same incident, so at 07:00 the down is still true and is sent.
   - It never runs: one push at 07:00.
2. **A backup fails with exit status 3 at 03:12.** It goes down at once, and from there it is as in case 1. The message says "failed with exit status 3"; the output stays in the dashboard.
3. **A shared host goes down.** Six heartbeats in two projects go down over two minutes as their deadlines pass, which makes six incidents. Each person gets the first at once and the rest in a single second message after the gather period. Phase 2's dependencies make it one incident with a cause.
4. **A heartbeat flaps.** Down at 10:00 (push), up at 10:03 (recovering), down at 10:06 (still the same incident, no message), up at 10:09 and stays up. Resolved at 10:19, with one recovery message. Two messages in all, and the live stream flips once.
5. **Allstill was down for two hours, or its clock jumped forward.** Checks become `unknown` and nothing opens. A due "down" for an incident still open is sent; one for an incident that has resolved is not. Telling anyone that Allstill itself was down is the outside watcher's job (`docs/running.md`, R11).
6. **A colleague in one project only.** They hear only about that project. They are removed from it while a message waits: at its due time they no longer get alerts there, so nothing is sent.
7. **A check is moved to another project while it is down.** The new project's people become recipients and are told; the old project's people no longer follow it, so they get no recovery message.
8. **A check is paused while down.** Its incident resolves as paused, and nobody gets a recovery message. If it is resumed and fails again, a new incident opens.
9. **Urgency is lowered from `now` to `digest` while a message waits for quiet hours to end.** At 07:00 the sender follows the new setting, and it goes in the digest.
10. **The shadow weeks.**
   - The maintainer adds their phone and email and sends test messages, which are really sent.
   - Every would-be message is listed, and `compare healthchecks` shows the differences.
   - The admin switches shadow off, and the nightly backups go off at Healthchecks first.

## Areas of concern

Decided by the maintainer on 2026-10-21: every recommendation below.

- **C1. Policies are documents, not skills.** Applied at b888ba6: the ADRs, AGENTS.md, REVIEW.md, the API description and the private needs. Recorded as the playbook asks; nothing to resolve.
- **C2. Web Push or email first.**
  - Email is small, but needs an SMTP server, which not every self-hoster has.
  - Web Push is the main channel and needs no account, but it is the bigger piece: its encryption is ours to get right, and on the iPhone the dashboard must be added to the home screen.
  - Recommendation: build incidents, the recipient records and shadow mode first, since they need no channel. Build Web Push during the shadow weeks, then email.
- **C3. Default urgency.** The API's default is `now`, and Healthchecks emails every down; with `now` and no quiet hours, a failed 03:00 backup wakes someone.
  - Recommendation: keep `now`, and give a new person quiet hours of 22:00 to 07:00, which they can change. Only critical checks then wake anyone.
- **C4. The settle period delays "recovered".**
  - Recommendation: 10 minutes, the same for every check for now.
  - It costs a late "it's back", and keeps a flapping check from sending anything more.
- **C5. The gather period spaces messages.**
  - Recommendation: 3 minutes between two messages to one person; the first goes at once.
  - It holds the second alert of a burst at most 3 minutes, within the five minutes the intent asks for.
- **C6. Secrets at rest.**
  - The VAPID private key lets its holder push to every subscribed device as this installation. Subscriptions let their holder push to a device.
  - ADR 0001 says "All keys and tokens are stored as hashes, except check UUIDs". Neither can be stored as a hash, since Allstill must use them, so they are a second exception.
  - Recommendation: keep both in the database, protected as the database and its backups are, and say so in `docs/running.md`. A new ADR records the exception before Web Push is built. Encrypt at rest when the first third-party secret arrives (bot tokens, phase 2).
- **C7. The comparison needs a Healthchecks API key again**, and its flips endpoint.
  - Recommendation: build it. It is small, and it turns "a few weeks" into evidence.
- **C8. No job output in messages.** Healthchecks' emails include the last ping's body. But output can hold secrets, and it passes a third party's push service and a lock screen.
  - Recommendation: no output; give the reason and a link. Revisit for email only if it is missed.
- **C9. An incident urgency of `none` changes the API's enum.** Adding a value can break a client that rejects unknown values.
  - Recommendation: add it now, while the API has no clients outside this repository.

## Open questions from the intent

- **Web Push or email first:** Web Push, after the parts that need no channel (C2).
- **A recovery message by default:** yes, per project (`recovery_messages`), sent after the settle period to whoever was told down (R6, C4).
- **How to compare with Healthchecks:** with `allstill compare healthchecks` (C7).
- **How fast "now" must be, and what if the phone is unreachable:**
  - At once for the first message, within 3 minutes for the next, and by email when no push device accepts it (R7, R12).
  - A phone that is off does not count as a failure, since Web Push has no receipt. The shadow weeks measure how well it works.
  - A native app only if Web Push proves unreliable, as ADR 0001 says.

## Out of scope

- **Inherited from the intent:**
  - dependencies, and flapping suppression beyond the settle period;
  - counts in a window, and going quiet after N messages;
  - other channels;
  - calendars and maintenance windows;
  - status pages.
- **Also left out:**
  - repeated reminders for an open incident;
  - snoozing in the dashboard (the API's snooze fits the recipient record, but the screen waits);
  - escalation to a second person.
