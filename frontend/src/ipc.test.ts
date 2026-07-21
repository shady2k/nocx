import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionHandle, WSClient } from './ipc'
import { FRAME_HEADER_SIZE, FRAME_VERSION, MSG_TYPE_DATA, encodeFrame } from './frame'

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

  const opening = client.openSession(80, 24)
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

  const openingA = client.openSession(80, 24)
  const reqsAfterA = socket().requests()
  const idA = reqsAfterA[reqsAfterA.length - 1].id
  socket().deliverText({ jsonrpc: '2.0', id: idA, result: { sessionId: SID } })
  const sessionA = await openingA

  const openingB = client.openSession(80, 24)
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

    void client.openSession(132, 43)
    const [req] = socket().requests()

    expect(req.method).toBe('open')
    expect(typeof req.id).toBe('number')
    expect(req.params).toEqual({ cols: 132, rows: 43, xpixel: 0, ypixel: 0 })
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

    const opening = client.openSession(80, 24)
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

    const opening = client.openSession(80, 24)
    const id = socket().requests()[0].id
    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: 'not-a-session-id' } })

    await expect(opening).rejects.toThrow(/invalid session-id/)
  })

  it('rejects — rather than hanging forever — when the socket dies mid-open', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    const opening = client.openSession(80, 24)
    socket().serverHangsUp()

    await expect(opening).rejects.toThrow('ws closed')
  })

  it('ignores a response correlated to some other request id', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    let settled = false
    const opening = client.openSession(80, 24).finally(() => (settled = true))
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

    const opening = client.openSession(80, 24)
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
    const opening = client.openSession(80, 24)
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
    const reopening = client.openSession(80, 24)
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
