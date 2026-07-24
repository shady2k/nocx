import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionHandle, WSClient } from './ipc'
import { FRAME_HEADER_SIZE, FRAME_VERSION, MSG_TYPE_DATA, encodeFrame } from './frame'

// Must match the un-exported constants in ipc.ts.
const ACK_INTERVAL_MS = 100
const MIN_BACKOFF_MS = 250

const SID = '0123456789abcdef0011223344556677'
const OTHER_SID = 'ffffffffffffffffffffffffffffffff'

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
  onclose: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null

  constructor(readonly url: string) {
    MockWebSocket.last = this
  }

  send(data: string | ArrayBuffer): void {
    this.sent.push(data)
  }

  close(): void {
    this.closeCalled = true
    this.readyState = MockWebSocket.CLOSED
  }

  serverAccepts(): void {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  serverFails(): void {
    this.onerror?.()
  }

  serverHangsUp(): void {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }

  deliverText(payload: unknown): void {
    this.onmessage?.({ data: typeof payload === 'string' ? payload : JSON.stringify(payload) })
  }

  deliverBinary(data: ArrayBuffer): void {
    this.onmessage?.({ data })
  }

  requests(): { id?: number; method?: string; params?: Record<string, unknown> }[] {
    return this.sent
      .filter((m): m is string => typeof m === 'string')
      .map(
        (m) => JSON.parse(m) as { id?: number; method?: string; params?: Record<string, unknown> },
      )
  }

  binaryFrames(): Uint8Array[] {
    return this.sent
      .filter((m): m is ArrayBuffer => m instanceof ArrayBuffer)
      .map((m) => new Uint8Array(m))
  }
}

function socket(): MockWebSocket {
  const ws = MockWebSocket.last
  if (!ws) throw new Error('no WebSocket was constructed')
  return ws
}

async function connectedSession(): Promise<{
  client: WSClient
  session: SessionHandle
  ws: MockWebSocket
}> {
  const client = new WSClient()
  const connecting = client.connect(9876)
  socket().serverAccepts()
  await connecting

  const opening = client.openSession(80, 24, false)
  const openID = socket().requests()[0].id
  socket().deliverText({ jsonrpc: '2.0', id: openID, result: { sessionId: SID } })
  const session = await opening

  return { client, session, ws: socket() }
}

async function twoSessions(): Promise<{
  client: WSClient
  sessionA: SessionHandle
  sessionB: SessionHandle
  ws: MockWebSocket
}> {
  const client = new WSClient()
  const connecting = client.connect(9876)
  socket().serverAccepts()
  await connecting

  const openingA = client.openSession(80, 24, false)
  const reqsAfterA = socket().requests()
  const idA = reqsAfterA[reqsAfterA.length - 1].id
  socket().deliverText({ jsonrpc: '2.0', id: idA, result: { sessionId: SID } })
  const sessionA = await openingA

  const openingB = client.openSession(80, 24, false)
  const reqsAfterB = socket().requests()
  const idB = reqsAfterB.find((r) => r.method === 'open' && r.id !== idA)!.id
  socket().deliverText({ jsonrpc: '2.0', id: idB, result: { sessionId: OTHER_SID } })
  const sessionB = await openingB

  return { client, sessionA, sessionB, ws: socket() }
}

let consoleLog: ReturnType<typeof vi.spyOn>
let consoleWarn: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  MockWebSocket.last = null
  vi.stubGlobal('WebSocket', MockWebSocket)
  consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined)
  consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
})

afterEach(() => {
  vi.unstubAllGlobals()
  consoleLog.mockRestore()
  consoleWarn.mockRestore()
})

describe('connect', () => {
  it('resolves once the handshake completes', async () => {
    const client = new WSClient()
    let resolved = false
    const connecting = client.connect(9876).then(() => (resolved = true))

    expect(resolved).toBe(false)
    socket().serverAccepts()
    await connecting

    expect(resolved).toBe(true)
    expect(client.connected).toBe(true)
  })

  it('dials the local session endpoint on the given port', async () => {
    const client = new WSClient()
    const connecting = client.connect(1234)
    expect(socket().url).toBe('ws://127.0.0.1:1234/session')
    socket().serverAccepts()
    await connecting
  })

  it('asks for arraybuffer frames, not blobs', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    expect(socket().binaryType).toBe('arraybuffer')
    socket().serverAccepts()
    await connecting
  })

  it('rejects when the socket errors', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverFails()
    await expect(connecting).rejects.toThrow('ws connection failed')
  })

  it('leaves the connection usable but with no sessions yet', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting
    expect(client.connected).toBe(true)
  })
})

describe('openSession', () => {
  it('sends a JSON-RPC open carrying the initial grid size', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    void client.openSession(132, 43, false)
    const [req] = socket().requests()

    expect(req.method).toBe('open')
    expect(typeof req.id).toBe('number')
    expect(req.params).toEqual({ cols: 132, rows: 43, xpixel: 0, ypixel: 0, enhanced: false })
  })

  it('sends enhanced:true when openSession requests enhanced input (nocx-4ff.10)', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    void client.openSession(80, 24, true)
    const [req] = socket().requests()

    expect(req.params).toEqual({ cols: 80, rows: 24, xpixel: 0, ypixel: 0, enhanced: true })
  })

  it('resolves with a SessionHandle carrying the server-assigned id (AD-7)', async () => {
    const { session } = await connectedSession()
    expect(session.sessionId).toBe(SID)
  })

  it('rejects on a JSON-RPC error response', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24, false)
    const id = socket().requests()[0].id
    socket().deliverText({
      jsonrpc: '2.0',
      id,
      error: { code: -32603, message: 'pty spawn failed' },
    })

    await expect(opening).rejects.toThrow('pty spawn failed')
  })

  it('rejects a session-id the server should never have sent', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24, false)
    const id = socket().requests()[0].id
    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: 'not-a-session-id' } })

    await expect(opening).rejects.toThrow(/invalid session-id/)
  })

  it('rejects — rather than hanging forever — when the socket dies mid-open', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24, false)
    socket().serverHangsUp()

    await expect(opening).rejects.toThrow('ws closed')
  })

  it('ignores a response correlated to some other request id', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    let settled = false
    const opening = client.openSession(80, 24, false).finally(() => (settled = true))
    const id = socket().requests()[0].id ?? 0
    socket().deliverText({ jsonrpc: '2.0', id: id + 999, result: { sessionId: SID } })
    await Promise.resolve()

    expect(settled).toBe(false)

    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: SID } })
    await opening
  })

  it('survives a control frame that is not JSON', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24, false)
    expect(() => socket().deliverText('}{ not json')).not.toThrow()

    const id = socket().requests()[0].id
    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: SID } })
    await opening
  })
})

describe('send', () => {
  it('frames input for the open session', async () => {
    const { session, ws } = await connectedSession()
    session.send('ls\n')

    const [frame] = ws.binaryFrames()
    expect(frame[0]).toBe(FRAME_VERSION)
    expect(frame[1]).toBe(MSG_TYPE_DATA)
    expect(
      Array.from(frame.slice(2, FRAME_HEADER_SIZE))
        .map((b) => b.toString(16).padStart(2, '0'))
        .join(''),
    ).toBe(SID)
    expect(new TextDecoder().decode(frame.slice(FRAME_HEADER_SIZE))).toBe('ls\n')
  })

  it('drops input after the session is closed', async () => {
    const { session, ws } = await connectedSession()
    session.close()
    session.send('ls\n')
    expect(ws.binaryFrames()).toHaveLength(0)
  })
})

describe('inbound data', () => {
  it('delivers the payload of a frame for this session', async () => {
    const { session, ws } = await connectedSession()
    const seen: string[] = []
    session.onData((d) => seen.push(d))

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('hello\r\n')))
    expect(seen).toEqual(['hello\r\n'])
  })

  it('ignores a frame addressed to another session', async () => {
    const { session, ws } = await connectedSession()
    const seen: string[] = []
    session.onData((d) => seen.push(d))

    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('not mine')))
    expect(seen).toEqual([])
  })

  it('ignores a malformed frame without tearing down the connection', async () => {
    const { session, ws, client } = await connectedSession()
    const seen: string[] = []
    session.onData((d) => seen.push(d))

    ws.deliverBinary(new Uint8Array([FRAME_VERSION, MSG_TYPE_DATA, 0x00]).buffer)
    expect(seen).toEqual([])
    expect(client.connected).toBe(true)
  })

  it('reassembles a UTF-8 rune split across two frames', async () => {
    const { session, ws } = await connectedSession()
    const seen: string[] = []
    session.onData((d) => seen.push(d))

    const euro = new TextEncoder().encode('€')
    ws.deliverBinary(encodeFrame(SID, euro.slice(0, 1)))
    ws.deliverBinary(encodeFrame(SID, euro.slice(1)))

    expect(seen.join('')).toBe('€')
    expect(seen.join('')).not.toContain('�')
  })

  it('reassembles a rune split three ways, one byte per frame', async () => {
    const { session, ws } = await connectedSession()
    const seen: string[] = []
    session.onData((d) => seen.push(d))

    const emoji = new TextEncoder().encode('🚀')
    for (const byte of emoji) {
      ws.deliverBinary(encodeFrame(SID, new Uint8Array([byte])))
    }

    expect(seen.join('')).toBe('🚀')
  })

  it('starts each connection with a clean decoder', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting
    const opening = client.openSession(80, 24, false)
    socket().deliverText({
      jsonrpc: '2.0',
      id: socket().requests()[0].id,
      result: { sessionId: SID },
    })
    await opening

    socket().deliverBinary(encodeFrame(SID, new TextEncoder().encode('€').slice(0, 1)))

    const reconnected = client.connect(9876)
    socket().serverAccepts()
    await reconnected
    const reopening = client.openSession(80, 24, false)
    const reqs = socket().requests()
    const id = reqs[reqs.length - 1]?.id
    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: SID } })
    const second = await reopening

    const seen: string[] = []
    second.onData((d) => seen.push(d))
    socket().deliverBinary(encodeFrame(SID, new TextEncoder().encode('ok')))

    expect(seen.join('')).toBe('ok')
  })
})

describe('exit notification', () => {
  it('fires onExit for this session', async () => {
    const { session, ws } = await connectedSession()
    const exited: string[] = []
    session.onExit((sid) => exited.push(sid))

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: SID } })
    expect(exited).toEqual([SID])
  })

  it('ignores an exit for another session', async () => {
    const { session, ws } = await connectedSession()
    const exited: string[] = []
    session.onExit((sid) => exited.push(sid))

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: OTHER_SID } })
    expect(exited).toEqual([])
  })

  it('removes the session so no further input is framed after exit', async () => {
    const { session, ws } = await connectedSession()
    session.onExit(() => {})

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: SID } })

    session.send('echo leak\n')
    expect(ws.binaryFrames()).toHaveLength(0)
  })
})

describe('sendResize', () => {
  it('sends the new grid size for the open session', async () => {
    const { session, ws } = await connectedSession()
    session.sendResize(100, 30)

    const resize = ws.requests().find((r) => r.method === 'resize')
    expect(resize?.params).toEqual({ sessionId: SID, cols: 100, rows: 30, xpixel: 0, ypixel: 0 })
  })
})

describe('close', () => {
  it('tells the server about the session, then forgets it', async () => {
    const { session, ws } = await connectedSession()
    session.close()

    const closeReq = ws.requests().find((r) => r.method === 'close')
    expect(closeReq?.params).toEqual({ sessionId: SID })
  })

  it('closes the WebSocket connection and clears state', async () => {
    const { client, ws } = await connectedSession()
    client.close()

    expect(ws.closeCalled).toBe(true)
    expect(client.connected).toBe(false)
  })

  it('is safe on a client that never connected', () => {
    expect(() => new WSClient().close()).not.toThrow()
  })
})

describe('multi-session', () => {
  it('assigns distinct session-ids for two opens on one connection', async () => {
    const { sessionA, sessionB } = await twoSessions()
    expect(sessionA.sessionId).toBe(SID)
    expect(sessionB.sessionId).toBe(OTHER_SID)
    expect(sessionA.sessionId).not.toBe(sessionB.sessionId)
  })

  it('routes a data frame for session A only to callback A', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    const seenA: string[] = []
    const seenB: string[] = []
    sessionA.onData((d) => seenA.push(d))
    sessionB.onData((d) => seenB.push(d))

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('hello from A\r\n')))
    expect(seenA).toEqual(['hello from A\r\n'])
    expect(seenB).toEqual([])
  })

  it('routes a data frame for session B only to callback B', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    const seenA: string[] = []
    const seenB: string[] = []
    sessionA.onData((d) => seenA.push(d))
    sessionB.onData((d) => seenB.push(d))

    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('hello from B\r\n')))
    expect(seenA).toEqual([])
    expect(seenB).toEqual(['hello from B\r\n'])
  })

  it('reassembles a UTF-8 rune split across two frames of A when a B frame is interleaved between the halves', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    const seenA: string[] = []
    const seenB: string[] = []
    sessionA.onData((d) => seenA.push(d))
    sessionB.onData((d) => seenB.push(d))

    const euro = new TextEncoder().encode('€')
    ws.deliverBinary(encodeFrame(SID, euro.slice(0, 1)))
    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('X')))
    ws.deliverBinary(encodeFrame(SID, euro.slice(1)))

    expect(seenA.join('')).toBe('€')
    expect(seenA.join('')).not.toContain('�')
    expect(seenB.join('')).toBe('X')
  })

  it('closing session A leaves session B usable', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    sessionA.close()

    const seenB: string[] = []
    sessionB.onData((d) => seenB.push(d))

    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('still alive\r\n')))
    expect(seenB).toEqual(['still alive\r\n'])

    const seenA: string[] = []
    sessionA.onData((d) => seenA.push(d))
    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('should not arrive')))
    expect(seenA).toEqual([])
  })

  it('fires onExit only for the session specified in the notification', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    const exitedA: string[] = []
    const exitedB: string[] = []
    sessionA.onExit((sid) => exitedA.push(sid))
    sessionB.onExit((sid) => exitedB.push(sid))

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: SID } })
    expect(exitedA).toEqual([SID])
    expect(exitedB).toEqual([])
  })

  it('after exit of A, input for A is not framed but B is unaffected', async () => {
    const { sessionA, sessionB, ws } = await twoSessions()
    sessionA.onExit(() => {})

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: SID } })

    sessionA.send('echo should not send\n')
    expect(ws.binaryFrames()).toHaveLength(0)

    const seenB: string[] = []
    sessionB.onData((d) => seenB.push(d))
    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('B alive\n')))
    expect(seenB).toEqual(['B alive\n'])
  })
})

// --- offset tracking (AD-9 reconnect client contract) ----------------------

describe('offset tracking', () => {
  it('advances per-session offset by payload byteLength, not decoded string length', async () => {
    const { client, session, ws } = await connectedSession()

    // Expose offset for assertion: read via internal map cast.
    const state = (client as unknown as { sessions: Map<string, { offset: number }> }).sessions
    expect(state.get(session.sessionId)?.offset).toBe(0)

    // Multi-byte runes: '€' is 3 bytes in UTF-8.
    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('€')))
    expect(state.get(session.sessionId)?.offset).toBe(3)

    // ASCII: 1 byte per character.
    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('abc')))
    expect(state.get(session.sessionId)?.offset).toBe(6)
  })

  it('tracks offset independently across sessions', async () => {
    const { client, sessionA, sessionB, ws } = await twoSessions()
    const state = (client as unknown as { sessions: Map<string, { offset: number }> })
      .sessions as unknown as Map<string, { offset: number }>

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('aaaa')))
    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('bb')))

    expect(state.get(sessionA.sessionId)?.offset).toBe(4)
    expect(state.get(sessionB.sessionId)?.offset).toBe(2)
  })
})

// --- ack throttling --------------------------------------------------------

describe('ack throttling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('sends an ack notification with the correct offset after the throttle interval', async () => {
    const { session, ws } = await connectedSession()

    // Deliver data to trigger ack scheduling.
    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('hello')))

    // No ack yet — timer is pending.
    const before = ws.requests().filter((r) => r.method === 'ack')
    expect(before).toHaveLength(0)

    // Advance past the throttle interval.
    vi.advanceTimersByTime(ACK_INTERVAL_MS)

    const acks = ws.requests().filter((r) => r.method === 'ack')
    expect(acks).toHaveLength(1)
    expect(acks[0].params).toEqual({ sessionId: session.sessionId, offset: 5 })
  })

  it('throttles multiple frames into a single ack', async () => {
    const { session, ws } = await connectedSession()

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('abc')))
    // Only 50ms passed — timer not yet fired.
    vi.advanceTimersByTime(50)
    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('def')))
    vi.advanceTimersByTime(50) // total 100ms — timer fires

    const acks = ws.requests().filter((r) => r.method === 'ack')
    expect(acks).toHaveLength(1)
    // Offset should be the total of both frames.
    expect(acks[0].params).toEqual({ sessionId: session.sessionId, offset: 6 })
  })

  it('does not send an ack when the connection is not open', async () => {
    const { ws } = await connectedSession()

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('data')))

    // Close the WS before the ack timer fires.
    ws.readyState = MockWebSocket.CLOSED

    vi.advanceTimersByTime(ACK_INTERVAL_MS)

    // No ack should have been sent (ws.readyState !== OPEN).
    const acks = ws.requests().filter((r) => r.method === 'ack')
    expect(acks).toHaveLength(0)
  })
})

// --- reconnect and reattach ------------------------------------------------

describe('reconnect and reattach', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  async function connectedSessionWithBackoff(client: WSClient): Promise<{
    session: SessionHandle
    firstWS: MockWebSocket
  }> {
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24, false)
    const openID = socket().requests()[0].id
    socket().deliverText({ jsonrpc: '2.0', id: openID, result: { sessionId: SID } })
    const session = await opening
    return { session, firstWS: socket() }
  }

  it('retries with exponential backoff on unexpected socket drop', async () => {
    const client = new WSClient()
    const { firstWS } = await connectedSessionWithBackoff(client)

    // Connection drops unexpectedly.
    firstWS.serverHangsUp()
    expect(client.reconnectPending).toBe(true)

    // Advance just past the first backoff window. 475ms > 250+125(max jitter).
    vi.advanceTimersByTime(475)

    // The reconnect attempt creates a new WebSocket. Accept it.
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()

    // Flush microtasks so _tryReconnect's continuation resets backoff.
    await Promise.resolve()

    // After a successful reconnect, backoff resets to initial value.
    expect(client.backoffMs).toBe(MIN_BACKOFF_MS)
    expect(client.connected).toBe(true)
  })

  it('stops retrying when close() is called deliberately', async () => {
    const client = new WSClient()
    const { firstWS } = await connectedSessionWithBackoff(client)

    firstWS.serverHangsUp()
    expect(client.reconnectPending).toBe(true)

    // Deliberate close must cancel the pending reconnect.
    client.close()
    expect(client.reconnectPending).toBe(false)

    // Advance well past any backoff — no reconnect should fire.
    vi.advanceTimersByTime(10000)
    // The original instance should still be the last (no new WS created).
    expect(MockWebSocket.last).toBe(firstWS)
  })

  it('sends attach for each known session after reconnect', async () => {
    const client = new WSClient()
    const { firstWS } = await connectedSessionWithBackoff(client)

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)

    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)
    expect(attaches[0].params).toEqual({ sessionId: SID, offset: 0 })
  })

  it('sends attach with the last received byte offset', async () => {
    const client = new WSClient()
    const { firstWS } = await connectedSessionWithBackoff(client)

    firstWS.deliverBinary(encodeFrame(SID, new TextEncoder().encode('12345')))
    const state = (client as unknown as { sessions: Map<string, { offset: number }> })
      .sessions as unknown as Map<string, { offset: number }>
    expect(state.get(SID)?.offset).toBe(5)

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)
    expect(attaches[0].params).toEqual({ sessionId: SID, offset: 5 })
  })

  it('continues without resetting on resumed response', async () => {
    const client = new WSClient()
    const { session, firstWS } = await connectedSessionWithBackoff(client)

    let resetFired = false
    session.onReset(() => {
      resetFired = true
    })

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)
    reconnectedWS.deliverText({
      jsonrpc: '2.0',
      id: attaches[0].id,
      result: { resumed: true, from: 5 },
    })

    expect(resetFired).toBe(false)
  })

  it('fires reset callback and renames decoder on reset response', async () => {
    const client = new WSClient()
    const { session, firstWS } = await connectedSessionWithBackoff(client)

    let resetFired = false
    session.onReset(() => {
      resetFired = true
    })

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)
    reconnectedWS.deliverText({
      jsonrpc: '2.0',
      id: attaches[0].id,
      result: { reset: true, from: 100 },
    })
    await Promise.resolve()

    expect(resetFired).toBe(true)
  })

  // DEFECT 1 regression: recreating the decoder on reset prevents stale
  // mid-rune bytes from splicing onto the resynced stream (nocx-ao7).
  it('recreates the per-session decoder on reset so stale mid-rune bytes are discarded', async () => {
    const client = new WSClient()
    const { session, firstWS } = await connectedSessionWithBackoff(client)

    // Feed the session a frame that ends mid-rune: the first byte of '€'
    // (UTF-8 E2 82 AC). The decoder now holds 0xE2, waiting for
    // continuation bytes.
    const euro = new TextEncoder().encode('€')
    firstWS.deliverBinary(encodeFrame(SID, euro.slice(0, 1))) // just E2
    expect(
      (client as unknown as { sessions: Map<string, { offset: number }> }).sessions.get(SID)
        ?.offset,
    ).toBe(1)

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    reconnectedWS.deliverText({
      jsonrpc: '2.0',
      id: attaches[0].id,
      result: { reset: true, from: 0 },
    })
    await Promise.resolve()

    // After reset, deliver 'ñ' (UTF-8 C3 B1). If the stale E2 leaked,
    // the decoder would try E2 C3 → invalid → U+FFFD, then B1 (lonely
    // continuation) → U+FFFD. A clean decoder produces just 'ñ'.
    const seen: string[] = []
    session.onData((d) => seen.push(d))
    const nTilde = new TextEncoder().encode('ñ')
    reconnectedWS.deliverBinary(encodeFrame(SID, nTilde))
    await Promise.resolve()

    expect(seen.join('')).toBe('ñ')
    expect(seen.join('')).not.toContain('\uFFFD')
  })

  it('updates offset from reset response', async () => {
    const client = new WSClient()
    const { firstWS } = await connectedSessionWithBackoff(client)

    const state = (client as unknown as { sessions: Map<string, { offset: number }> })
      .sessions as unknown as Map<string, { offset: number }>

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)
    reconnectedWS.deliverText({
      jsonrpc: '2.0',
      id: attaches[0].id,
      result: { reset: true, from: 99 },
    })
    await Promise.resolve()

    expect(state.get(SID)?.offset).toBe(99)
  })

  it('drops session locally on attach error', async () => {
    const client = new WSClient()
    const { session, firstWS } = await connectedSessionWithBackoff(client)

    const exited: string[] = []
    session.onExit((sid) => exited.push(sid))

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()

    // Flush _tryReconnect's microtask continuation (sends attaches).
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(1)

    // Deliver the error response. handleControlMessage calls reject()
    // which schedules the Promise rejection chain as microtasks:
    //   P1 reject → P2 (then) reject → P3 (catch) handler
    // Each step is one microtask tick.
    reconnectedWS.deliverText({
      jsonrpc: '2.0',
      id: attaches[0].id,
      error: { code: -32602, message: 'unknown sessionId' },
    })

    await Promise.resolve() // P1 rejection → P2 rejection
    await Promise.resolve() // P2 rejection → catch handler fires
    expect(exited).toEqual([SID])
  })

  it('sends attach for multiple sessions on reconnect', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const openingA = client.openSession(80, 24, false)
    const openIdA = socket()
      .requests()
      .find((r) => r.method === 'open')!.id!
    socket().deliverText({ jsonrpc: '2.0', id: openIdA, result: { sessionId: SID } })
    await openingA

    const openingB = client.openSession(80, 24, false)
    const reqsAfterB = socket().requests()
    const openIdB = reqsAfterB.find((r) => r.method === 'open' && r.id !== openIdA)!.id!
    socket().deliverText({ jsonrpc: '2.0', id: openIdB, result: { sessionId: OTHER_SID } })
    await openingB

    const firstWS = socket()
    firstWS.deliverBinary(encodeFrame(SID, new TextEncoder().encode('AAAA')))
    firstWS.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('BB')))

    firstWS.serverHangsUp()
    vi.advanceTimersByTime(475)
    const reconnectedWS = socket()
    reconnectedWS.serverAccepts()
    await Promise.resolve()

    const attaches = reconnectedWS.requests().filter((r) => r.method === 'attach')
    expect(attaches).toHaveLength(2)

    const offsets = new Map(attaches.map((a) => [a.params?.sessionId, a.params?.offset]))
    expect(offsets.get(SID)).toBe(4)
    expect(offsets.get(OTHER_SID)).toBe(2)
  })
})
