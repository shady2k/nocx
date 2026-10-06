# ADR-0080 — An authenticated event defers a settle, it does not take it

- **Status:** Accepted
- **Date:** 2026-10-05
- **Supersedes:** case 3 of [ADR-0074](0074-a-command-boundary-is-settled-by-an-event-never-by-a-timer.md) —
  "a completion for another nonce" as an event that _proves_ a fence will not come. The rest of
  ADR-0074 stands, including its rule that a boundary is settled by an event and never by a timer.
- **Related:** [ADR-0024](0024-authenticated-shell-integration-channel.md) (the completion and the
  fence are two carriers), [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md).
- **Beads:** `nocx-n5ent` (the loss this was bought with), `nocx-rb4ca` (the prefix loss next to it).

## Context

A command's end reaches the runtime twice, on two carriers that a remote shell orders only
loosely: the **completion** on the authenticated lifecycle channel, and the fence's **sighting**
in the terminal byte stream. `nocx.bash` sends the completion _before_ it writes the fence into
the pty (`internal/shellintegration/scripts/nocx.bash:1466`), so the ordinary order is
completion-first.

ADR-0074 case 3 listed a completion for another nonce among the events that settle a parked
interval, on the reading that it proves the fence will not come. It proves something weaker: that
the fence was **written**. It cannot say that the pty has been **read** up to it. Measured
2026-10-05 (`nocx-n5ent`, and the census the e2e failure context now prints): an interval was
sealed with `cursor=3841 endRow=3841` — a seal with no closing screen, at the count the fast
channel had reached — while the pty still held roughly 31 KB, about a buffer's worth, of that
command's own output. The artifact was sealed with `truncated=gap` and the person's last ~1160
rows were stored nowhere. They belonged to a next interval that never came.

## Decision

**An event on the authenticated channel defers the settle. It does not take it.**

A completion for another nonce, and a rendezvous-bound eviction, hold the interval open instead of
freezing it, and the wait is taken by the event that can actually speak for the byte stream:

- a **fence sighting** naming the deferred nonce seals that interval with the screen the fence sat
  on — which is what returns the tail;
- a fence sighting for **another** nonce flushes it with no screen, as case 3 already recorded;
- the **session's end** takes it, being the last event there is;
- where rows genuinely cannot come, the explicit `gap` the store already records stands.

No timer is introduced, and no bound moves: the boundary is the helper's own stream, which is what
ADR-0074 meant by "late is not missing" and what makes this a continuation of it rather than a
reversal.

## Consequences

- A block can stay open longer than the completion's arrival suggested. That is the cost, it is
  deliberate, and it is bounded by the stream rather than by a clock.
- The runtime's settle sites that pass no closing screen are no longer reachable by an
  authenticated event alone; `internal/sessionruntime` carries the deferral and the sighting that
  takes it.
- The environment-entry settle is deliberately NOT deferred: it reads the screen at the entry, the
  kernel is the authority on the lane, and it is not among the events this context is about.
- Known gap, recorded rather than smoothed over: the five settle events share one log line, so
  which of them fires cannot be named from a log today. Separating them needs the settle cause
  carried on the end marker, or a logger in a package that has none — a deliberate change of its
  own, not a side effect of this one.
