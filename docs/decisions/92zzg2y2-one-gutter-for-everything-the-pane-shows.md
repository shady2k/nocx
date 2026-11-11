# ADR-92zzg2y2 — One gutter for everything the pane shows

- **Status:** Accepted (owner decision, 2026-11-10, `nocx-4y82c.1.14`).
- **Date:** 2026-11-10
- **Supersedes:** [ADR-2yzsq8mk](2yzsq8mk-a-normal-command-is-one-column-and-a-full-screen-program-owns-the-width.md)
  in full for the pane's layout: its 24 px gutter, its full-width alternate
  screen and its one-row bottom margin. It also ends the last exception
  [ADR-v26axn1t](v26axn1t-the-live-row-and-the-frozen-row-are-one-column-and-the-pane-never-animates.md)
  §1 made for the alternate screen, and the resize-at-a-switch permission
  [ADR-3kqrem7v](3kqrem7v-a-running-program-keeps-one-header-and-one-grid-across-screen-modes.md)
  carried. Neither record is edited; the index carries the supersession.
- **Related:** [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md)
  (the frozen block's cell geometry), `e2e/one-gutter.spec.ts`,
  `e2e/pane-margins.spec.ts`, `e2e/grid-width.spec.ts`,
  `e2e/alt-screen-switch.spec.ts`, `e2e/alt-screen-exit-rows.spec.ts`,
  `e2e/output-jitter.spec.ts`.

## Context

An inline TUI over ssh (prime-agent) finished as a block whose rows started at
the left gutter and ran past the pane's right edge, clipped. The terminal's
column count had been computed for a width that did not subtract the gutters
the block body wears.

Under ADR-2yzsq8mk a normal command and a plain ssh shell wore a 24 px gutter,
while a full-screen program was flush to the pane and kept one row of bottom
margin. Entering the alternate screen therefore changed the column count and
sent one resize; leaving it kept the program's grid, and a markerless shell
handed the grid back at the person's first key. That machinery is why the pane
had two content boxes, two column counts and a set of rules for which one was
in force. A row that is fitted to one box and drawn in another is clipped, and
every attempt to keep the two in step produced a new state to remember.

The owner's decision of 2026-11-10 replaces all of it with one rule.

## Decision

1. **One gutter for everything the pane shows** — ordinary output, blocks,
   plain ssh, and a full-screen (alternate-screen) program alike. It is one
   character cell wide on the left and on the right, and half a row on top and
   on bottom. It is read from the terminal's own published cell metric, so it
   scales with the font. Nothing in the pane is flush to an edge.
2. **The terminal's size is the same in every mode.** `cols` and `rows` are
   computed in one place, from the pane's content box less that one gutter, and
   every mode uses that answer. Entering or leaving the alternate screen does
   not change the terminal's size and sends no resize.
3. **The resize-at-a-switch machinery is removed, not kept beside the rule.**
   The borrowed/inner grid, the hand-back on the first key, the row re-fit at
   exit, and the switch's resize request with the input fence it held are gone.
   A full-screen program is fitted exactly as a normal command is, and its rows
   are drawn in the same column.
4. **A real pane resize still resizes the program.** The rule is about a
   presentation change, not about the pane changing size: when the window or
   the split moves, the pty is told the new grid once, as it always was.
5. **No text touches the pane's edge, in any mode.** The four sides keep the
   one-cell/half-row gutter, and the fit reads the same content box the paint
   does, so no row is clipped to pay for it.

## Consequences

- The gutter is much narrower than 24 px at the default font, and wider at a
  larger one. The block header, its output, the live region, a markerless
  shell, a full-screen program and the composer's text all start on it.
- The composer is a card whose inline padding is the gutter less its border, so
  its text lands on the same column as a block's first glyph without pushing
  the card outside the pane.
- A full-screen program no longer gets one row of bottom margin more than a
  normal command; it gets the same half row at the top and bottom. The last row
  is inside the pane instead of touching it.
- A command that borrowed the alternate screen keeps the pane's column for its
  whole life, so the transcript does not move when its card freezes, and there
  is no borrowed presentation to hand back at a prompt or a key.
- The alternate-screen fence that held the person's input until a switch's
  resize applied has nothing left to wait for and is removed. Input produced
  after a switch reaches the program at once, in the order it was produced.
- Specs that asserted the old flush/24 px rule were rewritten to assert this
  one: `pane-margins`, `grid-width`, `alt-screen-switch`,
  `alt-screen-exit-rows`, `output-jitter`, and the new `one-gutter`.
