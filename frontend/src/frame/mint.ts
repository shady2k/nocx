// Live frame minting (spec §2.2, bead nocx-3j9b).
//
// A live frame is the cells + attributes + cursor of one capture identity,
// read from the active xterm buffer through a seam. The frame is TEXT-shaped
// data, never a picture.
//
// AD-8: this module owns no derivation. Per-cell attributes come from the
// serializer's cellAttrs (scrollback/serializer.ts) — the single owner of
// attribute extraction — and the theme snapshot is captured by the caller at
// mint time, exactly as a block freeze captures its own.

import type { IBufferLine } from '@xterm/xterm'
import { cellAttrs, emptyAttrs, type TerminalSnapshot } from '../scrollback/serializer'
import type { CapturedFrame, CaptureIdentity } from './types'

/** The seam a live mint reads the buffer through: a line reader plus the
 *  cursor and the theme snapshot. One mint reads ONE settled state — the
 *  caller fences with CaptureIdentityTracker.awaitSettled() first. */
export interface LiveFrameSeam {
  getLine(y: number): IBufferLine | undefined
  cursor: { line: number; col: number }
  snapshot: TerminalSnapshot
}

/** Mint a live frame: cells + attributes + cursor of ONE capture identity,
 *  over the absolute row range [range.start, range.end).
 *
 *  The frame never lies about gaps: a missing line mints as a blank row, and
 *  a cell beyond the line's length mints as a blank cell — the row range is
 *  the frame's promise, and emptiness is recorded, not skipped. */
export function mintLiveFrame(
  identity: CaptureIdentity,
  range: { start: number; end: number },
  seam: LiveFrameSeam,
): CapturedFrame {
  if (!(range.start >= 0 && range.end >= range.start)) {
    throw new RangeError(`invalid frame range [${range.start}, ${range.end})`)
  }
  const rows: CapturedFrame['rows'] = []
  for (let y = range.start; y < range.end; y++) {
    const line = seam.getLine(y)
    const cells = []
    for (let x = 0; x < identity.cols; x++) {
      const cell = line?.getCell(x)
      cells.push({
        char: cell ? cell.getChars() : ' ',
        attrs: line ? cellAttrs(seam.snapshot, line, x) : emptyAttrs(),
      })
    }
    rows.push({ kind: 'cells', cells })
  }
  return {
    rows,
    cursor: seam.cursor,
    provenance: {
      source: 'live',
      identity,
      range,
      scrollbackCapLines: 10000,
    },
  }
}
