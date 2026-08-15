// @vitest-environment jsdom
// Connection notice — the transport's condition, stated where a person is
// already looking (nocx-gbhwh). These tests drive the REAL dispatcher and
import {
  connectionNoticeStateForDisconnect,
  mountConnectionNotice,
  type ConnectionNoticeController,
} from './connection-notice'
// and the session reattach outcomes are the product's events, not a stub's.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { WSClient } from './ipc'

const SID = '0123456789abcdef0011223344556677'

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

  serverHangsUp(): void {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  deliverText(payload: unknown): void {
    const event = { data: typeof payload === 'string' ? payload : JSON.stringify(payload) }
    this.onmessage?.(event)
    this._fire('message', event)
  }

  requests(): { id?: number; method?: string; params?: Record<string, unknown> }[] {
    return this.sent
      .filter((m): m is string => typeof m === 'string')
      .map(
        (m) => JSON.parse(m) as { id?: number; method?: string; params?: Record<string, unknown> },
      )
  }
}

function socket(): MockWebSocket {
  const ws = MockWebSocket.last
  if (!ws) throw new Error('no WebSocket was constructed')
  return ws
}

/** Drain the microtask queue — the attach/aggregate/Solid-effect chains
 *  settle there. Ten turns covers the longest chain (attach resolve → then →
 *  Promise.all → aggregate handler → Solid effect). */
async function flush(): Promise<void> {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve()
  }
}
function mount(): {
  bar: HTMLElement
  client: WSClient
  controller: ConnectionNoticeController
} {
  const bar = document.createElement('div')
  document.body.append(bar)
  const dispatcher = new Dispatcher()
  const client = new WSClient(dispatcher)
  const controller = mountConnectionNotice(bar, dispatcher, client)
  return { bar, client, controller }
}

async function connect(client: WSClient): Promise<void> {
  const connecting = client.connect(9876)
  socket().serverAccepts()
  await connecting
}

async function openSession(client: WSClient): Promise<void> {
  const opening = client.openSession(80, 24)
  const openID = socket().requests()[0].id
  socket().deliverText({ jsonrpc: '2.0', id: openID, result: { sessionId: SID } })
  await opening
}

/** Drop the socket and let the backoff timer elapse so a reconnect attempt
 *  constructs a new WebSocket; returns it, accepted. */
function reconnectThroughBackoff(): MockWebSocket {
  socket().serverHangsUp()
  // 475 ms > 250 ms base + 125 ms max jitter — the first backoff fires.
  vi.advanceTimersByTime(475)
  const ws = socket()
  ws.serverAccepts()
  return ws
}

function attachID(ws: MockWebSocket): number {
  const req = ws.requests().find((r) => r.method === 'attach')
  if (req?.id === undefined) throw new Error('no attach request sent')
  return req.id
}

beforeEach(() => {
  MockWebSocket.last = null
  vi.stubGlobal('WebSocket', MockWebSocket)
  vi.useFakeTimers()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  document.body.replaceChildren()
})

describe('connection notice', () => {
  it('is hidden while the connection is healthy', async () => {
    const { bar, client } = mount()
    await connect(client)
    expect(bar.textContent ?? '').toBe('')
  })

  it('says the connection was lost and is reconnecting while a reconnect is pending', async () => {
    const { bar, client } = mount()
    await connect(client)

    socket().serverHangsUp()

    // The drop schedules a reconnect (backoff >= 250 ms); the product says
    // the attempt is coming, not merely that the socket is gone.
    expect(bar.textContent).toContain('Connection lost')
    expect(bar.textContent).toContain('reconnecting')
  })

  it('maps both disconnect inputs through the production decision', () => {
    // The no-pending input has no live path: an unexpected drop always
    // schedules a retry before the event fires, and a deliberate close()
    // clears the subscribers (production never calls close()). So the
    // decision is asserted for BOTH inputs against the production function
    // the wiring calls, and the sentence is rendered through the same
    // controller transition the event applies — never a fabricated state.
    expect(connectionNoticeStateForDisconnect(true)).toEqual({ kind: 'reconnecting' })
    expect(connectionNoticeStateForDisconnect(false)).toEqual({ kind: 'gone' })
  })

  it('says the connection was lost without a reconnect when none is scheduled', () => {
    const { bar, controller } = mount()
    controller.setState(connectionNoticeStateForDisconnect(false))
    expect(bar.textContent).toContain('Connection lost')
    expect(bar.textContent).not.toContain('reconnecting')
  })

  it('states that the session resumed after a successful reconnect', async () => {
    const { bar, client } = mount()
    await connect(client)
    await openSession(client)

    const ws = reconnectThroughBackoff()
    await flush()
    ws.deliverText({ jsonrpc: '2.0', id: attachID(ws), result: { reset: false } })
    await flush()

    expect(bar.textContent).toContain('Connection restored')
    expect(bar.textContent).toContain('session resumed')
  })

  it('states that the session was lost on reconnect when reattach fails', async () => {
    const { bar, client } = mount()
    await connect(client)
    await openSession(client)

    const ws = reconnectThroughBackoff()
    await flush()
    ws.deliverText({
      jsonrpc: '2.0',
      id: attachID(ws),
      error: { code: -32602, message: 'unknown session' },
    })
    await flush()

    expect(bar.textContent).toContain('Connection restored')
    expect(bar.textContent).toContain('session lost on reconnect')
  })

  it('states the connection was restored with no session claims when there were none', async () => {
    const { bar, client } = mount()
    await connect(client)

    reconnectThroughBackoff()
    await flush()

    expect(bar.textContent).toContain('Connection restored')
    expect(bar.textContent).not.toContain('session')
  })
})
