# The cell renderer choice — a design note

- **Date:** 2026-09-21
- **Task:** the search ADR-0066 §"What this costs" and design §6.11 left open: choose the
  client cell renderer for the terminal surface, after xterm.js.
- **Scope:** research and judgement only. No product code was written; this document is the
  only file created. Worktree `task-zg3k3-1-cell-renderer`, branch
  `task/zg3k3-1-cell-renderer`, clean at `0cc4f0f2`.
- **Method note on citations:** this worktree has no `node_modules` — nothing below was
  verified from an installed copy. What an install would contain is pinned by the lockfile:
  `frontend/package-lock.json:3468-3473` resolves `@xterm/xterm` 5.5.0 with
  `license: MIT` (addon-webgl 0.18.0 at `:3459-3467`, addon-canvas 0.7.0 at `:3432-3440`),
  under integrity hashes. The packages were inspected from those exact published npm
  tarballs, downloaded to `/tmp` and read there — the same bytes an `npm ci` would lay
  down. Two kinds of claim are distinguished throughout: the lockfile verifies version and
  licence **metadata** only; every constructor and service-graph claim below is verified
  from the tarball **sources**, which ship the full TypeScript `src/` tree beside the
  bundled `lib/xterm.js`. External candidates were verified against the npm registry and
  the GitHub API on 2026-09-21; every licence below was read from the registry/repo
  metadata or the licence file itself, and anything I could not check is named in those
  words in §7.

## 1. The question, and what already binds it

ADR-0066 removes xterm.js — "xterm.js is removed, not demoted"
(`docs/decisions/0066-one-emulator-and-it-is-the-backends.md:106`) — and moves the emulator
to a long-lived session runtime beside the PTY. The client becomes a painter of cells and a
sender of intent. The same record establishes why keeping xterm as a painter was ruled out
before this search started: its only input is `write(bytes)`
(`xterm.d.ts:1216`, verified in the 5.5.0 tarball) and its buffer is read-only
(`xterm.d.ts:809`, verified), so cells cannot be pushed in and its renderers cannot be
addressed without its parser (ADR-0066:110-113). Feeding it synthesised ANSI would make the
browser re-execute cursor moves and erases to rebuild cells the runtime already holds.

The design's §6.11 states the search criteria this document judges against
(`.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md:392-397`):
takes cells rather than bytes; carries grapheme widths; supports selection by coordinate,
IME and accessibility; is readable; is licensed to fork. §9 step 6 adds the build-order
constraint (`:493-496`): the reference client starts from **full snapshots** and "the
simplest renderer whose correctness can be inspected"; delta encoding and a fast canvas are
optimisations of a foundation that is already correct. A recommendation that requires WebGL
on day one is answering a different question, and this document does not make it.

One criterion sits above the seven, because the epic names it as the hard part: **identity**.
One grapheme must have the same identity in painting, hit-testing, selection, copying and
accessibility while frames keep changing. The architecture already decides what that
identity is, and the renderer's first obligation is to not re-derive it:

- The backend emulator's cell is the identity unit: `internal/emulator/emulator.go:159-163`
  declares `Cell{Grapheme, Width, HasText, Style}`, and the grapheme is "the whole cluster —
  the base codepoint followed by every combining codepoint the terminal assembled into it",
  not one rune (`internal/emulator/emulator.go:150-153`). The port's vocabulary names
  "cells with grapheme boundaries and authoritative widths" as the product
  (`internal/emulator/doc.go:26-27`), and the ghostty adapter reads the cluster whole
  (`internal/emulator/ghostty/terminal.go:517-544`) — that property is the reason
  ADR-0065 chose libghostty-vt, and it is accepted
  (`docs/decisions/0065-the-emulator-a-program-talks-to.md:1-4`).
- The wire already carries the same shape elsewhere: `screenCell` is "the whole grapheme
  cluster, and empty for a blank cell or a continuation", with `width` the load-bearing
  field — first cell width 2, continuation width 0
  (`contracts/helper/identities.schema.json:266-276`); `agent.emitting`'s `cells` is one
  entry per column, the continuation an empty string
  (`contracts/agent.emitting.schema.json:134-138`). A consumer that wants text skips
  width-0 cells; a consumer that wants a position reads the column index. Those two
  readings, kept apart, are exactly the identity contract a painter must honour.

So the practical test for every candidate below is: does it let the runtime's
`(row, column, grapheme, width)` tuple stay the one identity across all five surfaces, or
does it interpose a second segmentation (codepoints, ligatures, browser shaping) that can
disagree with the runtime's?

## 2. What the tree already owns in this area

Any recommendation that ignores what is solved is incomplete, so this is the inventory.

`frontend/src/scrollback/cell-fit.ts` is the measured answer to "does this cell land on the
grid". Its header records the failures that bought it — U+1F5D1 measured at 13.572px against
an 8px cell, U+27F3/U+27F2 at 9.087, U+2B22 at 8.903, a summed 8.649px drift = one column =
a TUI frame whose corners no longer met (`cell-fit.ts:1-10`) — and the discipline around
the answer: measure by cluster and face, never by codepoint (`:23-25`), batch all writes
before all reads so a freeze costs one layout regardless of N (`:15-21`, `:148-170`), key
the cache by the full style signature taken from a probe that wears the same classes and
font rules as the real rows, because "measuring with one shaping and keying with another"
is the defect class (`:31-36`, `:99-127`, `:176-190`), and return a verdict
`CellBox{cols, fit}` where `fit` only ever shrinks the paint, never stretches it
(`:57-65`, `:292-297`). `frontend/src/scrollback/cell-metric.ts` publishes the numbers the
rule needs — `--term-cell-width` and `--term-cell-delta` (`cell-metric.ts:22-23`, `:111-112`).

ADR-0009 is the geometry contract those numbers serve: per cell,
`spacing = columns × cellWidth − measured advance`; runs merge only when foreground,
background, extended attributes **and spacing** all agree; the run carries its spacing as
`letter-spacing` so each cell contributes exactly `columns × cellWidth`; backgrounds get
measured vertical padding so adjacent rows meet; nothing is clipped, and ink wider than its
columns is scaled, never re-advanced (`docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md:66-79`).
Rule 3 is the one that makes identity cheap for a painter: once every cell contributes an
exact known advance, **pixel→column hit-testing is arithmetic on the run layout, not a
measurement**. The record also states its own reversal thresholds (`:163-175`) — one more
per-codepoint exception, or a saved transcript that will not scroll in budget, and the DOM
projection has not paid for itself.

A working spec precedes this note and must be read with it:
`.internal/specs/2026-09-05-frozen-grid-renderer-design.md` — the document ADR-0009
records (it, like `cell-fit.ts`'s own comments, is written in Russian; both are
paraphrased in English here). Two things carry forward. First, its mechanism is the same
contract this note extends: it read the spacing rule out of xterm's own DOM renderer —
run merge requiring `bg`/`fg`/`ext`/`spacing` equality
(`src/browser/renderer/dom/DomRendererRowFactory.ts:157`), the letter-spacing
application (`:445`), the inline-block runs (`DomRenderer.ts:142`) — and those line
numbers check out against the 5.5.0 tarball verified for §3.1, so both documents describe
the same sources. Second, it planned `frontend/src/scrollback/frozen-grid.ts` as the one
owner of run geometry, and **that file does not exist in the tree today** — the
obligation never landed. The live painter must not become a second derivation: whichever
module builds runs and their spacing owns that for the frozen rows and the live grid
alike (the AD-8 corollary: one behaviour, one owner).

`frontend/src/renderers/types.ts:113` is the seam the painter plugs into. Parts of it die
with xterm: `write(data: string)` (`:152`), `onData` (`:162`) and the whole family of
OSC-derived callbacks (cwd, markers, fence, notifications) move to the backend under
ADR-0066. Parts survive as the painter's shape: the `fitViewport` / `setGrid` split — the
window's rectangle versus the session's grid, which are different facts for a client that is
not the one the channel follows (`:118-137`, nocx-eidfb.1/3) — is precisely the shape a
frame-driven painter needs, since the runtime owns the grid and the presentation layer owns
the viewport. ADR-0001's amendment already said the seam outlives the renderer
(`docs/decisions/0001-xterm-js-as-vt-frontend.md:10-17`).

Two smaller facts worth having on the record: today's client-side width authority is the
xterm unicode11 addon (`frontend/package.json:52`), which moves to the backend with the
emulator; and the frontend already has an `aria-live` announcement precedent outside the
terminal (`frontend/src/ui/block-notice.ts:57`), which is the shape accessibility takes
below.

## 3. The candidates, judged

Each is judged against all seven criteria, with an explicit identity statement. Licences
were checked in this session as described in §1.

### 3.1 Fork xterm.js, keep the renderer and selection

This is the option the brief refuses to let pass unjudged, and the package makes it
judgeable: the 5.5.0 tarball ships `src/` complete, so the dependencies are checkable
exactly.

**What it would give.** The most battle-tested surface in the space: the WebGL and canvas
renderers with their texture atlas and custom-glyph drawing (box-drawing and powerline
painted as primitives, not font glyphs — `src/browser/renderer/shared/CustomGlyphs.ts`),
`SelectionService` with its modes, wide-character handling, drag scroll and trim tracking,
`CompositionHelper` for IME, and the DOM renderer plus `WidthCache` whose techniques
ADR-0009 already adopted deliberately
(`docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md:64`, `:122-126`).

**What adopting or forking costs — verified in the package, and it is the whole answer.**
The renderer and selection are not modules; they are leaves of a service graph rooted in the
`Terminal`. `SelectionService`'s constructor takes an element, a screen element, a linkifier
and six injected services — `IBufferService`, `ICoreService`, `IMouseService`,
`IOptionsService`, `IRenderService`, `ICoreBrowserService`
(`src/browser/services/SelectionService.ts:123-133`). The buffer service is not a detail:
the selection model subscribes to buffer trims and buffer activation
(`SelectionService.ts:137-142`), so a selection over _frames_ has to either keep a full
client-side buffer — a second emulator, the exact fidelity defect ADR-0066 exists to remove
— or implement xterm's private `IBuffer` over a frame store, against APIs that are not
public, not typed for external use, and free to change between minor releases. The same
entanglement is in `CompositionHelper` (`textarea`, `compositionView`,
`IBufferService`, `IOptionsService`, `ICoreService`, `IRenderService` —
`src/browser/input/CompositionHelper.ts:44-50`) and in every renderer: `DomRenderer`'s
constructor takes the `ITerminal`, an instantiation service, a linkifier and five services
(`src/browser/renderer/dom/DomRenderer.ts:50-66`). "Extract the renderer" therefore means
extracting the service graph, and the service graph's root is the emulator.

Two specific pieces must be rebuilt no matter what:

- **The Alt-click path generates terminal cursor bytes.**
  `SelectionService.ts:700-711`: on Alt-click within 500 ms it builds
  `moveToCellSequence(...)` (`src/browser/input/MoveToCell.ts:21`) and injects it with
  `this._coreService.triggerDataEvent(sequence, true)`. Under ADR-0066 a client may not
  encode cursor motion — encoding lives where the modes live. This becomes a mouse intent
  (the emulator port already owns mouse encoding) or it is deleted; a fork cannot keep it.
- **`AccessibilityManager` consumes parser-produced events that frames do not reproduce.**
  It registers `onA11yChar`, `onLineFeed` and `onA11yTab`
  (`src/browser/AccessibilityManager.ts:112-114`). Those events are fired inside the VT
  parser — `src/common/InputHandler.ts:546` fires `onA11yChar` as it processes a character,
  and `Terminal.ts:171` forwards it from the input handler. A frame-based client has no such
  stream, so the extracted manager has no input; the accessibility layer is a rewrite
  regardless of the donor.

Per criterion: (1) cells — no, cells must be forced through a synthesised or adapted buffer;
(2) grapheme/widths — its buffer model is codepoints-per-cell with widths computed at parse
time, i.e. a second width authority, exactly what the cutover removes; (3) selection/IME —
yes, the strongest donor, modulo the byte-generating paths above; (4) no parser — the
extraction can exclude the parser, but the service graph it needs was built _around_ the
parser's buffer; (5) readable — the code is good, the indirection is dense, and forking
means owning all of it; (6) MIT — verified in the tarball (`package.json` `license: MIT`,
LICENSE file), fully forkable; (7) fast — proven.

**Identity:** the fork does not preserve runtime identity, it _re-derives_ it — segmentation
and widths would come from whatever fills the adapted buffer, and the five surfaces would
agree with each other but only as faithfully as that adapter, forever pinned to private
internals of an upstream release train. The honest sentence is: this option is a rewrite
wearing xterm's clothes, purchased at the price of also maintaining the disguise.

### 3.2 extraterm-char-render-canvas

The survey's "genuine parser-free WebGL cell painter" needs a correction and a confirmation.
The package is not published on npm under this name — the registry answers "Not Found"
(checked 2026-09-21) — it lives as a workspace package of the Extraterm monorepo
(`sedwards2009/extraterm`, MIT, repo-level licence verified via the GitHub API). The
confirmation: it is genuinely parser-free and cell-oriented — `CellPainter.renderLine`
paints a `CharCellLine` model (`packages/extraterm-char-render-canvas/src/CellPainter.ts:53`),
with glyph atlas, metrics and ligature handling. The correction: **the current head is not a
browser painter at all.** Its imports are `QImage`/`QPainter` from `@nodegui/nodegui`
(`CellPainter.ts:11`), its dependencies include `@nodegui/nodegui` 0.74.0, and the
monorepo's application tree (`main/src/`) is the Qt-era app. Extraterm moved from Electron
to Qt, and the renderer moved with it; the browser/WebGL version the survey remembers is
historical, reachable only through the repository's git history. I did not dig out and read
that historical version in this session — the shape of the Electron-era renderer is
**unverified** beyond the survey's characterisation.

Per criterion: (1) cells — yes; (2) grapheme/widths — partially and it matters: its cell
model is codepoint-oriented (`NormalizedCell.codePoint`, with ligature codepoints —
`CellPainter.ts:65-75`), so adopting it means converting the runtime's grapheme strings
back into codepoint sequences for the atlas. That is a second segmentation at exactly the
joint identity lives on (ZWJ sequences, VS15/VS16); it can be made to agree, but "can be
kept in step" is a weaker guarantee than "never re-derived". (3) selection/IME/a11y — none
of it is in the package; Extraterm keeps those in its application layer, so all three are
built anyway. (4) parser-free — yes. (5) readable — yes, clean and small. (6) MIT — yes
(`packages/extraterm-char-render-canvas/package.json` `license: MIT`). (7) fast — its atlas
is proven, in Qt and formerly in Electron.

**Identity:** codepoint-oriented input is an identity risk; painting and atlas agree with
each other but re-derive clusters the runtime already decided.

### 3.3 terme — a candidate the survey missed

Found in the npm keyword sweep for this search and worth naming because its pitch reads like
the exact thing missing: "Not a full terminal emulator: Terme focuses on rendering"
(`terme` 0.1.0 README, npm). MIT (LICENSE and registry, verified), TypeScript, WebGL2 with
MSDF font rendering, fallbacks to WebGL1/canvas2d/webgpu declared in its public types
(`dist/index.d.ts:13`), first release 2026-01-02, single author.

Its cell model decides the question: a cell is `{ charCode: number; attributes }` — a single
codepoint (`dist/index.d.ts:208-213`). There is no grapheme, no width, no continuation
concept anywhere in the public API; attributes carry fg/bg and ten SGR booleans
(`:16-41`); input is `setContent(lines: (string | TerminalCell[])[])` plus a caret position
(`:163`, `:175`), and the package also ships its own keyboard encoding (`handleKeyPress` in
the same typings), which ADR-0066 assigns to the backend. Per criterion: (1) takes a
_char grid_, not cells; (2) **fails** — a cluster cannot ride a `charCode`, and widths are
not represented at all; (3) fails — no selection, no IME, no accessibility surface; (4) yes,
parser-free; (5) readable and small; (6) MIT; (7) fast by design, unproven at scale, and
0.1.0 with a one-person bus factor.

**Identity:** fails by construction — a codepoint grid cannot carry the runtime's cluster
identity at all. Its realistic value is as prior art for the _later_ canvas painter, the
same shelf as xterm's `TextureAtlas`/`CustomGlyphs` (also MIT) — not as the foundation.

### 3.4 ratzilla

`ratatui/ratzilla` (moved from `orhun/ratzilla`; redirect followed), **Apache-2.0**,
repository licence field verified via the GitHub API, last push 2026-07-04. It is a Rust
framework for building terminal-themed web applications on ratatui, with rendering backends
`dom`, `canvas`, `webgl2` and `cell_sized` verified in its source tree (`src/backend/`).

Per criterion: (1) takes ratatui's cells — its own model, not an embeddable component's;
(2) widths come from ratatui's symbol+unicode-width model on the Rust side of the wasm
boundary — the runtime's cluster arrives as a string to be re-interpreted; (3) fails — no
selection, IME or accessibility story, because it is an application framework whose apps
don't have them; (4) yes, parser-free; (5) readable Rust, but adopting it puts a second
language, toolchain and wasm boundary into a TypeScript frontend for the one part of the
problem that is _not_ hard (painting), while contributing nothing to the parts that are;
(6) Apache-2.0 — fine, with a notice obligation; (7) fast.

**Identity:** re-derived across a language boundary. Wrong shape for a component of an
existing frontend.

### 3.5 firenvim

`glacambre/firenvim`, **GPL-3.0** (repository licence field verified, last push 2026-09-12).
Architecturally it is the closest existing proof that the target design works: Neovim owns
the emulator and streams structured grid redraws to the browser, and
`src/renderer.ts` (1,096 lines) paints them on canvas — per-cell font measurement
(`renderer.ts:279-310`), font string management (`:24-40`), no VT parser anywhere in the
renderer, because the parser lives in Neovim. Per criterion: (1) yes — cells from Neovim's
UI protocol; (2) Neovim's widths arrive precomputed; (3) composition handling exists
(Neovim's IME pathway) but selection/a11y are Neovim-side or absent; (4) yes; (5) readable
TypeScript; (6) **fails — GPL-3.0**. Code cannot be copied into or linked with an MIT
project (`LICENSE`, root) without the combined work becoming GPL. (7) fast enough in
practice — it ships in every firenvim install.

**Identity:** sound architecture, unusable code. It stays on the shelf as a reference for
the canvas stage — in particular its per-cell measurement and composition choreography —
and nowhere else.

### 3.6 hterm

The ChromeOS terminal (`libapps`), repo-level licence **BSD-3-Clause** (verified via the
GitHub API on the `libapps-mirror` repository; I did not verify a per-directory licence
file inside `hterm/` in this session). The design already disposed of it in one clause —
"hterm has the same shape" (`…session-screen-design.md:389`) — and that holds: parser,
screen model and DOM row rendering are one bundle with no extractor boundary between them.
Its DOM printer is the ancestor of the row-of-spans shape nocx already adopted for frozen
blocks (ADR-0009's precedent list cites the same lineage,
`0009-…md:31-35`). Per criterion: (1) no — bytes in; (2) its own width model; (3)
selection exists, IME/a11y are its own machinery; (4) no; (5) large, 2012-era JS;
(6) BSD — forkable; (7) proven but aging. **Identity:** same objection as the xterm fork,
with a less maintained codebase. A precedent, not a donor.

### 3.7 DomTerm

`PerBothner/DomTerm`, last push 2026-09-11. Licence: the GitHub licence field answers
NOASSERTION; the repo's `COPYING` is a three-clause BSD-style text with mixed provenance
(© Per Bothner, "loosely derived from WebTerminal, © Oracle") — permissive in substance,
but any code provenance claim would need the Oracle lineage read carefully. I did not read
DomTerm's terminal sources in this session; the judgement below is architectural.
DomTerm is the strongest _architectural_ precedent nocx has — a DOM terminal whose frozen
transcript and live grid anticipate ADR-0009 — but as a donor it fails criterion 4 the same
way: its DOM cell handling lives inside its own JS emulator bundle (it additionally
integrates xterm.js as an optional engine, which tells you which way the wind blows for
extraction). **Identity:** its DOM techniques overlap what ADR-0009 already built; nothing
in it answers the frame-identity question, because it does not receive frames.

### 3.8 ghostty-web, and the parser-bundled rest

`coder/ghostty-web` — MIT (verified), "Ghostty for the web with xterm.js API
compatibility" — embeds a WASM VT behind the familiar API, i.e. a parser, and was already
measured and rejected in this repository's own bake-off: no OSC surface, canvas-2d cell
rects that antialias into seams at fractional DPR, viewport reset under TUI output
(`docs/decisions/0001-xterm-js-as-vt-frontend.md:44-59`). Fails (4) by construction and was
already judged on (1)-(3) with evidence. `asciinema/avt` — **Apache-2.0** (verified) — is
the other web-cell renderer family anyone will name: a Rust VT interpreter whose cells feed
asciinema's player; the parser is the product, so it fails criterion 4 identically. No
other npm-published parser-free cell painter surfaced in the keyword sweep beyond `terme`,
judged above; absence of evidence from one sweep is noted as such, not as proof.

### 3.9 Write it

A DOM cell painter over full snapshots, in this tree, reusing the machinery §2 inventories.
Judged on all seven:

1. **Cells.** Yes, by definition of the design: the frame is the input, and the runtime's
   `(row, column, grapheme, width)` tuple is preserved verbatim. No synthesised ANSI, no
   client-side re-execution of cursor moves (ADR-0066:107-113).
2. **Grapheme boundaries and authoritative widths.** Yes — and this is the decisive
   property: the painter never segments. The grapheme string and its width arrive together
   from the emulator (§1); the painter's only width-adjacent job is the _typographic_ one
   ADR-0009 already solved — making the browser's advance agree with the authoritative
   width, per run, with `cell-fit.ts` as the measuring authority. Contrast every donor
   above: each one re-derives segmentation somewhere between the wire and the pixel.
3. **Selection by coordinate, IME, accessibility.** Yes, and the DOM surface is the reason.
   Selection by coordinate is native (a DOM selection, or an overlay driven by the same
   coordinate ranges — both keyed on column indices made exact by ADR-0009 rule 3); copy
   assembles text by the wire's own reading rule — cells in order, width-0 continuations
   skipped (`contracts/helper/identities.schema.json:267`); IME rides a hidden textarea
   whose composition events become text intent, which is the same shape the backend intent
   vocabulary already models (`internal/session/intent_ops.go:294-298`); accessibility is
   the strongest of any candidate — the DOM _is_ the screen-reader surface, with real text
   nodes and an `aria-live` channel for announced changes, where every canvas candidate
   must build a parallel shadow DOM (xterm itself uses a DOM renderer as its
   accessibility story, and its AccessibilityManager cannot be lifted because its inputs
   are parser events, §3.1).
4. **No VT parser.** Trivially — nothing in it parses; correctness is decidable against a
   snapshot, which is what "simplest renderer whose correctness can be inspected"
   (§6.11) asks for.
5. **Readable.** It is ours, small, and sits next to the code that already owns the two
   hard sub-problems (measurement and freezing). Nothing to reverse-engineer.
6. **Licensed to fork.** Not applicable — no licence to take, no upstream to track.
7. **Fast enough.** The honest risk, and the constraint that shapes it: the live region is
   viewport-sized (tens of rows, not ten thousand — the frozen half that _is_ tens of
   thousands already renders under `content-visibility` per ADR-0009:173-175), frames
   arrive coalesced at frame rate (AD-10's delivery classes,
   `docs/architecture.md:212-213`), and the painter applies full snapshots by _row-level
   diffing against the previous revision_ — the frame identifies which rows changed, so
   the DOM churn per frame is the changed rows, and an unchanged uniform row stays the
   single text node ADR-0009 preserves. What to avoid is known and named in the sources:
   xterm's own DOM renderer takes an explicit ~20% penalty from making every run
   `inline-block` (`src/browser/renderer/dom/DomRenderer.ts:143`, the author's own
   comment), which ADR-0009's no-inline-block shape already refuses
   (`0009-…md:136-144`). If a paged-redraw producer (`htop`-shaped, the design's §7.1
   worst case) still exceeds budget, the escape hatch is a canvas painter behind the same
   seam — an optimisation of a correct foundation, which is the order §9 prescribes.

**Identity (the binding statement):** the DOM painter is the only candidate whose identity
story is "the runtime's tuple is not re-derived anywhere". Painting runs are grouped by
column arithmetic on authoritative widths; hit-testing is the same arithmetic backwards;
selection anchors are column indices; copy and a11y read the same cells. The remaining
identity hazard is not in the painter but at the _joint_ — a frame changing mid-gesture
(selection drag or composition spanning a revision boundary), which design §6.8 already
names as the hard part (`…session-screen-design.md:370-378`), and it is handled by
protocol shape, not by any donor's machinery: apply a frame atomically per revision, then
re-anchor the live selection by clamping its stored coordinates. That obligation exists in
every option, including the fork — `SelectionService` re-anchors against buffer trims
today (`SelectionService.ts:137-142`).

## 4. Recommendation

**Write the DOM cell painter, over full snapshots, sharing the existing geometry
machinery.** It is the only option that passes all seven criteria without a qualifying
rewrite, it is the option the build order already calls for (§9 step 6: full snapshots,
inspectable correctness, delta and GPU as later optimisations), and it is the only one
whose identity answer is "never re-derived" rather than "kept in step".

### 4.1 The smallest correct version

What it must do first:

1. **Frame intake.** Accept a full snapshot — rows × columns of
   `{grapheme, width, hasText, style}` — behind an interface shaped like the surviving half
   of `TerminalRenderer` (`frontend/src/renderers/types.ts:113-137`: `mount`,
   `fitViewport`, `setGrid`), with `apply(snapshot)` replacing `write`
   (`types.ts:152`). Hold the previous revision for row-level diffing.
2. **Row painting by ADR-0009's rules.** Runs of adjacent cells merged only when
   foreground, background, attributes and spacing agree (`0009-…md:66-79`), per-run
   `letter-spacing`, measured background padding, ink scaled never re-advanced. The
   measuring authority is `cell-fit.ts`'s `CellFit` (`cell-fit.ts:67-79`), fed the same
   probe and signature as the frozen blocks — one owner for "does this cell land on the
   grid", per the AGENTS.md rule that a second implementation of one concept is a delayed
   regression, and `blocks.ts` already mounts exactly this machinery
   (`frontend/src/scrollback/blocks.ts:13`).
3. **Cursor** as an overlay positioned by `(row, col)` arithmetic — never a text-flow
   glyph.
4. **Coordinate mapping.** pixel→`(row, col)` and back, exact by ADR-0009 rule 3 (each
   cell contributes `width × cellWidth`), shared by hit-testing, selection anchors and
   cursor placement so the three cannot disagree.
5. **Selection and copy.** Coordinate ranges; copy reads cells in order and skips width-0
   continuations — the wire's own rule stated in
   `contracts/helper/identities.schema.json:267`.
6. **IME.** A hidden textarea; `compositionstart/update/end` become text intent to the
   backend; the composition is committed before any other send.
7. **Accessibility.** The rows are real text; changed rows are announced through an
   `aria-live` channel (precedent: `frontend/src/ui/block-notice.ts:57`).

What waits: delta application beyond row-level diffing; the canvas/WebGL painter (mining
xterm's `TextureAtlas`/`CustomGlyphs` and/or `terme`'s MSDF pipeline, both MIT, when the
frame-budget evidence says so); OSC 8 links, which are backend-owned after the cutover;
the richer underline styles; the scroll-aware encoder's interaction with repaint scope
(design §7).

Which part is hard to get right, named so it is budgeted rather than discovered:

- **Identity across a mid-gesture frame** — §3.9's joint: atomic per-revision application
  and coordinate-clamped re-anchoring of selection and composition. This is a protocol
  obligation on the frame intake, and design §6.8 already owns the question.
- **The measurement seam.** `cell-fit.ts`'s signature discipline exists because a probe
  that wears different classes measures with one shaping and keys with another
  (`cell-fit.ts:31-36`, `:99-127`). The live region must measure in the same typographic
  context as it paints and as the frozen blocks measure — same probe, same signature, or
  the cached verdict is silently for the wrong font. This is the defect class that cost
  four adversarial rounds once already (ADR-0009:145-147).
- **Run explosion on noisy rows.** A full-repaint row of alternating styles is a span per
  run; the merge key must include spacing (ADR-0009 rule 2), rows memoize by revision, and
  the row-level diff must treat "style changed nowhere" as "no DOM touched".

### 4.2 The honest cost of the option not chosen

Forking xterm.js would hand over, on day one, a GPU renderer with a mature glyph atlas and
custom-glyph drawing, selection with drag-scroll and wide-character handling, and a
composition helper — years of edge cases already paid for. Writing the DOM painter pays
those edge cases ourselves, in order, over time; the DOM surface is slower than WebGL at
the extreme and will need the canvas stage if a producer class outgrows it. That is the
real price, and it is paid on a foundation whose correctness is inspectable, against one
authority, in a codebase we own — rather than maintaining an adapter to private internals
of an upstream project whose service graph presumes the very client-side emulator this
cutover removes.

## 5. Where each candidate fails, in one line each

Fork xterm.js — passes (3)(6)(7); fails identity and effectively (1)(2) unless a shadow
buffer or a private-API adapter is built, and (4)'s exclusion still drags the buffer-shaped
service graph; the Alt-click byte path and the parser-fed AccessibilityManager are
unusable as-is (`SelectionService.ts:700-711`, `AccessibilityManager.ts:112-114`,
`InputHandler.ts:546`).

extraterm-char-render-canvas — passes (1)(4)(5)(6)(7); fails (2) at the identity joint
(codepoint/ligature model) and (3) wholesale; and its browser renderer is historical —
the current head paints through Qt's QPainter (`CellPainter.ts:11`), not the web.

terme — passes (4)(5)(6) and probably (7); fails (1) as char-grid-not-cells, (2) by
construction (`index.d.ts:208-213`), (3) wholesale; 0.1.0 maturity; future canvas-stage
prior art.

ratzilla — passes (1)(4)(6)(7); fails (2) at the wasm boundary, (3) wholesale, (5) as a
second language and toolchain for the wrong half of the problem.

firenvim — passes (1)(2)(4)(5)(7); fails (6) — GPL-3.0 caps everything; reference only.

hterm — passes (6)(7); fails (1)(4), (2) as its own authority, and its DOM techniques are
already adopted here (ADR-0009).

DomTerm — permissive in substance (BSD-style `COPYING`, GitHub NOASSERTION); fails (4);
architectural precedent, not a donor; sources not read this session.

ghostty-web / avt — fail (4) by construction; ghostty-web additionally already measured
and rejected in-repo (ADR-0001:44-59).

## 6. Deliberately out of scope for this note

The frame wire format, the delivery classes' numeric bounds, the alternate-screen contract
and the recovery/completeness rules are the runtime epic's subjects (design §6, §8; AD-10
amendment). This note only assumes what ADR-0066 commits to: frames outbound, structured
input inbound, cells with grapheme boundaries and authoritative widths. None of those wire
contracts exist yet, by that record's own design (`0066-…md:128-134`) — the schema-shaped
precedents cited here (`identities.schema.json`, `agent.emitting.schema.json`) are evidence
of the shape, not the frame contract itself.

## 7. What I could not verify

Said in those words, per the task rules:

- The **Electron-era (browser) extraterm-char-render-canvas** renderer: I verified the
  current head is Qt/NodeGUI and that the package is not on npm, but did not excavate the
  git history to read the historical browser version the survey characterises. Its
  "codepoint-oriented" description is confirmed against the current sources; the historical
  WebGL shape is not.
- **hterm's per-file licence** — the BSD-3-Clause claim is the repository-level licence
  field on the mirror; I did not open `hterm/LICENSE` itself.
- **DomTerm's sources** — licence text read, architecture stated from its documentation
  position and reputation in the cited ADRs; the code was not read this session.
- **`terme`'s runtime behaviour** — judged from its published typings, README, changelog
  and package metadata; it was not executed.
- **Design §6.8's citation drift** — the design points at
  `frontend/src/renderers/xterm.ts:976` and `frontend/src/terminal-content.ts:3335` for the
  mid-drag hazard; both files have moved since the design was written, and I verified the
  surrounding code paths exist but not that those exact lines still carry the cited
  content. The hazard itself is independent of the citation.
- **Performance numbers for the DOM painter at 60 fps** — argued from ADR-0009's
  measurements, xterm's own inline-block penalty note, the viewport-sized live region and
  the design's §7.1 frame statistics; not measured. The first frame-budget measurement is
  an implementation-stage obligation, and ADR-0009's reversal threshold
  (`0009-…md:163-175`) is the criterion that decides it.
