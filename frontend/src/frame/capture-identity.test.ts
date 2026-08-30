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

/** Drain the microtask queue by hopping to the next task. Not a delay: the
 *  assertions that use it are about what the fence did, and a promise chain
 *  several hops long must not read as "not settled yet". */
const flushMicrotasks = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0))

describe('CaptureIdentityTracker — the capture fence', () => {
  it('is not starved by a writer that always has the next repaint queued', async () => {
    // The nocx-2ryxf.3 shape, as an assertion: `top` repaints forever, so the
    // unsettled count settles one write while the next replaces it and NEVER
    // reaches zero. The barrier is behind the repaint that was queued when
    // the capture was asked for, so it opens anyway.
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)
    source.write('first repaint')
    let settled = false
    const capture = tracker.awaitSettled().then(() => {
      settled = true
    })

    // Every pass the writer queues its next repaint before the pass runs.
    for (let repaint = 0; repaint < 5 && !settled; repaint++) {
      source.write(`repaint ${repaint}`)
      source.parseOnePass()
      await flushMicrotasks()
    }

    await capture
    const evidence = {
      settled,
      pendingAtCapture: source.unsettledWriteCount(),
    }
    // The witness: the capture resolved WHILE the queue still had work. A
    // fence waiting for global emptiness would still be parked here.
    expect(evidence.settled).toBe(true)
    expect(
      evidence.pendingAtCapture,
      `the fence must open with writes still queued: ${JSON.stringify(evidence)}`,
    ).toBeGreaterThan(0)
  })

  it('waits for EVERY write queued before the request, not just the pass that follows it', async () => {
    // xterm breaks its parse loop on a 12 ms budget BETWEEN chunks, so a
    // completed parse pass can leave bytes that were already queued when the
    // capture was asked for unparsed. Answering at that boundary would hand
    // back a screen from several passes ago on a bulk write; the barrier
    // waits for the whole backlog, and for none of the future.
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)
    source.write('backlog chunk 1')
    source.write('backlog chunk 2')

    let settled = false
    const capture = tracker.awaitSettled().then(() => {
      settled = true
    })

    // A pass that gets through only the first chunk: a boundary fired and the
    // generation advanced, but the backlog is not in the buffer yet. Drain
    // the microtask queue (a task hop, not a delay) so "not settled" is a
    // fact about the fence and not about how many ticks the assertion waited.
    source.parseOnePass()
    await flushMicrotasks()
    expect(settled).toBe(false)
    expect(source.unsettledWriteCount()).toBe(1)

    // The pass that finishes the backlog reaches the barrier behind it.
    source.parseOnePass(2)
    await capture
    expect(settled).toBe(true)
    expect(source.unsettledWriteCount()).toBe(0)
  })

  it('a write that arrives AFTER the request cannot postpone the fence', async () => {
    const source = new FakeSource()
    const tracker = new CaptureIdentityTracker(source)
    source.write('the backlog')
    const capture = tracker.awaitSettled()

    // Queued behind the barrier: it is the next screen, not this one.
    source.write('the next repaint')
    source.parseOnePass(2) // the backlog chunk, then the barrier

    await capture
    expect(source.unsettledWriteCount()).toBe(1)
  })

  it('rejects a barrier that never settles — a wedged parse queue is bounded locally, not by the broker', async () => {
    vi.useFakeTimers()
    try {
      const source = new FakeSource()
      const tracker = new CaptureIdentityTracker(source, 5000)
      source.write('a chunk whose pass is never scheduled')
      const failure = expect(tracker.awaitSettled()).rejects.toThrow(CaptureUnsettledError)

      await vi.advanceTimersByTimeAsync(5000)

      await failure
      expect(source.unsettledWriteCount()).toBe(1)
      // The parked capture was removed: a later pass is ordinary cleanup,
      // not a late resolution of a request that already failed.
      source.flush()
    } finally {
      vi.useRealTimers()
    }
  })

  it('takes a frame immediately when nothing is queued, and does NOT advance the generation of a motionless screen', async () => {
    const source = new FakeSource()
    source.seed(['x'])
    const tracker = new CaptureIdentityTracker(source)
    const before = tracker.identity()
    let resolved = false
    await tracker.awaitSettled().then(() => {
      resolved = true
    })
    expect(resolved).toBe(true)
    // A barrier costs a parse pass, and a parse pass advances the generation.
    // Issuing one here would report a screen nobody wrote to as `moved` —
    // ADR-0029 tolerates a false positive, it never manufactures one.
    expect(tracker.compareIdentity(before)).toEqual({ status: 'same' })
    await tracker.awaitSettled()
    expect(tracker.compareIdentity(before)).toEqual({ status: 'same' })
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
