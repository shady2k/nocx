# ADR-8epbky7n — The live box's ceiling is a row boundary

- **Status:** Accepted (owner report, 2026-11-08, on `nocx-djx1u`; implements
  the last clause of ADR-v26axn1t §3 and narrows ADR-3kqrem7v's "the height
  minus 32px is the desired grid height" to the rows that height can hold)
- **Date:** 2026-11-08
- **Decided by:** the owner's own words on the stand: "the composer is back, the
  live grid is cut short mid-row, and a blank band lies between them"; and their
  check for this round: "no frame may show a blank stretch where rows exist".

## Context

The live region's box is clipped by its own height, and its rows are laid at
whole multiples of the cell height. The height came from the pane — the
scroller's own fractional pixels less the 32px running header (ADR-3kqrem7v) —
and at the hand-off, when the composer takes 130px back, that number landed
between two rows: measured in the e2e container, 514px of box for rows 17px
apart, which is 30.24 rows, so the clip sliced the thirty-first and four pixels
of it stayed on screen. The same shape appeared in the running cap on any pane
whose height was not a multiple of a row.

Rounding to whole pixels is not the cure: ADR-3kqrem7v's one-geometry rule and
`nocx-3an1s.1.4` bought fractional pixels precisely so the fit and the box would
agree, and a pixel rounding is what clipped the last row there.

## Decision

**The cap and every filled pane box end on a row boundary.** `runningLiveCap`
and `_fillPane` floor the space they are given to the largest whole number of
cells that fits in it, so the box that clips the rows and the grid the fit asks
for end on the same line. The fit's row count is unchanged — `floor(height /
cell)` of a floored height is the same number — and no pane change or
presentation change alters it.

## Consequences

- The box can be up to one row shorter than the pane's raw height. That row was
  never usable: the fit never claimed it, and showing a slice of it was the
  defect.
- A unit test that asserted the cap equals the pane's fractional height now
  asserts the stronger pair: the cap is at or below that height, less than one
  row below it, and the rows the prompt fitted still fit the box that shows
  them.
- Nothing else about the geometry moves: the gutter, the header cost and the
  one-paint rule are ADR-v26axn1t's.
