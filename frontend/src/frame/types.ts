// The frame model (spec §2.2–2.4, bead nocx-3j9b).
//
// A frame is cells + attributes + cursor of ONE capture identity, minted in
// the renderer at the instant of pointing. Text, never a picture. The capture
// identity is what a frame belongs to and can be compared against: buffer
// instance (normal, or WHICH alt-screen session), geometry (cols × rows) and
// a content generation.
//
// The two capture sources are NOT the same path, and the frame records which
// one it came from — they are never silently substituted for one another:
//
//   live   — alt screen / running block: cells of the active xterm buffer.
//            Provenance records the buffer instance, geometry, generation and
//            that rows may be evicted by the 10000-line scrollback cap while
//            the block still runs.
//   frozen — a block after its freeze: NO xterm cells are left (they are
//            cleared after the visual freeze), and the serializer has already
//            joined wrapped lines and dropped leading/trailing blanks.
//            Provenance records source=frozen and the SERIALIZER VERSION.
//
// AD-8: this module defines the SEAMS. The serializer (scrollback/serializer.ts)
// remains the single owner of text derivation; this module never re-derives
// VT output. The renderer remains the single owner of the buffer; this module
// reads it through the LiveFrameSeam.

import type { CellAttrs } from '../scrollback/serializer'

/** Which buffer instance a frame belongs to. The normal buffer is one
 *  instance; EACH entry into the alternate screen is a new one (entering
 *  mints a new identity, leaving terminates it — spec §2.3). */
type BufferInstance = { kind: 'normal' } | { kind: 'alternate'; altSession: number }

/** Capture identity: what a frame belongs to and can be compared against.
 *  `generation` advances on onWriteParsed plus the explicit state-changing
 *  operations (buffer switch, resize, clear, reset) — NEVER on onRender
 *  (ADR-0005 forces periodic repaints on Linux/WebKitGTK, so a paint-driven
 *  generation would stale a motionless screen continuously, on one platform
 *  only). */
export interface CaptureIdentity {
  buffer: BufferInstance
  cols: number
  rows: number
  generation: number
}

/** The outcome of comparing a saved identity with the current one.
 *
 *  `notComparable` is a DISTINCT VALUE, never a flag on staleness: a buffer
 *  switch or a resize is incomparability (the alt buffer's contents are
 *  discarded on exit; a resize reflows and shifts absolute line indices).
 *  Only `moved` — same instance, same geometry, generation differs — is
 *  "the screen was written to", and per ADR-0029 it is a trigger for
 *  re-evaluation, never itself a verdict.
 */
export type CaptureComparability =
  { status: 'same' } | { status: 'moved' } | { status: 'notComparable' }

/** One cell of a live frame: the character plus its attributes as xterm
 *  reports them, resolved against the theme snapshot taken at mint time. */
interface FrameCell {
  char: string
  attrs: CellAttrs
}

/** One row of a frame. Live rows are cells+attributes; frozen rows are TEXT —
 *  a frozen block has no xterm cells left, and its text has already been
 *  transformed by the serializer. The row kind records which. */
export type FrameRow = { kind: 'cells'; cells: FrameCell[] } | { kind: 'text'; text: string }

/** Provenance of a live frame. Records the buffer instance, geometry,
 *  generation, and that rows may be evicted by the 10000-line scrollback cap
 *  while the block still runs. */
interface LiveProvenance {
  source: 'live'
  identity: CaptureIdentity
  /** Absolute buffer row range this frame holds ([start, end)). */
  range: { start: number; end: number }
  /** The active buffer is a 10000-line cap: as new output arrives the OLDEST
   *  rows are evicted from the top of the buffer, so the frame's low row
   *  indices are a suffix — the block may have had rows that the cap already
   *  dropped while it still ran. The frame records what survives, and this
   *  note names why earlier rows may be gone. */
  scrollbackCapLines: 10000
}

/** Provenance of a frozen frame. Records source=frozen and the serializer
 *  version — the version of the transform that produced the text (wrapped
 *  lines joined, leading/trailing blanks dropped). A frozen block's identity
 *  is closed: it can never move, so it can never go stale. */
interface FrozenProvenance {
  source: 'frozen'
  /** Serializer version that produced this block's text — a bumpable
   *  integer; the constant lives with the serializer (AD-8). */
  serializerVersion: number
  /** The serializer's transforms, recorded so the text's provenance is never
   *  mistaken for raw cells: wrapped lines are joined and leading/trailing
   *  blanks dropped. */
  transforms: 'wrapped lines joined; leading/trailing blanks dropped'
  /** A frozen block's identity is closed — its generation never advances
   *  again (spec §2.3, "the degenerate case"). */
  closed: true
}

type CaptureProvenance = LiveProvenance | FrozenProvenance

/** A frame minted in the renderer: rows + cursor of one capture identity,
 *  with provenance naming the source. Text, never a picture. */
export interface CapturedFrame {
  rows: FrameRow[]
  /** Absolute cursor position. null for a frozen frame — there is no cursor
   *  in a serialized block. */
  cursor: { line: number; col: number } | null
  provenance: CaptureProvenance
}

/** The renderer fact surface the capture identity is derived from.
 *
 *  XtermRenderer satisfies this structurally (the same event shapes as
 *  TerminalRenderer). The tracker requires the WHOLE contract: a source that
 *  cannot report clear/reset/write-pending would silently under-count the
 *  generation, which is exactly the false "unchanged" the spec forbids.
 */
export interface CaptureEventSource {
  /** Fires after a written chunk has been parsed into the buffer — the
   *  generation's advance signal AND the capture fence. xterm fires this at
   *  the end of every parse pass, which can be BETWEEN chunks of a large
   *  write; hasUnsettledWrite() distinguishes "settled" from "chunk done". */
  onWriteParsed(cb: () => void): void
  onBufferChange(cb: (type: 'normal' | 'alternate') => void): void
  onResize(cb: (cols: number, rows: number) => void): void
  /** Fired AFTER the renderer executed a full clear (clearViewport). */
  onClear(cb: () => void): void
  /** Fired AFTER the renderer executed a full reset. */
  onReset(cb: () => void): void
  /** Fires when the source is disposed (tab close, renderer replacement) —
   *  the fence's CLOSING event. A capture parked on hasUnsettledWrite()
   *  after this can never settle on its own: the per-write callback went
   *  away with the terminal, so the pending count is stuck. AGENTS.md rule
   *  3 — an invariant needs both ends; this is the end that lets the
   *  tracker reject pending awaitSettled() waiters instead of orphaning
   *  them. */
  onDispose(cb: () => void): void
  /** True while bytes queued via write() have not finished parsing. The
   *  capture fence: a frame minted mid-queue can hold row 1 from before a
   *  write and row 20 from after it — a state that never existed. */
  hasUnsettledWrite(): boolean
  readonly cols: number
  readonly rows: number
}
