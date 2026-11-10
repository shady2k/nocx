# ADR-ywzcpbqe — A block's rows are a set of ranges, and its height is its geometry

- **Status:** Accepted (owner review of PR #789, 2026-11-10, `nocx-4y82c.1.18`)
- **Date:** 2026-11-10
- **Refines:** [ADR-y45sp41c](y45sp41c-a-stored-row-read-is-a-page-with-a-slot.md),
  decisions 1 and 3. It keeps that record's page, slot and retry decisions and replaces the
  loaded-window model beneath them.
- **Related:** [ADR-2yzsq8mk](2yzsq8mk-a-normal-command-is-one-column-and-a-full-screen-program-owns-the-width.md)
  (nothing moves under a reader), ADR-0019 §6/§7, `nocx-2v80t.3.7` (streamed block rows).

## Context and problem

ADR-y45sp41c made a stored-row read a page and reserved a block's height from its row count.
The first implementation modelled what a block held as "the first loaded row .. the last
loaded row". Three defects follow from that model and none of them is a tuning problem:

- Two pages at 0–199 and 4800–4999 paint 401 rows for a 5000-row block. The interior is
  treated as one loss notice, so the block is 4600 rows too short and the transcript below
  moves.
- Coverage asks only whether the viewport row lies between the first and last loaded row.
  Once the tail is loaded, scrolling into the interior satisfies that test and never fetches
  the missing 200–4799 rows.
- A capped block keeps a head run and a tail run. Deriving its last row from `rowCount`
  puts the end at 599 for 600 retained rows of a 5000-row block; the first page then reports
  4999 and the painter grows the reservation, moving the reader.

The same page path also dropped the server's capacity refusal on the floor: the read helper
caught `-32014` and called it unreadable, so the retry queue never saw it. A short page was
accepted as complete. And a reconnect refreshed only blocks without rows, so a small block
that grew while disconnected kept its cached rows forever.

## Decision

1. **Geometry is read from metadata, once.** A closed rows artifact's payload carries
   `firstRow`, `endRow` (one past the whole stream span), `rowCount` and `retainedRanges`
   (the runs the chunks hold, each `[from, to)`). The geometry survives the seal. A
   `ledger.artifact` page reports the same fields for every read, so an open block learns
   its geometry from its first page. A payload written before these fields falls back to one
   run covering `firstRow .. firstRow+rowCount`; it is a legacy shape, not a second model.
2. **Loaded rows are a set of ranges.** Coverage, "is this row here", and "what to fetch
   next" work on loaded ranges. An interior hole is a missing range like any other, so a
   reader that reaches it fetches it even when the tail is loaded.
3. **Height is the geometry, never the pages.** The body is the retained runs in order: rows
   where they are loaded, one spacer whose height is the unloaded row count, and one
   fixed-size notice per gap between runs (and after the last run if `endRow` extends past
   it). A page swaps a spacer for rows of the same height. A cap's gap is one line whatever
   its row count. A live block paints with `reserve:false`: its rows are still arriving and
   its body grows with them; frozen and restored blocks reserve.
4. **The queue owns a refusal and a short page.** `-32014` and a page that came back short
   of the retained rows it asked for while more follow are thrown through to the priority
   queue, which waits the hint and sends its most urgent request. A short page is not done.
5. **Reconnect re-validates every block with cached rows.** One metadata read per block,
   small blocks included, compares artifact id and geometry with what is loaded; a change
   submits that block's refresh. A block with no cached rows is already being read. The
   large block's give-back is observable on the DOM (`data-rows-reset`) so a test waits for
   it rather than measuring a state the attachment may not have produced yet.

## Consequences

- `ledger.artifact`'s result and the rows payload carry `endRow` and `retainedRanges`;
  `ProtocolVersion` moves to 25 and the surface golden is regenerated. The block-rows
  contract's summary definition gains both fields.
- The painter no longer decides height from the loaded lines. Its children are a reconciled
  segment list, so a page fills space that was reserved for it.
- A capped block's height is retained rows plus one notice per retained gap, not its
  absolute stream span. The notice is the same fact the block already stated at the cut,
  now fixed before any page arrives.
