// The throttle that makes a running row's numbers readable — and the
// defect a throttle introduces, which is a row frozen at 98% because the
// last update was held and never released.
//
// The clock and the interval are injected and the timers are fake: a test
// that waited a real quarter-second would be a test that depends on timing,
// which is broken on a fast machine too.
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createThrottledOperations, OPERATIONS_RENDER_INTERVAL_MS } from './render-throttle'
import type { Operation } from './operations'

const INTERVAL = OPERATIONS_RENDER_INTERVAL_MS

function op(over: Partial<Operation> & { id: string }): Operation {
  return {
    kind: 'upload',
    title: over.id,
    destination: '/srv',
    machine: 'deploy@srv-01',
    phase: 'running',
    done: null,
    total: 100,
    speedBytesPerSecond: null,
    error: null,
    startedAt: null,
    endedAt: null,
    cancel: null,
    ...over,
  }
}

/** A clock the test moves by hand, wired to the fake timers so that
 *  advancing one advances the other — a released update must see the same
 *  time the release was scheduled against. */
function clock(): { now: () => number; advance: (ms: number) => void } {
  let t = 1000
  return {
    now: () => t,
    advance: (ms: number) => {
      t += ms
      vi.advanceTimersByTime(ms)
    },
  }
}

let dispose: (() => void) | null = null

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  dispose?.()
  dispose = null
  vi.useRealTimers()
})

/** Mount the throttle over a mutable list and hand back the levers: what is
 *  on screen, how to move the data, and how to move the clock. */
function harness(initial: Operation[]): {
  shown: () => Operation[]
  set: (list: Operation[]) => void
  advance: (ms: number) => void
} {
  const c = clock()
  let list = initial
  let bump: ((f: (n: number) => number) => void) | null = null
  const shown = createRoot((d) => {
    dispose = d
    // A signal the test owns, so the source is unmistakably external to the
    // throttle: the throttle must subscribe to it, and the test decides when
    // it moves.
    const [tick, setTick] = createSignal(0)
    bump = setTick
    return createThrottledOperations(
      (): Operation[] => {
        tick()
        return list
      },
      { now: c.now },
    )
  })
  return {
    shown,
    set: (next: Operation[]) => {
      list = next
      bump?.((n) => n + 1)
    },
    advance: c.advance,
  }
}

describe('the numbers are held so a person can read them', () => {
  it('publishes the first list at once — a panel does not open on a held frame', () => {
    const h = harness([op({ id: 'a', done: 10 })])
    expect(h.shown().map((o) => o.done)).toEqual([10])
  })

  it('holds a byte count that moves inside the window', () => {
    const h = harness([op({ id: 'a', done: 10 })])
    expect(h.shown()[0].done).toBe(10)
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 20 })])
    // Published: the window had elapsed.
    expect(h.shown()[0].done).toBe(20)
    h.advance(10)
    h.set([op({ id: 'a', done: 30 })])
    h.advance(10)
    h.set([op({ id: 'a', done: 40 })])
    // Both held: 20 ms into a 250 ms window.
    expect(h.shown()[0].done).toBe(20)
  })

  it('releases the NEWEST held value when the window closes, not the oldest', () => {
    const h = harness([op({ id: 'a', done: 10 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 20 })])
    h.set([op({ id: 'a', done: 30 })])
    h.set([op({ id: 'a', done: 40 })])
    h.advance(INTERVAL)
    expect(h.shown()[0].done).toBe(40)
  })

  it('does not repaint again for frames that changed nothing', () => {
    // One release per window, not one per frame.
    const h = harness([op({ id: 'a', done: 10 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 20 })])
    const first = h.shown()
    h.advance(INTERVAL)
    expect(h.shown()).toBe(first)
  })
})

// THIS IS THE DEFECT A THROTTLE INTRODUCES. A held update that is never
// released is a row that says 98% for the rest of the session.
describe('the last update always lands', () => {
  it('publishes a terminal phase immediately, mid-window', () => {
    const h = harness([op({ id: 'a', done: 90 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 98 })])
    h.advance(10)
    h.set([op({ id: 'a', done: 99 })])
    // Held — 10 ms into the window.
    expect(h.shown()[0].done).toBe(98)
    // And now it finishes. No waiting: the phase changed.
    h.set([op({ id: 'a', done: 100, phase: 'written', endedAt: 2000 })])
    expect(h.shown()[0].phase).toBe('written')
    expect(h.shown()[0].done).toBe(100)
  })

  it('cancels the pending release, so the finished value is not overwritten by a stale one', () => {
    // The trap inside the trap: a timer armed before the transfer ended
    // fires after it, and if it published whatever it found it would be
    // harmless — but if it published a captured value it would resurrect
    // the old frame. Advancing past the window must change nothing.
    const h = harness([op({ id: 'a', done: 90 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 98 })])
    h.advance(10)
    h.set([op({ id: 'a', done: 99 })])
    h.set([op({ id: 'a', done: 100, phase: 'written', endedAt: 2000 })])
    const settled = h.shown()
    h.advance(INTERVAL * 4)
    expect(h.shown()).toBe(settled)
    expect(h.shown()[0].phase).toBe('written')
  })

  it('publishes every other phase change immediately too', () => {
    // `unsettled` is not terminal and is still a change of state the person
    // must see: the row loses its numbers and grows a badge.
    const h = harness([op({ id: 'a', done: 50 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 51 })])
    h.advance(10)
    h.set([op({ id: 'a', done: 51, phase: 'unsettled' })])
    expect(h.shown()[0].phase).toBe('unsettled')
  })
})

describe('a row appearing or leaving is never held', () => {
  it('shows a new operation at once', () => {
    const h = harness([op({ id: 'a', done: 10 })])
    h.advance(INTERVAL)
    h.set([op({ id: 'a', done: 11 })])
    h.advance(10)
    h.set([op({ id: 'a', done: 11 }), op({ id: 'b', done: 0 })])
    expect(h.shown().map((o) => o.id)).toEqual(['a', 'b'])
  })

  it('drops a retired operation at once', () => {
    const h = harness([op({ id: 'a' }), op({ id: 'b' })])
    h.advance(INTERVAL)
    h.set([op({ id: 'b' })])
    expect(h.shown().map((o) => o.id)).toEqual(['b'])
  })
})
