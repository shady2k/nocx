# ADR-0096 — Box drawing and block elements are drawn procedurally, to cell bounds

- **Status:** Accepted
- **Date:** 2026-10-21
- **Beads:** `nocx-1ebrs.15` (three owner sightings: v0.6.0 clean-Mac pass, 0.6.1 twice)
- **Related:** AD-8 (one owner per behaviour), [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md)
  (explicit cell geometry; rules 1–6), [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md)
  (the live cell painter this extends), `nocx-ec18` / `scrollback/cell-fit.ts` (the measuring
  authority), `nocx-zg3k3.7` / `scrollback/run-geometry.ts` (the run-geometry owner).
- **Supersedes:** nothing. It decides how two Unicode blocks are PAINTED, which no earlier
  record decided; cell-fit's measurement verdicts stand for every cluster this one does not
  claim.

## Context

A vertical run of U+2502 — every full-screen program's sidebar border — paints in nocx as a
dashed line, while the same program in Ghostty, kitty, iTerm2 or WezTerm draws it solid. The
glyph comes from the font at its own drawn height; the cell's line height is taller; each row
therefore leaves a gap where the next glyph cannot reach. This is not a tuning failure but a
construction failure: a font glyph is designed for a typographic cell and cannot tile, and at
a fractional display scale it can never promise to fill its cell in either axis. The owner saw
the dashed border three times (v0.6.0, then 0.6.1 twice in herdr), and with it the block
elements U+2580–259F rendering with visible seams and the font's proportions instead of the
cell's aspect — the same cause: paint fetched from the font instead of computed from the cell.

## Decision

**U+2500–257F (box drawing) and U+2580–259F (block elements) are never painted from the
font.** `frontend/src/painter/cell-ink.ts` owns their per-codepoint segment geometry, computed
from the cell's own rectangle — `RunMetric.cellWidth` across, the published row pitch
(`--term-cell-height`) down. Segments fill the cell, so a vertical run is continuous by
construction, a horizontal run meets every corner, and a block element's aspect is the cell's
aspect, not the font's.

**The character stays text in the model and the DOM.** The painted cell carries the program's
characters as a text node in the row — no aria-label stands in for them — so selection, copy,
find and the accessibility tree read exactly what the program painted. Only the font glyph is
made invisible (`color: transparent` on the glyph wrapper, written beside the run's own ink
colour, which the SVG must keep reading through `currentColor`), and the procedural ink paints
to cell bounds behind it. A glyph nobody can see cannot tile wrongly; the geometry is the
SVG's. The regression is exact: a row the program painted as `│ a ─┼ b ▀▄` copies as
`│ a ─┼ b ▀▄`, live and frozen (painter/procedural-paint.test.ts).

The boundaries this keeps:

- **Measurement stays with cell-fit.** A procedural cell consumes no font advance, so it is
  never measured: cell-fit's candidate batch skips it, and its cache answers only for glyphs
  the font still draws. The row pitch rides `RunMetric` beside `padY`, read where `padY` always
  read it — one publisher, no second probe.
- **Run formation stays with run-geometry**, extended by an own-run predicate: a procedural
  cell merges with another procedural cell of equal attributes, never into an ordinary run —
  an atomic inline between tracked text takes the run's letter-spacing around itself and lands
  off its column. An own run paints as ONE `.term-cell` with one SVG behind one text node: a
  whole line of `─` is a single coordinate space, so cell boundaries inside a run never meet a
  per-element device-pixel snap, and the characters of the whole run are the text the row
  carries.
- **Colour stays with style.ts.** Shapes paint with `currentColor`, so bold brightening,
  inverse, faint and the readability floor keep their single owner.
- **Seams are answered in geometry.** A line that continues across a row edge overshoots that
  edge by half a CSS pixel — overlapping opaque ink is invisible, a gap never is. Block
  fractions round complementarily, so halves tile with no gap and no double-draw. Dashes tile
  per cell, diagonals overshoot their corners along their own slope.
- **The degrade is the font glyph.** A multi-codepoint cluster, a codepoint outside the two
  blocks, a missing metric or an unpublished pitch all fall back to today's painting.

## Why this rather than the obvious alternative

_Tune the font or the line height until the glyphs join_ — impossible by construction; the
glyph's drawn height is the font's, the pitch is the renderer's, and no rule keeps them equal
at every zoom and every device ratio. _Render the grid on canvas_ — replaces the DOM painter
ADR-0072 and the design behind it chose, and takes selection, copy, IME and the accessibility
tree with it, for a seam this module answers without any of that — and without giving up the
one thing this record now states outright: the characters stay text, so the row a program
painted is the row a person can select, copy, find and hear. _Draw only U+2502, the
character reported_ — a partial box-drawing answer is a second vocabulary for one concept, the
exact defect AGENTS.md's one-owner rule exists to prevent; the two blocks are closed sets
(160 codepoints) and are claimed whole, with a sweep test that fails the day any of them
yields no geometry.

## What stands

Everything else about both painters stands: run-geometry's rules 1–6, ADR-0009's row DOM, the
frozen block's var-based re-pitch (procedural boxes size themselves from the published
`--term-cell-width`/`--term-cell-height`, and the SVG stretches with them), and cell-fit's
authority over every cluster this record does not name. Frozen blocks render the same glyphs
through the same `paintRow`, so a frozen frame stays a photograph of the live grid it came
from. Deliberately out: other procedurally-drawn sets (braille, powerline, legacy computing),
thickness derived from font underline metrics — the weight derives from the cell itself — and
any behaviour for a CJK-wide cell beyond honouring the span the frame already declares.
