<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Plan: tell people when something is wrong, calmly

Spec: [spec.md](spec.md), accepted 2026-10-21. Each step is a release candidate that runs in shadow mode on a real installation before the next.

1. **Incidents, recipients and shadow mode** (C2: what needs no channel).
   - Migration 0003: incidents, incident checks, recipients, channels, deliveries; urgency and critical on checks; alert settings on memberships; quiet hours and the digest time on people.
   - The incident step in `saveStatus`, the settle period, and moved checks.
   - The sender (`internal/alert`): decides at due time, gathers, records in shadow mode, and sends the digest.
   - The clock-jump guard in the scheduler.
   - The incident on its check page, "Messages" in the dashboard, and `allstill admin set-alerting`.
   - In the API description: the incident's urgency `none`, `recovering_since` and `resolve_reason`.
2. **The comparison.** `allstill compare healthchecks`, so the shadow weeks give evidence (C7).
   - It compares when checks went down and came back up, which is when each would alert, not when a person would have been told: that is on Messages.
   - `-project` filters; checks are matched by UUID, across the projects one Healthchecks project was split over.
3. **Web Push.**
   - The ADR for keeping the VAPID key and subscriptions in the database (C6).
   - RFC 8030, 8291 and 8292 against their test vectors; the service worker; channels with a test message.
   - The health endpoint, and `attention` for failed deliveries and a sender that is behind (R11).
   - In the API description: channels, messages and the shadow setting.
4. **Email** through SMTP, as the fallback and for the digest.
5. **Switch off shadow mode**, and the first checks at Healthchecks: the nightly backups.
   - First settle R10. Today the digest leaves out what was told only in shadow mode, but R10 says it lists it. An incident opened in shadow mode whose down still waits would be alerted for real.
   - A failed delivery for want of a channel recurs every 10 minutes while its incident is open: prune or fold them.
