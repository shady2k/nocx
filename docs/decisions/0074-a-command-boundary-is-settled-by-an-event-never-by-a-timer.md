# ADR-0074 — A command boundary is settled by an event, never by a timer

- **Status:** Accepted
- **Date:** 2026-09-24
- **Supersedes:** [ADR-0066](0066-one-emulator-and-it-is-the-backends.md), the clause of
  "What stands verbatim" that reads "so both arrival orders remain real and a bounded
  missing-fence policy remains necessary" — its last four words. The rest of that paragraph,
  and ADR-0066 as a whole, stand.
- **Related, and NOT superseded:** [ADR-0024](0024-authenticated-shell-integration-channel.md)
  decisions 1 and 7 (a sighted marker only LOCATES an authenticated event; the rendezvous is the
  nonce-matched OSC 1337), [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md).
- **Beads:** `nocx-2v80t.3.11` (this record), `nocx-2v80t.3.9` (the defect that forced it).

## Context

A command's end reaches the runtime twice, on two carriers that SSH orders independently: the
**completion** (the authenticated lifecycle event) and the fence's **sighting** in the terminal
stream. ADR-0066 kept ADR-0024's observation that both arrival orders are real, and added that
"a bounded missing-fence policy remains necessary". It was built as one: every pending meeting
armed a `time.AfterFunc` (500 ms from the helper's composition root, behind a
`sessionruntime.Config` seam); when it fired, a meeting still waiting for its sighting was
marked expired and its completeness became "no fence".

The owner decided on 2026-09-23 that a block's output is streamed from the emulator and that
at the command's END MARKER the rows still on screen are appended and the block is closed
(recorded on `nocx-2v80t.3.4`). Measured on 2026-09-24 (`nocx-2v80t.3.9`, a 500-command e2e
run): when the completion won the race, the runtime read the closing screen at the completion,
by which time the shell had printed the next command's first rows — one block carried the next
command's head and the next block never froze. The screen that belongs to a block is the one
at its end marker, and the end marker is the half that arrives in the ordered stream.

With the screen taken only at the sighting, the timer no longer bounds anything the product
needs: it only decides, by elapsed time, that a sighting which is still coming will not come.
AGENTS.md forbids exactly that shape — "a test may not depend on timing" — and a product rule
whose correctness depends on how fast a remote shell's bytes arrive is the same defect outside
the tests.

## Decision

**No timer exists in the command-boundary rendezvous.** Each case is settled by an event:

1. **Sighting first** (the ordinary local case): the sighting captures the screen and the row
   count in one critical section; the completion that follows authenticates it and seals the
   record. Unchanged.
2. **Completion first:** the completion PARKS the interval — it records its nonce and the row
   count it measured, and reads no screen. The seal happens when the sighting arrives and joins
   it, with the sighting's screen.
3. **A sighting that never arrives** (the fence was undecodable, the shell died, the stream was
   cut) is settled by the next event that proves it will not come: the **next interval's start**
   (a fence or a completion for another nonce), the **session's end**, or the contract's own
   `ExpireRendezvous` call. The parked record is sealed with **no closing screen**, at
   `max(rows already streamed, the parked count)` so an end never falls behind rows the stream
   already carried, and is marked no-fence — which the block shows as "output may be
   incomplete".
4. **A completion for a fence nobody sighted, arriving after another fence has already taken the
   interval**, parks nothing and invents no record: the interval is the other fence's.

The number of meetings pending at once stays bounded by count (`MaxPendingRendezvous`), with
evictions counted; that bound is a size, not a duration, and is unchanged.

## Consequences

- A block's closing screen is always the screen at its end marker; the defect that attributed
  one command's rows to the next cannot recur through the completion path.
- A remote session over a slow link no longer loses its closing screen because its bytes were
  late: late is not missing, and only an event decides missing.
- The window during which a boundary is unsettled is now unbounded in time, bounded only by the
  next command or the session's end. A session that runs one command and then sits idle keeps
  that command's block open until something happens; its rows are already stored as they
  stream, so what waits is only the closing screen and the "closed" mark. This is the accepted
  cost.
- `internal/sessionruntime` reads no clock for the rendezvous, and its tests sleep for nothing
  (78350de6, d5d2515d, merged at 6679a561).
