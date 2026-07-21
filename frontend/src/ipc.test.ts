import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WSClient } from './ipc'
import { FRAME_HEADER_SIZE, FRAME_VERSION, MSG_TYPE_DATA, encodeFrame } from './frame'

const SID = '0123456789abcdef0011223344556677'
const OTHER_SID = 'ffffffffffffffffffffffffffffffff'

// A WebSocket stand-in with a manual clock: nothing happens until the test says
// so, which is what makes the ordering invariants (AD-7) assertable.
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

  // --- test-side triggers ---------------------------------------------------
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

  // The JSON-RPC requests this socket has been asked to send, parsed.
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

// Drives the client to the state it reaches after a successful handshake plus
// an open result: connected, sessionId known, data plane usable.
async function connectedClient(): Promise<{ client: WSClient; ws: MockWebSocket }> {
  const client = new WSClient()
  const connecting = client.connect(9876)
  socket().serverAccepts()
  await connecting

  const opening = client.openSession(80, 24)
  const openID = socket().requests()[0].id
  socket().deliverText({ jsonrpc: '2.0', id: openID, result: { sessionId: SID } })
  await opening

  return { client, ws: socket() }
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
  // The nocx-2ho.2 regression: connect() returned a promise that never
  // resolved, so every caller awaiting it hung and no terminal ever appeared.
  // Nothing in tsc, eslint or `npm run build` can catch that — only this.
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

  it('leaves the session unopened — connect is not openSession', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting
    expect(client.sid).toBeNull()
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
    // AD-1: the PTY is created at this size. Never spawn-then-resize.
    expect(req.params).toEqual({ cols: 132, rows: 43, xpixel: 0, ypixel: 0 })
  })

  it('resolves with the server-assigned session-id (AD-7)', async () => {
    const { client } = await connectedClient()
    expect(client.sid).toBe(SID)
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
    expect(client.sid).toBeNull()
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
    expect(client.sid).toBeNull()
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
    expect(client.sid).toBeNull()

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
  // AD-7: the client MUST NOT send data frames before the open result arrives.
  it('drops input until the session-id is known', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    client.send('ls\n')
    expect(socket().binaryFrames()).toHaveLength(0)
  })

  it('frames input for the open session once it is', async () => {
    const { client, ws } = await connectedClient()
    client.send('ls\n')

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

  it('drops input after close', async () => {
    const { client, ws } = await connectedClient()
    client.close()
    client.send('ls\n')
    expect(ws.binaryFrames()).toHaveLength(0)
  })
})

describe('inbound data', () => {
  it('delivers the payload of a frame for this session', async () => {
    const { client, ws } = await connectedClient()
    const seen: string[] = []
    client.onData((d) => seen.push(d))

    ws.deliverBinary(encodeFrame(SID, new TextEncoder().encode('hello\r\n')))
    expect(seen).toEqual(['hello\r\n'])
  })

  it('ignores a frame addressed to another session', async () => {
    const { client, ws } = await connectedClient()
    const seen: string[] = []
    client.onData((d) => seen.push(d))

    ws.deliverBinary(encodeFrame(OTHER_SID, new TextEncoder().encode('not mine')))
    expect(seen).toEqual([])
  })

  it('ignores a malformed frame without tearing down the connection', async () => {
    const { client, ws } = await connectedClient()
    const seen: string[] = []
    client.onData((d) => seen.push(d))

    ws.deliverBinary(new Uint8Array([FRAME_VERSION, MSG_TYPE_DATA, 0x00]).buffer)
    expect(seen).toEqual([])
    expect(client.connected).toBe(true)
  })

  // nocx-ao7: a PTY read can end mid-rune, so a multi-byte character arrives
  // split across two frames. A fresh TextDecoder per frame turns the halves
  // into two U+FFFD replacement characters — the streaming decoder must carry
  // the partial rune across frames instead.
  it('reassembles a UTF-8 rune split across two frames', async () => {
    const { client, ws } = await connectedClient()
    const seen: string[] = []
    client.onData((d) => seen.push(d))

    const euro = new TextEncoder().encode('€') // e2 82 ac
    ws.deliverBinary(encodeFrame(SID, euro.slice(0, 1)))
    ws.deliverBinary(encodeFrame(SID, euro.slice(1)))

    expect(seen.join('')).toBe('€')
    expect(seen.join('')).not.toContain('�')
  })

  it('reassembles a rune split three ways, one byte per frame', async () => {
    const { client, ws } = await connectedClient()
    const seen: string[] = []
    client.onData((d) => seen.push(d))

    const emoji = new TextEncoder().encode('🚀') // f0 9f 9a 80
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

    // A truncated rune left over from a dead connection must not bleed into
    // the next one's first character.
    socket().deliverBinary(encodeFrame(SID, new TextEncoder().encode('€').slice(0, 1)))

    const reconnected = client.connect(9876)
    socket().serverAccepts()
    await reconnected
    const reopening = client.openSession(80, 24)
    const reqs = socket().requests()
    const id = reqs[reqs.length - 1]?.id
    socket().deliverText({ jsonrpc: '2.0', id, result: { sessionId: SID } })
    await reopening

    const seen: string[] = []
    client.onData((d) => seen.push(d))
    socket().deliverBinary(encodeFrame(SID, new TextEncoder().encode('ok')))

    expect(seen.join('')).toBe('ok')
  })
})

describe('exit notification', () => {
  it('fires onExit for this session', async () => {
    const { client, ws } = await connectedClient()
    const exited: string[] = []
    client.onExit((sid) => exited.push(sid))

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: SID } })
    expect(exited).toEqual([SID])
  })

  it('ignores an exit for another session', async () => {
    const { client, ws } = await connectedClient()
    const exited: string[] = []
    client.onExit((sid) => exited.push(sid))

    ws.deliverText({ jsonrpc: '2.0', method: 'exit', params: { sessionId: OTHER_SID } })
    expect(exited).toEqual([])
  })
})

describe('sendResize', () => {
  it('sends the new grid size for the open session', async () => {
    const { client, ws } = await connectedClient()
    client.sendResize(100, 30)

    const resize = ws.requests().find((r) => r.method === 'resize')
    expect(resize?.params).toEqual({ sessionId: SID, cols: 100, rows: 30, xpixel: 0, ypixel: 0 })
  })

  it('stays quiet before the session is open', async () => {
    const client = new WSClient()
    const connecting = client.connect(9876)
    socket().serverAccepts()
    await connecting

    client.sendResize(100, 30)
    expect(socket().requests()).toHaveLength(0)
  })
})

describe('close', () => {
  it('tells the server before hanging up, then forgets the session', async () => {
    const { client, ws } = await connectedClient()
    client.close()

    const closeReq = ws.requests().find((r) => r.method === 'close')
    expect(closeReq?.params).toEqual({ sessionId: SID })
    expect(ws.closeCalled).toBe(true)
    expect(client.sid).toBeNull()
    expect(client.connected).toBe(false)
  })

  it('is safe on a client that never connected', () => {
    expect(() => new WSClient().close()).not.toThrow()
  })
})
