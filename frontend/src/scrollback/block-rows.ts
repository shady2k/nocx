import type { SessionFrame } from '../generated/session.frame'
import type { LedgerBlockRowsLine } from '../generated/ledger.blockRows'
import { createCellModel } from '../cell-model'
import { paintRow } from '../painter/paint-row'
import type { RunMetric } from './run-geometry'
import type { TerminalSnapshot } from './serializer'

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

function snapshotForRows(lines: readonly LedgerBlockRowsLine[]) {
  if (lines.length === 0) return null
  const cols = lines[0].row.cells.length
  if (cols === 0 || lines.some((line) => line.row.cells.length !== cols)) return null
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
}

/** Replace a command block's body with rows read from the ledger artifact. */
export function paintStoredRows(
  block: HTMLElement,
  stored: StoredBlockRows,
  opts: StoredBlockPaintOptions,
): void {
  block
    .querySelectorAll(':scope > .cmd-output, :scope > [data-output-incomplete]')
    .forEach((el) => el.remove())
  const snapshot = snapshotForRows(stored.lines)
  if (snapshot !== null) {
    const output = document.createElement('div')
    output.className = 'cmd-output'
    for (const row of snapshot.rows) {
      output.appendChild(paintRow(row, opts))
    }
    block.appendChild(output)
  }
  const missing = stored.droppedRows + stored.lostRows
  if (missing > 0) {
    const notice = document.createElement('div')
    notice.className = 'cmd-output cmd-output-incomplete'
    notice.dataset.outputIncomplete = 'true'
    notice.textContent = `Output incomplete: ${missing} rows missing`
    block.appendChild(notice)
  }
}
