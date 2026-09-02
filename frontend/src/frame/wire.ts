// Live frame → wire conversion (nocx-ljfwz): the ONE place a live
// CapturedFrame becomes the wire shape the backend validates
// (agent.readScreenResolved's frame body — the same row/cell/attribute/
// identity vocabulary as the agent.captureFrame push, per design §2.4's
// one-frame-shape rule).
//
// AD-8: this module owns the conversion; the mint (frame/mint.ts) owns
// producing the frame, the backend's captureFrame validation owns checking
// it. The frozen path's conversion lives in agent-ask.ts beside the frozen
// mint — the ask transaction must not change in this slice.

import type { CapturedFrame } from './types'

export interface ReadScreenFrameWire {
  rows: { kind: 'text'; text: string }[]
  cursor: { line: number; col: number }
  identity: {
    buffer: { kind: 'normal' | 'alternate'; altSession?: number | null }
    cols: number
    rows: number
    generation: number
  }
  range: { start: number; end: number }
}

/** Convert a minted live frame to the wire body of a readScreen resolution.
 *  The frame's provenance (source live, identity, range) is the source of
 *  the identity and range fields — the frame records which buffer instance,
 *  geometry and generation it belongs to, and the wire restates exactly
 *  that. */
export function liveFrameToWire(frame: CapturedFrame): ReadScreenFrameWire {
  if (frame.provenance.source !== 'live') {
    throw new Error('liveFrameToWire: expected a live frame, got ' + frame.provenance.source)
  }
  if (frame.cursor === null) {
    throw new Error('liveFrameToWire: a live frame must carry a cursor')
  }
  const identity = frame.provenance.identity
  const buffer: { kind: 'normal' | 'alternate'; altSession?: number | null } = {
    kind: identity.buffer.kind,
  }
  if (identity.buffer.kind === 'alternate') {
    buffer.altSession = identity.buffer.altSession
  }
  return {
    rows: frame.rows.map((row) => {
      if (row.kind !== 'cells') {
        throw new Error('liveFrameToWire: a live frame row must be cells')
      }
      // THE ATTRIBUTES DO NOT TRAVEL, and nothing on the far side misses
      // them: both backend consumers already join the row's characters and
      // throw the rest away (internal/assistant/blocks.go's session.read
      // text, internal/content's frameText). Sending them cost a measured
      // 878 KB for one `top` screen whose text is ~9 KB — the renderer keeps
      // the cells locally, which is where the drawing happens (nocx-u3vxd).
      //
      // Blanks are kept rather than trimmed: the row's width IS the screen's
      // width, and session.read's Text is byte-for-byte what it was before.
      return { kind: 'text' as const, text: row.cells.map((c) => c.char).join('') }
    }),
    cursor: frame.cursor,
    identity: {
      buffer,
      cols: identity.cols,
      rows: identity.rows,
      generation: identity.generation,
    },
    range: { start: frame.provenance.range.start, end: frame.provenance.range.end },
  }
}
