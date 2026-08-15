// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher, SATURATION_TOAST_WINDOW_MS, resetSaturationToastDedup } from './dispatcher'
import { clearToasts, toasts } from './ui/toast'

/**
 * The saturation error the executor sends for a refused control request
 * (contracts/control.saturated.schema.json): code -32004, fixed reason
 * vocabulary, the admission's scope, retryable, and a retry hint.
 */
const SATURATION_ERROR = {
  code: -32004,
  message: 'Control plane busy',
  data: {
    reason: 'control-saturated',
    scope: 'exec',
    retryable: true,
    retryAfterMs: 250,
  },
}

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static last: MockWebSocket | null = null

  readyState: number = MockWebSocket.CONNECTING
  binaryType = 'blob'
  readonly sent: (string | ArrayBuffer)[] = []
  closeCalled = false

  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null

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

  private _fire(type: string, event?: unknown): void {
    for (const fn of this._listeners.get(type) ?? []) {
      fn(event)
    }
  }

  constructor(readonly url: string) {
    MockWebSocket.last = this
  }

  send(data: string | ArrayBuffer): void {
    this.sent.push(data)
  }

  close(): void {
    this.closeCalled = true
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  serverAccepts(): void {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
    this._fire('open')
  }

  deliverText(payload: unknown): void {
    const event = { data: typeof payload === 'string' ? payload : JSON.stringify(payload) }
    this.onmessage?.(event)
    this._fire('message', event)
  }

  requests(): { id?: number; method?: string }[] {
    return this.sent
      .filter((m): m is string => typeof m === 'string')
      .map((m) => JSON.parse(m) as { id?: number; method?: string })
  }
}

function socket(): MockWebSocket {
  const ws = MockWebSocket.last
  if (!ws) throw new Error('no WebSocket was constructed')
  return ws
}

async function connected(d: Dispatcher): Promise<void> {
  const connecting = d.connect(9876)
  socket().serverAccepts()
  await connecting
}

/**
 * Start an in-flight request and return its id. The promise's rejection is
 * swallowed here — the test delivers the response itself and asserts on the
 * returned promise from the caller's own `call`.
 */
function startCall(d: Dispatcher, method = 'some.method'): number {
  const call = d.call(method, {})
  call.catch(() => {})
  const sent = socket().requests()
  const id = sent[sent.length - 1].id
  if (id === undefined) throw new Error('no request id')
  return id
}

function deliverSaturation(id: number): void {
  socket().deliverText({ jsonrpc: '2.0', id, error: SATURATION_ERROR })
}

beforeEach(() => {
  MockWebSocket.last = null
  vi.stubGlobal('WebSocket', MockWebSocket)
  resetSaturationToastDedup()
  vi.useFakeTimers()
})

afterEach(() => {
  clearToasts()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('control-plane saturation visibility', () => {
  it('surfaces a refused request to the user without the calling surface opting in', async () => {
    const d = new Dispatcher()
    await connected(d)
    const call = d.call('ssh.connect', {})
    const id = socket().requests()[0].id!

    deliverSaturation(id)

    // The refusal still rejects the caller's promise with the full error...
    await expect(call).rejects.toMatchObject({ code: -32004, message: 'Control plane busy' })
    // ...and the global fallback makes it visible: one danger toast, no
    // call-site handler involved. The toast module is imported lazily (the
    // dispatcher must stay DOM-free at load), so flush that import first.
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].level).toBe('danger')
    expect(toasts()[0].message).toContain('busy')
  })

  it('deduplicates a burst of refusals into one toast', async () => {
    const d = new Dispatcher()
    await connected(d)
    const ids: number[] = []
    for (let i = 0; i < 5; i++) {
      ids.push(startCall(d))
    }
    for (const id of ids) {
      deliverSaturation(id)
    }
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)
  })

  it('raises a fresh toast once the dedup window has passed', async () => {
    const d = new Dispatcher()
    await connected(d)

    deliverSaturation(startCall(d))
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)

    // A refusal inside the window stays silent.
    deliverSaturation(startCall(d))
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)

    // After the window a new episode deserves a new toast.
    vi.advanceTimersByTime(SATURATION_TOAST_WINDOW_MS)
    deliverSaturation(startCall(d))
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(2)
  })

  it('leaves an ordinary RPC error untouched — rejects, no saturation toast', async () => {
    const d = new Dispatcher()
    await connected(d)
    const call = d.call('some.method', {})
    const id = socket().requests()[0].id!

    // A non-saturation error with data, and one without data at all.
    socket().deliverText({
      jsonrpc: '2.0',
      id,
      error: { code: -32603, message: 'internal error', data: { reason: 'something-else' } },
    })

    await expect(call).rejects.toMatchObject({ code: -32603, message: 'internal error' })
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(0)
  })

  it('raises the same toast for a refused NOTIFICATION (control.saturated has no id)', async () => {
    const d = new Dispatcher()
    await connected(d)

    // A refused notification cannot carry the -32004 error (no id to
    // answer), so the server emits the control.saturated notification
    // instead. The dispatcher subscribes to it at construction and raises
    // the same deduplicated toast.
    socket().deliverText({
      jsonrpc: '2.0',
      method: 'control.saturated',
      params: { methodClass: 'ssh', scope: 'probe' },
    })
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)
    expect(toasts()[0].level).toBe('danger')

    // Inside the dedup window a second notification stays silent — the
    // same episode, the same toast.
    socket().deliverText({
      jsonrpc: '2.0',
      method: 'control.saturated',
      params: { methodClass: 'config', scope: 'control' },
    })
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(1)
  })
})

describe('lifecycle event ordering', () => {
  it('disconnect handlers observe reconnectPending when the drop is unexpected', async () => {
    // The disconnect event fires AFTER the reconnect policy is decided, so a
    // subscriber reading reconnectPending at event time sees the state that
    // will hold — the sentence can say "reconnecting" instead of guessing
    // (nocx-gbhwh).
    const d = new Dispatcher()
    await connected(d)

    let seen: boolean | null = null
    d.onDisconnect(() => {
      seen = d.reconnectPending
    })
    socket().close()

    expect(seen).toBe(true)
  })
})
