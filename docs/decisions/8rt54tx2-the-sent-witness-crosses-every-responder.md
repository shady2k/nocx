# ADR-8rt54tx2 — The sent witness crosses every responder, and a running block keeps its body

- **Status:** Accepted (owner review of PR #789, round 3, 2026-11-10, `nocx-4y82c.1.18`)
- **Date:** 2026-11-10
- **Refines:** [ADR-y45sp41c](y45sp41c-a-stored-row-read-is-a-page-with-a-slot.md) and
  [ADR-ywzcpbqe](ywzcpbqe-a-blocks-rows-are-a-set-of-ranges.md). It adds no wire change.
- **Related:** ADR-2yzsq8mk (nothing moves under a reader), `nocx-2v80t.3.7`.

## Context and problem

Three defects survived the range model.

The stored-row slot is held until the RESPONSE has left the socket, but the witness
travels through the optional `TryResultSent` method. Every production handler is built
with a `sealedNormalizer`, and that wrapper forwarded only the three base Responder
methods; the handler's optional-interface assertion therefore failed and released the slot
at enqueue. The rule existed only in tests whose responder implemented the method directly.

A running block larger than the lazy threshold was reset on reconnect like a frozen block.
The live painter reserves nothing beyond its loaded rows (`reserve:false`), so with the
lines removed it painted no body at all and removed `.cmd-output`. Its height collapsed and
the demand loop skipped it (no body to measure), while unchanged metadata passed
revalidation and scheduled no refresh. The block stayed blank.

A stored-row page replaces a spacer with painted rows. The reader's anchor was a path plus
a class signature, and every spacer has the class `cmd-output-spacer`, so after the swap it
could land on another spacer instead of the rows the person was reading.

## Decision

1. **The sent witness crosses the production responder chain.** `sealedNormalizer` forwards
   `TryResultSent` to the underlying responder (or, for a responder without it, calls
   `TryResult` and then the witness). The slot test runs through the normalizer, so the
   production shape — not a hand-built responder — is what proves the rule.
2. **A running block keeps its body and its reservation across a reconnect.** The reset
   before a page-read applies only to a sealed block. An open block keeps its loaded rows,
   its `reserve:false` body grows from them, and the reconnect's metadata comparison
   refreshes any growth. The painter keeps an output element whenever the block has
   geometry, even with no lines yet, and the demand loop measures the block when no body is
   present, so demand never depends on an element the painter may remove.
3. **A spacer has a logical row.** Each spacer carries `data-logical-line` for the stream
   row it starts at. The reader's anchor treats a spacer as a row and restores by that
   line; when a page replaces the spacer with painted rows, the same line is the row the
   reader keeps.
4. **An open artifact's metadata carries its geometry.** `executionsFor` enriches a rows
   artifact that has no retained ranges — an open block, or a payload written before the
   fields existed — by reading its chunks and putting `retainedRanges`, `endRow`,
   `firstRow` and `rowCount` into the payload `ledger.get` returns. A reader therefore
   reserves an open block's real runs from the first metadata read, and the first page
   cannot reveal an evicted middle that the reservation had invented as kept.
5. **One block index is the lookup every door uses.** A map from an entry id to its
   element (and live record) is written where a block is bound to an attempt and where a
   restored block is built — top-level or nested under a turn, with rows or still empty.
   The loader, refresh, revalidation and settle look a block up there instead of walking
   the transcript or a list of live records. The block manager reports the elements it
   removes (clear-all, an erase boundary), the freeze re-points the entry at its new
   element, and a stale entry is dropped when its element is no longer connected — so the
   index stays bounded through churn.
6. **Reconnect revalidates restored blocks, and refresh reads fresh geometry first.**
   `_restoredRows` is a reconnect source alongside the live block records; a restored
   block's refresh paints through its own element, never through `applyStoredRows` (which
   parks rows in a queue only a live block flushes). The geometry read by revalidation is
   passed into the refresh, and an open block with no such geometry reads it before
   choosing a window. A block cached below the lazy threshold that crossed it during the
   outage is therefore fetched as a page, not as a whole body.

## Consequences

- No schema, protocol version or wire change: the responder method and DOM attributes are
  internal seams.
- The demand loop's fallback rect is the block when `.cmd-output` is absent; a block with
  geometry but no body is still a place in the transcript.
- A spacer's `data-logical-line` is its first stream row, not a count, so a spacer split or
  replaced by rows keeps one identity.
- Restored blocks and live block records are one reconnect set, but they have different
  paint doors: a record goes through `applyStoredRows`, an element through
  `paintStoredRows`.
