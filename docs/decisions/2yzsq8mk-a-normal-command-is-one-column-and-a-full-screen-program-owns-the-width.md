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
   fit's as well as the box's: the program is told the rows that fit above it.
3. **Entering or leaving the alternate screen changes the column count and
   sends exactly one resize.** That is accepted, not a defect: a program that
   takes the pane should be told the pane's width, and it is told once per
   switch, never repeatedly.

## Consequences

- A switch to `top`, `vim` or `htop` sends one resize to the full width; the
  specs that used to forbid it now assert it.
- A command's output no longer moves sideways when it finishes, and the earlier
  records' oscillation — a resize at every switch caused by a gutter that
  depended on presentation — is replaced by one deliberate resize per switch.
- The bottom margin costs a full-screen program one row of height. It is the
  owner's Warp parity, and the fit subtracts it so no row is clipped for it.
