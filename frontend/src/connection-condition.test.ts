import { describe, expect, it } from 'vitest'

import {
  connectionCondition,
  connectionObserves,
  connectionRoundTripMs,
  connectionWarning,
  observedAgo,
} from './connection-condition'
import type { PaneConnection } from './generated/pane.connection'

/** One observation, built from the contract's generated type. Every branch of
 *  the transport oneOf has its own override, so a test says which state it is
 *  about and the rest stays valid. */
function observation(transport: PaneConnection['transport'], over: Partial<PaneConnection> = {}) {
  return {
    paneId: 'pane-1',
    connectionId: 'conn-1',
    connectionEpoch: 1,
    instanceId: 'instance-1',
    transport,
    attempt: { shown: 'none' as const, candidate: 'none' as const, reason: '' },
    observedAt: '2026-11-14T00:00:00Z',
    ...over,
  }
}

describe('connectionCondition', () => {
  it('says nothing for a pane that has never observed a connection', () => {
    expect(connectionCondition(null)).toBe('reachable')
  })

  it('draws nothing while opening, waiting for a person, reconnecting or plain', () => {
    expect(connectionCondition(observation({ state: 'opening', stage: 'tcp', stateMs: 10 }))).toBe(
      'reachable',
    )
    expect(
      connectionCondition(
        observation({ state: 'waiting_person', person: 'password', stateMs: 10 }),
      ),
    ).toBe('reachable')
    expect(
      connectionCondition(observation({ state: 'reconnecting', attempt: 2, stateMs: 10 })),
    ).toBe('reachable')
    expect(connectionCondition(observation({ state: 'plain', stateMs: 10 }))).toBe('reachable')
  })

  it('takes the slow grade from the backend rather than the milliseconds', () => {
    const connected = (quality?: PaneConnection['quality']) =>
      observation({ state: 'connected', stateMs: 10, plainShells: 1 }, quality ? { quality } : {})
    expect(connectionCondition(connected({ slow: false, roundTripMs: 9000 }))).toBe('reachable')
    expect(connectionCondition(connected({ slow: true, roundTripMs: 4 }))).toBe('slow')
  })

  it('reports a stalled connection as unreachable, never as lost', () => {
    const stalled = observation({ state: 'stalled', stateMs: 45_000, plainShells: 1 })
    expect(connectionCondition(stalled)).toBe('unreachable')
    expect(connectionWarning(stalled)).toEqual({ label: 'Host not answering' })
  })

  it('lets failed and ended outrank everything else', () => {
    const failed = observation({
      state: 'failed',
      cause: 'auth-rejected',
      stateMs: 10,
    })
    const ended = observation({ state: 'ended', cause: 'lost-link', stateMs: 10 })
    expect(connectionCondition(failed)).toBe('lost')
    expect(connectionCondition(ended)).toBe('lost')
    expect(connectionWarning(failed)).toEqual({ label: 'Connection lost' })
  })
})

describe('connectionObserves', () => {
  it('stops asserting observations for exactly the two states that stop the stream', () => {
    expect(connectionObserves('reachable')).toBe(true)
    expect(connectionObserves('slow')).toBe(true)
    expect(connectionObserves('unreachable')).toBe(false)
    expect(connectionObserves('lost')).toBe(false)
  })
})

describe('connectionRoundTripMs', () => {
  it('treats absent and zero alike — neither is an instant reply', () => {
    const connected = (quality?: PaneConnection['quality']) =>
      observation({ state: 'connected', stateMs: 10, plainShells: 1 }, quality ? { quality } : {})
    expect(connectionRoundTripMs(connected())).toBeNull()
    expect(connectionRoundTripMs(connected({ slow: false, roundTripMs: 0 }))).toBeNull()
    expect(connectionRoundTripMs(connected({ slow: false, roundTripMs: 42 }))).toBe(42)
    expect(connectionRoundTripMs(null)).toBeNull()
  })
})

describe('observedAgo', () => {
  it('says nothing about a fresh belief and ages an old one in words', () => {
    const now = Date.parse('2026-11-14T12:00:00Z')
    expect(observedAgo('2026-11-14T11:59:30Z', now)).toBeUndefined()
    expect(observedAgo('2026-11-14T11:55:00Z', now)).toBe('since 5 min ago')
    expect(observedAgo('2026-11-14T09:00:00Z', now)).toBe('since 3 h ago')
    expect(observedAgo(undefined, now)).toBeUndefined()
  })
})
