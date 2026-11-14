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

  it('admits a connected transport whose shell failed with its retry offer', () => {
    const value = observation({
      transport: { state: 'connected', stateMs: 10, plainShells: 0 },
      shellFailure: { code: -32603, message: 'no builtin' },
      reconnect: { endsShells: 0 },
    })
    expect(parsePaneConnection(value)).not.toBeNull()
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
      // The cross-field rules: a shell-only retry that would end shells, a
      // failure without an offer, and a failed shell carrying a session id.
      observation({
        transport: { state: 'connected', stateMs: 1, plainShells: 1 },
        shellFailure: { code: 1, message: 'x' },
        reconnect: { endsShells: 2 },
      }),
      observation({
        transport: { state: 'connected', stateMs: 1, plainShells: 1 },
        shellFailure: { code: 1, message: 'x' },
      }),
      observation({
        transport: { state: 'connected', stateMs: 1, plainShells: 0 },
        sessionId: 's',
        shellFailure: { code: 1, message: 'x' },
        reconnect: { endsShells: 0 },
      }),
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

  it("compares against the client's identity and resets when the backend changes", () => {
    const wire = source()
    let backend: string | null = 'instance-1'
    const store = new ConnectionStore(wire, 'pane-1', () => backend)

    wire.deliver('pane.connection', observation({ paneId: 'pane-2' }))
    expect(store.current()).toBeNull()

    wire.deliver('pane.connection', observation({ connectionEpoch: 2 }))
    // A stale observation from another backend is refused even though its
    // epoch is higher: epochs belong to a backend, not to the pane.
    wire.deliver('pane.connection', observation({ connectionEpoch: 3, instanceId: 'instance-2' }))
    expect(store.current()?.observation.connectionEpoch).toBe(2)

    // The client learns it is talking to a replacement backend: the epoch
    // counter belongs to the old one, so the replacement's epoch 1 is applied.
    backend = 'instance-2'
    wire.deliver('pane.connection', observation({ connectionEpoch: 1, instanceId: 'instance-2' }))
    expect(store.current()?.observation.instanceId).toBe('instance-2')
    expect(store.current()?.observation.connectionEpoch).toBe(1)
    store.dispose()
  })

  it('never pins an identity from the first observation', () => {
    const wire = source()
    // The client has not named a backend yet: a stale first observation must
    // be accepted (there is nothing to compare), and must not become the
    // identity a later one is refused against.
    let backend: string | null = null
    const store = new ConnectionStore(wire, 'pane-1', () => backend)
    wire.deliver('pane.connection', observation({ connectionEpoch: 9, instanceId: 'stale' }))
    expect(store.current()?.observation.connectionEpoch).toBe(9)

    // THE FIRST NAME RESETS THE ANONYMOUS COUNTER: this backend's epoch 1
    // must not be suppressed by an observation accepted before the client
    // knew which backend it was talking to.
    backend = 'instance-1'
    wire.deliver('pane.connection', observation({ connectionEpoch: 1, instanceId: 'instance-1' }))
    expect(store.current()?.observation.instanceId).toBe('instance-1')
    expect(store.current()?.observation.connectionEpoch).toBe(1)
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

  it('does not turn a session exit into a connection verdict', () => {
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

    // The exit is the session's end; the connection's verdict is the next
    // pane.connection's to state.
    wire.deliver('exit', { sessionId: 'session-a', cause: 'interrupted' })
    expect(store.condition()).toBe('reachable')
    expect(store.current()?.observation.transport.state).toBe('connected')
    store.dispose()
  })

  it('keeps a connected transport with a shell failure and its shell-only offer', () => {
    const wire = source()
    const store = new ConnectionStore(wire, 'pane-1')
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 2,
        transport: { state: 'connected', stateMs: 500, plainShells: 0 },
        shellFailure: { code: -32603, message: 'the shell could not start' },
        reconnect: { endsShells: 0 },
      }),
    )
    expect(store.current()?.observation.shellFailure?.message).toBe('the shell could not start')
    expect(store.offer()).toEqual({ connectionEpoch: 2, endsShells: 0 })
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
