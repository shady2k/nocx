# ADR-2yzsq8mk — A normal command is one column; a full-screen program owns the width

- **Status:** Accepted (owner decision, 2026-11-08, `nocx-djx1u`; confirmed after
  it was restated: "a normal command, running or finished: one column with the
  same left gutter, so nothing moves when it ends. A full-screen program
  (alternate screen: top, vim, htop): the whole width, NO left gutter, and a
  bottom margin of about one row so its last row does not touch the window's
  bottom edge, as Warp does. Entering the alternate screen changes the column
  count and sends one resize — that is accepted.")
- **Date:** 2026-11-08
- **Supersedes, in part:** [ADR-3kqrem7v](3kqrem7v-a-running-program-keeps-one-header-and-one-grid-across-screen-modes.md)'s
  "Active grid inline gutter 0px" for the PRIMARY running grid, and its rule
  that a presentation change never sends a resize. Supersedes
  [ADR-v26axn1t](v26axn1t-the-live-row-and-the-frozen-row-are-one-column-and-the-pane-never-animates.md)
  §1 for the ALTERNATE screen only: the one column stays, and the program is
  outside it. Neither record is edited; the index carries the supersession.
- **Related:** [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md)
  (the frozen block's geometry), `e2e/row-gutter.spec.ts`,
  `e2e/grid-width.spec.ts`, `e2e/alt-screen-switch.spec.ts`,
  `e2e/restored-tui-refit.spec.ts`, `e2e/output-jitter.spec.ts`.

## Context

ADR-3kqrem7v made the active grid edge-to-edge in both buffers so that a screen
switch could not change the program's size. ADR-v26axn1t then made the running
row wear the frozen column's gutter, because a command's output jumped 24px
right at the instant its card froze — the owner's own report — and that record
could not hold with the first one: the live row cannot both fill the pane and
sit in the transcript's column. The owner has now decided the layout with the
two cases separated, which is what the two earlier records conflated.

## Decision

1. **A normal command — running or finished — is one column with the
   transcript's gutter.** The prompt, the running body and the frozen body all
   wear `--pane-inline-padding`, and the grid is fitted inside it. Nothing moves
   when a command ends: the shape, the column count and the position are the
   same before the freeze and after it.
2. **A full-screen program owns the whole width.** Under an alternate-screen
   program the live region pays no inline gutter, the grid is fitted to the
   pane's own content width, and the box keeps **one row of bottom margin** so
   the program's last row does not touch the window's edge. The margin is the
   fit's as well as the box's: the program is told the rows that fit above it,
   and the box gives that row back, so the box and its margin are the pane.
3. **Entering the alternate screen changes the column count and sends exactly
   one resize.** That is accepted, not a defect: a program that takes the pane
   should be told the pane's width, and it is told once per entry.
4. **Leaving it does not resize the program.** The program keeps the grid it
   was given until its command ends, and the running block that holds its rows
   is drawn flush for the rest of that command, because its rows were written
   at the pane's width. The prompt is where the transcript's column returns,
   and the one resize owed for that happens at that boundary.
5. **No text touches the pane's edge.** In a normal command — the prompt, a
   running command, the frozen card — and in a markerless session (a plain ssh
   shell with no OSC 133 yet) every painted row keeps the ledger's margin on
   all four sides: `--pane-inline-padding` on the left and on the right, so the
   last column stops short of the pane's right edge, and the same token as the
   live box's vertical margin, so the first row never touches the top edge and
   the last never the bottom. The fit and the box read the same two numbers, so
   nothing is clipped to pay for them. A markerless session has no blocks to
   align with, but it is still text in a pane: the margin is not the ledger
   column's, it is the pane's.
6. **A block's header, its output and the composer's text are one column.**
   The composer's first glyph starts on `--pane-inline-padding` too — the x the
   directory, the time and the command start on — so the line a person types is
   the block it will become, before it runs. The target switch that says where
   Enter goes follows the editor in that row instead of preceding it.

## Consequences

- A switch to `top`, `vim` or `htop` sends one resize to the full width; the
  specs that used to forbid it now assert it.
- A command's output no longer moves sideways when it finishes, and the earlier
  records' oscillation — a resize at every switch caused by a gutter that
  depended on presentation — is replaced by one deliberate resize per entry.
- **The exit is where the loop would be.** A program that borrows the alternate
  screen to redraw is borrowing it because the pane resized, and a resize sent
  at its exit would be the SIGWINCH that makes it borrow again — measured at
  four entries, three exits and eight resizes for one pane resize. Decision 4
  is what makes that impossible rather than merely rare. The price, stated: a
  command that borrowed the alternate screen keeps its flush, one-row-shorter
  presentation until it ends, and the transcript's column returns at the prompt
  — so for that one command a 24px shift remains at the freeze. That is the
  cost of joining a full-width program to a guttered ledger, and it is paid
  once per command that borrows the screen rather than once per keystroke.
- The bottom margin costs a full-screen program one row of height. It is the
  owner's Warp parity, and the fit subtracts it so no row is clipped for it.
- The four-side margin costs a normal command three rows of pane at the default
  size (one top, one bottom, and the remainder of the gutter's 48px inline) and
  a markerless session the same, and it is the one thing every mode but the
  alternate screen now shares. The composer reaches its column by giving up its
  own inline padding, not by moving its card: the card's margin is what is left
  of `--pane-inline-padding` after its border and padding.
