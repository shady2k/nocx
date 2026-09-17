import { describe, expect, it } from 'vitest'

import type { SessionSignalUndelivered } from './generated/session.signalUndelivered'
import { subscribeSignalUndelivered, undeliveredNotice } from './session-signal-undelivered'

/** A dispatcher stand that keeps one subscriber per method, which is all this
 *  module uses: it is a boundary guard around a server-initiated notification,
 *  and the guard is the half worth pinning. */
function captureDispatcher(): {
  subscribe: (method: string, handler: (params: unknown) => void) => () => void
  deliver: (method: string, params: unknown) => void
} {
  const subs = new Map<string, (params: unknown) => void>()
  return {
    subscribe: (method, handler) => {
      subs.set(method, handler)
      return () => subs.delete(method)
    },
    deliver: (method, params) => subs.get(method)?.(params),
  }
}

const fact = (reason: SessionSignalUndelivered['reason']): SessionSignalUndelivered => ({
  sessionId: 'sid-1',
  signal: 'stop',
  attempt: 'att-1',
  reason,
})

describe('session.signalUndelivered at the boundary (nocx-zas0d)', () => {
  it('delivers a well-formed fact and ignores a payload that is not one', () => {
    const dispatcher = captureDispatcher()
    const seen: SessionSignalUndelivered[] = []
    subscribeSignalUndelivered(dispatcher as never, (f) => seen.push(f))

    dispatcher.deliver('session.signalUndelivered', fact('attempt-closed'))
    expect(seen).toEqual([fact('attempt-closed')])

    // Every field the fact is made of, missing one at a time: none of these
    // is a fact, and half-applying one would clear a mark on a name that is
    // not there.
    for (const broken of [
      {},
      { sessionId: 'sid-1' },
      { sessionId: 'sid-1', signal: 'stop' },
      { sessionId: 'sid-1', signal: 'stop', attempt: 'att-1' },
      null,
      42,
    ]) {
      dispatcher.deliver('session.signalUndelivered', broken)
    }
    expect(seen).toHaveLength(1)
    expect(seen[0]).toEqual(fact('attempt-closed'))
  })

  it('says something true for every reason the contract allows', () => {
    // The set is closed by contracts/session.signalUndelivered.schema.json;
    // this row list is the renderer's copy of it, and a reason with no
    // sentence would fail here rather than show a blank toast.
    const reasons: SessionSignalUndelivered['reason'][] = [
      'attempt-closed',
      'write-refused',
      'write-failed',
      'lane-refused',
      'unsupported',
    ]
    for (const reason of reasons) {
      const notice = undeliveredNotice(fact(reason))
      expect(notice, `${reason} has no sentence`).not.toBeNull()
      expect(notice!.message.length).toBeGreaterThan(20)
    }
    // The command having ENDED is not a failure to shout about; a byte that
    // never got written while the command may still be running is.
    expect(undeliveredNotice(fact('attempt-closed'))!.level).toBe('info')
    for (const reason of ['write-refused', 'write-failed', 'lane-refused'] as const) {
      expect(undeliveredNotice(fact(reason))!.level).toBe('warning')
    }
    // And the warning says what to do about it, because the retry is the
    // person's next press.
    expect(undeliveredNotice(fact('write-refused'))!.message).toContain('press Stop again')
  })
})
