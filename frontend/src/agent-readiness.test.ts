// The readiness STORE's own tests (nocx-rikz5).
//
// Two claims here that no surface test can make cheaply. The first is that
// the store is a stored fact with a refresh, not a function called at
// refusal time — the shape terminal-content.ts had, which cannot repaint
// anything. The second is the ordering rule: two refreshes racing is the
// ORDINARY case (configure an agent, then immediately choose a model is one
// gesture apart), so a reply that resolves late must never repaint over a
// newer one.
import { describe, it, expect, vi } from 'vitest'
import { AgentReadiness, modelChipState } from './agent-readiness'
import type { AgentStatusResult } from './generated/agent.status'

/** A status the wire could actually send. The defaults are the ready
 *  state; every test names only the facts it is about. */
function status(over: Partial<AgentStatusResult> = {}): AgentStatusResult {
  return {
    message: null,
    answering: { ready: true, reason: null, agentId: 'openrouter', model: 'm-a' },
    ...over,
  }
}

const READY = status()
const MISSING_MODEL = status({
  answering: { ready: false, reason: 'model-gone', agentId: null, model: null },
})
const NOT_INSTALLED = status({
  answering: { ready: false, reason: 'not-installed', agentId: null, model: null },
})
const UNAVAILABLE = status({
  answering: { ready: false, reason: 'unavailable', agentId: null, model: null },
})

/** A source whose answers are queued: each call takes the next deferred,
 *  so a test decides the ORDER the replies land in, independently of the
 *  order the asks went out. */
function deferredSource() {
  const pending: Array<(st: AgentStatusResult) => void> = []
  return {
    source: {
      status: () =>
        new Promise<AgentStatusResult>((resolve) => {
          pending.push(resolve)
        }),
    },
    /** Answer the nth ask (0-based) — deliberately not FIFO. */
    settle(nth: number, st: AgentStatusResult) {
      pending[nth](st)
    },
    get asked() {
      return pending.length
    },
  }
}

describe('AgentReadiness — the one owner of the readiness fact (AD-8)', () => {
  it('holds nothing until it has asked, and holds the answer afterwards', async () => {
    const readiness = new AgentReadiness({ status: () => Promise.resolve(READY) })
    expect(readiness.status).toBeNull()
    await readiness.refresh()
    expect(readiness.status).toEqual(READY)
  })

  it('tells every subscriber the new fact, and stops after unsubscribe', async () => {
    const seen: Array<AgentStatusResult | null> = []
    let answer = READY
    const readiness = new AgentReadiness({ status: () => Promise.resolve(answer) })
    const unsub = readiness.subscribe((st) => seen.push(st))
    await readiness.refresh()
    answer = MISSING_MODEL
    await readiness.refresh()
    expect(seen).toEqual([READY, MISSING_MODEL])
    unsub()
    answer = NOT_INSTALLED
    await readiness.refresh()
    expect(seen).toEqual([READY, MISSING_MODEL])
    // The store itself still moved — only the listener stopped hearing.
    expect(readiness.status).toEqual(NOT_INSTALLED)
  })

  it('discards a late reply that would repaint an older state', async () => {
    const deferred = deferredSource()
    const settle = (nth: number, st: AgentStatusResult): void => deferred.settle(nth, st)
    const source = deferred.source
    const readiness = new AgentReadiness(source)
    const seen: Array<AgentStatusResult | null> = []
    readiness.subscribe((st) => seen.push(st))

    // Two asks in flight: the first carries the OLD facts (no agent ready),
    // the second the new ones. The second answers first.
    const first = readiness.refresh()
    const second = readiness.refresh()
    settle(1, READY) // the SECOND ask answers first
    await second
    expect(readiness.status).toEqual(READY)

    settle(0, NOT_INSTALLED)
    await first
    // The older answer arrived last and changed nothing: no repaint, and
    // no second notification.
    expect(readiness.status).toEqual(READY)
    expect(seen).toEqual([READY])
  })

  it('keeps the last fact when a refresh fails, and reports the failure to its caller', async () => {
    let fail = false
    const readiness = new AgentReadiness({
      status: () => (fail ? Promise.reject(new Error('ws closed')) : Promise.resolve(READY)),
    })
    await readiness.refresh()
    fail = true
    await expect(readiness.refresh()).rejects.toThrow('ws closed')
    // A socket that could not answer is not a fact about the assistant:
    // the chip keeps saying what was last true rather than going blank.
    expect(readiness.status).toEqual(READY)
  })

  it('asks the source once per refresh — the fact is stored, not re-fetched per read', async () => {
    const ask = vi.fn(() => Promise.resolve(READY))
    const readiness = new AgentReadiness({ status: ask })
    await readiness.refresh()
    expect(readiness.status).toEqual(READY)
    expect(readiness.status).toEqual(READY)
    expect(ask).toHaveBeenCalledTimes(1)
  })
})

describe('modelChipState — the chip reads the ladder, it does not invent words', () => {
  it('names the agent and the model when the answering role resolves', () => {
    expect(modelChipState(READY)).toEqual({ kind: 'ready', agentId: 'openrouter', model: 'm-a' })
  })

  it("offers the rung's own sentence and the page that fixes it", () => {
    expect(modelChipState(MISSING_MODEL)).toEqual({
      kind: 'action',
      text: 'The answering agent is unavailable.',
      page: 'agent-installed',
    })
    expect(modelChipState(NOT_INSTALLED)).toEqual({
      kind: 'action',
      text: 'The answering agent is unavailable.',
      page: 'agent-installed',
    })
  })

  it('gives a rung no page when no page repairs it', () => {
    expect(modelChipState(UNAVAILABLE)).toEqual({
      kind: 'action',
      text: 'The answering agent is unavailable.',
      page: 'agent-installed',
    })
  })

  it('has nothing to say before a status has been read', () => {
    expect(modelChipState(null)).toBeNull()
  })
})

it('shows Agent default as ready when the harness does not name an explicit model', () => {
  expect(
    modelChipState(
      status({ answering: { ready: true, reason: null, agentId: 'pi', model: null } }),
    ),
  ).toEqual({ kind: 'ready', agentId: 'pi', model: 'Agent default' })
})

describe('AgentReadiness invocation gate', () => {
  it.each([
    ['not-installed', 'Install pi to use the assistant.'],
    ['not-configured', 'Configure a provider in pi.'],
    ['not-configured', 'The provider credentials are invalid.'],
    ['model-gone', 'The chosen model is no longer available.'],
  ] as const)(
    'opens agent settings with the %s reason before invoking',
    async (reason, message) => {
      const readiness = new AgentReadiness({
        status: () =>
          Promise.resolve(
            status({
              answering: { ready: false, reason, agentId: 'pi', model: null },
              message,
            }),
          ),
      })
      const repair = vi.fn()
      expect(await readiness.ensureReady(repair)).toBe(false)
      expect(repair).toHaveBeenCalledExactlyOnceWith(message)
      expect(modelChipState(readiness.status)).toEqual({
        kind: 'action',
        text: message,
        page: 'agent-installed',
      })
    },
  )

  it('allows a ready agent without opening settings', async () => {
    const readiness = new AgentReadiness({ status: () => Promise.resolve(READY) })
    const repair = vi.fn()
    expect(await readiness.ensureReady(repair)).toBe(true)
    expect(repair).not.toHaveBeenCalled()
  })
})
