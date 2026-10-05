import type { SessionFrame } from '../generated/session.frame'
import type { LedgerBlockRowsLine } from '../generated/ledger.blockRows'
import { createCellModel, rowColumnsOf } from '../cell-model'
import { fitCandidatesOf, paintRow } from '../painter/paint-row'
import { decorateLinks } from '../terminal-links/decorate'
import type { FitCandidate } from './cell-fit'
import type { RunMetric } from './run-geometry'
import type { TerminalSnapshot } from './serializer'
import { BlockNotice } from '../ui/block-notice'

export interface StoredBlockRows {
  readonly lines: readonly LedgerBlockRowsLine[]
  /** Immutable artifact identity pinned by the ledger read. */
  readonly artifactVersion?: string
  readonly droppedRows: number
  readonly lostRows: number
  /** Rows lost while nocx's server was unavailable: the terminal kept them
   *  in its scrollback, but the server that stores blocks was away, and the
   *  scrollback pruned them before it returned (ADR-0076 decision 3).
   *  Optional so hand-built fixtures need not carry it; the store omits it
   *  when zero. */
  readonly unavailableRows?: number
  readonly truncated: 'cap' | 'gap' | 'suppressed' | null
  /** The artifact is sealed — its block can grow no more. Only a sealed
   *  read may say a block printed nothing: an open one may simply not have
   *  been streamed yet. Optional only so hand-built fixtures need not carry
   *  it; a read from the wire always sets it (the artifact's own state). */
  readonly sealed?: boolean
}

interface RowsArtifactMetadata {
  readonly truncated: StoredBlockRows['truncated']
  readonly payload: unknown
  /** The artifact's own state, as ledger.get sends it: 'open' | 'sealed'. */
  readonly state?: unknown
}

interface StoredRowLine {
  readonly from: unknown
  readonly row: unknown
}

function nonNegativeInteger(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : 0
}

function rowLine(value: unknown): LedgerBlockRowsLine {
  if (typeof value !== 'object' || value === null) {
    throw new Error('stored block row is malformed')
  }
  const line = value as StoredRowLine
  if (
    !Number.isInteger(line.from) ||
    (line.from as number) < 0 ||
    typeof line.row !== 'object' ||
    line.row === null
  ) {
    throw new Error('stored block row is malformed')
  }
  return {
    from: line.from as number,
    row: line.row as LedgerBlockRowsLine['row'],
  }
}

export function parseStoredBlockRows(
  body: string,
  metadata: RowsArtifactMetadata,
  artifactVersion?: string,
): StoredBlockRows {
  const lines = body
    .split('\n')
    .filter((line) => line.trim() !== '')
    .map((line) => {
      let value: unknown
      try {
        value = JSON.parse(line) as unknown
      } catch {
        throw new Error('stored block row is malformed')
      }
      return rowLine(value)
    })
  const payload =
    typeof metadata.payload === 'object' && metadata.payload !== null
      ? (metadata.payload as Record<string, unknown>)
      : {}
  return {
    lines,
    ...(artifactVersion === undefined ? {} : { artifactVersion }),
    droppedRows: nonNegativeInteger(payload.droppedRows),
    lostRows: nonNegativeInteger(payload.lostRows),
    unavailableRows: nonNegativeInteger(payload.unavailableRows),
    truncated: metadata.truncated,
    sealed: metadata.state === 'sealed',
  }
}

/** The single column count every row in a stored block is padded to: the
 *  widest line among the ones read. `snapshotForRows` uses it to build the
 *  synthetic frame; the frozen-line drift instrument (cell-drift.ts,
 *  nocx-2v80t.3.18) uses it too, to compare the rows it actually measures
 *  against the width the grid painted them at — every row in a stored
 *  block shares this one count, unlike the retired live-buffer path, which
 *  handed out one column count per line. */
export function blockColumnsOf(lines: readonly LedgerBlockRowsLine[]): number {
  return lines.length === 0 ? 0 : Math.max(...lines.map((line) => rowColumnsOf(line.row)))
}

/** A stored block carries no frame geometry of its own (nocx-zg3k3.2.12):
 *  each line's row is only as wide as its own explicit content, which
 *  differs line to line (a short line, an inverse status bar that goes to
 *  the block's own right edge). Building the rectangle the cell model
 *  needs is therefore choosing `cols` — the widest line among the ones
 *  read — and letting the model's own decode pad every shorter row out to
 *  it in the default style, exactly as it pads a live frame's row against
 *  geometry.cols. There is no cell-level padding here any more: that was
 *  the [grapheme, width, hasText]-per-column shape's own bookkeeping, and
 *  the compact wire does not carry cells to pad. */
export function snapshotForRows(lines: readonly LedgerBlockRowsLine[]) {
  if (lines.length === 0) return null
  const cols = blockColumnsOf(lines)
  if (cols === 0) return null
  const frame: SessionFrame = {
    revision: 1,
    geometry: {
      cols,
      rows: lines.length,
      cellWidthPx: 1,
      cellHeightPx: 1,
      revision: 1,
    },
    cursor: { x: 0, y: 0, visible: false },
    rows: lines.map((line) => line.row),
  }
  const result = createCellModel().apply(frame)
  return result.ok ? result.snapshot : null
}

/** Selection reads the same parsed rows that paint the card, never its DOM text.
 * The immutable artifact id is part of the key so a replaced artifact cannot
 * silently resolve an old endpoint. */
const selectionRows = new Map<string, StoredBlockRows>()
function selectionKey(blockId: string, artifactVersion: string): string {
  return `${blockId}\u0000${artifactVersion}`
}
export function storedBlockRowsForSelection(
  blockId: string,
  artifactVersion: string,
): StoredBlockRows | null {
  return selectionRows.get(selectionKey(blockId, artifactVersion)) ?? null
}

export interface StoredBlockPaintOptions {
  readonly metric: RunMetric | null
  readonly palette: TerminalSnapshot
  /** Cell-fit's batch write, run once for the WHOLE block before any row
   *  paints (nocx-2v80t.3.18): cell-fit.ts's own rule is "every write, then
   *  every read" — one forced layout for every candidate this block's rows
   *  carry, so `boxOf` is a pure cache read for the paint pass that
   *  follows. Without it every cell measures as unclassified and no glyph
   *  is ever boxed, whatever the metric says — cell-fit.ts's `warm`,
   *  wired by the caller that owns the CellFit instance (this module holds
   *  no reference of its own, matching `metric` above). Absent is a valid
   *  degrade: a caller with nowhere to measure paints with no boxing,
   *  unchanged from before this wiring existed. */
  readonly warm?: (candidates: Iterable<FitCandidate>) => void
}

// One cause of a card's missing rows, in the store's own vocabulary
// (ledger_block_rows.go: DroppedRows, LostRows, TruncGap, TruncSuppressed):
// a predicate, the count the store carries for it when it carries one, and
// the cause's own sentence — words a person can act on. A cause that arrives
// later (rows lost while the coordinator was away, from the resend work) is
// ONE MORE ENTRY here, never a second derivation beside the others.
interface RowsMissingCause {
  readonly present: (stored: StoredBlockRows) => boolean
  /** How many rows the store says this cause took, or null when the cause
   *  has no count: a stream whose end never arrived, a capture that never
   *  ran. The sentence is worded for both. */
  readonly count: (stored: StoredBlockRows) => number | null
  readonly sentence: (count: number | null) => string
}

// otherLosses is lostRows without the share the server's absence accounts
// for: the store adds a coordinator-unavailable loss to both counts.
function otherLosses(s: StoredBlockRows): number {
  return Math.max(0, s.lostRows - (s.unavailableRows ?? 0))
}

const MISSING_ROW_CAUSES: readonly RowsMissingCause[] = [
  {
    // The per-command output cap (history.outputCapKB) — the one cause a
    // person can act on, so it leads and names the setting. DroppedRows is
    // derived at close; a cap verdict before that arrives with no count.
    present: (s) => s.droppedRows > 0 || s.truncated === 'cap',
    count: (s) => (s.droppedRows > 0 ? s.droppedRows : null),
    sentence: (n) =>
      n === null
        ? 'Output incomplete: the output passed the history output limit (the history.outputCapKB setting).'
        : `Output incomplete: ${n} rows are missing — the output passed the history output limit. Raise the history.outputCapKB setting to keep more.`,
  },
  {
    // The store's one lostRows count sums whatever rode the wire's single
    // loss field: the runtime's struck feeds count ONE per feed (a prune
    // inside a feed may have taken hundreds of rows, and the emulator's
    // ABI cannot count them — internal/sessionruntime/rowstream.go), and
    // the wire contract also lets a helper's own dropped rows ride the
    // same field as exact rows (internal/helper/proto/rows_frame.go). No
    // reader can tell which produced the number, so the sentence claims
    // LOSSES, never a row count (nocx-zg3k3.5.9). The tail stays
    // source-neutral: the same stored field is documented to carry exact
    // helper-drop rows too, so no producer is named as the limitation.
    // Not the cap's doing (deriveBlockRowsDropped subtracts loss before
    // its verdict): raising a limit recovers none of these.
    // unavailableRows is the cause-tagged SHARE of lostRows the store keeps
    // (ledger_block_rows.go unavailableShare), named by its own entry below;
    // counting it here too would state one loss twice.
    present: (s) => otherLosses(s) > 0,
    count: (s) => otherLosses(s),
    sentence: (n) =>
      `Output incomplete: output was lost ${n === 1 ? 'once' : `${n} times`} before it could be captured; this count is not a row count.`,
  },
  {
    // Rows lost while nocx's server was unavailable (ADR-0076 decision 3):
    // the terminal kept them in its scrollback, but the server that stores
    // blocks was away, and the scrollback pruned them before it returned.
    // Distinct from the scrollback's own losses above: the server's absence
    // is the cause, and it is not a limit a person can raise.
    present: (s) => (s.unavailableRows ?? 0) > 0,
    count: (s) => s.unavailableRows ?? 0,
    sentence: (n) =>
      `Output incomplete: ${n === 1 ? '1 row was' : `${n} rows were`} lost while nocx's server was unavailable.`,
  },
  {
    // The stream never arrived whole — its completion fence was never seen,
    // so the ending at least is missing. No count exists for it.
    present: (s) => s.truncated === 'gap',
    count: () => null,
    sentence: () =>
      'Output incomplete: the output stream overflowed, so part of it could not be kept.',
  },
  {
    // Capture was refused by policy and never ran, so there is nothing to
    // count.
    present: (s) => s.truncated === 'suppressed',
    count: () => null,
    sentence: () => 'Output incomplete: capture was refused by policy, so nothing was kept.',
  },
]

/** The one sentence a sealed block with no rows and nothing missing says —
 *  its own readable state, never an unexplained empty body (nocx-zg3k3.5.5). */
const EMPTY_OUTPUT_SENTENCE = 'This command printed no output.'

/** Replace a command block's body with rows read from the ledger artifact. */
export function paintStoredRows(
  block: HTMLElement,
  stored: StoredBlockRows,
  opts: StoredBlockPaintOptions,
): void {
  const blockId = block.dataset.entryId
  if (blockId && stored.artifactVersion) {
    selectionRows.set(selectionKey(blockId, stored.artifactVersion), stored)
  }
  block
    .querySelectorAll(
      ':scope > .cmd-output, :scope > [data-output-incomplete], :scope > [data-output-empty], :scope > [data-output-unreadable]',
    )
    .forEach((el) => el.remove())
  const snapshot = snapshotForRows(stored.lines)
  if (snapshot !== null) {
    opts.warm?.(fitCandidatesOf(snapshot.rows))
    const output = document.createElement('div')
    output.className = 'cmd-output'
    for (let index = 0; index < snapshot.rows.length; index++) {
      const painted = paintRow(snapshot.rows[index], opts)
      const storedLine = stored.lines[index]
      if (storedLine) {
        painted.dataset.blockId = block.dataset.entryId ?? ''
        if (stored.artifactVersion !== undefined) {
          painted.dataset.artifactVersion = stored.artifactVersion
        }
        painted.dataset.logicalLine = String(storedLine.from)
      }
      output.appendChild(painted)
    }
    // Paths and urls become clickable HERE, once per paint, the same "one
    // pass beats a pass per click" rule the retired outputHtml path used
    // (nocx-2v80t.3.18): that call site died with the html string it
    // decorated (block bodies come from stored rows now, blocks.ts
    // freezeBlock's outputHtml is always ''), and nothing replaced it, so a
    // stored block's URLs stopped being links. terminal-links/surface.ts
    // still attaches the one click gesture per tab; this only puts the
    // rows in its reach.
    decorateLinks(output)
    block.appendChild(output)
  }
  // The notice is the kit's own statement line (ui/README.md BlockNotice),
  // one per block by its data attribute, naming EACH cause the store sent —
  // two causes both named, with their counts where the store counted them.
  const causes = MISSING_ROW_CAUSES.filter((cause) => cause.present(stored))
  if (causes.length > 0) {
    const notice = new BlockNotice({
      text: causes.map((cause) => cause.sentence(cause.count(stored))).join(' '),
      tone: 'warning',
    })
    notice.root.dataset.outputIncomplete = 'true'
    notice.mount(block)
  } else if (stored.lines.length === 0 && stored.sealed === true) {
    // A sealed block that holds no rows and lost none: the command printed
    // nothing, which is a real answer and gets its own words. An OPEN block
    // with nothing yet stays silent — its output may still arrive, and the
    // running header already says as much.
    const empty = new BlockNotice({ text: EMPTY_OUTPUT_SENTENCE, tone: 'saved' })
    empty.root.dataset.outputEmpty = 'true'
    empty.mount(block)
  }
}

/** Say, on the block, that its stored rows could not be read
 *  (nocx-2v80t.3.27): the store could not be asked, the artifact read
 *  failed, or what came back does not parse. It is NOT the same sentence as
 *  an empty body — a command that printed output and a command that printed
 *  nothing must not look alike — and not "incomplete" either, which is a
 *  fact the store counted about rows it holds. Whatever rows an earlier read
 *  painted stay: they are true, only the rest is missing. The next read that
 *  succeeds replaces this with what it read (`paintStoredRows` removes it).
 *  One notice per block, by its data attribute: a second failed read
 *  restates it rather than stacking a second line. */
export function paintUnreadableRows(block: HTMLElement): void {
  block.querySelectorAll(':scope > [data-output-unreadable]').forEach((el) => el.remove())
  const notice = new BlockNotice({ text: 'Output could not be read', tone: 'warning' })
  notice.root.dataset.outputUnreadable = 'true'
  notice.mount(block)
}
