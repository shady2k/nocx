// The store's refusals and its one fold (design §5.6, ADR-ba6xet1b).
//
// A surface may not draw an observation the backend could not have sent, nor
// one a later observation has already overtaken. Both rules are cheap and both
// are the difference between a stale belief and a truthful one, so each has a
// test that would pass a permissive implementation nowhere.

import { describe, expect, it, vi } from 'vitest'

import type { PaneConnection } from '../generated/pane.connection'
import { ConnectionStore, parsePaneConnection, type ConnectionSource } from './connection-store'

type Handler = (params: unknown) => void

/** A dispatcher-shaped recorder: the test delivers a notification the way the
 *  socket would, through the subscription the store made. */
function source(): ConnectionSource & { deliver(method: string, params: unknown): void } {
  const handlers = new Map<string, Handler[]>()
  return {
    subscribe(method, handler) {
      const list = handlers.get(method) ?? []
      list.push(handler)
      handlers.set(method, list)
      return () => {
        handlers.set(
          method,
          (handlers.get(method) ?? []).filter((h) => h !== handler),
        )
      }
    },
    deliver(method, params) {
      for (const handler of handlers.get(method) ?? []) handler(params)
    },
  }
}

function observation(over: Partial<PaneConnection> = {}): PaneConnection {
  return {
    paneId: 'pane-1',
    connectionId: 'conn-1',
    connectionEpoch: 1,
    instanceId: 'instance-1',
    transport: { state: 'opening', stage: 'tcp', stateMs: 1200 },
    attempt: { shown: 'none', candidate: 'none', reason: '' },
    observedAt: '2026-11-14T00:00:00Z',
    ...over,
  }
}

describe('parsePaneConnection', () => {
  it('accepts every state branch the contract declares', () => {
    const branches: PaneConnection['transport'][] = [
      { state: 'opening', stage: 'shell', stateMs: 1 },
      { state: 'waiting_person', person: 'host-key', stateMs: 1 },
      { state: 'connected', stateMs: 1, plainShells: 0 },
      { state: 'stalled', stateMs: 1, plainShells: 1, lookAnsweredAt: '2026-11-14T00:00:00Z' },
      { state: 'reconnecting', attempt: 3, stateMs: 1 },
      { state: 'failed', cause: 'no-auth-method', stateMs: 1 },
      { state: 'ended', cause: 'silence', stateMs: 1 },
      { state: 'plain', stateMs: 1 },
    ]
    for (const transport of branches) {
      expect(parsePaneConnection(observation({ transport })), transport.state).not.toBeNull()
    }
  })

  it('refuses a shape the contract could not have sent', () => {
    const bad = [
      // A stage on a connected connection, a cause on an opening one.
      observation({
        transport: { state: 'connected', stateMs: 1, plainShells: 0, stage: 'tcp' } as never,
      }),
      observation({
        transport: { state: 'opening', stage: 'tcp', stateMs: 1, cause: 'released' } as never,
      }),
      // A state nobody wrote a branch for.
      observation({ transport: { state: 'half-open', stateMs: 1 } as never }),
      // Fields with no branch of their own.
      observation({ connectionEpoch: 0 }),
      observation({ observedAt: 'not a time' }),
      observation({ attempt: { shown: 'typed', candidate: 'none', reason: '' } as never }),
      observation({ reconnect: { endsShells: -1 } }),
      // An unknown extra key: additionalProperties is false in the schema.
      { ...observation(), extra: true },
    ]
    for (const value of bad) expect(parsePaneConnection(value)).toBeNull()
  })
})

describe('ConnectionStore', () => {
  it('applies the newer observation and refuses an older one', () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')
    const seen = vi.fn()
    store.onChange(seen)

    wire.deliver('pane.connection', observation({ connectionEpoch: 2 }))
    expect(store.current()?.observation.connectionEpoch).toBe(2)

    wire.deliver('pane.connection', observation({ connectionEpoch: 1 }))
    expect(store.current()?.observation.connectionEpoch).toBe(2)
    expect(seen).toHaveBeenCalledTimes(1)
    store.dispose()
  })

  it('ignores another pane and another backend instance', () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')

    wire.deliver('pane.connection', observation({ paneId: 'pane-2' }))
    expect(store.current()).toBeNull()

    wire.deliver('pane.connection', observation({ connectionEpoch: 2 }))
    wire.deliver('pane.connection', observation({ connectionEpoch: 3, instanceId: 'instance-2' }))
    expect(store.current()?.observation.connectionEpoch).toBe(2)
    store.dispose()
  })

  it("reads the button from the backend's offer and nowhere else", () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')
    wire.deliver('pane.connection', observation({ connectionEpoch: 4 }))
    expect(store.offer()).toBeNull()

    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 5,
        transport: { state: 'failed', cause: 'auth-rejected', stateMs: 10 },
        reconnect: { endsShells: 2 },
      }),
    )
    expect(store.offer()).toEqual({ connectionEpoch: 5, endsShells: 2 })
    store.dispose()
  })

  it('turns the session exit into the lost condition, and a new session clears it', () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 1,
        sessionId: 'session-a',
        transport: { state: 'connected', stateMs: 100, plainShells: 1 },
      }),
    )
    expect(store.condition()).toBe('reachable')

    wire.deliver('exit', { sessionId: 'session-a', cause: 'interrupted' })
    expect(store.condition()).toBe('lost')

    // A `connected` observation minted before the channel died does not
    // resurrect it.
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 2,
        sessionId: 'session-a',
        transport: { state: 'connected', stateMs: 200, plainShells: 1 },
      }),
    )
    expect(store.condition()).toBe('lost')

    // A NEW shell is the end of the loss.
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 3,
        sessionId: 'session-b',
        transport: { state: 'connected', stateMs: 10, plainShells: 1 },
      }),
    )
    expect(store.condition()).toBe('reachable')
    store.dispose()
  })

  it('stops listening when disposed', () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')
    store.dispose()
    wire.deliver('pane.connection', observation())
    expect(store.current()).toBeNull()
  })
})
