<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Spec: watch sites, ports and certificates

Intent: [intent.md](intent.md)
Status: accepted (2026-10-28, with the recommendations of C2 to C10)
Written: 2026-10-28, by Claude for the maintainer; revised the same day after an independent review
Codebase: MartijnMul/allstill at 2d787c2
Policies in force: no skills yet; ADR 0001 to 0004, AGENTS.md, REVIEW.md, `api/openapi.yaml` 0.5.0, the alerts spec, and needs from real setups (kept privately)
Prompt: the requirements-and-design prompt of the AI-native SDLC playbook, run on intent.md

## Summary

The intent asks that Allstill look at addresses itself: a site or an API over HTTP, a port that should be open, and the TLS certificate in front of either, with a probe for networks the server cannot reach.

This spec adds checks that Allstill runs instead of waiting for a ping. Each attempt becomes a run, and from there what is already built applies: states, incidents, calm alerts, the digest. The server runs them through the client that keeps out of its own network; a probe runs the ones inside another network and reports back. Certificates are not a check of their own: every TLS connection a check makes reads the certificate.

It flags how many failures make a check down, how Allstill tells its own network's outage from a site's, how certificates warn (short-lived ones too), how a probe stays within the projects it serves, and where secrets in a request would live.

## Requirements

Functional
- R1. A check can be an HTTP check or a TCP check. (Proposed outcome)
  - HTTP: a URL, GET or HEAD, the status codes that count as up, and at most one field in a JSON body that must equal a value. Optionally where a redirect must lead (its `Location`).
  - TCP: a host and a port, optionally with TLS.
- R2. A check runs every minute by default, at most once a minute and at least once a day, with a timeout of at most 60 seconds, by default 10. Each attempt is a run with its outcome, duration, reason and what it measured: the status code, the certificate's days left. (Proposed outcome)
- R3. A check goes down only after a number of failed attempts in a row, by default 2, and up on the first attempt that passes. The reason says what failed in plain words: "HTTP 503", "no answer within 10 s", "status is degraded instead of ok", "certificate expired", "blocked by a bot challenge (Cloudflare)", "rate limited (HTTP 429)". (Constraints: calm)
- R4. Every TLS connection a check makes reads the certificate the server proved it has (the verified chain). The check is a warning when less than 14 days are left, or less than a third of the certificate's lifetime when that is shorter; an attempt fails when less than 3 days are left (or a tenth of the lifetime), the chain does not verify, or the host name does not match. Both can be turned off per check. (Proposed outcome)
- R5. Redirects are not followed unless the check says so; then at most 5, each to an address that may be reached. (Proposed outcome: "a redirect can be healthy"; ADR 0004)
- R6. A probe is `allstill probe`, run inside a network with a key. It connects out to the server, fetches its checks, runs them and sends the runs back; it opens no port. A check is run by the server, or by one probe. (Proposed outcome; ADR 0001)
- R7. When the server or a probe cannot run a check, because it is down or its own network is, and Allstill can tell (a probe always can; the server when an administrator gives it a canary, see C4), the check becomes unknown, not down, until a run judges it, and nobody is alerted for it; an open incident stays open meanwhile, with no recovery message. A probe that has not been heard from for 3 minutes is down itself, as a check of its own, and alerts like one. (Constraints: green is proven)
- R8. A check is added and changed with `allstill admin add-check` and `set-check`, and shows on the overview and its page like a ping check, with its last runs, their duration and reason, and the certificate's days left. (Proposed outcome)
- R9. The digest lists checks in warning, so a certificate's coming expiry is read in the morning, not pushed at once. This extends the alerts spec's R8. (Open question)

Quality
- R10. Every request the server makes goes through the client of ADR 0001 and 0003: addresses checked as they are dialled, the server's own addresses refused, redirects only as R5 allows. Checks use a transport of their own that keeps no connection for the next attempt, so each attempt resolves, connects and shakes hands anew. An administrator can allow private ranges for the server (`-allow-private`), which opens them to every editor of every project; a probe allows only the ranges given on its own command line. (Constraints; ADR 0001)
- R11. A request carries `User-Agent: Allstill/<version>`, with a contact address only when the administrator sets one. It reads at most 1 MB of a body, and only when a JSON field is to be judged. Attempts start spread over their interval, at most one at a time per host, and at most 20 at once per server or probe.
- R12. A URL is a secret, as a ping URL is: owners and editors of the check's project and workspace administrators see it whole, in the dashboard and through a token of theirs with the `manage` scope, as does a project's API key with that scope; everyone else, viewers and `read` tokens included, sees only its scheme and host, and no ping URL at all (today every member sees ping URLs). The same holds for an expected `location`. It is never in logs, reasons or messages, nor is a `Location` it was sent to. (ADR 0004)

Compliance and privacy
- R13. A probe belongs to the workspace and serves the projects a workspace administrator gives it; only checks of those projects can be assigned to it. Its key is stored as a hash, shown once, and can be replaced. (ADR 0001)
- R14. The API description gains the new check kinds, their settings and runs, and the probe's two endpoints, in the change that builds them; the unused `certificate` kind leaves it, and `CheckKind` tells clients to expect new kinds. A check's `url`, `location` and `ping_url` are returned whole only with the `manage` scope held by someone who may see them whole (R12), which the description says for each. Removing a value is allowed only because the API has no clients outside this repository yet, as for the alerts spec's C9. (ADR 0001)

## Design

**Checks.**
- Migration 0007 rebuilds `checks` (the migrations copy the database first and check the foreign keys before they commit). It drops the `kind` constraint, so a new kind needs no rebuild again: kinds are checked in code, as 0001's header already says of the settings in JSON. It adds `fail_streak`, the failed attempts in a row.
- `runs` gains a `reason` column (no rebuild).
- What a check fetches lives in `checks.settings`, as the API's `HttpCheck` describes, plus `location`, `follow_redirects`, `fail_after`, `warn_days`, `fail_days`, `ca` and `no_verify`. A TCP check has `host`, `port` and `tls`.
- A ping to an HTTP or TCP check is not applied: it is answered 409, "this check is run by Allstill", so a script that pings with `curl -f` notices.

**What the state machine learns.** (C2)
- An `OnAttempt` beside `OnPing` and `OnDeadline`. A passed attempt is up (or warning, for a certificate) and clears `fail_streak`; a failed one adds to it and is down from `fail_after` on, with its reason. Below `fail_after`, the state stays as it was, with the failure in the last runs. An attempt that cannot judge neither adds nor clears, so a site that was down before an outage of Allstill's own network is down again on the first failure after it.
- An active check is never late: its deadline is the next attempt plus a grace of 2 minutes or the interval, whichever is longer, and a missed deadline makes it unknown ("not run since 14:02"), not down. Unknown has no deadline; the next run decides.
- An attempt that could not judge the target (below) is not a run. It makes the check unknown at once, whatever it was, with the reason ("not judged: the server's network was down"); the next run that judges decides.
- For an HTTP or TCP check, unknown holds an open incident open: it resolves only once the check has been up (or warning) for the settle period. Otherwise a probe going quiet while a site is down would send "recovered" for a site nobody can see. This extends the alerts spec's R3, which counts unknown as settled for ping checks.
- A request that times out is a failed run with its reason, not the `timeout` outcome, which is for a ping check's run left open.

**Who runs a check.**
- The server, inside `allstill serve`, or a probe. Each keeps its checks with their next attempt and one timer for the earliest, as the scheduler does, starting each at a fixed offset within its interval (from the check's id).
- An attempt is recorded through the store as a run, in one transaction per tick, so ping ingest is not held up.

**Telling a target's outage from one's own.** (C4)
- An error that proves the network works counts against the target: any HTTP answer, a refused connection, a TLS error, a host name that does not exist.
- An error that could be either (a timeout, no route, an unreachable network, a resolver that does not answer) is checked against a canary, an address independent of the targets:
  - for a probe, its own post to the server;
  - for the server, the addresses an administrator gives with `-canary` (such as a public DNS server's port 53).
- When the canary cannot be reached either, the attempt cannot judge the target: the check becomes unknown, and the server or probe shows itself offline (`attention` and `/health` for the server, its own check for a probe). Otherwise it counts against the target.
- Without a canary, Allstill leans towards alerting: every such error counts against the target.

**Probes.** (C6, C7)
- `allstill admin create-probe NAME -project P [-serves Q,R]` makes a probe, prints its key once, and creates its own check in project P, which every post pings. `rekey-probe` replaces the key.
- `set-check -probe NAME` assigns a check, when the probe serves its project.
- Under `/api/v1`, with the probe's key as bearer:
  - `GET /probe/checks` returns its checks with their settings, the protocol version and the server's time.
  - `POST /probe/runs` takes a batch of runs, each with an id unique to the probe and how many seconds ago it ended.
- The server stamps each run with its own clock, ignores an id it has seen, and drops a run older than the last one it applied for that check.
- A probe whose clock is more than a minute off the server's says so, and its checks become unknown until it is set right: certificates are verified and measured against its clock. A probe of another protocol version is refused with what to update.

**Certificates.** (C3, C5)
- Each attempt over TLS measures the time the verified chain has left, in hours, shown as days; the thresholds compare hours, so a short-lived certificate's 14 hours are measured as such.
- A certificate signed by an internal CA verifies with `ca` (a file of certificates, per check, or `-ca` on a probe). `no_verify` skips verification for a self-signed one, and still reads its expiry.
- When `add-check` makes a second check on the same host and port, it says the certificate is watched already, and how to turn its warnings off there (`-warn-days 0`).

**Adding checks.** `allstill admin add-check -project P -name N -http URL [-expect 200,301] [-location URL] [-json /status=ok] [-every 1m] [-follow] [-probe NAME]`, or `-tcp host:port [-tls]`. It refuses an address that may not be reached, with what to do (allow the range, or use a probe), and warns when HEAD is asked of a server that answers it with 405.

## Cases played through

1. **A public site goes down at night.** Checked every minute, down after 2 failures: at 03:02 an incident opens, held for quiet hours unless the check is critical, as now. It is back at 03:40; the incident settles at 03:50, and the digest says it happened.
2. **A health endpoint says "degraded".** HTTP 200 with `/status` `degraded`: the attempt fails with "status is degraded instead of ok", and after the second the check is down.
3. **www and the apex.** `https://www.example.com` expects 301 to `https://example.com/`, and `https://example.com` expects 200. When the redirect breaks or points elsewhere, only the first goes down. Both may present the same certificate; to have it warn once, its warnings are turned off on one of them (`-warn-days 0`).
4. **A certificate runs out.** A 90-day certificate: on day 13 before expiry the check turns warning and the next digest lists it. Not renewed, 3 days before it fails and an alert goes out. A 6-day certificate warns under 2 days and fails under 14 hours, so its renewals stay quiet.
5. **A shared host dies.** Five sites on it: connections are refused or time out while the canary answers, so all five go down, five incidents, gathered into two messages (the alerts spec's case 3). Phase 2's dependencies make it one.
6. **Cloudflare is down.** Every site behind it answers 5xx or not at all, while the canary answers: they go down. That is an outage of those sites, as visitors see it.
7. **Allstill's own network drops.** Every attempt times out, and so does the canary (one is set): the checks become unknown, the overview and `/health` say the server is offline, and nobody is alerted for twelve sites. The outside watcher of `/health` tells about Allstill itself.
8. **The server has no IPv6.** An IPv6-only host cannot be reached while the canary (over IPv4) answers: it goes down with "no route to the address". The reason points at the server's network; the check moves to a probe that has IPv6.
9. **A service inside a customer's network.** A probe there serves that customer's project. Its network goes down: its posts stop, its own check goes down after 3 minutes and alerts once, and its checks turn unknown.
10. **An editor of another project tries the customer's probe.** `set-check -probe` refuses: the probe does not serve their project.
11. **A probe retries a batch after a lost answer.** The server has seen those ids and ignores them, so no failure counts twice.
12. **A self-signed certificate in a home lab.** With `-no-verify`, the check is up, and still warns when the certificate's end comes near.
13. **A site that blocks unknown visitors.** It answers 403 with a Cloudflare challenge. The reason says so; its owner allows Allstill through, or the check is not the right tool for that site.
14. **A mail server.** A TCP check on `mail.example.com:993` with TLS: down when the port is closed, warning when its certificate runs low. STARTTLS on 25 and 587 is not covered.

## Areas of concern

Decided by the maintainer on 2026-10-28: every recommendation below, including what C9 changes for viewers.

- **C1. A probe in this step.** Decided by the maintainer on 2026-10-28: the probe comes now, with the server's allowed ranges as well.
- **C2. When a check is down.**
  - Every minute and down after 2 failures means an alert about two minutes after a site breaks, and none for a single lost request. Once a minute is also the least the schema allows; 30 seconds would double the load for 30 seconds gained.
  - With `-keep-runs` at its default of 1000, a check run every minute keeps about 16 hours of runs.
  - Recommendation: those defaults, adjustable per check (`-every`, `-fail-after`).
- **C3. A certificate is not a check of its own.**
  - Every check that speaks TLS already holds the certificate; a separate check would fetch the same host twice and be configured twice. The cost: several checks on one host warn about one certificate, which `add-check` points out.
  - Recommendation: measure it in every TLS attempt, and drop the `certificate` kind from the API description (it has no clients yet).
- **C4. Allstill's own outage.**
  - A server or probe whose network is down sees every target fail. Without a way to tell, it alerts for every site, which breaks "green is proven". A rule on how many fail at once would hide the most common real outage, a shared host dying.
  - Recommendation: the error's kind plus a canary, as above. Without a canary, lean towards alerting, and say in the docs that a canary keeps Allstill's own outage quiet.
  - This departs from the intent's "green is proven" for a server without a canary: there, its own network going down alerts for its checks. A canary chosen by default would be an outside service Allstill depends on; ADR 0001 names none beyond the channels an alert leaves by, and promises no external accounts.
- **C5. How a certificate warns.**
  - Recommendation: in the digest from 14 days (or a third of its life), at once from 3 days (or a tenth), from the verified chain. An automatically renewed certificate never reaches the digest, even a short-lived one. This departs from the intent's "at least 14 days ahead" for certificates that live less than six weeks, which would otherwise always warn.
- **C6. A probe's reach.**
  - A probe sits inside one party's network. Any editor who can assign checks to it can make it fetch from that network and read the answers.
  - Recommendation: a probe serves the projects an administrator names; its own check lives in one of them, whose people are alerted when it goes quiet. `-allow-private` on the server stays server-wide, and the docs say it opens those ranges to every project.
- **C7. The probe's protocol.**
  - Polling every 30 seconds is simple, works through any firewall that lets HTTPS out, and doubles as its sign of life. Runs carry ids and ages, so a retry or a wrong clock cannot move a check.
  - Recommendation: polling with an ETag, a protocol version, and runs in batches at least every 30 seconds.
- **C8. Secrets in a request.**
  - Unknown yet whether health endpoints need a header or a token. A token in a URL works and is a secret (R12), but lives in the database in plain text, as ping URLs do. Headers with secrets would be the first third-party secret, which ADR 0003 ties to encryption at rest.
  - Recommendation: no headers in this step; tokens in a URL only. Headers come with the ADR on encryption at rest, when a real endpoint needs one.
  - Decided by the maintainer with [ADR 0004](../../docs/adr/0004-what-a-check-fetches.md), which amends ADR 0003: a check's URL is stored as a ping URL is, and checks may follow redirects with every hop checked.
- **C9. URLs for viewers.**
  - Viewers are often outsiders who only need a status, yet today they see ping URLs whole.
  - Recommendation: viewers see only a URL's scheme and host, and not a ping URL at all; editors see both whole. This changes what viewers see today.
- **C10. Checks from the command line first.**
  - There is no form for any check in the dashboard yet; ping checks come from imports and `create=1`.
  - Recommendation: `admin add-check` and `set-check` now; the dashboard form and the JSON API to manage checks follow together.

## Open questions from the intent

- **Headers or tokens:** not in this step; tokens in a URL are secrets (C8).
- **How often, and how many failures:** every minute, down after 2 (C2).
- **A certificate as its own check:** no, part of every TLS check (C3).
- **The certificate's warning:** in the digest from 14 days, at once from 3, shorter for short-lived certificates (C5).

## Out of scope

- **Inherited from the intent:** browser checks and several steps, response times as graphs and reports, words in an HTML body, status pages.
- **Also left out:**
  - headers and bodies for a request, and methods other than GET and HEAD;
  - STARTTLS on mail ports, ICMP ping and DNS checks;
  - checks run from several places to agree on an outage;
  - dependencies between a probe or a host and its checks (phase 2), beyond the probe's own check;
  - a form in the dashboard (C10).
