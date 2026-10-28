<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent: watch sites and certificates, not only jobs that ping

Author: Martijn Mullié (maintainer)
Status: accepted
Started: 2026-10-28

## Problem

Allstill only knows what jobs tell it. A website, an API or a health endpoint does not ping, so today it takes a second tool, or a cron job that fetches an address and pings Allstill when the answer is good. That script is one more thing to write, keep running and watch.

What goes wrong without it is real. A public site was unreachable for months, and someone outside found it by chance. A health endpoint can answer 200 while its body says "degraded". A site may work at its main address while the www variant, or the redirect to it, is broken. And a TLS certificate expires without a word, until visitors see a warning instead of the site.

## Proposed outcome

- Allstill fetches an address itself, on a schedule, and judges the answer: the status codes that count as up (a redirect can be healthy), and where it matters a field in a JSON body, such as a health endpoint's status.
- Every hostname that matters can be watched, including the variants that should redirect.
- A TLS certificate is watched for its expiry, with a warning at least 14 days ahead, and more urgency as the day comes closer.
- A port that should be open, such as a mail or database server, can be watched too.
- Addresses inside a network the server cannot reach are checked from inside it, by a small Allstill probe there.
- Such a check behaves like any other: it shows on the overview, opens an incident when it goes down, and alerts as calmly as the rest. One failed request is not an outage.
- Success: the cron scripts that only fetch an address and ping are replaced, no separate uptime tool is needed, and a certificate never expires unannounced.

## Affected users and systems

- People who run sites, APIs and services for themselves, customers and a home lab, often on shared hosts.
- The Allstill server, which now makes requests to addresses people choose.
- The networks Allstill runs in: public addresses are checked from outside, internal ones need to be reachable from where the check runs.
- The overview, incidents and alerts, which take these checks as they take ping checks.

## Constraints

- A server that fetches addresses people choose must not become a way into its own network (ADR 0001): private addresses are refused unless an administrator allows them.
- Calm, as for alerts: a single failed request, a slow answer or a short blip does not alert.
- Green is proven: when Allstill cannot run a check (its own outage, its network down), the check is unknown, not down.
- One binary and one SQLite file; no external service to run the checks.
- What a check fetches may hold secrets (a token in a URL, a header): they are not logged or shown to people who may not see them.

## Out of scope

- Checks that run a browser, follow several steps or log in through a form.
- Response times as a metric to graph, and service-level reports.
- Checks on the body of a page beyond one JSON field, such as a word in HTML.
- Status pages for outsiders.

## Open questions

- Do some health endpoints need a header or a token, and where would that secret live?
- How often should a check run by default, and how many failures in a row make it down?
- Is a certificate a check of its own, or part of an HTTP check on the same host?
- Should a certificate's warning go in the digest, and only its last days alert at once?
