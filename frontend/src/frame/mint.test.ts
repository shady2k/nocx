// @vitest-environment jsdom
// Frame minting acceptance tests (bead nocx-3j9b, spec §2.2).
//
// A frame is cells + attributes + cursor of one capture identity, text never
// a picture. TWO capture sources, recorded, never silently substituted:
// live (cells of the active xterm buffer) and frozen (the serializer's text
// — no xterm cells are left after a freeze). The frozen path defines a SEAM
// and satisfies it from what already exists (blockOutputText + the
// serializer version); it never re-derives VT output (AD-8).

import { describe, expect, it } from 'vitest'
import { DEFAULT_SNAPSHOT, SERIALIZER_VERSION } from '../scrollback/serializer'
import { lineWith, XTERM_CM_P16 } from '../scrollback/test-helpers'
import { CaptureIdentityTracker } from './capture-identity'
import { mintFrozenFrame, frozenFrameSourceFromBlock } from './frozen'
import { mintLiveFrame, type LiveFrameSeam } from './mint'
import type { CapturedFrame, FrameRow } from './types'
import { FakeSource, seedSource } from './test-source'

function seamFor(source: FakeSource): LiveFrameSeam {
  return {
    getLine: (y) => source.getBufferLine(y),
    cursor: source.cursor,
    snapshot: DEFAULT_SNAPSHOT,
  }
}

/** Frame rows as plain text — the user-facing content of a frame. */
function rowText(frame: CapturedFrame): string[] {
  return frame.rows.map((r: FrameRow) => {
    if (r.kind === 'text') return r.text
    return r.cells
      .map((c) => c.char)
      .join('')
      .trimEnd()
  })
}

describe('live mint', () => {
  it('mints cells + attributes + cursor of one capture identity, with live provenance', () => {
    const source = seedSource(['AB', 'CD'])
    const tracker = new CaptureIdentityTracker(source)
    const identity = tracker.identity()

    const frame = mintLiveFrame(identity, { start: 0, end: 2 }, seamFor(source))

    expect(frame.rows).toHaveLength(2)
    expect(rowText(frame)).toEqual(['AB', 'CD'])
    expect(frame.cursor).toEqual(source.cursor)
    // Cells carry attributes resolved against the theme snapshot — the
    // serializer's single attribute extraction, not a second derivation.
    expect(frame.rows[0]).toMatchObject({ kind: 'cells' })
    expect(frame.rows[1]).toMatchObject({ kind: 'cells' })
    expect(frame.provenance).toMatchObject({
      source: 'live',
      identity,
      range: { start: 0, end: 2 },
      scrollbackCapLines: 10000,
    })
  })

  it('carries per-cell attributes — bold red text arrives bold with its color', () => {
    const source = seedSource(['X'])
    // Bold ANSI red 'R' followed by a default 'x' — the attribute contract
    // flows through the serializer's cellAttrs, the single owner (AD-8).
    source.setLine(
      0,
      lineWith({ chars: 'R', bold: true, fg: 1, fgMode: XTERM_CM_P16 }, { chars: 'x' }),
    )
    const tracker = new CaptureIdentityTracker(source)
    const frame = mintLiveFrame(tracker.identity(), { start: 0, end: 1 }, seamFor(source))

    const cells = frame.rows[0]
    expect(cells.kind).toBe('cells')
    if (cells.kind !== 'cells') return
    expect(cells.cells[0].char).toBe('R')
    expect(cells.cells[0].attrs.bold).toBe(true)
    expect(cells.cells[0].attrs.fg).toBe('#f7768e') // ANSI red under the default snapshot
    expect(cells.cells[1].attrs.bold).toBe(false)
  })

  it('records a missing line as a blank row rather than dropping it — the frame never lies about gaps', () => {
    const source = seedSource(['row0'])
    const tracker = new CaptureIdentityTracker(source)
    const frame = mintLiveFrame(tracker.identity(), { start: 0, end: 2 }, seamFor(source))
    expect(rowText(frame)).toEqual(['row0', ''])
  })
})

describe('frozen mint', () => {
  it('mints text rows with source=frozen and the SERIALIZER VERSION — never substituted for live', () => {
    const frame = mintFrozenFrame({
      text: () => 'row one\nrow two',
      serializerVersion: SERIALIZER_VERSION,
    })

    expect(rowText(frame)).toEqual(['row one', 'row two'])
    expect(frame.cursor).toBeNull()
    expect(frame.provenance).toEqual({
      source: 'frozen',
      serializerVersion: SERIALIZER_VERSION,
      transforms: 'wrapped lines joined; leading/trailing blanks dropped',
      closed: true,
    })
  })

  it('satisfies the frozen seam from what already exists — the block DOM and blockOutputText', () => {
    // A frozen block's output element holds the serializer's HTML: one
    // .term-line span per logical line, line breaks restored by the existing
    // blockOutputText owner (nocx-6w4z). The seam takes the BLOCK — that is
    // what the ask surface holds — and resolves the output element itself
    // (nocx-ex636).
    const el = document.createElement('div')
    el.className = 'cmd-block'
    el.innerHTML =
      '<div class="cmd-output"><span class="term-line">first</span><span class="term-line">second</span></div>'

    const frame = mintFrozenFrame(frozenFrameSourceFromBlock(el))
    expect(frame.provenance.source).toBe('frozen')
    if (frame.provenance.source === 'frozen') {
      expect(frame.provenance.serializerVersion).toBe(SERIALIZER_VERSION)
    }
  })

  it('a frozen block with no output element produces an empty frame, still frozen', () => {
    const frame = mintFrozenFrame(frozenFrameSourceFromBlock(null))
    expect(rowText(frame)).toEqual([])
    expect(frame.provenance.source).toBe('frozen')
  })
})

describe('the capture fence', () => {
  it('mints after the parse settles when ONE write spans parse passes — no frame mixes rows from before and after the write', async () => {
    const source = seedSource(['AAAA'])
    // Park the cursor at the start of the buffer so each chunk lands on its
    // own row, like fresh output in a real terminal.
    source.cursor = { line: 0, col: 0 }
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    // What an UNFENCED mint at this instant would read: the pre-write buffer.
    const unfenced = mintLiveFrame(before, { start: 0, end: 1 }, seamFor(source))
    expect(rowText(unfenced)).toEqual(['AAAA'])

    // ONE write, split by xterm's WriteBuffer across parse passes (the
    // per-write callback fires only on the pass that empties it — the old
    // test issued two writes and settled each by hand, testing the model
    // the fake chose, not xterm's interleaving). Start the fence, then run
    // the passes one at a time.
    source.write('BBBB\nCCCC\n')
    const settled = tracker.awaitSettled()

    source.parseOnePass(5) // 'BBBB\n' — the write is still pending
    source.parseOnePass(5) // 'CCCC\n' — the write settles
    await settled

    const after = tracker.identity()
    const frame = mintLiveFrame(after, { start: 0, end: 2 }, seamFor(source))

    // The fenced frame holds the complete post-write state — every row from
    // after the write, none from before it.
    expect(rowText(frame)).toEqual(['BBBB', 'CCCC'])
    expect(frame.provenance.source).toBe('live')
    if (frame.provenance.source === 'live') {
      expect(frame.provenance.identity.generation).toBeGreaterThan(before.generation)
    }
  })
})
