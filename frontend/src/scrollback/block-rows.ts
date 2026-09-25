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
  readonly droppedRows: number
  readonly lostRows: number
  readonly truncated: 'cap' | 'gap' | 'suppressed' | null
}

interface RowsArtifactMetadata {
  readonly truncated: StoredBlockRows['truncated']
  readonly payload: unknown
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
    droppedRows: nonNegativeInteger(payload.droppedRows),
    lostRows: nonNegativeInteger(payload.lostRows),
    truncated: metadata.truncated,
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
function snapshotForRows(lines: readonly LedgerBlockRowsLine[]) {
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

/** Replace a command block's body with rows read from the ledger artifact. */
export function paintStoredRows(
  block: HTMLElement,
  stored: StoredBlockRows,
  opts: StoredBlockPaintOptions,
): void {
  block
    .querySelectorAll(
      ':scope > .cmd-output, :scope > [data-output-incomplete], :scope > [data-output-unreadable]',
    )
    .forEach((el) => el.remove())
  const snapshot = snapshotForRows(stored.lines)
  if (snapshot !== null) {
    opts.warm?.(fitCandidatesOf(snapshot.rows))
    const output = document.createElement('div')
    output.className = 'cmd-output'
    for (const row of snapshot.rows) {
      output.appendChild(paintRow(row, opts))
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
  const missing = stored.droppedRows + stored.lostRows
  if (stored.truncated !== null || missing > 0) {
    const notice = document.createElement('div')
    notice.className = 'cmd-output cmd-output-incomplete'
    notice.dataset.outputIncomplete = 'true'
    // The count is only known for a cap/gap the chunks or the emulator
    // actually counted (deriveBlockRowsDropped, LostRows). `suppressed`
    // means capture was refused by policy and never ran, so there is
    // nothing to count; a cap/gap with no counted rows yet (a close still
    // in flight, or a reason the count does not cover) says the same thing
    // without inventing a number.
    notice.textContent =
      missing > 0
        ? `Output incomplete: ${missing} rows missing`
        : stored.truncated === 'suppressed'
          ? 'Output incomplete: capture was refused'
          : 'Output incomplete'
    block.appendChild(notice)
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
