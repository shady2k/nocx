import type { SessionFrame } from '../generated/session.frame'
import type { Cell, LedgerBlockRowsLine, Row, Run, Style } from '../generated/ledger.blockRows'
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

function defaultStyle(): Style {
  return {
    foreground: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
    background: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
    underlineColor: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
    attributes: 0,
    underline: 0,
  }
}

function isDefaultStyle(style: Style): boolean {
  return (
    style.attributes === 0 &&
    style.underline === 0 &&
    style.foreground.kind === 0 &&
    style.foreground.palette === 0 &&
    style.foreground.rgb.r === 0 &&
    style.foreground.rgb.g === 0 &&
    style.foreground.rgb.b === 0 &&
    style.background.kind === 0 &&
    style.background.palette === 0 &&
    style.background.rgb.r === 0 &&
    style.background.rgb.g === 0 &&
    style.background.rgb.b === 0 &&
    style.underlineColor.kind === 0 &&
    style.underlineColor.palette === 0 &&
    style.underlineColor.rgb.r === 0 &&
    style.underlineColor.rgb.g === 0 &&
    style.underlineColor.rgb.b === 0
  )
}

function nonEmptyRuns(runs: readonly Run[]): [Run, ...Run[]] {
  if (runs.length === 0) {
    throw new Error('stored row has no style runs')
  }
  return runs.map(([style, length]) => [style, length] as Run) as [Run, ...Run[]]
}

function padStoredRow(row: Row, width: number): Row {
  const missing = width - row.cells.length
  if (missing <= 0) return row

  const cells: Cell[] = [...row.cells]
  for (let i = 0; i < missing; i++) cells.push(['', 1, false])

  const runs = nonEmptyRuns(row.runs)
  const last = runs[runs.length - 1]
  if (last !== undefined && isDefaultStyle(last[0])) {
    last[1] += missing
  } else {
    runs.push([defaultStyle(), missing])
  }
  return { ...row, cells, runs }
}

function normalizeStoredRows(lines: readonly LedgerBlockRowsLine[]): LedgerBlockRowsLine[] {
  const width = Math.max(...lines.map((line) => line.row.cells.length))
  if (width === 0) return []
  return lines.map((line) => ({ ...line, row: padStoredRow(line.row, width) }))
}

function snapshotForRows(lines: readonly LedgerBlockRowsLine[]) {
  if (lines.length === 0) return null
  const normalized = normalizeStoredRows(lines)
  if (normalized.length === 0) return null
  const cols = normalized[0].row.cells.length
  const frame: SessionFrame = {
    revision: 1,
    geometry: {
      cols,
      rows: normalized.length,
      cellWidthPx: 1,
      cellHeightPx: 1,
      revision: 1,
    },
    cursor: { x: 0, y: 0, visible: false },
    rows: normalized.map((line) => line.row),
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
