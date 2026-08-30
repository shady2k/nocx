import { describe, expect, it } from 'vitest'

import { connectionCondition, connectionRoundTripMs } from './connection-condition'
import type { SessionLiveness } from './generated/session.liveness'

const liveness = (over: Partial<SessionLiveness>): SessionLiveness => ({
  sessionId: 's',
  instanceId: 'i',
  sessionEpoch: 1,
  liveness: 'alive',
  livenessEpoch: 1,
  observedAt: '2026-08-29T00:00:00Z',
  ...over,
})

describe('connectionCondition', () => {
  // A local pane is never probed, so it has no liveness at all — and that is
  // not "unknown", it is "nothing to say".
  it('says nothing for a pane whose host is never probed', () => {
    expect(connectionCondition({ sessionLost: false, liveness: null })).toBe('reachable')
  })

  it('draws nothing for a host that is answering promptly', () => {
    expect(connectionCondition({ sessionLost: false, liveness: liveness({}) })).toBe('reachable')
  })

  // The backend's grade is taken, never re-derived: a renderer thresholding
  // the milliseconds for itself would disagree with the backend exactly at
  // the boundary, because the grade has hysteresis and the number does not.
  it('takes the slow grade from the backend rather than the milliseconds', () => {
    expect(
      connectionCondition({ sessionLost: false, liveness: liveness({ roundTripMs: 9000 }) }),
    ).toBe('reachable')
    expect(
      connectionCondition({
        sessionLost: false,
        liveness: liveness({ slow: true, roundTripMs: 4 }),
      }),
    ).toBe('slow')
  })

  it('reports an unreachable host', () => {
    expect(
      connectionCondition({ sessionLost: false, liveness: liveness({ liveness: 'unknown' }) }),
    ).toBe('unreachable')
  })

  // A session that ended outranks every reachability statement, including a
  // stale "alive" that arrived before it died. The two are different facts and
  // the terminal one is final.
  it('lets a lost session outrank any reachability the wire last reported', () => {
    expect(connectionCondition({ sessionLost: true, liveness: liveness({}) })).toBe('lost')
    expect(
      connectionCondition({ sessionLost: true, liveness: liveness({ liveness: 'unknown' }) }),
    ).toBe('lost')
  })
})

describe('connectionRoundTripMs', () => {
  it('treats absent and zero alike — neither is an instant reply', () => {
    expect(connectionRoundTripMs({ sessionLost: false, liveness: liveness({}) })).toBeNull()
    expect(
      connectionRoundTripMs({ sessionLost: false, liveness: liveness({ roundTripMs: 0 }) }),
    ).toBeNull()
    expect(
      connectionRoundTripMs({ sessionLost: false, liveness: liveness({ roundTripMs: 42 }) }),
    ).toBe(42)
  })
})
