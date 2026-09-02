// The live frame's wire conversion (nocx-u3vxd): a resolution is bounded by
// the screen's TEXT, not by its cell grid.

import { describe, expect, it } from 'vitest'
import { liveFrameToWire } from './wire'
import { emptyAttrs } from '../scrollback/serializer'
import type { CapturedFrame } from './types'

/** A live frame of `rows` rows × `cols` cells, each row filled with `fill`
 *  and padded to the width with blanks — the shape a full-screen mint has. */
function liveFrame(cols: number, rows: number, fill: string): CapturedFrame {
  return {
    rows: Array.from({ length: rows }, () => ({
      kind: 'cells' as const,
      cells: Array.from({ length: cols }, (_, x) => ({
        char: x < fill.length ? fill[x] : ' ',
        attrs: emptyAttrs(),
      })),
    })),
    cursor: { line: 0, col: 0 },
    provenance: {
      source: 'live',
      identity: { buffer: { kind: 'normal' }, cols, rows, generation: 1 },
      range: { start: 0, end: rows },
      scrollbackCapLines: 10000,
    },
  }
}

describe('liveFrameToWire', () => {
  it('sends each row as its text, cells joined in order and blanks kept', () => {
    const wire = liveFrameToWire(liveFrame(6, 2, 'hi'))
    expect(wire.rows).toEqual([
      { kind: 'text', text: 'hi    ' },
      { kind: 'text', text: 'hi    ' },
    ])
  })

  it('keeps the identity, cursor and range the frame recorded', () => {
    const wire = liveFrameToWire(liveFrame(6, 2, 'hi'))
    expect(wire.cursor).toEqual({ line: 0, col: 0 })
    expect(wire.identity).toEqual({
      buffer: { kind: 'normal' },
      cols: 6,
      rows: 2,
      generation: 1,
    })
    expect(wire.range).toEqual({ start: 0, end: 2 })
  })

  it('costs the order of the screen text, not the order of its cell grid', () => {
    // The owner measured 878,536 bytes for one session.read over a running
    // top; the same screen as text is ~9 KB (nocx-u3vxd).
    const bytes = JSON.stringify(liveFrameToWire(liveFrame(200, 44, 'top'))).length
    expect(bytes).toBeLessThan(20_000)
  })
})
