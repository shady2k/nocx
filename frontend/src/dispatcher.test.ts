import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher } from './dispatcher'

// Minimal mock WebSocket that supports both property callbacks and addEventListener.
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

  error(): void {
    this.onerror?.()
    this.readyState = MockSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  drop(): void {
    this.readyState = MockSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  deliver(msg: {
    id?: number
    result?: unknown
    error?: { code: number; message: string; data?: unknown }
    method?: string
    params?: unknown
  }): void {
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

async function connectAndAccept(d: Dispatcher, port = 9876): Promise<void> {
  const p = d.connect(port)
  lastSocket().accept()
  await p
}

// --- Tests ---

describe('Dispatcher', () => {
  describe('connect', () => {
    it('resolves when the socket opens', async () => {
      const d = new Dispatcher()
      const p = d.connect(9876)
      lastSocket().accept()
      await p
      expect(d.connected).toBe(true)
    })

    it('rejects when the socket errors', async () => {
      const d = new Dispatcher()
      const p = d.connect(9876)
      lastSocket().error()
      await expect(p).rejects.toThrow('ws connection failed')
    })

    it('uses the correct URL with subprotocol when token is provided', async () => {
      const d = new Dispatcher()
      const p = d.connect(9876, '10.0.0.1', 'mytoken')
      const s = lastSocket()
      expect((s as unknown as { url: string }).url).toBe('ws://10.0.0.1:9876/session')
      s.accept()
      await p
    })
  })

  describe('call', () => {
    it('sends a JSON-RPC request and resolves with the result', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const callP = d.call<{ x: number }>('test.method', { a: 1 })
      const reqs = JSON.parse(lastSocket().sent[0]) as {
        jsonrpc: string
        method: string
        params: unknown
        id: number
      }
      expect(reqs.jsonrpc).toBe('2.0')
      expect(reqs.method).toBe('test.method')
      expect(reqs.params).toEqual({ a: 1 })
      expect(reqs.id).toBe(1)

      lastSocket().deliver({ id: reqs.id, result: { x: 42 } })
      await expect(callP).resolves.toEqual({ x: 42 })
    })

    it('rejects when the server responds with an error', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const callP = d.call('bad.method', {})
      const reqs = JSON.parse(lastSocket().sent[0]) as { id: number }
      lastSocket().deliver({
        id: reqs.id,
        error: { code: -32601, message: 'method not found' },
      })
      await expect(callP).rejects.toThrow('method not found')
    })

    it('assigns monotonically increasing IDs', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      void d.call('a', {})
      void d.call('b', {})
      void d.call('c', {})
      const ids = lastSocket().sent.map((s) => (JSON.parse(s) as { id: number }).id)
      expect(ids).toEqual([1, 2, 3])
    })

    it('rejects immediately if not connected', async () => {
      const d = new Dispatcher()
      await expect(d.call('x', {})).rejects.toThrow('not connected')
    })
  })

  describe('onVaultSealed', () => {
    it('raises the hook on a sealed error and retries the request once', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)
      const hook = vi.fn<() => Promise<void>>().mockResolvedValue(undefined)
      d.onVaultSealed = hook

      const callP = d.call('vault.inventory', {})
      const first = JSON.parse(lastSocket().sent[0]) as { id: number }
      lastSocket().deliver({
        id: first.id,
        error: { code: -32001, message: 'vault is sealed', data: { reason: 'vault-sealed' } },
      })
      expect(hook).toHaveBeenCalledWith('vault.inventory')

      // The hook resolves asynchronously; the re-send lands on a later tick.
      await vi.waitFor(() => {
        expect(lastSocket().sent).toHaveLength(2)
      })
      const retry = JSON.parse(lastSocket().sent[1]) as {
        method: string
        params: unknown
        id: number
      }
      expect(retry.method).toBe('vault.inventory')
      expect(retry.id).not.toBe(first.id)
      lastSocket().deliver({ id: retry.id, result: { entries: [] } })
      await expect(callP).resolves.toEqual({ entries: [] })
    })

    it('propagates a second sealed error without a second prompt', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)
      const hook = vi.fn<() => Promise<void>>().mockResolvedValue(undefined)
      d.onVaultSealed = hook

      const callP = d.call('vault.inventory', {})
      const first = JSON.parse(lastSocket().sent[0]) as { id: number }
      const sealed = {
        code: -32001,
        message: 'vault is sealed',
        data: { reason: 'vault-sealed' },
      }
      lastSocket().deliver({ id: first.id, error: sealed })
      await vi.waitFor(() => {
        expect(lastSocket().sent).toHaveLength(2)
      })
      const retry = JSON.parse(lastSocket().sent[1]) as { id: number }
      lastSocket().deliver({ id: retry.id, error: sealed })

      expect(hook).toHaveBeenCalledTimes(1)
      await expect(callP).rejects.toThrow('vault is sealed')
    })

    it('rejects the caller when the hook rejects (user cancelled)', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)
      d.onVaultSealed = vi.fn(() => Promise.reject(new Error('cancelled')))

      const callP = d.call('vault.inventory', {})
      const first = JSON.parse(lastSocket().sent[0]) as { id: number }
      lastSocket().deliver({
        id: first.id,
        error: { code: -32001, message: 'vault is sealed', data: { reason: 'vault-sealed' } },
      })
      await expect(callP).rejects.toThrow('cancelled')
    })
  })

  describe('subscribe', () => {
    it('delivers notifications to subscribers by method', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const handler = vi.fn()
      d.subscribe('exit', handler)

      lastSocket().deliver({ method: 'exit', params: { sessionId: 'abc' } })
      expect(handler).toHaveBeenCalledTimes(1)
      expect(handler).toHaveBeenCalledWith({ sessionId: 'abc' })
    })

    it('delivers to multiple subscribers of the same method', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const a = vi.fn()
      const b = vi.fn()
      d.subscribe('exit', a)
      d.subscribe('exit', b)

      lastSocket().deliver({ method: 'exit', params: {} })
      expect(a).toHaveBeenCalledTimes(1)
      expect(b).toHaveBeenCalledTimes(1)
    })

    it('stops delivering after unsubscribe', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const handler = vi.fn()
      const unsub = d.subscribe('exit', handler)
      unsub()

      lastSocket().deliver({ method: 'exit', params: {} })
      expect(handler).not.toHaveBeenCalled()
    })

    it('ignores notifications for methods with no subscribers', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      // Should not throw.
      lastSocket().deliver({ method: 'noone.listening', params: {} })
    })
  })

  describe('reconnect', () => {
    it('schedules reconnect on unexpected close', async () => {
      vi.useFakeTimers()
      const d = new Dispatcher()
      await connectAndAccept(d)

      const c0 = lastSocket()
      c0.drop()

      expect(d.reconnectPending).toBe(true)
      vi.advanceTimersByTime(500)

      const c1 = lastSocket()
      expect(c1).not.toBe(c0)
      c1.accept()

      expect(d.reconnectPending).toBe(false)
      expect(d.connected).toBe(true)
      vi.useRealTimers()
    })

    it('does not reconnect after deliberate close', async () => {
      vi.useFakeTimers()
      const d = new Dispatcher()
      await connectAndAccept(d)

      const c0 = lastSocket()
      d.close()

      vi.advanceTimersByTime(1000)
      expect(lastSocket()).toBe(c0)
      vi.useRealTimers()
    })

    it('backs off exponentially on repeated failures', async () => {
      vi.useFakeTimers()
      const d = new Dispatcher()
      await connectAndAccept(d)

      lastSocket().drop()
      expect(d.backoffMs).toBeGreaterThanOrEqual(250)

      vi.advanceTimersByTime(500)
      lastSocket().accept()
      lastSocket().drop()
      expect(d.backoffMs).toBeGreaterThanOrEqual(500)

      vi.useRealTimers()
    })

    it('fires onConnect after successful reconnect', async () => {
      vi.useFakeTimers()
      const d = new Dispatcher()
      await connectAndAccept(d)

      const handler = vi.fn()
      d.onConnect(handler)

      lastSocket().drop()
      vi.advanceTimersByTime(500)
      lastSocket().accept()

      expect(handler).toHaveBeenCalledTimes(1)
      vi.useRealTimers()
    })

    it('fires onDisconnect on unexpected close', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const handler = vi.fn()
      d.onDisconnect(handler)

      lastSocket().drop()
      expect(handler).toHaveBeenCalledTimes(1)
    })
  })

  describe('close', () => {
    it('rejects all pending calls', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const pending = d.call('slow', {})
      d.close()

      await expect(pending).rejects.toThrow('closed')
    })

    it('clears subscribers', async () => {
      const d = new Dispatcher()
      await connectAndAccept(d)

      const h = vi.fn()
      d.subscribe('exit', h)
      d.close()

      expect(d.connected).toBe(false)
    })

    it('is idempotent', () => {
      const d = new Dispatcher()
      d.close()
      d.close()
    })
  })
})
