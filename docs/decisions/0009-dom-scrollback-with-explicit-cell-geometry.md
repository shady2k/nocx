# ADR-0009 — The frozen scrollback is DOM, and it declares its cell geometry

- **Status:** Accepted
- **Date:** 2026-09-05 (records a decision taken 2026-07-24 and amends it)
- **Related:** supersedes [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §4
  ("Output stays in one xterm; no freeze in the MVP"); amends **AD-6** in
  [architecture.md](../architecture.md); constrained by
  [ADR-0040](0040-a-block-is-a-node-in-an-ordered-tree.md) (a block is a node in an
  ordered tree); implements the record `nocx-4ff.25` reserved and never wrote; spike
  `nocx-4ff.17`; defects `nocx-yy9g`, `nocx-ec18`, `nocx-4ff.26`.

## Why this record exists at all, and late

`nocx-4ff.25` reserved `0009` on 2026-07-24 to record the direction change away from
ADR-0004 §4. **The file was never written.** The bead is still open. `INDEX.md` has since
described 0009 as "never allocated; no file with that number has ever existed", which is
true of the file and false about the decision: the decision was taken, shipped, and has
governed every scrollback change since.

The cost of that gap was paid on 2026-09-05. Three binding documents described a product
that does not exist — AD-6 said the VT frontend owns _grid, scrollback and selection_,
ADR-0004 §4 said output stays in one xterm and there is no freeze, and ADR-0008 quoted
both as constraints. Anyone reasoning from them would have designed the wrong thing, and
the day's architecture review had to reconstruct the real model from a closed spike's
notes and a comment in `controller.ts`.

## Context

Two decisions were taken and only their consequences were written down.

**2026-07-24 (spike `nocx-4ff.17`, GO 6/7).** Instead of DOM decorations over one
full-screen xterm, the scrollback became DOM: xterm is the VT engine and the live region,
and when a command ends its rows are serialized into a frozen block and **leave the grid**
(`controller.ts` — otherwise the live region redisplays them below the block, `nocx-m87n`).
Precedents cited: Wave Terminal, Extraterm, DomTerm.

That bought four properties an in-canvas grid cannot give:

1. native selection across heterogeneous blocks;
2. search over the whole scrollback;
3. clickable links in cold history;
4. restore after a restart, from a durable body rather than a live buffer.

A fifth arrived later and is now the binding one: **ADR-0040** made the scrollback an
ordered _tree_ of entries — assistant prose, tool calls and command blocks nest and
interleave. xterm is a flat array of fixed-height rows; a DOM node of arbitrary height
between rows is not a decoration but a hole in a coordinate space xterm cannot own.

**What it cost.** The frozen block is typographic text; the live region is a grid. The gap
was bridged by one number — `letter-spacing: (cellWidth − naturalAdvance)` on `.term-line`,
with `naturalAdvance` probed from a run of `W` (`nocx-yy9g`). That is exact on the subset
where every glyph advances like `W`, and only there. Everything outside it arrived as a
separate exception. Measured on the owner's pane (macOS, 8px cell): U+1F5D1 at 13.572px,
U+27F3 and U+27F2 at 9.087, U+2B22 at 8.903 — 8.649px over one row, exactly one column, and
a TUI frame whose corners no longer met (`nocx-ec18`). Then, the same day: a logo drawn in
background-coloured spaces striped, because an **inline** element's background paints a
content box derived from font metrics — 16.5px inside a 19px row.

## Decision

**The frozen scrollback stays in DOM, and it stops asking the browser to lay terminal cells
out typographically. It declares the geometry instead.**

Concretely, the rule the DOM renderer of xterm.js itself uses, adopted deliberately:

1. Per cell, `spacing = columns × cellWidth − measured advance(chars, bold, italic)`.
2. Adjacent cells merge into one run only when foreground, background, extended attributes
   **and `spacing`** all agree.
3. The run carries that `spacing` as its `letter-spacing`, so each cell contributes
   `advance + spacing = columns × cellWidth` and the row's total is exact **without any
   declared width**.
4. Every run is `display: inline-block; height: 100%; vertical-align: top`, so a background
   covers the row's rectangle rather than the font's content box.
5. The row has the grid's exact height and `overflow: hidden`.
6. Where `spacing` is negative — the glyph's paint is wider than its columns — the ink is
   scaled to fit; the advance is never changed to accommodate it.

The global `--term-cell-delta` on `.term-line` is retired by this. Per-run measured spacing
replaces it.

## Rationale

**Why not keep patching.** The list of ways browser shaping differs from a terminal cell is
open: BiDi re-applied over a visual order the terminal already fixed, block elements and
powerline that a GPU renderer fills with primitives and DOM fills with a font glyph,
underline styles and colours, dim as opacity against dim as colour multiplication, conceal,
bold-as-bright, OSC 8 identity against re-detection by regex, VS15/VS16 and ZWJ clusters,
italic overhang, late font loads, DPR changes between displays. A rule cannot be inferred
from a list of exceptions, and the next reader would inherit the list.

The contract above is closed: _a cell occupies a known number of columns; the background
covers that rectangle; only the shape of the ink inside may differ._

**Why one number could never have worked.** It was believed that a two-column cell is
unfixable by tracking, because CSS applies letter-spacing per typographic character and
grants one opportunity where the grid gives two columns. That is wrong, and the correction
matters: one opportunity suffices when it carries the right number — `2 × cellWidth −
advance`. The defect was a single delta for the whole row, not the instrument.

**Why not one xterm for the whole scrollback** (that is, why ADR-0004 §4 is superseded
rather than restored). ADR-0040. Beyond it: there is no public API for deleting an
arbitrary range of buffer rows, so a block cannot be collapsed; native selection across a
canvas and neighbouring DOM does not exist; search becomes federated; a marker dies with
the range a trim removes; a very large scrollback becomes O(history) in memory and reflow
even when only the viewport is drawn.

**Why not reuse `DomRenderer` itself.** It is not public API — absent from the typings, its
constructor wants an `ITerminal` and half the internal DI, it holds a viewport-sized row
array rather than an archive, and its styles are scoped to a generated terminal class.
Cloning its DOM detaches selection, links and invalidation. The techniques are adopted; the
class is not.

**Why not a canvas with an invisible text layer** (the PDF.js shape, which is otherwise
elegant): a 10 000-row block is a canvas of 1216 × 190 000 px, past the maximum, so it
needs virtualisation and repaint-on-scroll — a terminal renderer — while the text layer is
still required for every row or search cannot reach what is off-screen. The renderer is
paid for and no DOM node is saved.

## Consequences

- **A ~20% render penalty on `inline-block`**, by xterm's own note beside the rule. It is
  measured on a real transcript as part of the work, not after it.
- **A width cache with invalidation** becomes load-bearing. That is the defect class which
  cost four adversarial rounds in `cell-fit.ts`; the cache exists and is tested, and this
  adds a reader to it.
- **`overflow: hidden` on the row clips ink that overflows vertically** — tall emoji lose
  their extremes. Accepted so that a row cannot paint over its neighbour.
- **A restored block stays approximate** until geometry is stored beside the durable body.
  `serializeRangeSGR` does not express cell widths and must not: extending it would bind
  the style parser to a private geometry protocol.
- **`nocx-4ff.26` is untouched by this.** A block is serialized at the `D` marker while
  xterm's buffer is capped at 10 000 lines, so long output has already lost its beginning.
  That is about _when_ the copy is taken, not how it is laid out.
- **AD-6 and ADR-0004 §4 are amended by this record**, not left to be discovered again.

## The threshold that would reverse this

Written now so it cannot be chosen afterwards:

**If, after the geometry is explicit, one more fidelity fix has to be an exception for a
particular code point, a particular font or a particular OS, the DOM projection has not
paid for itself.** The conversation then returns to a single xterm for the whole
scrollback, together with retiring ADR-0040 and moving the assistant and the API pane out
of the shared scroll flow.

A second, performance threshold: if a typical saved transcript cannot be scrolled within
frame budget without giving up native selection and search, move to virtualised specialised
surfaces rather than masking it with more `content-visibility`.
