<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Intent

One folder per change that is big or hard to reverse: `intent/<slug>/`. It holds the chain of documents the change goes through, each read by the next step:

1. `intent.md`: what is wanted and why, in the maintainer's words. No design.
2. `spec.md`: the requirements and the design, with the concerns the maintainer decides before anything is built.
3. `plan.md`, when useful: the order of work.

The maintainer writes or confirms the intent, decides the concerns in the spec, and sets `Status:` (draft, accepted, superseded or closed). Claude drafts; nothing is accepted without the maintainer. Small changes need no folder: a commit message is enough.

Everything here is public, like the rest of the repository: needs from real setups appear as generic cases with invented examples. An accepted design that is hard to reverse also gets an ADR in `docs/adr/` when it changes one.
