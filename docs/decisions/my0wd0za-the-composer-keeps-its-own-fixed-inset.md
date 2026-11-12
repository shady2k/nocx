# ADR-my0wd0za — The composer is a card with its own fixed inset

- **Status:** Accepted (owner correction, 2026-11-12, `nocx-4y82c.1.27`).
- **Date:** 2026-11-12
- **Supersedes:** one line of [ADR-92zzg2y2](92zzg2y2-one-gutter-for-everything-the-pane-shows.md),
  and only that line:

  > The composer is a card whose inline padding is the gutter less its border, so
  > its text lands on the same column as a block's first glyph without pushing
  > the card outside the pane.

  Every other decision of that record stands: the pane's output, blocks, a plain
  ssh shell and a full-screen program share one gutter of one character cell on
  the sides and half a row top and bottom, the terminal's size is the same in
  every mode, and the resize-at-a-switch machinery is gone.

- **Related:** [ADR-2yzsq8mk](2yzsq8mk-a-normal-command-is-one-column-and-a-full-screen-program-owns-the-width.md)
  (the record whose 24 px gutter the composer's geometry was written
  against), `frontend/src/styles/components/composer-frame.css`,
  `e2e/pane-margins.spec.ts`, `frontend/src/scrollback/trailing-edge.test.ts`.

## Context

ADR-92zzg2y2 recorded one decision about the text inside the pane's output: a
row fitted to one box and drawn in another is clipped, so every mode keeps the
same one-cell gutter. Its Consequences then extended that rule to a surface the
decision had not been about — the composer — and said the card's inline padding
would be "the gutter less its border", so the command a person types would
start on the same column as a block's first glyph.

The owner never decided that. Asked about it, the owner said they had said
nothing about the editor. The decision of 2026-11-10 was about the pane's
output, and the composer was carried along by a sentence that read a
consequence as a decision.

Commit `25e68dc4e` had already made the same move in the stylesheet: the card's
side margin became `0` and its inline padding became `var(--pane-inline-padding)

- 1px`. At a 24 px gutter the card had been inset from the pane by 11 px (the
  gutter less its 12 px padding and its 1 px border) and the card's text stood on
  the ledger's column. At a one-cell gutter there was no room left for that
  arithmetic — a cell is 8 px at the shipped font, less than the card's own border
  plus padding — so the card spanned the pane, wore the gutter as its padding, and
  stopped being an inset card at all.

## Decision

1. **The composer's geometry is restored from before `25e68dc4e`, as fixed
   values.** The card's side margin is `--space-6` (24 px) less
   `--terminal-card-padding` (12 px) less its 1 px border, which resolves to
   11 px, and its padding is `--terminal-card-padding` on every side. Nothing in
   the composer reads `--pane-inline-padding`: the numbers are the spacing
   scale's, not the renderer's published cell metric, so the card looks the same
   at every font scale.
2. **The composer's text therefore stands at a fixed 24 px inset.** It is the
   column the pane's text had before the one-cell gutter, and it is no longer a
   block's first column. That is the accepted consequence, not a defect: the
   owner's rule is about the text the pane's output shows.
3. **The card is not a row, and the clipping argument does not reach it.**
   Nothing in the composer is fitted to a PTY column: the editor lays its own
   text out from its own box, so there is no fitted row to draw in a second box
   and nothing to clip. The card's inset costs no column and cannot push it
   outside the pane, because the pane's width is not a grid's width.

## Consequences

- The card's border box is 11 px inside the pane's content box on the left and
  on the right, at every font scale, and its text starts 24 px in. The
  measurements in `e2e/pane-margins.spec.ts` assert both at two cell metrics.
- The composer no longer shares the ledger's column, so a spec that asserted
  "the composer's text and the block's row start on one column" states the
  superseded rule and was rewritten: the block's rows keep the one-cell gutter,
  and the composer keeps its own fixed inset.
- `ADR-92zzg2y2` is not edited. Its row in the index gains the supersession of
  this one clause and its other decisions go on governing.
