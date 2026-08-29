// @vitest-environment jsdom
// Capture-identity acceptance tests (bead nocx-3j9b, spec §2.3, ADR-0029).
//
// The generation is the "the screen was written to" signal, never a verdict:
// a moved generation is a trigger for re-evaluation; a buffer switch or a
// resize is INCOMPARABILITY — a distinct value in the API, never a flag on
// staleness. Nothing here wires generation inequality to a refusal (that
// belongs to DRIVE), and no surface may present drift as "stale".

import { describe, expect, it, vi } from 'vitest'
import {
  CaptureAbortedError,
  CaptureIdentityTracker,
  CaptureUnsettledError,
  ReadScreenRangeError,
} from './capture-identity'
import { FakeSource } from './test-source'

describe('ReadScreenRangeError', () => {
  it('preserves the range facts without naming the retired tool', () => {
    const error = new ReadScreenRangeError('requested rows [10,12) exceed the buffer with 5 rows')

    expect(error.message).toBe('requested rows [10,12) exceed the buffer with 5 rows')
    expect(error.name).toBe('ReadScreenRangeError')
  })
})

describe('CaptureIdentityTracker — the generation', () => {
  it('advances when a write parses — even one that repaints IDENTICAL cells, and that reads as "moved" (the deliberate false positive)', () => {
    const source = new FakeSource()
    source.seed(['hello world'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    // A write whose parsed content is byte-identical to what the buffer
    // already holds. The generation must STILL advance: we prefer a false
    // "it moved" (costs a re-ask) to a false "unchanged" (describes a
    // screen that is gone). Asserted so this cannot be quietly "optimised"
    // into a false negative later.
    source.write('hello world')
    source.flush()

    const after = tracker.identity()
    expect(after.generation).toBe(before.generation + 1)
    expect(after.buffer).toEqual(before.buffer)
    expect(after.cols).toBe(before.cols)
    expect(after.rows).toBe(before.rows)
    expect(tracker.compareIdentity(before)).toEqual({ status: 'moved' })
  })

  it('does NOT advance on a repaint — the tracker has no render subscription at all', () => {
    // The seam (CaptureEventSource) has no onRender member: ADR-0005 forces
    // periodic repaints on Linux/WebKitGTK, so a paint-driven generation
    // would stale a motionless screen continuously on one platform only.
    // The structural absence is the assertion here; the renderer-level test
    // proves refreshAtlas/applyTheme leave the identity untouched.
    const source = new FakeSource()
    source.seed(['still'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    // An offscreen mutation DOES advance: a write to rows below the viewport
    // (scrollback growth) is still a write.
    source.write('offscreen')
    source.flush()

    expect(tracker.identity().generation).toBe(before.generation + 1)
  })

  it('advances on the explicit state-changing operations: clear and reset', () => {
    const source = new FakeSource()
    source.seed(['rows'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    source.clear()
    expect(tracker.identity().generation).toBe(before.generation + 1)

    source.reset()
    expect(tracker.identity().generation).toBe(before.generation + 2)
  })
})

describe('CaptureIdentityTracker — comparability, not staleness', () => {
  it('is "same" only for an identical identity', () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    const identity = tracker.identity()
    expect(tracker.compareIdentity(identity)).toEqual({ status: 'same' })
    expect(tracker.compareIdentity({ ...identity, generation: identity.generation + 1 })).toEqual({
      status: 'moved',
    })
  })

  it('a buffer switch is NOT COMPARABLE — a distinct value, not a stale flag', () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    const normal = tracker.identity()

    source.enterAlt()
    const alt = tracker.identity()
    expect(alt.buffer).toEqual({ kind: 'alternate', altSession: 1 })
    expect(tracker.compareIdentity(normal)).toEqual({ status: 'notComparable' })

    // Leaving the alternate screen TERMINATES that identity: the alt frame
    // can never be compared again — its contents are discarded on exit.
    source.leaveAlt()
    const back = tracker.identity()
    expect(back.buffer).toEqual({ kind: 'normal' })
    expect(tracker.compareIdentity(alt)).toEqual({ status: 'notComparable' })
  })

  it('entering the alternate screen mints a NEW identity each time — two alt sessions are not comparable', () => {
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)

    source.enterAlt()
    const first = tracker.identity()
    source.leaveAlt()
    source.enterAlt()
    const second = tracker.identity()

    expect(first.buffer).toEqual({ kind: 'alternate', altSession: 1 })
    expect(second.buffer).toEqual({ kind: 'alternate', altSession: 2 })
    // Same kind, same geometry, but a DIFFERENT alt-screen session — the
    // alternate buffer's contents were discarded in between, so there is
    // nothing to compare: not comparable, never "stale".
    expect(tracker.compareIdentity(first)).toEqual({ status: 'notComparable' })
  })

  it('a resize is NOT COMPARABLE — the grid reflowed and absolute line indices shifted', () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    source.resize(100, 30)
    const after = tracker.identity()

    expect(after.cols).toBe(100)
    expect(after.rows).toBe(30)
    expect(tracker.compareIdentity(before)).toEqual({ status: 'notComparable' })
    // A resize also advances the generation (spec §2.3 lists it among the
    // explicit operations) — but the verdict is incomparability, not moved.
    expect(after.generation).toBe(before.generation + 1)
  })

  it('a clear or reset leaves the SAME buffer instance comparable but moved', () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    source.clear()
    expect(tracker.compareIdentity(before)).toEqual({ status: 'moved' })
  })
})

describe('CaptureIdentityTracker — the capture fence', () => {
  it('waits for the next completed parse pass even when the same write spans another pass', async () => {
    const source = new FakeSource()
    source.seed(['AAAA'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()

    source.write('BBBBCCCC')
    const capture = tracker.awaitSettled()

    // Pass 1 is a complete xterm parse pass and therefore a coherent buffer
    // boundary, even though the write's remaining bytes stay queued.
    source.parseOnePass(4)
    await capture
    expect(source.hasUnsettledWrite()).toBe(true)
    expect(tracker.identity().generation).toBe(before.generation + 1)

    // Drain the remainder for test isolation. It advances identity again but
    // does not retroactively change the frame boundary the capture selected.
    source.parseOnePass(4)
    expect(source.hasUnsettledWrite()).toBe(false)
    expect(tracker.identity().generation).toBe(before.generation + 2)
  })

  it('captures at a completed parse pass even when a continuous writer keeps one later write pending', async () => {
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)
    source.write('first repaint')
    let settled = false
    const capture = tracker.awaitSettled().then(() => {
      settled = true
    })

    // The TUI queues its next repaint before xterm finishes this one. Every
    // pass fires, the generation advances, and the pending count is exact —
    // but global emptiness never occurs: 1 settles while 1 replaces it.
    source.write('next repaint')
    const before = tracker.identity().generation
    source.parseOnePass()
    await capture
    const evidence = {
      pending: source.unsettledWriteCount(),
      generationDelta: tracker.identity().generation - before,
    }

    try {
      expect(evidence).toEqual({ pending: 1, generationDelta: 1 })
      expect(settled, `instrumented fence evidence: ${JSON.stringify(evidence)}`).toBe(true)
    } finally {
      // Drain the replacement write for test isolation; the capture already
      // resolved at the preceding coherent pass.
      source.flush()
      await capture
    }
  })

  it('rejects a claimed pending write that produces no parse boundary', async () => {
    vi.useFakeTimers()
    try {
      const source = new FakeSource()
      const tracker = new CaptureIdentityTracker(source, 1000)
      source.write('a callback that never arrives')
      const failure = expect(tracker.awaitSettled()).rejects.toThrow(CaptureUnsettledError)

      await vi.advanceTimersByTimeAsync(1000)

      await failure
      expect(source.unsettledWriteCount()).toBe(1)
      // The timed-out waiter was removed: a later parse is ordinary cleanup,
      // not a late resolution of the request that already failed.
      source.parseOnePass()
    } finally {
      vi.useRealTimers()
    }
  })

  it('takes a frame immediately when nothing is queued — no wait for an event that never comes', async () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    let resolved = false
    await tracker.awaitSettled().then(() => {
      resolved = true
    })
    expect(resolved).toBe(true)
  })

  it('disposing the source mid-capture settles (rejects) the pending awaitSettled — the fence has a closing event (AGENTS.md rule 3)', async () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)

    source.write('BBBB')
    const pending = tracker.awaitSettled()
    source.dispose()
    await expect(pending).rejects.toThrow(CaptureAbortedError)
  })

  it('rejects a disposed source even when its pending count is already zero', async () => {
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)
    source.dispose()

    await expect(tracker.awaitSettled()).rejects.toThrow(CaptureAbortedError)
  })

  it('an awaitSettled issued AFTER disposal rejects immediately — a stuck counter cannot hang a later capture', async () => {
    const source = new FakeSource()
    source.write('x') // will never parse — its source is gone
    source.dispose()
    const tracker = new CaptureIdentityTracker(source)
    await expect(tracker.awaitSettled()).rejects.toThrow(CaptureAbortedError)
  })
})
