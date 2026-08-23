// The settle window between "a pane looks idle" and "its work finished"
// (nocx-n3nfg, notification design §3.4).
//
// The clock is fake and the window is named from the module: a test that
// waited a real five seconds would be a test that depends on timing, which
// AGENTS.md forbids because it is broken on a fast machine too — and five
// seconds per case would be a minute of suite for nine cases.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PANE_WORK_FINISHED_SETTLE_MS, WorkFinishedWatch } from './pane-work-finished'

const SETTLE = PANE_WORK_FINISHED_SETTLE_MS

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

/** A watch over a mutable session, with the sessions it reported. */
function harness(initialSession: string | null = 'sess-a'): {
  watch: WorkFinishedWatch
  fired: string[]
  setSession: (id: string | null) => void
} {
  let session = initialSession
  const fired: string[] = []
  const watch = new WorkFinishedWatch({
    session: () => session,
    onFinished: (id) => fired.push(id),
  })
  return { watch, fired, setSession: (id) => (session = id) }
}

describe('the working → idle edge (design §3.4 rule 1)', () => {
  it('fires once the pane has held idle for the settle window', () => {
    const { watch, fired } = harness()

    watch.edge('working', 'idle')
    expect(fired).toEqual([])

    // Held, not yet settled. The whole point of the window is that this
    // moment is not an answer.
    vi.advanceTimersByTime(SETTLE - 1)
    expect(fired).toEqual([])

    vi.advanceTimersByTime(1)
    expect(fired).toEqual(['sess-a'])
  })

  it('does NOT fire on null → idle, however long it holds', () => {
    // This is the rule a naive implementation gets wrong, and the one the
    // caller got wrong before this state machine existed: markActivity()
    // ran whenever the new value was idle, regardless of the previous one.
    // agent-status.ts says why in its own words — "a title that never
    // mentions an agent is not an idle agent". A pane that has never shown
    // a spinner has not been working, so nothing about it has finished.
    const { watch, fired } = harness()

    watch.edge(null, 'idle')
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('does not fire on any other transition', () => {
    const { watch, fired } = harness()
    for (const [prev, next] of [
      [null, 'working'],
      ['idle', 'working'],
      ['working', null],
      ['idle', null],
    ] as const) {
      watch.edge(prev, next)
      vi.advanceTimersByTime(SETTLE * 2)
    }
    expect(fired).toEqual([])
  })
})

describe('the settle window collapses an oscillation (design §3.4 rule 2)', () => {
  it('produces ONE event for a title that flickers, not one per edge', () => {
    // Claude Code swaps ✳ ↔ spinner every 1–3 s between tool calls. A bare
    // edge fires on each of them; this is the assertion that says it does
    // not. Three finished-looking edges inside the window, one event.
    const { watch, fired } = harness()

    watch.edge('working', 'idle')
    vi.advanceTimersByTime(2000)
    watch.edge('idle', 'working')
    vi.advanceTimersByTime(1000)
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(3000) // past SETTLE measured from the FIRST edge
    watch.edge('idle', 'working')
    vi.advanceTimersByTime(1000)
    watch.edge('working', 'idle')

    expect(fired).toEqual([])

    // Now it settles, and the window is measured from the LAST edge.
    vi.advanceTimersByTime(SETTLE)
    expect(fired).toEqual(['sess-a'])
  })

  it('fires again for a second run of work, once that run settles too', () => {
    // Collapsing an oscillation must not collapse the next real finish:
    // a person who starts a second build wants telling when it ends.
    const { watch, fired } = harness()

    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE)
    expect(fired).toEqual(['sess-a'])

    watch.edge('idle', 'working')
    vi.advanceTimersByTime(60_000)
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE)
    expect(fired).toEqual(['sess-a', 'sess-a'])
  })
})

// All four cancels §3.4 rule 2 names, one test each. Three of them are the
// same mechanism reached by different inputs and one is not, so they are
// written out rather than tabulated: a table would hide which of them is
// load-bearing.
describe('the four cancels (design §3.4 rule 2)', () => {
  it('cancels on idle → working: the pane picked the work back up', () => {
    const { watch, fired } = harness()
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE - 1)
    watch.edge('idle', 'working')
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('cancels on the title going null: the pane stopped saying anything', () => {
    // A TUI clears its title on the way out (OSC 0/2 with an empty string),
    // and the classifier answers null for it. Null is not idle — it is the
    // absence of evidence — so an armed window over it has nothing left to
    // settle.
    const { watch, fired } = harness()
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE - 1)
    watch.edge('idle', null)
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('cancels on tab close', () => {
    const { watch, fired } = harness()
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE - 1)
    watch.cancel()
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('cancels on session replacement: it reports the session it armed on, or nothing', () => {
    // This is the cancel with no event to hang on, and the reason it is a
    // fire-time check rather than a call: the pane is told about a title,
    // never about a reattach, and a session id is server-authoritative and
    // never reused (AD-7). So the watch remembers which session went idle
    // and declines to speak for a different one — the backend refuses the
    // dead id anyway, and a row filed against the wrong live session would
    // be worse than the refusal.
    const { watch, fired, setSession } = harness('sess-a')
    watch.edge('working', 'idle')
    vi.advanceTimersByTime(SETTLE - 1)
    setSession('sess-b')
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('cancels when the pane is left with no session at all', () => {
    const { watch, fired, setSession } = harness('sess-a')
    watch.edge('working', 'idle')
    setSession(null)
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })
})

describe('a pane with nothing to address', () => {
  it('arms nothing when there is no session at the edge', () => {
    // Between sessions a pane has no id to name, and sessionId is
    // ADDRESSING: a record without one addresses nothing. Nothing is armed
    // rather than armed-and-dropped, so a session arriving later cannot
    // inherit a window it was never part of.
    const { watch, fired, setSession } = harness(null)
    watch.edge('working', 'idle')
    setSession('sess-late')
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })

  it('cancel() on a watch that never armed is a no-op', () => {
    const { watch, fired } = harness()
    watch.cancel()
    watch.cancel()
    vi.advanceTimersByTime(SETTLE * 10)
    expect(fired).toEqual([])
  })
})
