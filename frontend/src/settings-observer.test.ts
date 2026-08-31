import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { SettingsObserver } from './settings-observer'

class MockSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState: number = MockSocket.CONNECTING
  readonly url = 'ws://127.0.0.1:9876/session'
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
  }
  drop(): void {
    this.readyState = MockSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }
  deliver(msg: Record<string, unknown>): void {
    const data = JSON.stringify(msg)
    const event = { data }
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
  } as unknown as { new (url: string): WebSocket; OPEN: number }
  fn.OPEN = OriginalWebSocket.OPEN
  globalThis.WebSocket = fn as unknown as typeof WebSocket
})

afterEach(() => {
  globalThis.WebSocket = OriginalWebSocket
})

function lastSocket(): MockSocket {
  if (!nextSocket) throw new Error('no WebSocket was constructed')
  return nextSocket
}

async function connect(d: Dispatcher): Promise<void> {
  d.retryNow()
  await Promise.resolve()
  lastSocket().accept()
}

describe('SettingsObserver', () => {
  it('calls handler on settings.changed notification', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 6, keys: ['k'] },
    })
    expect(handler).toHaveBeenCalledTimes(1)

    obs.stop()
  })

  it('ignores duplicate or older revision', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 6, keys: ['a'] },
    })
    expect(handler).toHaveBeenCalledTimes(1)

    // Same revision again — duplicate.
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 6, keys: ['a'] },
    })
    expect(handler).toHaveBeenCalledTimes(1)

    // Older revision.
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 4, keys: ['a'] },
    })
    expect(handler).toHaveBeenCalledTimes(1)

    obs.stop()
  })

  it('accepts gap (non-sequential revision) and calls handler', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 10, keys: ['a'] },
    })
    expect(handler).toHaveBeenCalledTimes(1)

    obs.stop()
  })

  it('calls handler on reconnect', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)
    expect(handler).toHaveBeenCalledTimes(0)

    lastSocket().drop()
    await connect(d)

    expect(handler).toHaveBeenCalledTimes(1)

    obs.stop()
  })

  it('does not call handler after stop', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)
    obs.stop()

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 6, keys: ['a'] },
    })
    expect(handler).toHaveBeenCalledTimes(0)
  })

  it('notifies every active handler without one cleanup stopping the others', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const first = vi.fn()
    const second = vi.fn()

    const stopFirst = obs.start(first)
    obs.start(second)
    obs.setRevision(5)

    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 6, keys: ['a'] },
    })
    expect(first).toHaveBeenCalledTimes(1)
    expect(second).toHaveBeenCalledTimes(1)

    stopFirst()
    lastSocket().deliver({
      jsonrpc: '2.0',
      method: 'settings.changed',
      params: { revision: 7, keys: ['a'] },
    })
    expect(first).toHaveBeenCalledTimes(1)
    expect(second).toHaveBeenCalledTimes(2)

    obs.stop()
  })

  it('does not call handler on reconnect after stop', async () => {
    const d = new Dispatcher(fixedEndpoint(9876))
    await connect(d)
    const obs = new SettingsObserver(d)
    const handler = vi.fn()

    obs.start(handler)
    obs.setRevision(5)
    obs.stop()

    lastSocket().drop()
    await connect(d)

    expect(handler).toHaveBeenCalledTimes(0)
  })
})
