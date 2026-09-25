# ADR-0075 — An overflowing row stream ends the block in flight incomplete, and the buffers are the person's

- **Status:** Accepted
- **Date:** 2026-09-26
- **Decided by:** the owner, 2026-09-25 (recorded on `nocx-2v80t.3.36`)
- **Supersedes:**
  - [AD-10](../architecture.md), the nocx-k6p18.3 amendment's sentence "the recording records the
    hole rather than stopping at it", **for the block row stream only**. The helper's output
    window, the bytes a reattaching reader asks for, keeps the rule as written: discard the
    oldest bytes, state the gap, go on.
  - [ADR-0074](0074-a-command-boundary-is-settled-by-an-event-never-by-a-timer.md), decision 3's
    list of the events that settle a missing sighting. That list gains one more: the row
    stream's overflow marker.
- **Related, and NOT superseded:** [ADR-0024](0024-authenticated-shell-integration-channel.md)
  decision 1 (a sighted marker only locates an authenticated event). ADR-0074's other decisions
  and its rule that no timer settles a boundary also stand.
- **Beads:** `nocx-2v80t.3.40` (this record), `nocx-2v80t.3.36`, `nocx-2v80t.3.38` (the code).

## Context

A block's output reaches the coordinator's history as a stream from the helper: rows that left
the screen, each end marker carrying its command's fence, and clear boundaries. The fence is what
puts the rows on the right block (ADR-0074). The stream is queued on both sides, and AD-10 says
no queue may grow unbounded: the helper's memory is spent on somebody else's machine, and the
coordinator holds one stream per pane.

So a queue can fill: behind a slow store, a busy coordinator, or a link that stopped
acknowledging. It was answered three times on this stage, and each answer was reviewed and
failed.

1. **Dropping markers past a count** (`nocx-2v80t.3.26`). A dropped end left its block open,
   and every later block queued behind it until detach.
2. **Folding markers into one fixed-size record** (`nocx-2v80t.3.31`), replayed as ends without
   a fence. This failed twice:
   - An end without a fence cannot say which block it closes. Resolved to "whichever block is
     current", it closed the wrong one whenever another close was in flight: that block got two
     `block.closed`, and its own block stayed open.
   - The fixed-size record expanded back into every end it had counted, into a coordinator list
     with no bound. The growth moved from the helper to the coordinator; it did not stop.

The common cause is that folding throws away the fence, the one thing that ties an end to its
block, and then still tries to place the ends on blocks.

## Decision

**When a buffer overflows, the block in flight ends, and its output is incomplete. Nothing is
recorded again until the next command begins after the stream is healthy.**

1. **The helper's buffer overflows.**
   - It stops queuing and sends one marker: the block in flight ended incomplete, from row N.
   - It keeps no rows and no markers it cannot hold. It drops rows until the runtime's next end
     marker, which it sends with that end's own fence.
   - Rows after that end are recorded again.
2. **The coordinator's buffer overflows.** Its buffer holds rows and closing screens while its
   store is slow, and the same rule applies: the block in flight is settled incomplete, and
   recording resumes at the next command's end marker.
3. **Every block the overflow touched ends incomplete and closed, never open.** This covers the
   block in flight and a command started during the overflow, which can happen when the link is
   slow but alive. Each is settled by its own fence, whichever of its completion and the marker
   arrives first. Its rows are stored up to where recording stopped, it is sealed as a gap, and
   it shows "Output incomplete".
4. **An end or a clear that fell inside the overflow is lost, never replaced.** No end without a
   fence stands in for a real one. A lost clear leaves earlier blocks visible in history although
   the screen erased them: losing a clear is safer than losing output.
5. **Both buffers are the person's settings, not ours.** They are `history.helperBufferMB` and
   `history.coordinatorBufferMB` in Settings, in whole megabytes per session, with defaults sized
   from the limits before this record.
   - The value applies to sessions opened after it changes.
   - The settings accept 4 to 256 MB. The helper clamps its buffer, as AD-10 clamps its window,
     to a floor of 4 MiB (`MinRowBufferBytes`: one closing screen of 400 × 250 cells, about 3.9
     MiB, beside the incomplete marker), a ceiling of 256 MiB (half the helper's default
     aggregate budget), and the helper-wide aggregate budget it shares with the output window: a
     session asking for more than is left gets what is left, and a spawn that cannot fit the
     floor is refused. The coordinator applies the same floor (`MinBlockRowsBufferBytes`).
   - A session records the size it actually got.
6. **The coordinator's bound counts every queue** that holds rows or closing screens for the
   stream, not only the rows waiting for the store.

A full link loss does not start new commands in the dark: a person's input and the orchestration's
`workers.spawn` both go through the coordinator. An overflow with the link down therefore touches
only the block in flight.

## Consequences

- An end is never attributed to a block it does not name, so the wrong-block close and the
  duplicate `block.closed` cannot recur through overflow.
- Memory is bounded on both sides by numbers the person can see and change, and the helper's
  share stays inside its aggregate budget.
- The loss is stated in the product: an overflowed block says "Output incomplete", and nothing
  claims completeness it does not have.
- **Accepted cost:** a block that overflowed keeps only the rows that arrived before the overflow,
  even when later rows of the same command would have fitted after it drained. A clear inside an
  overflow is lost.
- If the coordinator's store stays slow, it will overflow again; each time costs one block, and
  nothing grows.
