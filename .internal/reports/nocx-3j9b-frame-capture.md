# nocx-3j9b — frame minted in the renderer, with an identity you can compare

## What was built

A frame model in a new `frontend/src/frame/` module, per spec §2.1–2.4 and ADR-0029.

**Capture identity** (buffer instance incl. which alt-screen session + geometry + generation)
and **comparability** (`same | moved | notComparable` — a distinct union value, never a stale
flag). **Generation** advances on `onWriteParsed` + buffer switch + resize + clear + reset,
NEVER on `onRender` (ADR-0005). **Capture fence**: `awaitSettled()` defers a capture while
writes are mid-parse and only opens when the FINAL parse pass settles (xterm fires
onWriteParsed between chunks of a large write, so the fence re-checks
`hasUnsettledWrite()` after every fire — verified against xterm 5.5.0 WriteBuffer source).

**Two sources, both recorded, never substituted:**

- live: `mintLiveFrame` reads cells+attrs+cursor through a `LiveFrameSeam`; provenance
  records buffer identity, geometry, generation, the row range, and the 10000-line
  scrollback-cap eviction note. Per-cell attributes come from the serializer's existing
  `cellAttrs` (AD-8 — no second derivation).
- frozen: `mintFrozenFrame` via a `FrozenFrameSource` seam satisfied from the existing
  `blockOutputText` owner + a new `SERIALIZER_VERSION = 1` constant; provenance records
  source=frozen, the serializer version, the transforms (wrapped lines joined,
  leading/trailing blanks dropped), and `closed: true`.

**Renderer exposure** (`TerminalRenderer` + `XtermRenderer`): `onWriteParsed` (the
task-mandated exposure; subscribers registered before mount are attached at mount via
`_ensureWriteParsed()` so the generation signal is never lost), `onClear`/`onReset`
(renderer-fired AFTER its own `clearViewport()`/`reset()` executed), `hasUnsettledWrite()`
(the fence state — pending count settled via write()'s per-chunk callback, exact even when
onWriteParsed fires mid-write), and `cursorCol()`.

## Files

- NEW `frontend/src/frame/` — `types.ts`, `capture-identity.ts`, `mint.ts`, `frozen.ts`,
  `test-source.ts`, `capture-identity.test.ts`, `mint.test.ts` (7 files)
- `frontend/src/renderers/xterm.ts`, `frontend/src/renderers/types.ts`
- `frontend/src/renderers/xterm.test.ts` (+7 tests)
- `frontend/src/scrollback/serializer.ts` (+7 lines) — **the one scrollback change, called
  out as permitted**: adds the exported `SERIALIZER_VERSION` constant only. Nothing else in
  scrollback/ was touched. No other files changed (git status confirms).

## Tests — 24 added, 83 passing in the scoped suite

| File                     | Tests | Covers                                                                                                                                                                                                                                                                                                                                                                                            |
| ------------------------ | ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| capture-identity.test.ts | 10    | identical-cell write → moved (deliberate false positive asserted); no onRender in the seam (structural) + offscreen write advances; clear/reset advance; same/moved/notComparable values; alt enter/leave/re-enter minting (sessions 1, 2 not comparable); resize → notComparable; fence defers and waits for the FINAL pass (asserted unresolved after pass 1 of 2); immediate resolve when idle |
| mint.test.ts             | 7     | live cells+attrs+cursor+provenance (bold-red attribute travels via serializer cellAttrs); missing line → blank row (frame never lies about gaps); frozen text rows + provenance + version; seam from real block DOM (.term-line spans → blockOutputText); empty block → empty frame, still frozen; fence: unfenced mint reads pre-write rows, fenced mint reads complete post-write rows — no mix |
| xterm.test.ts            | 7 new | onWriteParsed fires after write; a pre-mount subscriber is attached at mount; hasUnsettledWrite true→false (real renderer); onClear/onReset fire; cursorCol after write; refreshAtlas+applyTheme do NOT advance generation (ADR-0005 repaint); write advances generation + awaitSettled on the real renderer                                                                                      |

Scoped run: `vitest run src/frame/capture-identity.test.ts src/frame/mint.test.ts
src/renderers/xterm.test.ts` → 3 files, 83 tests, exit 0. (xterm.test.ts was 59 before;
frame tests are all new.)

## Verification numbers

- Repo `tsc --noEmit` (project-wide, required): **0 errors** (final). During work: 1 error
  (SERIALIZER_VERSION number vs string typing) — fixed.
- Test-file typecheck: repo tsconfig EXCLUDES `*.test.ts`, so I ran a one-off tsc over the
  frame sources + tests AND `xterm.test.ts` with matching flags: **0 errors**. (The
  required repo tsc alone would not have caught a test-only type error — vitest transpiles
  without checking.)
- Red-first evidence: before implementation, the two frame test files failed vitest with
  "Failed to resolve import ./capture-identity / ./mint / ./frozen" (2 failed, 59 passed).

## What I could NOT verify

- End-to-end wiring to the picker / `agent.captureFrame`: another bead, not built.
- Real xterm multi-chunk write timing (jsdom): the fence tests model WriteBuffer's
  semantics from the xterm 5.5.0 source (per-chunk settle callbacks, onWriteParsed between
  passes). A real-browser check of a >12 ms write burst was out of scope for this bead.
- The rest of the suite (full vitest, e2e, containers) — scoped verification only, per the
  brief.

## Contracts / notes for the next bead

- The tracker should still be constructed after mount (matching the existing
  onScroll/onBufferChange consumer contract), but the new onWriteParsed signal is no
  longer silently lossy pre-mount — subscribers buffer and attach at mount.
- Frozen identity is `closed: true` — it can never go stale; live frames compare via
  `compareIdentity`.
- Live frames are minted over an explicit row range; the whole-buffer range is the
  consumer's choice (the seam has no buffer-length getter — avoiding a 10k-line accidental
  commitment). The eviction note in provenance covers the cap.
- Generation advances per parse PASS (onWriteParsed fire), not per write() call — two
  small writes coalescing into one pass advance once. That matches "advances on
  onWriteParsed" and is the conservative direction.
