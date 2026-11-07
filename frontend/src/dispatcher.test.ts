// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EndpointProvider, EndpointResult } from './endpoint'
import { CONNECT_ATTEMPT_TIMEOUT_MS, Dispatcher, HEARTBEAT_IDLE_WINDOW_MS } from './dispatcher'
import { log } from './log'
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

  /** The credit advertisement this socket answers transport.credit with.
   *  A well-behaved server always answers; a test that needs the pre-advert
   *  window sets answerCredit = false and calls deliverCredit later. */
  creditWindow = 8
  creditExempt = [
    'ack',
    'vault.unlockResolved',
    'connections.passwordResolved',
    'agent.runResolved',
    'host.resolved',
    'agent.laneInteractivity',
    'transport.ping',
    'rpc.cancel',
    'app.diagnostics',
  ]
  creditReserved = ['vault.unseal']
  answerCredit = true
  /** The handshake request ids, kept out of `sent` and `requests()` so the
   *  tests that inspect call traffic keep reading the calls they mean. */
  readonly creditRequests: number[] = []

  constructor(readonly url: string) {
    MockWebSocket.last = this
    MockWebSocket.all.push(this)
  }

  send(data: string | ArrayBuffer): void {
    if (typeof data === 'string') {
      const frame = JSON.parse(data) as { id?: number; method?: string }
      if (frame.method === 'transport.credit') {
        if (frame.id !== undefined) this.creditRequests.push(frame.id)
        if (this.answerCredit) this.deliverCredit()
        return
      }
    }
    this.sent.push(data)
  }

  /** Answer the (one) outstanding credit request. */
  deliverCredit(params?: { window: number; exempt: string[]; reserved: string[] }): void {
    const id = this.creditRequests[this.creditRequests.length - 1]
    if (id === undefined) throw new Error('no transport.credit request to answer')
    this.deliverText({
      jsonrpc: '2.0',
      id,
      result: params ?? {
        window: this.creditWindow,
        exempt: this.creditExempt,
        reserved: this.creditReserved,
      },
    })
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

  requests(): { id?: number; method?: string; origin?: string }[] {
    return this.sent
      .filter((m): m is string => typeof m === 'string')
      .map((m) => JSON.parse(m) as { id?: number; method?: string; origin?: string })
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

/** Connect with a named credit window: the socket must be told before it
 *  accepts, because the advertisement is the connection's first answer. */
async function connectedWith(d: Dispatcher, window: number): Promise<void> {
  d.start()
  await flush()
  socket().creditWindow = window
  socket().serverAccepts()
  await flush()
}

function deliverSaturation(id: number): void {
  socket().deliverText({ jsonrpc: '2.0', id, error: SATURATION_ERROR })
}

beforeEach(() => {
  MockWebSocket.last = null
  MockWebSocket.all = []
  vi.stubGlobal('WebSocket', MockWebSocket)
  vi.useFakeTimers()
  vi.spyOn(Math, 'random').mockReturnValue(0)
})

afterEach(() => {
  clearToasts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('the invocation origin on the wire', () => {
  // ADR-8eej3e66 §2: origin travels with the call, and the backend reads it
  // to decide the bulkhead class. Absent, the backend treats a call as
  // person, so the renderer must actually send background.
  it('sends the call origin, and person is explicit', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)

    void d.call('git.status', { bindingId: 'b1' }, undefined, { origin: 'background' })
    void d.call('git.status', { bindingId: 'b1' })
    await flush()

    const frames = socket()
      .sent.filter((m): m is string => typeof m === 'string')
      .map((m) => JSON.parse(m) as { method?: string; origin?: string })
      .filter((f) => f.method === 'git.status')
    expect(frames.map((f) => f.origin)).toEqual(['background', 'person'])
  })
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

  // nocx-n14oo.11: a failing e2e test reads its own connection's backend
  // lines by trace_id, which only works if the socket that test's page opens
  // actually carries the id the harness minted. This is the one seam that
  // proves it does, without a real backend: the endpoint the provider
  // resolves is the harness's shim (frontend/src/endpoint.ts:traceparent),
  // and the URL constructed from it is what the wire actually receives.
  it('carries traceparent on the connection URL when the endpoint supplies one', async () => {
    const provider = new TestEndpointProvider()
    provider.enqueue({
      ok: true,
      endpoint: {
        host: 'h',
        port: 1,
        token: 't',
        traceparent: '00-0123456789abcdef0123456789abcdef-0123456789abcdef-01',
      },
    })
    const d = new Dispatcher(provider)

    await connected(d)

    expect(socket().url).toBe(
      'ws://h:1/session?traceparent=00-0123456789abcdef0123456789abcdef-0123456789abcdef-01',
    )
  })

  it('omits the traceparent query parameter entirely when the endpoint has none', async () => {
    const provider = new TestEndpointProvider()
    provider.enqueue({ ok: true, endpoint: { host: 'h', port: 1, token: 't' } })
    const d = new Dispatcher(provider)

    await connected(d)

    expect(socket().url).toBe('ws://h:1/session')
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

  it('automatically retries provider no-server failures until the backend returns', async () => {
    const provider = new TestEndpointProvider()
    provider.enqueue({
      ok: false,
      failure: { kind: 'no-server', message: 'No server.', remedy: 'Start the server.' },
    })
    provider.enqueue(SUCCESS)
    const d = new Dispatcher(provider)

    d.start()
    await flush()
    expect(d.connectionState.kind).toBe('waiting')
    expect(d.reconnectPending).toBe(true)
    expect(provider.calls).toBe(1)

    vi.advanceTimersByTime(250)
    await flush()
    expect(provider.calls).toBe(2)
    expect(d.connectionState.kind).toBe('connecting')
    socket().serverAccepts()
    await flush()
    expect(d.connectionState.kind).toBe('online')
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
      failure: {
        kind: 'server-binary-unusable',
        message: 'No server binary.',
        remedy: 'Install the server.',
      },
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

  /**
   * The overlay's own `canRetry` only draws Retry for `waiting` and
   * `blocked` — never `connecting` — so a `connecting` attempt that never
   * settles (a stalled coordinator discovery, or a WebSocket that never
   * fires open/error/close) is a `connecting` with no way out: no Retry to
   * click, and nothing else moves the state. This is the losing order
   * behind connection-overlay.spec.ts:237 and :324 (nocx-oez54, nocx-61k0g):
   * an automatic background reconnect can win the race against a person's
   * deliberate Retry and leave a hung attempt behind it, and there was no
   * deadline to recover from that. A give-up deadline is the fix, matching
   * the same shape of problem already solved for the heartbeat and for the
   * ssh keepalive (nocx-y6fh7).
   */
  it('gives up a connecting attempt that never settles, so Retry can act again', async () => {
    const provider = new TestEndpointProvider()
    let hungResolve!: (result: EndpointResult) => void
    provider.resolveImpl = () =>
      new Promise<EndpointResult>((resolve) => {
        hungResolve = resolve
      })
    const d = new Dispatcher(provider)
    const states: string[] = []
    d.onConnectionStateChange((state) => states.push(state.kind))

    d.start()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'connecting' })
    expect(provider.calls).toBe(1)

    // Short of the deadline: still connecting, still no way out.
    vi.advanceTimersByTime(CONNECT_ATTEMPT_TIMEOUT_MS - 1)
    await flush()
    expect(d.connectionState).toEqual({ kind: 'connecting' })

    vi.advanceTimersByTime(1)
    await flush()
    expect(d.connectionState.kind).toBe('waiting')
    expect(d.reconnectPending).toBe(true)

    // The abandoned call resolving late must not resurrect the attempt it
    // belonged to — the same guarantee `_openSocket` already gives a
    // superseded socket's late `open`.
    hungResolve(SUCCESS)
    await flush()
    expect(d.connectionState.kind).toBe('waiting')
    expect(MockWebSocket.all).toHaveLength(0)

    // And Retry — which was hidden the whole time — now actually retries
    // instead of being swallowed as "already connecting".
    provider.resolveImpl = null
    provider.enqueue(SUCCESS)
    d.retryNow()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'connecting' })
    socket().serverAccepts()
    await flush()
    expect(d.connectionState).toEqual({ kind: 'online' })
  })

  it('gives up a socket that never fires open, error or close, and closes it', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)

    d.start()
    await flush()
    expect(MockWebSocket.all).toHaveLength(1)
    const hungSocket = socket()
    expect(hungSocket.closeCalled).toBe(false)

    vi.advanceTimersByTime(CONNECT_ATTEMPT_TIMEOUT_MS)
    await flush()
    expect(d.connectionState.kind).toBe('waiting')
    expect(hungSocket.closeCalled).toBe(true)

    // A stale open firing on the abandoned socket must not flip the state
    // back to online.
    hungSocket.serverAccepts()
    await flush()
    expect(d.connectionState.kind).toBe('waiting')
  })
})

describe('abortable RPC calls', () => {
  it('withdraws an in-flight request on the backend when its signal aborts', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)
    const abort = new AbortController()
    const pending = d.call('shell.complete', { line: 'git st' }, abort.signal)
    pending.catch(() => {})
    const request = socket().requests()[0]
    if (request.id === undefined) throw new Error('no request id')

    abort.abort()

    const cancel = socket()
      .sent.filter((message): message is string => typeof message === 'string')
      .map((message) => JSON.parse(message) as { method?: string; params?: { id?: number } })
      .find((message) => message.method === 'rpc.cancel')
    expect(cancel).toEqual({ jsonrpc: '2.0', method: 'rpc.cancel', params: { id: request.id } })
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('still withdraws the request after a vault-sealed retry gave it a new id', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    d.onVaultSealed = () => Promise.resolve()
    await connected(d)
    const abort = new AbortController()
    const pending = d.call('history.query', { scope: 'everywhere' }, abort.signal)
    pending.catch(() => {})
    const first = socket().requests()[0].id
    if (first === undefined) throw new Error('no request id')

    // The vault was sealed: the dispatcher raises the unlock and re-sends the
    // request VERBATIM under a fresh id, keeping the caller's promise alive.
    socket().deliverText({
      jsonrpc: '2.0',
      id: first,
      error: { code: -32001, message: 'sealed', data: { reason: 'vault-sealed' } },
    })
    await Promise.resolve()
    await Promise.resolve()
    const resent = socket().requests()
    const retried = resent[resent.length - 1].id
    if (retried === undefined || retried === first) throw new Error('no retry')

    // The caller's signal must still reach the backend — and name the id the
    // backend is actually working on, not the one it has already answered.
    abort.abort()

    const cancels = socket()
      .sent.filter((message): message is string => typeof message === 'string')
      .map((message) => JSON.parse(message) as { method?: string; params?: { id?: number } })
      .filter((message) => message.method === 'rpc.cancel')
    expect(cancels).toEqual([{ jsonrpc: '2.0', method: 'rpc.cancel', params: { id: retried } }])
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
  })
})

describe('dispatcher vault intent', () => {
  it('rejects a sealed report intact without unlock or replay, while an operation unlocks and retries', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    d.onVaultSealed = vi.fn().mockResolvedValue(undefined)
    await connected(d)
    const error = {
      code: -32001,
      message: 'vault.inventory: vault is sealed',
      data: { reason: 'vault-sealed' },
    }
    const report = d.call('vault.inventory', {}, undefined, { intent: 'report' })
    const operation = d.call('vault.inventory', {})
    const [reportRequest, operationRequest] = socket().requests()
    expect(reportRequest).toEqual({
      jsonrpc: '2.0',
      id: reportRequest.id,
      method: 'vault.inventory',
      params: {},
      origin: 'person',
    })
    socket().deliverText({ jsonrpc: '2.0', id: reportRequest.id, error })
    await expect(report).rejects.toMatchObject(error)
    expect(d.onVaultSealed).not.toHaveBeenCalled()
    expect(socket().requests()).toHaveLength(2)

    socket().deliverText({ jsonrpc: '2.0', id: operationRequest.id, error })
    await flush()
    expect(d.onVaultSealed).toHaveBeenCalledExactlyOnceWith('vault.inventory')
    const requests = socket().requests()
    expect(requests).toHaveLength(3)
    expect(requests[2].id).not.toBe(operationRequest.id)
    socket().deliverText({ jsonrpc: '2.0', id: requests[2].id, result: { entries: [] } })
    await expect(operation).resolves.toEqual({ entries: [] })
    d.close()
  })
})

describe('dispatcher mutations', () => {
  // ADR-8eej3e66 §4: a mutation carries an identity minted here, and the one
  // automatic re-send in the renderer — the vault unlock — is allowed only
  // when the backend's record says the first attempt did not start. A timeout
  // never implies a rollback, so it never authorises a replay.

  function frames(): { jsonrpc?: string; id?: number; method?: string; mutation?: string }[] {
    return socket()
      .sent.filter((message): message is string => typeof message === 'string')
      .map((message) => JSON.parse(message) as { method?: string; mutation?: string })
  }

  function methodFrames(method: string) {
    return frames().filter((frame) => frame.method === method)
  }

  it('carries an identity on a mutating call and none on a read', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)
    const write = d.call('settings.set', { key: 'theme', value: 'dark' })
    const read = d.call('vault.status', {})
    write.catch(() => {})
    read.catch(() => {})

    const [writeFrame, readFrame] = frames()
    expect(typeof writeFrame.mutation).toBe('string')
    expect(writeFrame.mutation).not.toBe('')
    expect(readFrame).not.toHaveProperty('mutation')
    d.close()
  })

  it('does not re-send a mutation whose record says it ran, and says which answer it is', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    const unlock = vi.fn().mockResolvedValue(undefined)
    d.onVaultSealed = unlock
    await connected(d)
    const pending = d.call('settings.set', { key: 'theme', value: 'dark' })
    pending.catch(() => {})
    const first = methodFrames('settings.set')[0]

    socket().deliverText({
      jsonrpc: '2.0',
      id: first.id,
      error: { code: -32001, message: 'vault is sealed', data: { reason: 'vault-sealed' } },
    })
    await flush()
    await flush()

    // It asks before it would re-send, and it names the same identity.
    const asks = methodFrames('operation.outcome')
    expect(asks).toHaveLength(1)
    expect((asks[0] as { params?: { mutation?: string } }).params).toEqual({
      mutation: first.mutation,
    })

    socket().deliverText({
      jsonrpc: '2.0',
      id: asks[0].id,
      result: { outcome: 'done', operation: 'settings.set' },
    })
    await expect(pending).rejects.toMatchObject({
      name: 'MutationNotReplayedError',
      message: 'It finished.',
    })
    // No second copy of the write, and no unlock prompt for work that is over.
    expect(methodFrames('settings.set')).toHaveLength(1)
    expect(unlock).not.toHaveBeenCalled()
    d.close()
  })

  it('re-sends a mutation once when the record says it did not start, under the same identity', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    d.onVaultSealed = vi.fn().mockResolvedValue(undefined)
    await connected(d)
    const pending = d.call('settings.set', { key: 'theme', value: 'dark' })
    const first = methodFrames('settings.set')[0]

    socket().deliverText({
      jsonrpc: '2.0',
      id: first.id,
      error: { code: -32001, message: 'vault is sealed', data: { reason: 'vault-sealed' } },
    })
    await flush()
    await flush()
    const ask = methodFrames('operation.outcome')[0]
    socket().deliverText({
      jsonrpc: '2.0',
      id: ask.id,
      result: { outcome: 'not-started', operation: 'settings.set' },
    })
    await flush()
    await flush()
    await flush()

    expect(d.onVaultSealed).toHaveBeenCalledExactlyOnceWith('settings.set')
    const writes = methodFrames('settings.set')
    expect(writes).toHaveLength(2)
    expect(writes[1].id).not.toBe(first.id)
    expect(writes[1].mutation).toBe(first.mutation)
    socket().deliverText({ jsonrpc: '2.0', id: writes[1].id, result: { saved: true } })
    await expect(pending).resolves.toEqual({ saved: true })
    d.close()
  })

  it('turns a replay refusal into the answer the record carries', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)
    const pending = d.call('settings.set', { key: 'theme' })
    pending.catch(() => {})
    const [write] = methodFrames('settings.set')
    socket().deliverText({
      jsonrpc: '2.0',
      id: write.id,
      error: {
        code: -32005,
        message: 'This mutation already ran',
        data: { reason: 'mutation-replayed', outcome: 'done', operation: 'settings.set' },
      },
    })
    await expect(pending).rejects.toMatchObject({
      name: 'MutationNotReplayedError',
      message: 'It finished.',
    })
    d.close()
  })

  it('a close during a withdrawal answer ask leaves no unhandled rejection', async () => {
    // THE ASK IS THE DISPATCHER'S OWN REQUEST. When the socket goes before the
    // record answers, that promise must not reject with nobody listening: the
    // caller is still settled, and vitest fails a run on an unhandled
    // rejection even when every test passed (measured: ci-frontend's 7672
    // green tests and "Error: ws closed" out of rejectAllPending).
    const seen: unknown[] = []
    const onUnhandled = (reason: unknown): void => {
      seen.push(reason)
    }
    process.on('unhandledRejection', onUnhandled)
    try {
      const d = new Dispatcher(new TestEndpointProvider())
      await connectedWith(d, 8)
      const abort = new AbortController()
      const pending = d.call('settings.set', { key: 'theme' }, abort.signal)
      pending.catch(() => {})
      await flush()
      abort.abort() // the withdrawal puts operation.outcome on the wire
      await flush()
      expect(
        socket()
          .requests()
          .filter((request) => request.method === 'operation.outcome'),
      ).toHaveLength(1)
      d.close() // the socket goes before the answer comes back
      await flush()
      await flush()
      // The caller is answered anyway, with the sentence that says nobody can
      // say — and nothing was left to reject on its own.
      await expect(pending).rejects.toMatchObject({
        name: 'AbortError',
        message: /unknown|Checking what happened/,
      })
      expect(seen).toEqual([])
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }
  })

  it('never re-sends a withdrawn mutation, and the rejection carries its answer', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 8)
    const abort = new AbortController()
    const pending = d.call('settings.set', { key: 'theme' }, abort.signal)
    pending.catch(() => {})
    await flush()
    abort.abort()
    await flush()
    // No second settings.set, whatever the backend would say: a withdrawal
    // re-sends nothing. The one ask is the ANSWER — §8's three sentences
    // reach the person through the call's own rejection, because the row that
    // used to read them out is the row that goes when the call settles.
    expect(methodFrames('settings.set')).toHaveLength(1)
    const ask = socket()
      .requests()
      .find((request) => request.method === 'operation.outcome')
    expect(ask).toBeDefined()
    socket().deliverText({
      jsonrpc: '2.0',
      id: ask!.id!,
      result: { outcome: 'outcome-unknown', operation: 'settings.set' },
    })
    await expect(pending).rejects.toMatchObject({
      name: 'AbortError',
      message: 'Checking what happened…',
    })
    expect(methodFrames('settings.set')).toHaveLength(1)
    d.close()
  })

  it('a mutation withdrawn before it was sent answers did-not-start without asking', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const filler = d.call('vault.seal', {}) // the whole window
    filler.catch(() => {})
    const abort = new AbortController()
    const queued = d.call('git.commit', { message: 'x' }, abort.signal)
    queued.catch(() => {})
    await flush()
    abort.abort()
    // Nothing reached the backend and no credit was spent, so the record has
    // never heard of this identity and must not be asked. "Did not start" is
    // known here, and it is the sentence the caller gets.
    await expect(queued).rejects.toMatchObject({
      name: 'AbortError',
      message: 'It did not start — nothing was changed.',
    })
    expect(
      socket()
        .requests()
        .filter((request) => request.method === 'operation.outcome'),
    ).toHaveLength(0)
    expect(methodFrames('git.commit')).toHaveLength(0)
    d.close()
  })
})

describe('dispatcher heartbeat', () => {
  it('pings an idle socket and stays online when the response arrives', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)

    vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS - 1)
    expect(
      socket()
        .requests()
        .filter((request) => request.method === 'transport.ping'),
    ).toHaveLength(0)

    vi.advanceTimersByTime(1)
    const ping = socket()
      .requests()
      .find((request) => request.method === 'transport.ping')
    expect(ping).toBeDefined()
    socket().deliverText({
      jsonrpc: '2.0',
      id: ping!.id,
      result: { serverTimeMs: 1756500000000 },
    })
    await flush()
    vi.advanceTimersByTime(5_000)

    expect(socket().closeCalled).toBe(false)
    expect(d.connectionState).toEqual({ kind: 'online' })
  })

  // Only what the CLIENT sends defers the ping, because only that refreshes the
  // server's read deadline. The second half of this test used to assert that an
  // INBOUND frame defers it too, which is the reading nocx-8jzbp was: it let a
  // socket that was being talked at, and never talking back, run past the
  // window and be closed.
  it('lets its own traffic defer the ping, and a server frame not defer it', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)
    const pings = () =>
      socket()
        .requests()
        .filter((request) => request.method === 'transport.ping')

    d.notify('activity', {})
    vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS - 1)
    expect(pings()).toHaveLength(0)

    socket().deliverText({ jsonrpc: '2.0', method: 'activity', params: {} })
    vi.advanceTimersByTime(1)

    expect(pings()).toHaveLength(1)
    expect(socket().closeCalled).toBe(false)
  })

  // A PENDING CALL IS WHEN THE CLIENT SENDS NOTHING, NOT WHEN IT IS BUSY
  // (nocx-8jzbp).
  //
  // The server closes a client that has sent nothing for 30s, and the deadline
  // is refreshed only by frames the CLIENT sends. The heartbeat used to stand
  // down while anything was pending — on the reading that an in-flight request
  // proves the socket alive, which is true of the connection and false of the
  // server's read deadline. So a skills.audit, which is a model reading a whole
  // skill, died at thirty seconds and took every other call on the socket with
  // it: "This skill was not read — ws closed".
  it('keeps pinging while a slow call is still pending', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)

    const slow = d.call('skills.audit', { name: 'agentmail' })
    const audit = socket()
      .requests()
      .find((request) => request.method === 'skills.audit')
    expect(audit).toBeDefined()

    // Three idle windows with the call still in flight — more than the 30s the
    // server allows — and each one answered, as the real server answers
    // transport.ping on the read loop while the control lane works.
    for (let window = 1; window <= 3; window++) {
      vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS)
      const pings = socket()
        .requests()
        .filter((request) => request.method === 'transport.ping')
      expect(pings).toHaveLength(window)
      socket().deliverText({
        jsonrpc: '2.0',
        id: pings[window - 1].id,
        result: { serverTimeMs: 1756500000000 },
      })
      await flush()
    }

    expect(socket().closeCalled).toBe(false)
    socket().deliverText({ jsonrpc: '2.0', id: audit!.id, result: { verdict: 'clear' } })
    await expect(slow).resolves.toMatchObject({ verdict: 'clear' })
  })

  // The same confusion from the other side: a frame the SERVER sent does not
  // refresh the server's own read deadline, so it cannot stand in for one the
  // client owes. A run streaming its answer for a minute sends the renderer
  // plenty and the renderer nothing.
  it('pings even while the server is streaming notifications at it', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)

    for (let tick = 0; tick < 3; tick++) {
      vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS / 2)
      socket().deliverText({ jsonrpc: '2.0', method: 'agent.delta', params: {} })
    }
    vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS / 2)

    expect(
      socket()
        .requests()
        .filter((request) => request.method === 'transport.ping').length,
    ).toBeGreaterThanOrEqual(1)
  })

  it('closes an idle socket when the heartbeat response times out through close', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)
    const disconnect = vi.fn()
    d.onDisconnect(disconnect)

    vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS)
    const ping = socket()
      .requests()
      .find((request) => request.method === 'transport.ping')
    expect(ping).toBeDefined()
    const pending = d.call('long.operation', {})
    pending.catch(() => {})

    vi.advanceTimersByTime(4_999)
    expect(socket().closeCalled).toBe(false)

    vi.advanceTimersByTime(1)
    await expect(pending).rejects.toThrow('ws closed')
    expect(socket().closeCalled).toBe(true)
    expect(disconnect).toHaveBeenCalledTimes(1)
    expect(d.connectionState.kind).toBe('waiting')
  })

  it('keeps a healthy idle socket alive across three heartbeat windows', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connected(d)

    for (let id = 1; id <= 3; id++) {
      vi.advanceTimersByTime(HEARTBEAT_IDLE_WINDOW_MS)
      const pings = socket()
        .requests()
        .filter((request) => request.method === 'transport.ping')
      const ping = pings[pings.length - 1]
      expect(ping).toBeDefined()
      if (!ping) throw new Error('heartbeat ping was not sent')
      socket().deliverText({
        jsonrpc: '2.0',
        id: ping.id,
        result: { serverTimeMs: 1756500000000 + id },
      })
      await flush()
    }

    expect(socket().closeCalled).toBe(false)
    expect(
      socket()
        .requests()
        .filter((request) => request.method === 'transport.ping'),
    ).toHaveLength(3)
  })
})

describe('control-plane refusals (ADR-8eej3e66 stage 1)', () => {
  // Stage 1 removes the refusal TOAST and nothing underneath: the backend
  // still refuses, the wire discriminator still arrives, and every surface a
  // person uses now owns the refusal it caused. What the dispatcher owes is
  // (a) a sentence that names the cause without the words "busy" or "try
  // again in a moment", (b) a record with the invocation's origin, and (c) no
  // toast on either path — the failure is shown where it happened or not at
  // all, never centrally over whatever the person is doing.
  it('rejects a person call with a sentence naming the cause, and raises no toast', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    const call = d.call('ssh.connect', {})
    const id = socket().requests()[0].id!
    socket().deliverText({
      jsonrpc: '2.0',
      id,
      error: { ...SATURATION_ERROR, data: { ...SATURATION_ERROR.data, scope: 'filesystem' } },
    })

    // The refusal still rejects the caller's promise with the full error, so
    // a surface can branch on the discriminator.
    await expect(call).rejects.toMatchObject({ code: -32004 })
    const err = (await call.catch((e: unknown) => e)) as Error
    expect(err.message).toContain('refused')
    expect(err.message).toContain('file operations')
    expect(err.message.toLowerCase()).not.toContain('busy')
    expect(err.message.toLowerCase()).not.toContain('try again in a moment')

    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(0)
  })

  it('a background refusal is quiet to the person and recorded with its origin', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    const warn = vi.spyOn(log, 'warn')
    const call = d.call('files.list', {}, undefined, { origin: 'background' })
    const id = socket().requests()[0].id!

    deliverSaturation(id)

    await expect(call).rejects.toMatchObject({ code: -32004 })
    await vi.dynamicImportSettled()
    expect(toasts()).toHaveLength(0)
    const record = warn.mock.calls.find((c) => String(c[0]).includes('control request refused'))
    expect(record).toBeDefined()
    expect(record?.[1]).toMatchObject({ origin: 'background', method: 'files.list' })
  })

  it('records a person refusal with origin person, and treats an absent origin as person', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    const warn = vi.spyOn(log, 'warn')

    const implicit = d.call('profiles.list', {})
    implicit.catch(() => {})
    deliverSaturation(socket().requests()[0].id!)
    await expect(implicit).rejects.toMatchObject({ code: -32004 })

    const explicit = d.call('profiles.list', {}, undefined, { origin: 'person' })
    explicit.catch(() => {})
    deliverSaturation(socket().requests()[1].id!)
    await expect(explicit).rejects.toMatchObject({ code: -32004 })

    const origins = warn.mock.calls
      .filter((c) => String(c[0]).includes('control request refused'))
      .map((c) => (c[1] as { origin?: string }).origin)
    expect(origins).toEqual(['person', 'person'])
  })

  it('records a refused notification instead of toasting it (control.saturated carries no id)', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    const warn = vi.spyOn(log, 'warn')

    socket().deliverText({
      jsonrpc: '2.0',
      method: 'control.saturated',
      params: { methodClass: 'ssh', scope: 'probe' },
    })
    await vi.dynamicImportSettled()

    expect(toasts()).toHaveLength(0)
    const record = warn.mock.calls.find((c) => String(c[0]).includes('control action refused'))
    expect(record).toBeDefined()
    expect(record?.[1]).toMatchObject({ methodClass: 'ssh', scope: 'probe' })
  })

  it('names a scope this build does not know without inventing a cause', async () => {
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    const call = d.call('ssh.connect', {})
    const id = socket().requests()[0].id!
    socket().deliverText({
      jsonrpc: '2.0',
      id,
      error: {
        ...SATURATION_ERROR,
        data: { ...SATURATION_ERROR.data, scope: 'exec' },
      },
    })
    const err = (await call.catch((e: unknown) => e)) as Error
    expect(err.message).toContain('refused')
    expect(err.message).not.toContain('exec')
    expect(err.message.toLowerCase()).not.toContain('busy')
  })

  it('leaves an ordinary RPC error untouched — rejects with its own message', async () => {
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

  it('an outbound stall drops the socket, because the connection cannot deliver what it owes (nocx-mrtvy)', async () => {
    // outbound.stalled is what a full refreshable queue sends through its one
    // reserved slot after dropping a frame (internal/transport/outbound:
    // "the renderer treats it as a cue to reconnect"). Until this handler it
    // treated it as nothing: the notification arrived, found no subscriber,
    // and was discarded — so every frame that episode dropped was lost for
    // good, with no resync and nothing said. Its INBOUND twin,
    // control.saturated, was handled all along.
    const provider = new TestEndpointProvider()
    const d = new Dispatcher(provider)
    await connected(d)
    let disconnects = 0
    d.onDisconnect(() => {
      disconnects++
    })
    const ws = socket()
    const inFlight = d.call('history.query', {})

    ws.deliverText({ jsonrpc: '2.0', method: 'outbound.stalled' })

    // The existing close path, deliberately: it rejects what is pending,
    // publishes waiting, schedules the retry and fires onDisconnect — the
    // same route the heartbeat's dead socket takes. Reconnecting is what
    // makes every store re-read; a handler that only logged would leave the
    // renderer confidently displaying state the backend has moved past.
    expect(ws.closeCalled).toBe(true)
    expect(disconnects).toBe(1)
    expect(d.reconnectPending).toBe(true)
    await expect(inFlight).rejects.toThrow()
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

describe('credit window (ADR-8eej3e66 §7)', () => {
  const EXEMPT = [
    'ack',
    'vault.unlockResolved',
    'connections.passwordResolved',
    'agent.runResolved',
    'host.resolved',
    'agent.laneInteractivity',
    'transport.ping',
    'rpc.cancel',
    'app.diagnostics',
  ]

  function answer(id: number, result: unknown = {}): void {
    socket().deliverText({ jsonrpc: '2.0', id, result })
  }

  it('sends nothing ordinary until the backend has advertised the window', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    d.start()
    await flush()
    const ws = socket()
    ws.answerCredit = false
    ws.serverAccepts()
    await flush()

    const call = d.call('vault.seal', {})
    call.catch(() => {})
    await flush()
    expect(ws.requests()).toHaveLength(0)

    ws.deliverCredit({ window: 1, exempt: EXEMPT, reserved: ['vault.unseal'] })
    await flush()
    expect(ws.requests().map((r) => r.method)).toEqual(['vault.seal'])
  })

  it('sends no more than the window, and the next call waits for an answer', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 2)

    const calls = [d.call('vault.seal', {}), d.call('vault.seal', {}), d.call('vault.seal', {})]
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'vault.seal'])

    answer(socket().requests()[0].id!)
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'vault.seal', 'vault.seal'])
    for (const call of calls) call.catch(() => {})
  })

  it("announces a person's queued call at once and cancels it before it is ever sent", async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const announced: number[] = []
    const settled: number[] = []
    d.onCallPending = (call) => announced.push(call.id)
    d.onCallSettled = (id) => settled.push(id)

    void d.call('vault.seal', {}) // fills the window
    const waiting = d.call('vault.seal', {}, undefined, { origin: 'person' })
    waiting.catch(() => {})
    await flush()

    // The row source hears about it the moment the person acts, not when
    // credit finally arrives.
    expect(announced).toHaveLength(2)
    expect(settled).toHaveLength(0)
    expect(socket().requests()).toHaveLength(1)

    d.cancel(announced[1])
    await expect(waiting).rejects.toMatchObject({ name: 'AbortError' })
    expect(settled).toEqual([announced[1]])
    expect(socket().requests()).toHaveLength(1)
  })

  it('a person-friendly queued call waits past the row threshold, shows Cancel, and completes when credit frees', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)

    const announced: Array<{ id: number; method: string; cancel: () => void }> = []
    const settled: Array<[number, string]> = []
    d.onCallPending = (call) => announced.push(call)
    d.onCallSettled = (id, settlement) => settled.push([id, settlement])

    void d.call('vault.seal', {}) // the whole window
    const queued = d.call('vault.status', {})
    await flush()
    expect(socket().requests()).toHaveLength(1)
    // The row exists from the moment the person acted, and it carries Cancel.
    const row = announced.find((call) => call.method === 'vault.status')
    expect(row).toBeDefined()
    expect(typeof row!.cancel).toBe('function')

    // THREE SECONDS: past the two-second row (ADR-8eej3e66 §8) and past the
    // one-second turn the catalogue used to give every call. A person's call
    // is still waiting and nothing has answered it.
    await vi.advanceTimersByTimeAsync(3000)
    expect(settled).toEqual([])
    await expect(
      Promise.race([queued.then(() => 'settled'), Promise.resolve('waiting')]),
    ).resolves.toBe('waiting')

    // The window frees: the queued call goes, and completes.
    answer(socket().requests()[0].id!, { sealed: true })
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'vault.status'])
    answer(socket().requests()[1].id!, { locked: false })
    await expect(queued).resolves.toEqual({ locked: false })
  })

  it('does not start a BACKGROUND call whose turn expires while it waits for credit', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    void d.call('vault.seal', {}) // fills the window
    // BACKGROUND KEEPS THE SHORT BOUND: a poll that never got credit is not
    // worth thirty seconds of a window, and a queue of them must still end.
    // A person's call keeps waiting — the next test holds it for three
    // seconds.
    const waiting = d.call('vault.status', {}, undefined, { origin: 'background' })
    waiting.catch(() => {})
    await flush()
    expect(socket().requests()).toHaveLength(1)

    vi.advanceTimersByTime(1000)
    await expect(waiting).rejects.toMatchObject({ name: 'CallNotStartedError' })
    expect(socket().requests()).toHaveLength(1)
  })

  it("merges calls on their key within one origin, and keeps a person's call its own", async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 8)

    const first = d.call('files.list', { path: '/tmp' }, undefined, { origin: 'background' })
    const second = d.call('files.list', { path: '/tmp' }, undefined, { origin: 'background' })
    // ORIGIN IS PART OF THE KEY (ADR-8eej3e66 §2). The two background polls
    // above are one question and share one request; a person's identical
    // action is a second invocation with its own verdict, and joining it to a
    // request already on the wire would hand the person the background
    // call's quiet refusal — the silence §2 exists to end.
    const person = d.call('files.list', { path: '/tmp' })
    const other = d.call('files.list', { path: '/var' })
    await flush()

    const requests = socket().requests()
    expect(requests.map((r) => r.method)).toEqual(['files.list', 'files.list', 'files.list'])
    expect(requests.map((r) => r.origin)).toEqual(['background', 'person', 'person'])

    const [a, b, c] = requests
    answer(a.id!, { entries: ['a'] })
    answer(b.id!, { entries: ['a'] })
    answer(c.id!, { entries: ['b'] })
    await expect(Promise.all([first, second])).resolves.toEqual([
      { entries: ['a'] },
      { entries: ['a'] },
    ])
    await expect(person).resolves.toEqual({ entries: ['a'] })
    await expect(other).resolves.toEqual({ entries: ['b'] })
  })

  it('lets the unlock and rescue traffic through a full window, and reserves vault.unseal', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)

    void d.call('vault.seal', {}) // the whole ordinary window
    void d.call('vault.unlockResolved', { id: 1 }) // exempt: the answer to the unlock
    void d.call('vault.unseal', { means: 'passphrase', secret: 'x' }) // its own reserved credit
    const secondUnseal = d.call('vault.unseal', { means: 'passphrase', secret: 'y' })
    secondUnseal.catch(() => {})
    await flush()

    // The full-window unlock: the window is full of waiting calls and both
    // the resolver and the unseal are on the wire. The second unseal waits
    // for the reserved credit to come back.
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'vault.unlockResolved', 'vault.unseal'])
  })

  it('rejects what was queued on disconnect and asks the new connection for its window', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const first = d.call('vault.seal', {})
    const queued = d.call('vault.seal', {})
    first.catch(() => {})
    queued.catch(() => {})
    await flush()
    expect(socket().requests()).toHaveLength(1)

    const old = socket()
    old.close()
    await expect(first).rejects.toThrow('ws closed')
    await expect(queued).rejects.toThrow('ws closed')
    expect(d.connectionState.kind).toBe('waiting')

    // The renderer's ordinary reconnection, and the advertisement it owes.
    vi.advanceTimersByTime(300)
    await flush()
    const fresh = socket()
    expect(fresh).not.toBe(old)
    expect(fresh.creditRequests).toHaveLength(0)
    fresh.serverAccepts()
    await flush()
    expect(fresh.creditRequests).toHaveLength(1)

    void d.call('vault.seal', {})
    await flush()
    expect(fresh.requests().map((r) => r.method)).toEqual(['vault.seal'])
  })

  it("tells the Operations source as soon as a person's call is queued, and settles it when it runs", async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const announced: { id: number; method: string; startedAt: number }[] = []
    const settled: number[] = []
    d.onCallPending = (call) =>
      announced.push({ id: call.id, method: call.method, startedAt: call.startedAt })
    d.onCallSettled = (id) => settled.push(id)

    void d.call('vault.seal', {}, undefined, { origin: 'background' }) // fills the window
    const queued = d.call('vault.status', {}) // waits for credit under its turn
    queued.catch(() => {})
    await flush()

    // The source hears about it the moment the person acts — the wait it
    // reports is exactly the wait for credit — even though nothing is sent.
    expect(announced).toHaveLength(1)
    expect(announced[0].method).toBe('vault.status')
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal'])

    // A PERSON'S CALL IS NOT SETTLED BY ITS TURN BEFORE IT CAN BE SEEN
    // (ADR-8eej3e66 §8): three seconds in, still waiting, still announced.
    await vi.advanceTimersByTimeAsync(3000)
    expect(settled).toEqual([])
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal'])

    // The window frees and it runs; the row leaves with the answer.
    answer(socket().requests()[0].id!, { sealed: true })
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'vault.status'])
    answer(socket().requests()[1].id!, { locked: false })
    await expect(queued).resolves.toEqual({ locked: false })
    expect(settled).toEqual([announced[0].id])
  })

  // THE WITHDRAWAL GIVES THE CREDIT BACK WITH THE CANCEL, AND BEFORE ANYTHING
  // SPENDS IT (ADR-8eej3e66 §7). The server releases a withdrawn request's
  // credit when it handles `rpc.cancel` on the read loop
  // (internal/transport/ws_rpc_cancel.go), and a peer that sends one past its
  // window is disconnected there and then (internal/transport/credit.go).
  // Freeing the slot here and pumping the queue first puts one request past
  // the window on the wire, so the backend hangs up on a well-behaved client
  // and every waiting call loses its answer.
  it('asks the backend to give the credit back BEFORE the call that spends it', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const announced: number[] = []
    d.onCallPending = (call) => announced.push(call.id)

    const first = d.call('agent.cancel', {})
    first.catch(() => {})
    const queued = d.call('files.close', { path: '/tmp/x' })
    queued.catch(() => {})
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['agent.cancel'])
    const wire = socket().requests()[0].id!

    d.cancel(announced[0])
    await flush()

    const frames = socket().requests()
    expect(frames.map((r) => r.method)).toEqual(['agent.cancel', 'rpc.cancel', 'files.close'])
    // The cancel names the id the backend is working on, not the call's.
    expect((frames[1] as { params?: { id?: number } }).params).toEqual({ id: wire })
    await expect(first).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('asks the backend to give the credit back BEFORE the call that spends it, for a mutation too', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const announced: number[] = []
    d.onCallPending = (call) => announced.push(call.id)

    const mutation = d.call('vault.seal', {}) // outcome-unknown: a mutation
    mutation.catch(() => {})
    const queued = d.call('vault.status', {})
    queued.catch(() => {})
    await flush()

    d.cancel(announced[0])
    await flush()

    // The first three frames, because a withdrawn mutation goes on to ask the
    // record what became of it — and that ask waits for credit like any call.
    expect(
      socket()
        .requests()
        .slice(0, 3)
        .map((r) => r.method),
    ).toEqual(['vault.seal', 'rpc.cancel', 'vault.status'])

    // The record's answer is what the caller is owed, so the withdrawal does
    // not settle the caller until it lands.
    answer(socket().requests()[2].id!, { locked: false })
    await flush()
    const ask = socket().requests()[3]
    expect(ask.method).toBe('operation.outcome')
    answer(ask.id!, { outcome: 'not-started', operation: 'vault.seal' })
    await expect(mutation).rejects.toMatchObject({ name: 'AbortError' })
  })
})

// THE TWO IDENTITIES OF ONE CALL (ADR-8eej3e66 §8). The wire id is per
// attempt and it is all the backend knows: `rpc.cancel` names it, and so does
// `operation.row` (internal/transport/ws_operation_rows.go). The id a row and
// `cancel()` name is this renderer's, and it survives a sealed retry. The two
// counters are separate, so they agree only while every call is sent in the
// order it was made — one queued call that a person's call overtakes, or one
// that expires before it is sent, and every later wire id names a different
// call.
describe('a call has two identities, and the backend only knows one', () => {
  function answer(id: number, result: unknown = {}): void {
    socket().deliverText({ jsonrpc: '2.0', id, result })
  }

  it('answers the wire id with the id its row and cancel() use, and never the other way', async () => {
    const d = new Dispatcher(new TestEndpointProvider())
    await connectedWith(d, 1)
    const announced: { id: number; method: string }[] = []
    d.onCallPending = (call) => announced.push({ id: call.id, method: call.method })

    const filler = d.call('agent.cancel', {}) // observe 1, wire 1
    filler.catch(() => {})
    // A background poll takes the second call's place and waits.
    const poll = d.call('files.close', { path: '/poll' }, undefined, { origin: 'background' })
    poll.catch(() => {})
    // A person's call is made third and goes first: it is sent as wire id 2
    // while its own id is 3, and the poll keeps id 2.
    const send = d.call('files.close', { path: '/send' })
    send.catch(() => {})
    await flush()
    // The background poll is not announced: its row is a person's question
    // (ADR-8eej3e66 §8), so only the two person calls are.
    expect(announced.map((call) => call.method)).toEqual(['agent.cancel', 'files.close'])
    answer(socket().requests()[0].id!, {})
    await flush()
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['agent.cancel', 'files.close'])

    // THE CALL ON THE WIRE IS A DIFFERENT CALL FROM THE ONE THAT HOLDS THE
    // SAME NUMBER.
    expect(d.callIDForRequest(2)).toBe(3)
    expect(d.callIDForRequest(1)).toBeNull() // answered: no live call
    expect(d.callIDForRequest('2')).toBeNull() // ids this renderer mints are numbers

    // And the withdrawal of the call on the wire reaches THAT call, not the
    // one whose own id happens to be its wire id.
    d.cancel(d.callIDForRequest(2)!)
    await flush()
    await expect(send).rejects.toMatchObject({ name: 'AbortError' })
    // The poll was not withdrawn: it is sent as the credit comes back.
    expect(
      socket()
        .requests()
        .map((r) => r.method),
    ).toEqual(['agent.cancel', 'files.close', 'rpc.cancel', 'files.close'])
    answer(socket().requests()[3].id!, {})
    await expect(poll).resolves.toEqual({})
  })
})
