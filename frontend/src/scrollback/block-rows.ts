import type { SessionFrame } from '../generated/session.frame'
import type { LedgerBlockRowsLine } from '../generated/ledger.blockRows'
import { createCellModel, rowColumnsOf } from '../cell-model'
import { fitCandidatesOf, paintedRowText, paintRow } from '../painter/paint-row'
import { decorateLinks } from '../terminal-links/decorate'
import type { FitCandidate } from './cell-fit'
import type { RunMetric } from './run-geometry'
import type { TerminalSnapshot } from './serializer'
import { BlockNotice } from '../ui/block-notice'
import { tPlural } from '../i18n/locale-model'

export interface StoredRowRange {
  readonly from: number
  /** Exclusive. */
  readonly to: number
}

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
  /** The geometry of the WHOLE block (ADR-y45sp41c): firstRow..endRow is the
   *  stream span it ever held, retainedRanges names the runs the store keeps,
   *  and rowCount is how many retained rows that is. Height is reserved from
   *  this geometry once; a page arriving later never changes it. */
  readonly firstRow?: number
  readonly lastRow?: number
  readonly endRow?: number
  readonly rowCount?: number
  readonly retainedRanges?: readonly StoredRowRange[]
  /** False for the LIVE block: its rows are still arriving, and reserving a
   *  stored span it may not paint yet is the "room first, rows later" frame
   *  output-jitter exists to catch. Frozen and restored blocks reserve. */
  readonly reserve?: boolean
}

/** The artifact's own geometry, as a read reports it. */
export interface StoredBlocksRange {
  readonly firstRow: number
  readonly lastRow: number
  readonly endRow: number
  readonly rowCount: number
  readonly retainedRanges: readonly StoredRowRange[]
  readonly rangeStart: number | null
  readonly rangeCount: number
  readonly hasMoreBefore: boolean
  readonly hasMoreAfter: boolean
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

/** The runs of stream rows a set of loaded lines covers, ascending. A page
 *  around the viewport and the tail as it grows are two runs with an interior
 *  hole between them; coverage is a question about these, never about the
 *  first and last loaded row alone (ADR-y45sp41c). */
export function loadedRowRanges(
  lines: readonly Pick<LedgerBlockRowsLine, 'from'>[],
): StoredRowRange[] {
  const ranges: { from: number; to: number }[] = []
  for (const line of lines) {
    const from = line.from
    const last = ranges[ranges.length - 1]
    if (last !== undefined && from === last.to) last.to = from + 1
    else if (last !== undefined && from < last.to) continue
    else ranges.push({ from, to: from + 1 })
  }
  return ranges
}

/** The rows a block still has to fetch: retained runs minus the loaded ones,
 *  ascending. An interior hole is a missing range like any other — that is
 *  why coverage cannot be a first/last-row test (ADR-y45sp41c). */
export function missingRowRanges(
  retained: readonly StoredRowRange[],
  loaded: readonly StoredRowRange[],
): StoredRowRange[] {
  const out: StoredRowRange[] = []
  let at = 0
  for (const range of retained) {
    let cursor = range.from
    while (cursor < range.to) {
      while (at < loaded.length && loaded[at].to <= cursor) at++
      const hit = at < loaded.length ? loaded[at] : undefined
      if (hit !== undefined && hit.from <= cursor) {
        cursor = Math.min(range.to, hit.to)
        continue
      }
      const stop = hit !== undefined && hit.from < range.to ? hit.from : range.to
      out.push({ from: cursor, to: stop })
      cursor = stop
    }
  }
  return out
}

/** The page to fetch for a visible stream row, or null when that row is
 *  already loaded or lies in a gap the store does not hold. The decision uses
 *  the missing RANGES, so an interior hole is fetched when the reader reaches
 *  it even if the tail is loaded (ADR-y45sp41c). */
export function nextPageForRows(
  retained: readonly StoredRowRange[],
  loaded: readonly StoredRowRange[],
  target: number,
  pageSize: number,
): { fromRow: number; rowLimit: number } | null {
  const missing = missingRowRanges(retained, loaded).find(
    (range) => target >= range.from && target < range.to,
  )
  if (missing === undefined) return null
  const from = Math.max(missing.from, Math.min(missing.to - 1, target - Math.floor(pageSize / 4)))
  return { fromRow: from, rowLimit: Math.max(1, Math.min(pageSize, missing.to - from)) }
}

/** A geometry range list, validated. Anything malformed is no geometry at
 *  all: a client that invented runs from a broken payload would reserve a
 *  height nobody can correct. */
export function storedRowRanges(value: unknown): StoredRowRange[] | undefined {
  if (!Array.isArray(value)) return undefined
  const out: StoredRowRange[] = []
  let previousTo = -1
  for (const item of value) {
    if (typeof item !== 'object' || item === null) return undefined
    const raw = item as { from?: unknown; to?: unknown }
    const from = raw.from
    const to = raw.to
    if (
      typeof from !== 'number' ||
      typeof to !== 'number' ||
      !Number.isInteger(from) ||
      !Number.isInteger(to) ||
      from < 0 ||
      to <= from ||
      from < previousTo
    ) {
      return undefined
    }
    out.push({ from, to })
    previousTo = to
  }
  return out
}

function geometryOf(
  payload: Record<string, unknown>,
  range: StoredBlocksRange | undefined,
  lines: readonly LedgerBlockRowsLine[],
): Pick<StoredBlockRows, 'firstRow' | 'lastRow' | 'endRow' | 'rowCount' | 'retainedRanges'> {
  const carried =
    range?.retainedRanges ??
    storedRowRanges(payload.retainedRanges) ??
    (range === undefined
      ? storedRowRanges(payload.ranges)
      : (payload.retainedRanges as StoredRowRange[] | undefined))
  let retained =
    carried !== undefined && carried.length > 0
      ? [...carried]
      : (range?.rowCount ?? nonNegativeInteger(payload.rowCount)) > 0
        ? [
            {
              from: range?.firstRow ?? nonNegativeInteger(payload.firstRow),
              to:
                (range?.firstRow ?? nonNegativeInteger(payload.firstRow)) +
                (range?.rowCount ?? nonNegativeInteger(payload.rowCount)),
            },
          ]
        : loadedRowRanges(lines)
  if (retained.length === 0) {
    const first = range?.firstRow ?? nonNegativeInteger(payload.firstRow)
    const count = range?.rowCount ?? nonNegativeInteger(payload.rowCount)
    if (count > 0) retained = [{ from: first, to: first + count }]
  }
  // Loaded rows are always inside the geometry. A legacy payload with no
  // ranges is made to cover them rather than dropping the rows it cannot
  // place.
  for (const line of lines) {
    if (retained.some((rr) => line.from >= rr.from && line.from < rr.to)) continue
    retained.push({ from: line.from, to: line.from + 1 })
  }
  retained.sort((a, b) => a.from - b.from)
  const merged: StoredRowRange[] = []
  for (const rr of retained) {
    const last = merged[merged.length - 1]
    if (last !== undefined && rr.from <= last.to)
      merged[merged.length - 1] = { from: last.from, to: Math.max(last.to, rr.to) }
    else merged.push(rr)
  }
  const firstRow = merged[0]?.from ?? range?.firstRow ?? nonNegativeInteger(payload.firstRow)
  const lastRow = merged.length > 0 ? merged[merged.length - 1].to - 1 : firstRow
  const rowCount = merged.reduce((total, rr) => total + (rr.to - rr.from), 0)
  const endRow = Math.max(
    range?.endRow ?? nonNegativeInteger(payload.endRow),
    merged.length > 0 ? merged[merged.length - 1].to : 0,
    range?.lastRow !== undefined ? range.lastRow + 1 : 0,
    lines.length > 0 ? lines[lines.length - 1].from + 1 : 0,
  )
  return { firstRow, lastRow, endRow, rowCount, retainedRanges: merged }
}

export function parseStoredBlockRows(
  body: string,
  metadata: RowsArtifactMetadata,
  artifactVersion?: string,
  range?: StoredBlocksRange,
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
    ...geometryOf(payload, range, lines),
    reserve: true,
  }
}

/** Two reads of one artifact, joined by the row index each line carries.
 *  `next` owns the geometry and artifact state — it is the newer read — while
 *  the lines are the union, so a page of the tail cannot blank the pages
 *  above it. */
export function mergeStoredBlockRows(
  previous: StoredBlockRows | undefined,
  next: StoredBlockRows,
): StoredBlockRows {
  if (
    previous === undefined ||
    previous.artifactVersion === undefined ||
    previous.artifactVersion !== next.artifactVersion
  ) {
    return next
  }
  const byFrom = new Map<number, StoredBlockRows['lines'][number]>()
  for (const line of previous.lines) byFrom.set(line.from, line)
  for (const line of next.lines) byFrom.set(line.from, line)
  const lines = [...byFrom.values()].sort((a, b) => a.from - b.from)
  if (next.retainedRanges !== undefined && next.retainedRanges.length > 0) {
    return { ...next, lines }
  }
  if (previous.retainedRanges === undefined || previous.retainedRanges.length === 0) {
    return { ...next, lines }
  }
  // A legacy read that carried no geometry takes the one already known; the
  // lines it brought are still added, so a whole-body refresh cannot blank a
  // page that is already painted.
  return {
    ...next,
    lines,
    firstRow: previous.firstRow,
    lastRow: previous.lastRow,
    endRow: Math.max(next.endRow ?? 0, previous.endRow ?? 0),
    rowCount: previous.rowCount,
    retainedRanges: previous.retainedRanges,
  }
}

/** A block whose rows have not been fetched yet, with the height it must
 *  reserve. The body is empty on purpose: an unloaded block is not a block
 *  that printed nothing, and `sealed` is only set when the store said so. */
export function describeStoredBlockRows(
  metadata: RowsArtifactMetadata,
  artifactVersion: string | undefined,
  range: StoredBlocksRange,
): StoredBlockRows {
  return parseStoredBlockRows('', metadata, artifactVersion, range)
}

/** The single column count every row in a stored block is padded to: the
 *  widest line among the ones read. `snapshotForRows` uses it to build the
 *  synthetic frame; the frozen-line drift instrument (cell-drift.ts,
 *  nocx-2v80t.3.18) uses it too, to compare the rows it actually measures
 *  against the width the grid painted them at — every row in a stored
 *  block shares this one count, unlike the retired live-buffer path, which
 *  handed out one column count per line. */
export function blockColumnsOf(lines: readonly LedgerBlockRowsLine[]): number {
  let cols = 0
  for (const line of lines) cols = Math.max(cols, rowColumnsOf(line.row))
  return cols
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
  return snapshotAtColumns(lines, blockColumnsOf(lines))
}

/** Decode only the rows being painted, against the whole block's width.
 * Selection still requests its complete snapshot through snapshotForRows. */
function snapshotAtColumns(lines: readonly LedgerBlockRowsLine[], cols: number) {
  if (lines.length === 0 || cols === 0) return null
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
    state: {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: false,
      pointerShape: null,
      title: null,
      cwd: null,
    },
    rows: lines.map((line) => line.row),
  }
  const result = createCellModel().apply(frame)
  return result.ok ? result.snapshot : null
}

/** Selection reads the same parsed rows that paint the card, never its DOM text.
 *
 *  THE BLOCK ELEMENT OWNS ITS ROWS (nocx-3an1s.1.6). This was a module-global
 *  Map keyed by block id and artifact version, so every block ever painted
 *  stayed reachable for the life of the renderer and nothing ever deleted one —
 *  the owner measured 46 stored-row artifacts still held for 45 closed tabs.
 *  A weak key makes the entry's lifetime the block's: the tab closes, the
 *  element leaves the document, and the last reference to its rows goes with
 *  it, because nothing else keeps the element alive.
 *
 *  One entry per element also settles artifact versions: the read that paints
 *  a newer artifact REPLACES the older entry rather than accumulating beside
 *  it, and the version still has to match the one the selection asked for, so
 *  a superseded artifact resolves to nothing rather than serving rows that no
 *  longer describe the block. */
const selectionRows = new WeakMap<HTMLElement, StoredBlockRows>()

export function storedBlockRowsForSelection(
  block: HTMLElement,
  artifactVersion: string,
): StoredBlockRows | null {
  const stored = selectionRows.get(block)
  return stored !== undefined && stored.artifactVersion === artifactVersion ? stored : null
}

export interface StoredBlockPaintOptions {
  readonly metric: RunMetric | null
  readonly palette: TerminalSnapshot
  /** Whether the block reserves its whole stored geometry. A live block
   *  paints false: its rows are arriving and its body grows with them. */
  readonly reserve?: boolean
  /** Cell-fit's batch write, run once for all rows being repainted before
   *  any of them paints (nocx-2v80t.3.18): cell-fit.ts's own rule is "every
   *  write, then every read" — one forced layout for their candidates, so
   *  `boxOf` is a pure cache read for the paint pass that
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
    //
    // SAID HERE ONLY WHEN THE BLOCK HAS NO ROWS TO SAY IT AT (nocx-ipuy3).
    // A block whose rows carry a cut states the cap AT THE CUT
    // (storedRowCuts/cutSentence below): one line, in the history, where a
    // person scrolls to it, instead of a summary under the whole block that
    // is on screen whenever its end is. With no rows there is no cut to
    // point at, and this is the only place left for the fact.
    present: (s) =>
      (s.rowCount ?? s.lines.length) === 0 && (s.droppedRows > 0 || s.truncated === 'cap'),
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

function missingOutputText(stored: StoredBlockRows): string {
  return MISSING_ROW_CAUSES.filter((cause) => cause.present(stored))
    .map((cause) => cause.sentence(cause.count(stored)))
    .join(' ')
}

/** A completion can read backend-owned output even while its pane defers DOM
 * paint. Use the painter's text spelling after the cell model decodes, so
 * indentation, blanks and wide-cell spacers agree with visible output. */
export function storedBlockRowsText(stored: StoredBlockRows): string {
  const snapshot = snapshotForRows(stored.lines)
  return snapshot ? snapshot.rows.map(paintedRowText).join('\n') : missingOutputText(stored)
}

/** Reconcile a command block's body with rows read from the ledger artifact. */
export function paintStoredRows(
  block: HTMLElement,
  stored: StoredBlockRows,
  opts: StoredBlockPaintOptions,
): void {
  const blockId = block.dataset.entryId
  if (blockId && stored.artifactVersion) selectionRows.set(block, stored)
  else selectionRows.delete(block)
  block
    .querySelectorAll(
      ':scope > [data-output-incomplete], :scope > [data-output-empty], :scope > [data-output-unreadable]',
    )
    .forEach((el) => el.remove())
  // THE ARTIFACT'S OWN STATE, ON THE BLOCK (nocx-4y82c.1.3). A sealed artifact
  // holds every row the command will ever have, and `open` means more are
  // coming; a reader that measures the body can wait for `true` instead of
  // guessing from a row count. One attribute, written where the paint is, so
  // it cannot describe a different artifact than the rows beside it.
  if (stored.sealed === undefined) delete block.dataset.rowsSealed
  else block.dataset.rowsSealed = stored.sealed ? 'true' : 'false'
  // The eviction sentence says a body is not here, and a rows read IS that
  // body. A restored command answers `body: null` whenever its rows artifact
  // exists (restore-client.ts), so every one of them starts with the
  // sentence and then paints its real rows; keeping both left two
  // `.cmd-output` elements, one showing the output and one saying it is
  // gone. The sentence and the block's `data-output-evicted` are one fact,
  // written together by restored-block.ts, so they are taken back together.
  // A turn's prose notice is a claim about a different body and is never
  // this paint's to take back.
  if (block.dataset.blockKind !== 'ask' && block.dataset.outputEvicted === 'true') {
    block.querySelectorAll(':scope > .cmd-output.cmd-output-evicted').forEach((el) => el.remove())
    delete block.dataset.outputEvicted
  }
  paintOutput(block, stored, opts)
  // The notice is the kit's own statement line (ui/README.md BlockNotice),
  // one per block by its data attribute, naming EACH cause the store sent —
  // two causes both named, with their counts where the store counted them.
  const missing = missingOutputText(stored)
  if (missing !== '') {
    const notice = new BlockNotice({
      text: missing,
      tone: 'warning',
    })
    notice.root.dataset.outputIncomplete = 'true'
    notice.mount(block)
  } else if (stored.lines.length === 0 && (stored.rowCount ?? 0) === 0 && stored.sealed === true) {
    // A sealed block that holds no rows and lost none: the command printed
    // nothing, which is a real answer and gets its own words. An OPEN block
    // with nothing yet stays silent — its output may still arrive, and the
    // running header already says as much. A block whose rows exist but have
    // not been paged in yet is neither: it is reserving its height, and
    // saying it printed nothing would be a lie until the page arrives.
    const empty = new BlockNotice({ text: EMPTY_OUTPUT_SENTENCE, tone: 'saved' })
    empty.root.dataset.outputEmpty = 'true'
    empty.mount(block)
  }
}

/** Repaint already loaded artifacts without fetching or moving their block's
 * live region and notices. Selection and paint read the same cached rows. */
export function repaintStoredRows(container: HTMLElement, opts: StoredBlockPaintOptions): void {
  for (const output of container.querySelectorAll<HTMLElement>('.cmd-output')) {
    const row = output.querySelector<HTMLElement>('[data-artifact-version]')
    const block = output.parentElement
    const id = block?.dataset.entryId
    const version = row?.dataset.artifactVersion
    if (!block || !id || !version) continue
    const stored = storedBlockRowsForSelection(block, version)
    if (stored) paintOutput(block, stored, opts, true)
  }
}

interface PaintedStoredRow {
  readonly row: LedgerBlockRowsLine['row']
  readonly columns: number
  readonly element: HTMLElement
}

type PaintMetric = Pick<
  RunMetric,
  'cellWidth' | 'defaultSpacing' | 'padY' | 'pitch' | 'signature'
> & {
  readonly hasBox: boolean
}

/** Only the currently retained rows, weakly owned by their block. Compact
 *  comparison snapshots are not another decoded model or selection source:
 *  the existing cell model still decodes and selectionRows still owns copy. */
const paintedRows = new WeakMap<HTMLElement, StoredPaintState>()

function sameRow(a: LedgerBlockRowsLine['row'], b: LedgerBlockRowsLine['row']): boolean {
  if (a.text !== b.text || a.wrap !== b.wrap || a.continuation !== b.continuation) return false
  if ((a.marks?.length ?? 0) !== (b.marks?.length ?? 0)) return false
  for (let index = 0; index < (a.marks?.length ?? 0); index++) {
    const left = a.marks![index]
    const right = b.marks![index]
    if (left[0] !== right[0] || left[1] !== right[1] || left[2] !== right[2]) return false
  }
  if ((a.runs?.length ?? 0) !== (b.runs?.length ?? 0)) return false
  for (let index = 0; index < (a.runs?.length ?? 0); index++) {
    const [left, length] = a.runs![index]
    const [right, otherLength] = b.runs![index]
    if (length !== otherLength) return false
    if (left === 0 || right === 0) {
      if (left !== right) return false
    } else {
      for (let field = 0; field < left.length; field++) {
        if (left[field] !== right[field]) return false
      }
    }
  }
  return true
}

function samePaintOptions(previous: StoredPaintState, opts: StoredBlockPaintOptions): boolean {
  const before = previous.metric
  const now = opts.metric
  if (before === null || now === null) {
    if (before !== now) return false
  } else if (
    before.cellWidth !== now.cellWidth ||
    before.defaultSpacing !== now.defaultSpacing ||
    before.padY !== now.padY ||
    before.pitch !== now.pitch ||
    before.signature !== now.signature ||
    before.hasBox !== (now.boxOf !== undefined)
  ) {
    return false
  }
  const palette = previous.palette
  return (
    palette.defaultFg === opts.palette.defaultFg &&
    palette.defaultBg === opts.palette.defaultBg &&
    palette.palette.every((color, index) => color === opts.palette.palette[index])
  )
}

type Segment =
  | { readonly kind: 'row'; readonly from: number; readonly key: string }
  | { readonly kind: 'spacer'; readonly from: number; readonly rows: number; readonly key: string }
  | { readonly kind: 'notice'; readonly from: number; readonly rows: number; readonly key: string }

interface StoredPaintState {
  readonly output: HTMLElement
  readonly rows: Map<number, PaintedStoredRow>
  readonly segments: Segment[]
  readonly elements: Map<string, HTMLElement>
  readonly columns: number
  readonly reserve: boolean
  readonly metric: PaintMetric | null
  readonly palette: TerminalSnapshot
}

/** One block-height spacer. A row's height is the renderer's published cell
 *  height, so the reservation is exact without measuring anything, including
 *  before the first row of it has ever been painted. */
function spacerOf(rows: number, from: number): HTMLElement {
  const el = document.createElement('div')
  el.className = 'cmd-output-spacer'
  el.dataset.rows = String(rows)
  // A spacer is a row too, for the reader's anchor: its logical line is the
  // stream row it starts at, so replacing it with painted rows can be followed
  // to the same row rather than to whichever spacer is its neighbour.
  el.dataset.logicalLine = String(from)
  el.setAttribute('aria-hidden', 'true')
  el.style.height = `calc(var(--term-cell-height, 1.2em) * ${rows})`
  return el
}

/** The DOM order of a block's body, decided from its GEOMETRY: every retained
 *  run is either loaded rows, one spacer for what is not fetched, or a single
 *  notice where the store keeps no rows at all (ADR-y45sp41c). The height of
 *  this sequence is a fact about the artifact, never about what happened to
 *  load, so a page arriving only swaps a spacer for rows of the same height.
 *
 *  A LIVE block reserves nothing beyond what it may paint (reserve:false): its
 *  rows are still arriving and its held body must grow with them. */
function buildSegments(stored: StoredBlockRows): Segment[] {
  const loaded = [...stored.lines].sort((a, b) => a.from - b.from)
  if (stored.reserve === false) {
    const out: Segment[] = []
    let previous: number | null = null
    for (const line of loaded) {
      if (previous !== null && line.from > previous + 1) {
        out.push({
          kind: 'notice',
          from: previous,
          rows: line.from - previous - 1,
          key: `n:${previous}`,
        })
      }
      out.push({ kind: 'row', from: line.from, key: `r:${line.from}` })
      previous = line.from
    }
    return out
  }
  const ranges =
    stored.retainedRanges !== undefined && stored.retainedRanges.length > 0
      ? [...stored.retainedRanges].sort((a, b) => a.from - b.from)
      : (stored.rowCount ?? 0) > 0
        ? [
            {
              from: stored.firstRow ?? 0,
              to: stored.endRow ?? (stored.lastRow ?? stored.firstRow ?? 0) + 1,
            },
          ]
        : loadedRowRanges(loaded)
  if (ranges.length === 0) return []
  const endRow = Math.max(stored.endRow ?? 0, ranges[ranges.length - 1].to)
  const out: Segment[] = []
  let loadedAt = 0
  let previousEnd: number | null = null
  for (const range of ranges) {
    if (previousEnd !== null && range.from > previousEnd) {
      const rows = range.from - previousEnd
      out.push({ kind: 'notice', from: previousEnd, rows, key: `n:${previousEnd}` })
    }
    let cursor = range.from
    while (cursor < range.to) {
      while (loadedAt < loaded.length && loaded[loadedAt].from < cursor) loadedAt++
      const next = loadedAt < loaded.length ? loaded[loadedAt].from : null
      if (next === cursor) {
        out.push({ kind: 'row', from: cursor, key: `r:${cursor}` })
        cursor++
        continue
      }
      const stop = next !== null && next < range.to ? next : range.to
      const rows = stop - cursor
      out.push({ kind: 'spacer', from: cursor, rows, key: `s:${cursor}:${rows}` })
      cursor = stop
    }
    previousEnd = range.to
  }
  if (endRow > previousEnd!) {
    const rows = endRow - previousEnd!
    out.push({ kind: 'notice', from: previousEnd!, rows, key: `n:${previousEnd!}` })
  }
  return out
}

/** The ONE mapping from a visual row offset inside a block's body to the
 *  stream row drawn there. It walks the same segments the painter lays out, so
 *  a gap compressed to one notice maps to the first row of the run after it,
 *  not to its visual offset — the loader must ask for the capped tail, not for
 *  a row the store evicted (ADR-ywzcpbqe). */
export function streamRowAtVisualOffset(stored: StoredBlockRows, visualRow: number): number {
  const segments = buildSegments(stored)
  if (segments.length === 0) return stored.firstRow ?? 0
  const target = Math.max(0, visualRow)
  let cursor = 0
  for (const segment of segments) {
    if (segment.kind === 'row') {
      if (target === cursor) return segment.from
      cursor += 1
      continue
    }
    if (segment.kind === 'spacer') {
      if (target < cursor + segment.rows) return segment.from + (target - cursor)
      cursor += segment.rows
      continue
    }
    if (target === cursor) return segment.from + segment.rows
    cursor += 1
  }
  return stored.lastRow ?? (stored.endRow ?? 1) - 1
}

function blankRowElement(): HTMLElement {
  const el = document.createElement('div')
  el.className = 'term-grid-row'
  return el
}

function cutSentence(stored: StoredBlockRows, rows: number): string {
  if (stored.truncated === 'cap') {
    return tPlural(
      'scrollback.cut.cap',
      rows,
      "Earlier output was not kept: this session's size limit dropped {count} rows here.",
    )
  }
  return tPlural('scrollback.cut.rows', rows, '{count} rows of earlier output were not kept here.')
}

/** Reconcile the output's children with the desired segment sequence. The
 *  elements are owned by their keys: a page that fills an interior hole swaps
 *  one spacer for its rows and leaves every other node where it was. */
function layoutSegments(
  output: HTMLElement,
  segments: Segment[],
  elements: Map<string, HTMLElement>,
): void {
  const desired: HTMLElement[] = segments.map((segment) => {
    const element = elements.get(segment.key)
    if (element !== undefined) return element
    throw new Error(`no element for segment ${segment.key}`)
  })
  let cursor = output.firstChild
  const keep = new Set<Node>(desired)
  for (const element of desired) {
    if (element === cursor) {
      cursor = cursor.nextSibling
      continue
    }
    output.insertBefore(element, cursor)
  }
  while (cursor !== null) {
    const next = cursor.nextSibling
    if (!keep.has(cursor)) output.removeChild(cursor)
    cursor = next
  }
}

function paintOutput(
  block: HTMLElement,
  stored: StoredBlockRows,
  opts: StoredBlockPaintOptions,
  force = false,
): void {
  const cached = paintedRows.get(block)
  const previous = cached?.output.parentElement === block ? cached : undefined
  const output =
    previous?.output ??
    block.querySelector<HTMLElement>(':scope > .cmd-output') ??
    document.createElement('div')
  const lines = [...stored.lines].sort((a, b) => a.from - b.from)
  const reserve =
    opts.reserve ?? (stored.reserve !== undefined ? stored.reserve : (previous?.reserve ?? true))
  const segments = buildSegments({ ...stored, lines, reserve })
  if (segments.length === 0 && lines.length === 0) {
    // NEVER REMOVE A BODY THAT STILL HAS GEOMETRY (ADR-ywzcpbqe): a running
    // block between a reconnect and its first page keeps its element, and the
    // demand loop finds it. Only a block with nothing to reserve loses its
    // empty body.
    if ((stored.rowCount ?? 0) > 0 || (stored.retainedRanges?.length ?? 0) > 0) {
      output.className = 'cmd-output'
      output.dataset.rowsFirst = String(stored.firstRow ?? 0)
      output.dataset.rowsLast = String(stored.lastRow ?? 0)
      output.dataset.rowsEnd = String(stored.endRow ?? 0)
      output.dataset.rowsCount = String(stored.rowCount ?? 0)
      output.dataset.rowsLoaded = '0'
      if (output.parentElement !== block) block.appendChild(output)
      return
    }
    output.remove()
    paintedRows.delete(block)
    return
  }
  let columns = 0
  for (const line of lines) columns = Math.max(columns, rowColumnsOf(line.row))
  const all =
    force || !previous || previous.columns !== columns || !samePaintOptions(previous, opts)
  const changed = all
    ? lines
    : lines.filter((line) => {
        const old = previous?.rows.get(line.from)
        return (
          old === undefined || old.element.parentElement !== output || !sameRow(old.row, line.row)
        )
      })
  const changedWidths = new Map<number, number>()
  for (const line of changed) changedWidths.set(line.from, rowColumnsOf(line.row))
  const toPaint = changed
  const snapshot = toPaint.length > 0 && columns > 0 ? snapshotAtColumns(toPaint, columns) : null
  const nextRows = new Map<number, PaintedStoredRow>()
  for (const line of lines) {
    const old = all ? undefined : previous?.rows.get(line.from)
    if (old !== undefined && old.element.parentElement === output && !changed.includes(line)) {
      nextRows.set(line.from, old)
    }
  }
  if (snapshot !== null) opts.warm?.(fitCandidatesOf(snapshot.rows))
  for (let index = 0; index < toPaint.length; index++) {
    const line = toPaint[index]
    // A BLANK PAGE IS STILL ROWS (ADR-ywzcpbqe). No snapshot means no ink,
    // not no height: a row element keeps the cell height, so the page swaps a
    // spacer for rows of the same size and the reservation survives.
    const element =
      snapshot !== null
        ? paintRow(snapshot.rows[index], opts)
        : columns === 0
          ? blankRowElement()
          : spacerOf(1, line.from)
    if (snapshot !== null) decorateLinks(element)
    nextRows.set(line.from, {
      row: {
        ...line.row,
        marks: line.row.marks?.map((mark) => [...mark]),
        runs: line.row.runs?.map(([style, length]) => [style === 0 ? 0 : [...style], length]),
      },
      columns: changedWidths.get(line.from) ?? 0,
      element,
    })
  }
  const elements = new Map<string, HTMLElement>()
  for (const segment of segments) {
    if (segment.kind === 'row') {
      const painted = nextRows.get(segment.from)
      if (painted === undefined) continue
      const element = painted.element
      const blockId = block.dataset.entryId ?? ''
      if (element.dataset.blockId !== blockId) element.dataset.blockId = blockId
      if (element.dataset.artifactVersion !== stored.artifactVersion) {
        if (stored.artifactVersion === undefined) delete element.dataset.artifactVersion
        else element.dataset.artifactVersion = stored.artifactVersion
      }
      const logicalLine = String(segment.from)
      if (element.dataset.logicalLine !== logicalLine) element.dataset.logicalLine = logicalLine
      elements.set(segment.key, element)
      continue
    }
    const old = previous?.elements.get(segment.key)
    if (segment.kind === 'spacer') {
      const element =
        old !== undefined &&
        old.classList.contains('cmd-output-spacer') &&
        old.dataset.rows === String(segment.rows)
          ? old
          : spacerOf(segment.rows, segment.from)
      elements.set(segment.key, element)
      continue
    }
    const element =
      old !== undefined && old.classList.contains('cmd-output-gap')
        ? old
        : document.createElement('div')
    element.className = 'term-line cmd-output-gap'
    const text = cutSentence(stored, segment.rows)
    if (element.textContent !== text) element.textContent = text
    elements.set(segment.key, element)
  }
  layoutSegments(output, segments, elements)
  output.className = 'cmd-output'
  output.dataset.rowsFirst = String(stored.firstRow ?? 0)
  output.dataset.rowsLast = String(stored.lastRow ?? 0)
  output.dataset.rowsEnd = String(stored.endRow ?? 0)
  output.dataset.rowsCount = String(stored.rowCount ?? lines.length)
  output.dataset.rowsLoaded = String(lines.length)
  if (output.parentElement !== block) block.appendChild(output)
  const metric = opts.metric
  paintedRows.set(block, {
    output,
    rows: nextRows,
    segments,
    elements,
    columns,
    reserve,
    metric:
      metric === null
        ? null
        : {
            cellWidth: metric.cellWidth,
            defaultSpacing: metric.defaultSpacing,
            padY: metric.padY,
            pitch: metric.pitch,
            signature: metric.signature,
            hasBox: metric.boxOf !== undefined,
          },
    palette: { ...opts.palette, palette: [...opts.palette.palette] },
  })
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
