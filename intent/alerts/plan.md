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
3. **Web Push.** Built: the ADR, the encryption and signing, the outbound client, devices with a test message, the sender pushing and retrying once, the health endpoint and `attention` on the overview (R11), and the API description. The JSON API itself, like the rest of `/api/v1`, is not served yet.
   - [ADR 0003](../../docs/adr/0003-web-push-secrets.md): the VAPID key and subscriptions in the database (C6).
   - RFC 8030, 8291 and 8292 against their test vectors; the service worker; channels with a test message.
   - From the ADR's review: one channel per endpoint in a workspace: the same person sending the same subscription again keeps the channel and its confirmation, while a new key, or the device moving to another person, replaces it; errors scrubbed of endpoints before they reach a delivery or the log (Go's URL errors include the URL); and a flag on `allstill serve` that forces shadow mode, for serving a copy.
   - The health endpoint, and `attention` for failed deliveries and a sender that is behind (R11).
   - In the API description: channels, messages and the shadow setting.
4. **Email** through SMTP, as the fallback and for the digest. It must work for anyone who self-hosts, with the mail provider they already have. Built as described below.
   - Settings as other self-hosted services name them, each a flag and an environment variable (the password only a variable), never in the database: `ALLSTILL_SMTP_HOST`, `_PORT` (587), `_SECURITY` (`starttls`; `tls` on ports 465 and 2465; `none` for a relay the operator trusts, with a warning at start when it is public), `_USERNAME`, `_PASSWORD` or `_PASSWORD_FILE` (for container secrets), `_FROM` and `_CA_FILE` (for a relay with a certificate of its own, such as Proton Bridge). Separate settings, not one URL, as the spec now says. Settings that cannot work stop `serve`; at start it also signs in once, without sending, and logs the answer.
   - Allstill's own SMTP client on `net/smtp`'s connection: it refuses to send in plain text when STARTTLS was asked for, needs TLS 1.2, signs in with PLAIN or LOGIN (as Microsoft 365 offers), introduces itself with the dashboard's host name, and has timeouts. No OAuth, so Outlook.com does not work; the docs say so.
   - `allstill admin test-email -to ADDRESS` sends one message with the server's settings and prints the mail server's answers, to get the settings right before anything depends on them.
   - An email channel is an address a person adds under Devices, filled in with their own address, and confirmed by the link in a test message, which only works signed in: that also catches a typo and a spam folder. At most three per person; a test at most once a minute per channel and ten a day per person. A test carries no text a person chose.
   - One message, many deliveries: each delivery records the message it belongs to (migration 0005). `now` goes to every confirmed device; when none accepted it (none confirmed, or each failed for good), the same message goes by email, with the same time, so it does not move the gather period. A digest goes by email, or as a short push when there is no email or it failed. When every delivery of a message failed, its downs are unheard and looked at again in 10 minutes, as when there is no channel.
   - An email that fails for now (no connection, a 4xx answer) is tried again after 1, 5 and 30 minutes. A 5xx answer that the recipient's mailbox does not work (enhanced codes 5.1.x and 5.2.x, but not the sender's 5.1.7 and 5.1.8, nor a "Sender address rejected"; once the server took the recipient, only 5.1.x, as Microsoft 365 answers a login that may not send as the From address with 5.2.0 then) marks that channel as failing; any other 5xx (servers such as Postfix give "Relay access denied" at the recipient too), a refused sign-in or certificate means the installation's settings are wrong, which shows for administrators instead of marking every channel. Pushes go before emails, and after a failed connection the pass's other emails wait for their retry.
   - Plain text in UTF-8, with a Message-ID per delivery that stays the same on a retry, `Auto-Submitted: auto-generated`, `X-Auto-Response-Suppress: All`, no line breaks from check names in headers, and no job output (C8). The digest email is the whole digest; its push is one line.
   - A push service that accepted a message has not shown it: a phone that is off never makes email follow. The docs say so.
5. **Switch off shadow mode**, and the first checks at Healthchecks: the nightly backups.
   - First settle R10. Today the digest leaves out what was told only in shadow mode, but R10 says it lists it. An incident opened in shadow mode whose down still waits would be alerted for real.
   - A failed delivery for want of a channel recurs every 10 minutes while its incident is open: prune or fold them.
   - Settled in step 4: a down that reached no channel at all counts as unheard, and a recovery waits while its down is on its way.
