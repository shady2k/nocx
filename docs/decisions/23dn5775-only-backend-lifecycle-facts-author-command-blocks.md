# ADR-23dn5775 — Only backend lifecycle facts author command blocks

- **Status:** Accepted (owner decision, 2026-11-12)
- **Date:** 2026-11-12
- **Extends:** [ADR-0074](0074-a-command-boundary-is-settled-by-an-event-never-by-a-timer.md).
- **Bead:** `nocx-peg8e.1.1`.

## Context

With earlier history above its start, procps top repaints the primary screen by
homing the cursor to row zero. The owner observed an apparent split at its first
refresh even though the coordinator recorded only one lifecycle start and no
completion until the owner quit. After `clear`, the same program stayed in one
block. The current source has a stale-anchor clipping path, but no grid-driven
production caller that opens a second block. Those are distinct claims: the
observed symptom does not establish a frontend-authored lifecycle start.

## Decision

Only backend lifecycle facts open and complete command blocks. ADR-0074's event
requirement does not authorize the renderer to infer an event from a cursor,
repaint, output anchor, command suffix or screen extent. A real backend start for
a new attempt still opens a new block; screen updates only change the existing
attempt's presentation.

Remove the unused frontend direct-start port, command-suffix clipping heuristic
and automatic settlement inferred from a later start. Keep the authenticated
lifecycle projection ports: the current tree has no grid-driven caller that
opens another block, and its command-suffix heuristic clips output rather than
splitting an attempt. Do not add such an inference or keep one behind a guard.

Removed, by symbol: `ScrollbackController.onCommandStart` (a start port no
caller used); `BlockManager._settleDisplacedRunning`, its `_displaced` map and
`MAX_DISPLACED` (settlement inferred from a later start); `BlockRecord.outputAnchorText`
with the backward walk that derived it from the screen, and the controller's
release of the live shift when that text stopped ending with the command. In
their place the runtime publishes the refreshed anchor (`refreshLiveOutputAnchorLocked`
in `internal/sessionruntime/rowstream.go`) through the existing
`block.outputStart` notification, whose ordered `fromRow` still marks the
interval's one start.
Moving output above an anchor or beyond a screen seam is a rendering concern,
not evidence that another command started or this one ended. The backend owns
which screen rows belong to the running attempt, including rows repainted above
its original anchor; the renderer paints that answer without matching command
text. The running attempt must retain the program's entire final screen.

## Consequences

The backend remains the sole boundary authority under AD-6 and AD-8. It also
publishes the current live output cut through the existing `block.outputStart`
screen anchor, using the same prefix owner as the closing screen. Its ordered
stream start stays unchanged. The renderer cannot manufacture a completed card
or a second running card from a repaint.
History-first primary-screen repaints and the after-clear control are tested
locally and over SSH. ADR-0074's backend rendezvous and authenticated lifecycle
rules remain unchanged.
