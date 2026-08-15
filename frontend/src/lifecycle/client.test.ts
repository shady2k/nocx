// LifecycleClient over the REAL dispatcher over a mock socket — the
// composition-seam test (files-client.test.ts pattern): the client is driven
// through new LifecycleClient(dispatcher), NOT a faked dispatcher, and the
// assertion is what the notification routing does with the wire payload.
// A fake seam cannot see the defect this exists for: a subscription the
// composition root forgets to register, or a guard that drops real facts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher } from '../dispatcher'
import { LifecycleClient } from './client'
import type { LifecycleChanged } from '../generated/lifecycle.changed'

// The same minimal mock WebSocket the dispatcher tests use: property
// callbacks + addEventListener, send recorded, deliver() synthesizes a frame.
class MockSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState: number = MockSocket.CONNECTING
  readonly sent: string[] = []

  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null

  private _listeners = new Map<string, Set<(...args: unknown[]) => void>>()

  addEventListener(type: string, fn: (...args: unknown[]) => void): void {
    let s = this._listeners.get(type)
    if (!s) {
      s = new Set()
      this._listeners.set(type, s)
    }
    s.add(fn)
  }

  removeEventListener(type: string, fn: (...args: unknown[]) => void): void {
    this._listeners.get(type)?.delete(fn)
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(): void {
    this.readyState = MockSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  accept(): void {
    this.readyState = MockSocket.OPEN
    this.onopen?.()
    this._fire('open')
  }

  deliver(msg: {
    id?: number
    result?: unknown
    error?: unknown
    method?: string
    params?: unknown
  }): void {
    const event = { data: JSON.stringify(msg) }
    this.onmessage?.(event)
    this._fire('message', event)
  }

  private _fire(type: string, event?: unknown): void {
    for (const fn of this._listeners.get(type) ?? []) {
      fn(event)
    }
  }
}

const OriginalWebSocket = globalThis.WebSocket
let nextSocket: MockSocket | null = null

beforeEach(() => {
  nextSocket = null
  const fn = function (url: string) {
    const s = new MockSocket()
    Object.defineProperty(s, 'url', { value: url, writable: false })
    nextSocket = s
    return s as unknown as WebSocket
  } as unknown as {
    new (url: string): WebSocket
    OPEN: number
    CONNECTING: number
    CLOSING: number
    CLOSED: number
  }
  fn.OPEN = OriginalWebSocket.OPEN
  fn.CONNECTING = OriginalWebSocket.CONNECTING
  fn.CLOSING = OriginalWebSocket.CLOSING
  fn.CLOSED = OriginalWebSocket.CLOSED
  globalThis.WebSocket = fn as unknown as typeof WebSocket
})

afterEach(() => {
  globalThis.WebSocket = OriginalWebSocket
})

function lastSocket(): MockSocket {
  if (!nextSocket) throw new Error('no WebSocket was constructed')
  return nextSocket
}

async function connectAndAccept(d: Dispatcher): Promise<void> {
  const p = d.connect(9876)
  lastSocket().accept()
  await p
}

function fact(over: Partial<LifecycleChanged> = {}): LifecycleChanged {
  return {
    sessionId: 'sid-2',
    lane: 'lane-1',
    lifecycle: 'prompt_ready',
    domain: 'dom-1',
    epoch: 1,
    ...over,
  }
}

describe('LifecycleClient', () => {
  it('routes a lifecycle.changed notification from the wire into the handler', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const handler = vi.fn()
    client.subscribeLifecycleChanged(handler).bindSession('sid-2')

    const f = fact({ lifecycle: 'running', attempt: { id: 'a1', state: 'open' } })
    lastSocket().deliver({ method: 'lifecycle.changed', params: f })

    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(f)
  })

  it('delivers a shared-socket notification only to its owning session', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const first = vi.fn()
    const second = vi.fn()
    client.subscribeLifecycleChanged(first).bindSession('sid-1')
    client.subscribeLifecycleChanged(second).bindSession('sid-2')

    const f = fact({ lifecycle: 'running', attempt: { id: 'a1', state: 'open' } })
    lastSocket().deliver({ method: 'lifecycle.changed', params: f })

    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledOnce()
    expect(second).toHaveBeenCalledWith(f)
  })
  // The subscription is installed before session.open, so it is live while
  // another tab's session is still publishing. An unbound subscription owns no
  // session id yet and must therefore deliver nothing at all — and it must not
  // buffer either: the backend installs a session's subscriber only after its
  // open result and replays the projection there, so catch-up has one owner
  // and it is not this one.
  it('delivers nothing until the open result binds its session id', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const handler = vi.fn()
    const subscription = client.subscribeLifecycleChanged(handler)

    lastSocket().deliver({ method: 'lifecycle.changed', params: fact({ sessionId: 'sid-1' }) })
    lastSocket().deliver({ method: 'lifecycle.changed', params: fact({ sessionId: 'sid-2' }) })
    expect(handler).not.toHaveBeenCalled()

    subscription.bindSession('sid-2')
    expect(handler).not.toHaveBeenCalled()

    const replayed = fact({ sessionId: 'sid-2' })
    lastSocket().deliver({ method: 'lifecycle.changed', params: replayed })
    expect(handler).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledWith(replayed)
  })

  it('does not deliver a payload without a lane (not a fact)', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const handler = vi.fn()
    client.subscribeLifecycleChanged(handler).bindSession('sid-2')

    lastSocket().deliver({ method: 'lifecycle.changed', params: { lifecycle: 'lost' } })
    lastSocket().deliver({ method: 'lifecycle.changed', params: null })

    expect(handler).not.toHaveBeenCalled()
  })

  it('unsubscribe stops delivery without affecting the socket', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const handler = vi.fn()
    const subscription = client.subscribeLifecycleChanged(handler)
    subscription.bindSession('sid-2')
    subscription.unsubscribe()

    lastSocket().deliver({ method: 'lifecycle.changed', params: fact() })

    expect(handler).not.toHaveBeenCalled()
  })

  it('submitAttempt sends the app-owned params and resolves the created attempt', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const p = client.submitAttempt({
      domain: 'd1',
      command: 'make',
      cwd: '/srv/app',
      host: 'build.example.com',
    })

    // The request went out with the app-owned half of the execution —
    // command, cwd and host captured at submit (ADR-0024 decision 5).
    const sent = JSON.parse(lastSocket().sent[0]) as { method: string; params: unknown }
    expect(sent.method).toBe('lifecycle.submitAttempt')
    expect(sent.params).toEqual({
      domain: 'd1',
      command: 'make',
      cwd: '/srv/app',
      host: 'build.example.com',
    })

    lastSocket().deliver({
      id: 1,
      result: {
        id: 'att-1',
        domain: 'd1',
        state: 'open',
        command: 'make',
        cwd: '/srv/app',
        host: 'build.example.com',
        origin: 'app',
        startedAt: '2026-08-08T12:00:00Z',
      },
    })
    await expect(p).resolves.toMatchObject({ id: 'att-1', state: 'open', origin: 'app' })
  })

  it('submitAttempt surfaces a backend refusal as a rejected promise', async () => {
    const dispatcher = new Dispatcher()
    await connectAndAccept(dispatcher)
    const client = new LifecycleClient(dispatcher)
    const p = client.submitAttempt({ domain: 'd1', command: 'make', cwd: '', host: '' })
    lastSocket().deliver({
      id: 1,
      error: { code: -32602, message: 'lifecycle: no prompt is ready' },
    })
    await expect(p).rejects.toMatchObject({ code: -32602 })
  })
})
