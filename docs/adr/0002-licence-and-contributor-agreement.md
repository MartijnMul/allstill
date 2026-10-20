# 0002. AGPL for Allstill, Apache-2.0 for its API, with a contributor licence agreement

- Status: Accepted
- Date: 2026-10-21

## Context

Allstill is meant for anyone who wants to know whether their jobs and sites still run, on their own server: individuals, small companies, freelancers who watch their customers' systems. [ADR 0001](0001-stack-and-architecture.md) left the licence to this record and stated the intent.

What the licence has to do:

- **Anyone can run it themselves, for free,** including companies for their own checks and for their customers'.
- **People contribute and enjoy doing so.** That means a licence developers know and trust, and calling it open source honestly.
- **Improvements stay open.** Someone who offers a changed Allstill as a service shares their changes.
- **Others can build on the API freely.** Scripts, integrations, dashboards and other clients should not need to worry about the server's licence.
- **Its owner keeps the option** of offering a hosted Allstill later, with features of its own, should demand ask for it.

The options considered:

- **MIT or Apache-2.0 for everything.** The easiest for contributors and integrators, but anyone can turn Allstill into a closed hosted service and keep their improvements. Healthchecks itself is BSD-licensed, and hosted competitors exist.
- **AGPL-3.0 for everything.** Improvements to the server stay open, but a client library generated from the API would be AGPL too, which puts off people who only want to call the API from their own code.
- **AGPL-3.0 without a contributor licence agreement.** Once others have contributed, the owner could use their code only under the AGPL, so a hosted version with closed features would be impossible.
- **A Developer Certificate of Origin instead.** Lighter for contributors, but it grants no right to relicense, so it has the same limit as no agreement at all.
- **Source-available licences** (Functional Source License, Elastic License 2.0, Business Source License). They forbid others to offer Allstill as a service, but they are not open source, which works against the community this decision is for.

## Decision

1. **Allstill is licensed under the GNU Affero General Public License, version 3 only** (`AGPL-3.0-only`): the server, the CLI and probe, the dashboard, and the macOS app. `LICENSE` at the root holds the AGPL text unchanged.
2. **The API description and the client libraries generated from it are licensed under the Apache License 2.0** (`Apache-2.0`): the OpenAPI document and generated clients, each in its own directory with its own `LICENSE`. Code written against the API, in any licence, needs nothing from the AGPL.
3. **Every source file starts with an SPDX identifier** (`SPDX-License-Identifier: AGPL-3.0-only` or `Apache-2.0`), so the licence of a file is clear without looking up its directory.
4. **Allstill is owned by Martijn Mullié personally.** Contributors sign the [contributor licence agreement](../../CLA.md) once, before their first contribution is merged. They keep the copyright; they grant Martijn Mullié a licence to use their contribution under any terms, including the AGPL, Apache-2.0 and a commercial licence.
5. **The name Allstill is Martijn Mullié's.** Forks are welcome under another name.
6. **Section 13 of the AGPL is made easy to follow:** every page the server serves links to the source of the running version, from a setting (`ALLSTILL_SOURCE_URL`) that defaults to the official repository. A changed version only has to point it to its own source.

## Consequences

- Allstill may be called open source. Anyone may run it unchanged for any purpose, commercial included, without obligations towards the people who use it.
- Anyone who runs a changed Allstill must offer the source of their version to everyone who uses it over the network, including people who only see a status page.
- Integrations, scripts and other clients can use the Apache-licensed API description and clients without the AGPL applying to them.
- Every dependency must have a licence compatible with its part: the AGPL for the server and the app, Apache-2.0 for the API description and generated clients. Today that rules out nothing ADR 0001 names.
- A contributor licence agreement puts some people off. Asking for it once, through a check on the first pull request, keeps that small. Without it, the hosted option is lost for good, because contributions cannot be relicensed afterwards.
- The CLA check is set up when the repository is published. Its signatures are kept in the repository itself, not with an outside service.
- The name needs protecting: a trademark registration and a short policy on using the name, before anyone else offers Allstill under that name.

## Not decided here

- A hosted service, its terms and any features of its own.
- The trademark registration itself.
