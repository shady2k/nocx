# ADR-40d9xt5n — A block's cached rows follow the retention they were accepted under, and one entry has one read owner

- **Status:** Accepted (implementation of `nocx-4y82c.1.24`, 2026-11-12; records the
  scheduling decisions PR #796 made where ADR-y45sp41c §3 and ADR-ywzcpbqe left them open)
- **Date:** 2026-11-12
- **Refines:** [ADR-y45sp41c](y45sp41c-a-stored-row-read-is-a-page-with-a-slot.md) §3 (the
  renderer's priority queue) and [ADR-ywzcpbqe](ywzcpbqe-a-blocks-rows-are-a-set-of-ranges.md)
  (a block's rows are a set of ranges). Both keep their decisions: the page, the slot and the
  range model stand. This record settles who orders two reads of ONE entry, and what a merge
  of two accepted reads may keep.
- **Related:** [ADR-4jz81kfa](4jz81kfa-a-cut-in-a-commands-output-is-stated-in-its-history-not-in-a-card.md)
  (a cut is a line in the block's own history), `nocx-2v80t.3.7` (streamed block rows).

## Context and problem

ADR-ywzcpbqe made the set of retained ranges the truth about a block's body: the store keeps
a head run and a tail run of a capped artifact, and the painter reserves the height those
runs add up to. The renderer kept its own cache beside that truth, and joined a new read to
it by row index alone. Two defects followed, and neither is a tuning problem.

The cache could only grow. Every growth notification reads the newest page, and the merge
appended that page to whatever was already cached without asking whether the store still
held the rows underneath. A block that crossed the retention cap in a single command
therefore cached every page it had ever read: a 50,000-row command held 49,950 rows in the
renderer while the store kept 400, painted all of them, and offered all of them to selection
and copy. A capped block's body is the one shape where the cache and the geometry disagree,
and the disagreement was invisible until it was huge.

Two reads of one entry also ran independently. A viewport page and a growth refresh each
started their own request and each painted whatever came back, so a page dispatched against
older geometry could land after a refresh that already applied newer geometry and restore
evicted rows and an older span. A response belonging to a pane that had since rebound could
paint into its replacement, and the freeze path waited on a chain that a page was not part
of.

## Decision

**Cached rows are clipped to the retention of the read that was accepted.** A merge keeps a
previously loaded row only when the accepted read's retained ranges still contain it; the
store owns retention and a page never extends it. An explicitly empty range set is full
eviction, not a missing one, and it evicts every cached row. This is the only rule that
keeps a capped block's cache equal to its geometry, and it makes the block's painted rows,
selection snapshot and copy text agree with the spacer-and-notice body by construction.

**One entry has one read owner.** Pages and growth refreshes of an entry share the existing
per-entry chain (`_blockRowsInFlight`). A request that arrives while the chain is busy is
recorded as demand, never dispatched beside it, and the completion recomputes from the
viewport rather than replaying an obsolete window. Only the promise that is still the
entry's owner may paint or release it, so a rebind, a reconnect or a disposal that clears
ownership leaves the old completion with nothing to write.

**Scheduling alternates the two demands.** A page asked for while a refresh is in flight
queues; when the refresh finishes it yields to exactly one pending page, and that page's
completion carries the queued growth forward before the next refresh. Sustained output
therefore cannot starve a reader: the tail keeps arriving, and so do the rows the person
scrolled to. The existing row-read priority queue (ADR-y45sp41c §3) still orders different
entries; this decision is only about one entry's own sequence.

**Freshness is the durable end within one artifact, and an equal end is not newer.** Loaded
rows are a cache, not a revision, so retention may drop the last loaded row while the
durable span stays put or grows; a smaller cache at an unchanged end is the newer truth, not
an older read. A delivery is older when its end is behind the accepted end of the same
artifact, or when its output mark is behind the accepted one (which would restore an echo
prefix the read already dropped). A replaced artifact is accepted whatever its span: its
rows share no index with the cache it replaces, so the old cache is not evidence about it.

**Revalidation compares the cache's own identity.** A reconnect's metadata read is compared
with the artifact the cache holds, not with the metadata's own id, and a metadata answer
whose cache snapshot changed while the read was in flight is not reused as a hint for the
refresh it submits.

## Considered alternatives

**Bound the cache by a row count.** Rejected: the store's bound is bytes, and a row-count
bound in the renderer would drift from it for every command whose rows are wider or narrower
than the guess. The retained ranges are already the exact answer.

**Keep the union and evict the oldest rows by a timer or a size.** Rejected: both invent a
second retention policy beside the store's, and neither can tell which cached row the store
still holds. The ranges say it without a clock.

**Let a page and a refresh race, and resolve by artifact sequence number.** Rejected: the
renderer would need a revision the wire does not carry, and the store's own answer is that
the durable end and the output mark are what a read reports. Adding a revision belongs to
the revision-consistent-read design, not to this correction.

**Serialize every read of every entry on one queue.** Rejected: it would turn one block's
slow page into every block's delay, and the connection's queue already orders different
entries by what is needed now.

## Consequences

- No protocol change. The decisions are renderer-local, and the store's payload, the read
  parameters and the refusal are untouched.
- A capped block's cache, painted rows and selectable rows now equal the retained rows it
  loaded, so the cost of a long command is the page it shows rather than everything it ever
  printed.
- A page can wait one refresh, and a refresh can wait one page. That is a scheduling delay
  measured in one read, not a dropped request: the demand is recorded and recomputed.
- The freshness check reads the durable end, so a store that reported a decreasing end for
  one artifact would hide the newer read. The store's span only grows; a test that shrinks
  it is testing a shape the wire does not produce.
