// @vitest-environment jsdom
//
// The head's sentences, its clock and its one button, per state branch of the
// generated contract type (design §5.8, task 5.5).
//
// The tests mount the REAL component over the REAL store, fed through a
// dispatcher-shaped recorder. A fixture built from the contract's own type is
// the point: a state branch the wire can send but the head cannot draw fails
// here, not in a person's window.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { PaneConnection } from '../generated/pane.connection'
import type { ConnectionSource } from './connection-store'
import { ConnectionStore } from './connection-store'
import { mountConnectionHead, type ConnectionHeadHandle } from './connection-head'

type Handler = (params: unknown) => void

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
    generation: 1,
    instanceId: 'instance-1',
    helper: { state: 'starting', action: 'none' },
    transport: { state: 'opening', stage: 'tcp', stateMs: 2000 },
    attempt: { shown: 'none', candidate: 'none', reason: '' },
    observedAt: '2026-11-14T00:00:00Z',
    ...over,
  }
}

let target: HTMLElement
let wire: ReturnType<typeof source>
let store: ConnectionStore
let head: ConnectionHeadHandle | null

function mount(onReconnect = vi.fn()): void {
  wire = source()
  store = new ConnectionStore(wire, 'pane-1')
  target = document.createElement('div')
  document.body.appendChild(target)
  head = mountConnectionHead(target, { store, onReconnect })
}

const line = (): string => target.querySelector('.ui-connection-head__text')?.textContent ?? ''

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-14T00:00:03Z'))
})

afterEach(() => {
  head?.dispose()
  head = null
  store.dispose()
  target.remove()
  document.body.replaceChildren()
  vi.useRealTimers()
})

describe('the connection head', () => {
  it('says it is connecting before the first observation, and never renders nothing', () => {
    mount()
    expect(target.querySelector('[data-testid="connection-head"]')).not.toBeNull()
    expect(line()).toBe('Connecting')
    expect(
      target.querySelector('[data-testid="connection-head"]')?.getAttribute('data-state'),
    ).toBe('starting')
    expect(
      target.querySelector('[data-testid="connection-head"]')?.getAttribute('data-stage'),
    ).toBeNull()
  })

  it('names the stage and ticks the clock locally', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'opening', stage: 'ssh-version', stateMs: 2000 },
        observedAt: '2026-11-14T00:00:03Z',
      }),
    )
    expect(line()).toBe('Connecting · 0:02 · waiting for the SSH answer')
    vi.advanceTimersByTime(3000)
    expect(line()).toBe('Connecting · 0:05 · waiting for the SSH answer')
  })

  it('says what a person is being asked for, with no clock running', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'waiting_person', person: 'key-signature', stateMs: 4000 },
      }),
    )
    expect(line()).toBe('Waiting for the key\u2019s signature; it may need a touch')
  })

  it('says nothing to sign in with in plain words (nocx-yfba)', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({ transport: { state: 'failed', cause: 'no-auth-method', stateMs: 10 } }),
    )
    expect(line()).toContain('nothing to sign in with')
    expect(
      target.querySelector('[data-testid="connection-head"]')?.getAttribute('data-state'),
    ).toBe('failed')
  })

  it('shows Reconnect exactly when the observation carries the offer, and says what it ends', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'failed', cause: 'auth-rejected', stateMs: 10 },
        reconnect: { endsShells: 2 },
      }),
    )
    const button = target.querySelector('.ui-button')
    expect(button?.textContent).toBe('Reconnect')
    expect(button?.getAttribute('title')).toContain('2 shell(s)')
    // Said before the press, not after it: the offer names what it ends.
    expect(line()).toContain('ends 2 shell(s)')
  })

  it('shows no button when the backend offers none', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'connected', stateMs: 1400, plainShells: 1 },
        observedAt: '2026-11-14T00:00:03Z',
      }),
    )
    expect(target.querySelector('.ui-button')).toBeNull()
    expect(line()).toBe('Connected · 1.4 s')
  })

  it('says a healthy transport whose shell failed is not Connected, and offers the shell-only retry', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'connected', stateMs: 500, plainShells: 0 },
        shellFailure: { code: -32603, message: 'the shell could not start' },
        reconnect: { endsShells: 0 },
      }),
    )
    expect(line()).toBe('Shell failed · the shell could not start')
    const button = target.querySelector('.ui-button')
    expect(button?.textContent).toBe('Retry shell')
    expect(button?.getAttribute('title')).toBe('Open a new shell on this connection')
    expect(
      target.querySelector('[data-testid="connection-head"]')?.getAttribute('data-shell-failure'),
    ).toBe('true')
  })

  it('says the host record it could not remember in the same line, and marks the state a warning', () => {
    mount()
    wire.deliver(
      'pane.connection',
      observation({
        transport: { state: 'connected', stateMs: 1400, plainShells: 1 },
        observedAt: '2026-11-14T00:00:03Z',
      }),
    )
    expect(line()).toBe('Connected · 1.4 s')
    head?.note('the host\u2019s method was not saved')
    // ONE LINE, STILL (design §5.8): the fact that matters about the host rides
    // the sentence the head already says, and the healthy tone becomes a
    // warning because there is something to act on.
    expect(line()).toBe('Connected · 1.4 s \u00b7 the host\u2019s method was not saved')
    expect(target.querySelector('[data-testid="connection-head"]')?.getAttribute('data-tone')).toBe(
      'warn',
    )
    head?.note(null)
    expect(line()).toBe('Connected · 1.4 s')
  })

  it('calls the press once and stays disabled until the next observation', () => {
    const onReconnect = vi.fn()
    mount(onReconnect)
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 2,
        transport: { state: 'ended', cause: 'released', stateMs: 10 },
        reconnect: { endsShells: 0 },
      }),
    )
    const button = target.querySelector<HTMLButtonElement>('.ui-button')!
    button.click()
    button.click()
    expect(onReconnect).toHaveBeenCalledTimes(1)
    expect(button.disabled).toBe(true)
    wire.deliver(
      'pane.connection',
      observation({
        connectionEpoch: 3,
        transport: { state: 'reconnecting', attempt: 1, stateMs: 0 },
        reconnect: { endsShells: 0 },
      }),
    )
    expect(target.querySelector<HTMLButtonElement>('.ui-button')?.disabled).toBe(false)
  })
})
