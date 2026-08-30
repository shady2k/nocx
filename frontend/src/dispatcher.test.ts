// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EndpointProvider, EndpointResult } from './endpoint'
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

const SUCCESS: EndpointResult = {
  ok: true,
  endpoint: { host: '127.0.0.1', port: 9876, token: 'token-1' },
}

class TestEndpointProvider implements EndpointProvider {
  calls = 0
  private readonly queued: (() => EndpointResult | Promise<EndpointResult>)[] = []
  resolveImpl: (() => EndpointResult | Promise<EndpointResult>) | null = null

  enqueue(result: EndpointResult): void {
    this.queued.push(() => result)
  }

  resolve(): Promise<EndpointResult> {
    this.calls++
    const result = this.resolveImpl?.() ?? this.queued.shift()?.() ?? SUCCESS
    return Promise.resolve(result)
  }
}

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static last: MockWebSocket | null = null
  static all: MockWebSocket[] = []

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
    MockWebSocket.all.push(this)
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

async function flush(): Promise<void> {
  await Promise.resolve()
}

async function connected(d: Dispatcher): Promise<void> {
  d.start()
  await flush()
  socket().serverAccepts()
  await flush()
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
  MockWebSocket.all = []
  vi.stubGlobal('WebSocket', MockWebSocket)
  resetSaturationToastDedup()
  vi.useFakeTimers()
  vi.spyOn(Math, 'random').mockReturnValue(0)
})

afterEach(() => {
  clearToasts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('dispatcher endpoint state machine', () => {
  it('asks the provider on every attempt and connects to its returned endpoint', async () => {
    const provider = new TestEndpointProvider()
    provider.enqueue({
      ok: true,
      endpoint: { host: 'first-host', port: 1001, token: 'first-token' },
    })
    provider.enqueue({
      ok: true,
      endpoint: { host: 'second-host', port: 1002, token: 'second-token' },
    })
    const d = new Dispatcher(provider)

    await connected(d)
    expect(provider.calls).toBe(1)
    expect(socket().url).toBe('ws://first-host:1001/session')

    socket().close()
    vi.advanceTimersByTime(250)
    await flush()

    expect(provider.calls).toBe(2)
    expect(socket().url).toBe('ws://second-host:1002/session')
  })

  it('publishes each state transition exactly once, including timer-fired connecting', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    const states: string[] = []
    d.onConnectionStateChange((state) => states.push(state.kind))

    d.start()
    await flush()
    expect(states).toEqual(['connecting'])
    socket().serverAccepts()
    await flush()
    expect(states).toEqual(['connecting', 'online'])

    socket().close()
    expect(states).toEqual(['connecting', 'online', 'waiting'])
    vi.advanceTimersByTime(250)
    await flush()
    expect(states).toEqual(['connecting', 'online', 'waiting', 'connecting'])
  })

  it('blocks on provider failure and retries only when retryNow is called', async () => {
    const provider = new TestEndpointProvider()
    const failure = {
      kind: 'server-binary-unusable' as const,
      message: 'The server cannot be started.',
      remedy: 'Reinstall the server and try again.',
    }
    provider.enqueue({ ok: false, failure })
    const d = new Dispatcher(provider)

    d.start()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'blocked', failure })
    expect(d.reconnectPending).toBe(false)
    expect(provider.calls).toBe(1)

    provider.enqueue(SUCCESS)
    d.retryNow()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'connecting' })
    expect(provider.calls).toBe(2)
    socket().serverAccepts()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'online' })
  })

  it('keeps retryNow single-flight while provider discovery is in flight', async () => {
    const provider = new TestEndpointProvider()
    let resolveProvider!: (result: EndpointResult) => void
    provider.resolveImpl = () =>
      new Promise<EndpointResult>((resolve) => {
        resolveProvider = resolve
      })
    const d = new Dispatcher(provider)

    d.start()
    d.retryNow()
    d.retryNow()
    await flush()
    expect(provider.calls).toBe(1)
    expect(MockWebSocket.all).toHaveLength(0)

    resolveProvider(SUCCESS)
    await flush()
    expect(MockWebSocket.all).toHaveLength(1)
  })

  it('ignores a superseded socket opening late', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    const onConnect = vi.fn()
    d.onConnect(onConnect)

    d.start()
    await flush()
    const oldSocket = socket()
    const replacement = new MockWebSocket('ws://replacement:9999/session')
    ;(d as unknown as { ws: MockWebSocket | null }).ws = replacement

    oldSocket.serverAccepts()

    expect(onConnect).not.toHaveBeenCalled()
    expect(d.connectionState).toEqual({ kind: 'connecting' })
  })

  it('removes the old connect API', () => {
    const d = new Dispatcher(new TestEndpointProvider())
    expect('connect' in d).toBe(false)
  })

  it('retries a waiting connection immediately on visibility or online events only once', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    expect(provider.calls).toBe(1)

    socket().close()
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
    await flush()

    expect(provider.calls).toBe(2)
    expect(d.connectionState).toEqual({ kind: 'connecting' })
  })

  it('does nothing for visibility or online events outside waiting', async () => {
    const onlineProvider = new TestEndpointProvider()
    const online = new Dispatcher(onlineProvider)
    await connected(online)
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
    expect(onlineProvider.calls).toBe(1)

    const blockedProvider = new TestEndpointProvider()
    blockedProvider.enqueue({
      ok: false,
      failure: { kind: 'no-server', message: 'No server.', remedy: 'Start the server.' },
    })
    const blocked = new Dispatcher(blockedProvider)
    blocked.start()
    await flush()
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
    expect(blockedProvider.calls).toBe(1)

    const connectingProvider = new TestEndpointProvider()
    connectingProvider.resolveImpl = () => new Promise<EndpointResult>(() => {})
    const connecting = new Dispatcher(connectingProvider)
    connecting.start()
    await flush()
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
    expect(connectingProvider.calls).toBe(1)
  })

  it('reaches online and stays there for a successful provider', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)

    expect(d.connectionState).toEqual({ kind: 'online' })
    vi.advanceTimersByTime(15_000)
    await flush()
    expect(d.connectionState).toEqual({ kind: 'online' })
    expect(provider.calls).toBe(1)
  })
})

describe('control-plane saturation visibility', () => {
  it('surfaces a refused request to the user without the calling surface opting in', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
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
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
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
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
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
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
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
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
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
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)

    let seen: boolean | null = null
    d.onDisconnect(() => {
      seen = d.reconnectPending
    })
    socket().close()

    expect(seen).toBe(true)
  })
})
