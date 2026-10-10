# ADR-0076 — The coordinator going away changes no block; only what the helper reports does

- **Status:** Accepted
- **Date:** 2026-09-29
- **Decided by:** the owner, 2026-09-29 (recorded on `nocx-zg3k3.5.3`): "disconnecting the
  coordinator must not affect blocks. If the coordinator comes back and ghostty no longer holds the
  data, the block honestly marks that part of the output is missing. The trigger for changing a
  block's state must be data from the helper."
- **Supersedes:**
  - [ADR-0074](0074-a-command-boundary-is-settled-by-an-event-never-by-a-timer.md), decision 3,
    where "the **session's end**" was read to include the coordinator detaching from a session's
    helper callbacks (`DetachBlockRows`, `internal/transport/ws_block_rows.go`). The coordinator
    detaching is not the session's end. A session ends when the helper says it ended.
- **Related, and NOT superseded:** ADR-0074's other decisions and its rule that no timer settles a
  boundary; [ADR-0075](0075-an-overflowing-row-stream-ends-the-block-in-flight-incomplete.md) (an
  overflow marker from the helper still ends the block in flight incomplete);
  [ADR-0024](0024-authenticated-shell-integration-channel.md) decision 1 (only the authenticated
  shell channel authorises a boundary — nothing here adds a second author).
- **Beads:** `nocx-zg3k3.5.3`.

## Context

The helper owns every session, and it keeps running when the coordinator (`nocx-server`) restarts
for an update or a crash. Ghostty's scrollback is the only buffer for rows the coordinator has not
stored (the owner's decision on `nocx-2v80t.3.4`). Measured on 2026-09-29 over the real helper: a
command still printing when the coordinator stopped lost every row after the restart. The
coordinator's detach sealed the open block `unknown` under ADR-0074 decision 3, so when the
re-adopted session streamed the rest, each row belonged to no block and was dropped.

## Decision

1. **The coordinator stopping, restarting or losing its helper connection changes no block's
   state.** An open block stays open and keeps its row cursor in the store.
2. **A block's state changes only on something the helper reports:** an authenticated interval end,
   a row stream overflow (ADR-0075), a loss count, or the session's end as the helper states it.
3. **On return,** the re-adopted session's rows continue the open block by their absolute
   departed-row index; rows the coordinator already stored are dropped as duplicates. Rows ghostty
   pruned in the meantime are counted on the block with the cause "coordinator unavailable", and
   the card says so.
4. **An end the helper saw while the coordinator was away** closes the block on return. If no end
   ever comes, the block stays running until the helper reports the session's end, which settles it
   under ADR-0074 decision 3 as before.

## Consequences

- `DetachBlockRows` stops sealing open blocks on a coordinator shutdown. A session the helper
  reports ended still settles them.
- A block can outlive a coordinator process, so the store keeps an open block's cursor durably.
- There is no timer anywhere in this. A block left open by a helper that never comes back is settled
  when that session's end is known, and never by guessing.
