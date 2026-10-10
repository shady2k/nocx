# ADR-0078 — The terminal's history has two tiers, and the live one is the emulator's own

- **Status:** Accepted
- **Date:** 2026-10-01
- **Decided by:** the owner, on 2026-09-21 (four decisions, recorded on `nocx-zg3k3.10` and its
  tasks), and on 2026-10-01, narrowing the second of them: "with no shell integration, on `clear`
  we simply pass on ghostty's view as it is".
- **Supersedes:** nothing. It changes what [ADR-0066](0066-one-emulator-and-it-is-the-backends.md)
  and [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md) implied about scrollback,
  and says how below; neither record is edited.
- **Related:** [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md)
  (the durable record follows the cell model), [ADR-0073](0073-the-screen-frame-is-keyed-by-its-session-and-its-reader-and-continues-on-its-own-carrier.md)
  (the frame's carrier, which a page of history rides).
- **Beads:** `nocx-zg3k3.10.5`; the stage `nocx-zg3k3.10`.

## Context

Until the cutover to painted cells, "scrolling up" meant xterm.js's buffer in the renderer, and
the frozen blocks of ADR-0009 above it. ADR-0066 moved the emulator to the backend, and ADR-0072
put the durable interval record on the cell model. Neither said where a person's scrollback
comes from once xterm no longer paints: the rows that left the screen in a session that has no
shell integration, and therefore no intervals and no cards.

The question was reasoned about twice in one week from scratch, and one of those times reached
an answer the owner had already rejected. This record exists so it is not reasoned out a third
time.

Underneath all of it is one finding about the library, established independently twice — by
the worker on `nocx-2v80t.2.1` on 2026-09-20 and by a review on 2026-09-21 that did not know that
work existed: **libghostty publishes no row-eviction event.** The scrollback depth count is the
only departure signal, and it is not monotonic, because retention prunes whole pages. There is
no scroll callback and no cumulative counter. That is what makes the first tier possible without
a copy (the library already holds the rows) and what bounds the second (a copy can only be taken
as rows depart, and a departure hidden inside a pruning feed is reported as a loss rather than
invented).

## Decision

1. **History has two tiers, and they are different things.**
   - The **live tier** is libghostty's own scrollback for the session, read by position through
     the HISTORY point tag (`Terminal.HistoryRows`). It is "how far you can scroll right now".
   - The **durable tier** is the interval record (ADR-0072; `nocx-zg3k3.5`): what survives a
     restart, and what cards are projected from.

   The owner's reason is the load-bearing one: without shell integration there are no execution
   intervals, so no cards, so a history organised around intervals has nothing to hang rows on —
   while the emulator's scrollback does not care, because it is only rows.

   _Rejected:_ a Go copy of the scrollback beside the emulator's. It would be a second owner of
   the same rows (AD-6, AD-8), bounded by rules of its own, and it could only be filled from the
   departure signal the finding above shows to be lossy under pruning.

2. **The durable record never deletes; `clear` bounds what is SHOWN there. The live tier shows
   the emulator's view as it is.**
   - In the durable tier only retention removes rows. `clear` writes a boundary
     (`RecordClearBoundary`, `nocx-2v80t.3.17`) — a cursor a reader applies, not a mark on the
     entries — and an ordinary read hides the cards before it. Revealing them is to be explicit,
     per client and per view, and is not built yet: the store answers only the "no reveal
     requested" case today. _Rejected:_ discarding the record's
     rows on ED3 — which is what `CSI 3 J`, "erase saved lines", literally asks for. Not doing so
     is deliberate, and the boundary is what keeps it honest rather than silent.
   - In the live tier, `clear` (`ESC[H ESC[2J ESC[3J`) is executed by the library like every other
     byte, and ED3 erases its saved lines: after it there is nothing above the cleared screen, as
     in any terminal. No boundary is invented there and there is no reveal (the owner,
     2026-10-01). _Rejected:_ withholding ED3 from the library so that its scrollback survives
     `clear`. It would break the feed's invariant that every byte reaches the emulator exactly
     once and in order (`internal/emulator/ghostty/terminal.go`, `ingestLocked`), need a buffer
     of withheld input for a sequence split across reads, and make the live tier's positions
     depend on a boundary nocx tracks rather than on what the library holds — all to reveal rows
     that, under shell integration, the durable tier already keeps.

3. **The durable record's retention is the settings that already exist.** `history.retentionDays`,
   `history.retentionMiB` and `history.diskCeilingMiB` bound the record; cards stay under
   `history.outputCapKB`. _Rejected:_ a per-session row-and-byte cap of the record's own — a
   second retention model beside one that exists (AD-8).

4. **How far the live terminal scrolls back is a setting of its own**, `terminal.scrollbackLines`,
   in a new `Terminal` section rather than in History: History means "survives a restart", this
   means "reachable right now". It is one knob, a line count, because that is the number a person
   reasons about. libghostty has two limits — lines and bytes, first reached wins — and the byte
   limit is wired as an internal memory ceiling in code beside the other engineering bounds, the
   way `history.diskCeilingMiB` stands behind `history.retentionMiB`. _Rejected:_ exposing both,
   which asks a person to reason about the library's page storage.

## What changes in the older records

- **ADR-0009** made the frozen scrollback DOM, above xterm's live grid. Its frozen half is now
  the durable tier's cards; the rows above the live screen in a session with no cards are the
  live tier's, painted from the emulator's cells by the same painter as the live grid, not by xterm
  and not by a DOM copy the renderer keeps.
- **ADR-0066** moved ownership of the screen to the backend's runtime without naming scrollback.
  Scrollback moves with it: the renderer holds no history of its own; it asks for a page and
  paints what it is given (`nocx-zg3k3.10.3`, `nocx-zg3k3.10.4`).
- **`docs/architecture.md`:** AD-6 still said the renderer owns scrollback, and AD-9 that
  "scrollback stays frontend-owned". Both were stated wrong by ADR-0066 and are amended in the same
  change as this record, citing it.

## What the next person inherits

- The live tier and the durable tier do not share a coordinate. The durable record carries its own
  cursor; live history positions move under reflow, pruning and ED3. A page of live history is
  addressed by a cursor that stays stable while output arrives, and the fields a join of the two
  tiers would need are reserved in the page's contract and filled only for the live tier.
- A reader of the live tier is always told where retention stopped. A short history and a range
  that ran past what the library retains are different answers.
- While the alternate screen holds the pane, neither tier's history is shown by ordinary scrolling
  (the task text of `nocx-zg3k3.10.4` records how other terminals behave and why).
