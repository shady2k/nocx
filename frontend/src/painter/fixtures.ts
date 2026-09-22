// Test fixtures for the painter: wire frames built the way the runtime
// declares them, so the painter is always driven through the real cell
// model (createCellModel().apply) rather than past it. Not imported by any
// production module.

import type { Color, SessionFrame, Style } from '../generated/session.frame'
import { createCellModel, type ScreenSnapshot } from '../cell-model'

export const DEFAULT_COLOR: Color = { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } }

export function styleOf(over: Partial<Style> = {}): Style {
  return {
    foreground: DEFAULT_COLOR,
    background: DEFAULT_COLOR,
    underlineColor: DEFAULT_COLOR,
    attributes: 0,
    underline: 0,
    ...over,
  }
}

export type CellSpec = [grapheme: string, width: 1 | 2 | 3 | 4, hasText: boolean]

export function frameOf(
  revision: number,
  rowsSpec: CellSpec[][],
  cursor: SessionFrame['cursor'] = { x: 0, y: 0, visible: false },
  geometry: Partial<SessionFrame['geometry']> = {},
): SessionFrame {
  const style = styleOf()
  return {
    revision,
    geometry: {
      cols: Math.max(...rowsSpec.map((cells) => cells.length)),
      rows: rowsSpec.length,
      cellWidthPx: 8,
      cellHeightPx: 20,
      revision,
      ...geometry,
    },
    cursor,
    rows: rowsSpec.map((cells) => ({
      cells,
      runs: [[style, cells.length]] as SessionFrame['rows'][number]['runs'],
      wrap: false,
      continuation: false,
    })),
  }
}

/** Apply a frame through the real model and hand back the installed
 *  snapshot; a refusal is a test bug, not a branch to explore. */
export function snapshotOf(frame: SessionFrame): ScreenSnapshot {
  const result = createCellModel().apply(frame)
  if (!result.ok) throw new Error(`fixture frame refused: ${JSON.stringify(result.refusal)}`)
  return result.snapshot
}
