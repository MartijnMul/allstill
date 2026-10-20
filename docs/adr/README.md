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
