# Architecture decision records

One short file per decision that is hard to reverse: the stack, storage, external services, the security model. The goal is that someone joining later can see why Allstill is built this way, not only how.

## How to add one

1. Copy the template below to `NNNN-short-title.md`, numbered after the last record.
2. Fill it in and commit it together with the change it describes.
3. A record is never rewritten after it is accepted. To change course, write a new record and set the old one's status to `Superseded by NNNN`, or name the parts it replaces when only some of it changes.

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context

What forces the decision: the problem, constraints, and options considered.

## Decision

What we chose, in one or two sentences.

## Consequences

What becomes easier, what becomes harder, and what we now have to look after.
```

## Records

- [0001. Stack and architecture](0001-stack-and-architecture.md): Accepted, 2026-10-20
- [0002. AGPL for Allstill, Apache-2.0 for its API, with a contributor licence agreement](0002-licence-and-contributor-agreement.md): Accepted, 2026-10-21; decides the licence left open in 0001
- [0003. Web Push keys and subscriptions live in the database](0003-web-push-secrets.md): Accepted, 2026-10-27; adds to 0001's exceptions to hashing
- [0004. What a check fetches: its URL as a secret, and redirects](0004-what-a-check-fetches.md): Accepted, 2026-10-28; amends 0003, and adds check URLs to 0001's exceptions to hashing
- [0005. Teams own projects, and the administrator is not shown them](0005-teams.md): Accepted, 2026-11-19; replaces parts of 0001's section on workspace, projects, people and keys, and lists them
