# ADR-0054 — ContentDB schema changes migrate or refuse

- **Status:** Accepted
- **Date:** 2026-09-01
- **Related:** [ADR-0018](0018-contentdb-engine-and-encryption-at-rest.md) (the
  encrypted ContentDB), [ADR-0019](0019-one-authoritative-ledger-disposable-projections.md)
  (the authoritative ledger), and AD-8 (one owner per behaviour).
- **Supersedes:** the schema-lifecycle passages in
  [ADR-0021](0021-secrets-in-the-prompt.md).

## Context

ADR-0021 was written when ContentDB was local and disposable. In that setting, a
schema change could rebuild the store and report the discarded-row count. The
record was correct for that moment, including its conclusion that there was no
migration to write.

That premise expired when `content.db` became durable encrypted storage whose
contents must survive an application version change. Rebuilding is no longer an
acceptable schema protocol, and the product no longer has a discard report to
show. The old record remains valuable because it explains why rebuild-and-report
was chosen; it needs a current record beside it so its historical claim is not
mistaken for today's behaviour.

This decision concerns only the ContentDB schema lifecycle. It does not change
ADR-0021's decisions about masking durable command text, redaction segments, or
vault-secret resolution.

## Decision

ContentDB uses an ordered migrate-or-refuse ladder.

- The current schema is created from `schemaV1`.
- Each supported schema edge has one explicit migration rung, and the rung moves
  the schema stamp only after its change commits.
- A file at an older supported schema is advanced through those rungs in order.
- A file stamped with a newer schema is refused rather than rebuilt or judged by
  an older binary.
- A schema change must add its rung and its shape guard in the same change. The
  ladder validation and shape checks are part of the startup protocol, not a
  convention left to a release checklist.

The refusal is an actionable product error: it names the schema situation and
tells the person which build can open it. There is no rebuild path and no
"history was discarded" surface in the current system.

## Rationale

Rebuild-and-discard was safe only while the store's contents were disposable.
Once ContentDB became durable, that operation would turn a version change into
loss of the record it exists to keep. A ladder makes each supported boundary
explicit, so the code that changes a shape is also the code that carries that
shape forward. Refusal is the safe answer when the build cannot establish that
it understands the file.

The old ADR is not rewritten. Its schema statement records a real decision made
under a different storage premise; this ADR supplies the superseding lifecycle
rule and the pointer makes the relationship discoverable.

## Consequences

- Future schema changes require an ordered rung, a shape check, and tests for the
  edge's commit and refusal behaviour.
- An older build does not silently reinterpret a newer ContentDB shape, and the
  current product does not describe a rebuild that it cannot perform.
- ADR-0021 remains the authority for prompt masking, redaction metadata, and
  secret resolution. Only its schema-lifecycle passages are superseded here.
