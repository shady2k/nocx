import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { SessionHandle, WSClient } from './ipc'
import { encodeFrame, MSG_TYPE_METADATA } from './frame'
import { MockWebSocket } from './test-support/panes-fixtures'

// The renderer's live-history seam (nocx-zg3k3.10.3): the session.historyPage
// call, and the page's rows arriving on the screen plane keyed by the pageId
// the result names. The next task (the scroll-up UI) consumes exactly this.

const SID = '0123456789abcdef0011223344556677'
const PAGE_ID = '0123456789abcdef0123456789abcdef'

function socket(): MockWebSocket {
  const ws = MockWebSocket.last
  if (!ws) throw new Error('no WebSocket was constructed')
  return ws
}

function metadataFrame(payload: unknown): ArrayBuffer {
  const json = JSON.stringify(payload)
  return encodeFrame(SID, new TextEncoder().encode(json), MSG_TYPE_METADATA)
}

async function connectedSession(): Promise<{
  client: WSClient
  session: SessionHandle
  ws: MockWebSocket
}> {
  const client = new WSClient(new Dispatcher(fixedEndpoint(9876, '127.0.0.1')))
  client.start()
  await Promise.resolve()
  socket().serverAccepts()

  const opening = client.openSession({ cols: 80, rows: 24, xpixel: 0, ypixel: 0 })
  const openID = socket().requests()[0].id
  socket().deliverText({
    jsonrpc: '2.0',
    id: openID,
    result: {
      sessionId: SID,
      instanceId: 'fedcba9876543210fedcba9876543210',
      sessionEpoch: 1,
    },
  })
  const session = await opening
  return { client, session, ws: socket() }
}

describe('historyPage', () => {
  let consoleLog: MockInstance
  let consoleWarn: MockInstance

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

  it('sends the params the contract declares and resolves the typed result', async () => {
    const { session, ws } = await connectedSession()

    const pending = session.historyPage(null, 30)
    const reqs = ws.requests()
    const req = reqs[reqs.length - 1]
    expect(req.method).toBe('session.historyPage')
    expect(req.params).toEqual({
      sessionId: SID,
      before: null,
      limit: 30,
    })

    ws.deliverText({
      jsonrpc: '2.0',
      id: req.id,
      result: {
        pageId: PAGE_ID,
        start: 10,
        end: 40,
        floor: 0,
        more: true,
        durableThrough: null,
      },
    })
    await expect(pending).resolves.toEqual({
      pageId: PAGE_ID,
      start: 10,
      end: 40,
      floor: 0,
      more: true,
      durableThrough: null,
    })
  })

  it('sends a numeric cursor through unchanged', async () => {
    const { session, ws } = await connectedSession()

    void session.historyPage(17, 5).catch(() => {})
    const reqs = ws.requests()
    const req = reqs[reqs.length - 1]
    expect(req.params).toEqual({
      sessionId: SID,
      before: 17,
      limit: 5,
    })
    ws.deliverText({
      jsonrpc: '2.0',
      id: req.id,
      result: { pageId: PAGE_ID, start: 12, end: 17, floor: 0, more: true, durableThrough: null },
    })
  })

  it('delivers the rows document the result names, from the screen plane', async () => {
    const { session, ws } = await connectedSession()

    const rows: unknown[] = []
    session.onHistoryPageRows((page) => rows.push(page))

    const pending = session.historyPage(null, 30)
    const reqs = ws.requests()
    const req = reqs[reqs.length - 1]

    // The carrier document is queued BEFORE the answer that names it: one
    // socket, one FIFO. The client reads in order, so the rows are already
    // in the callback when the promise resolves.
    ws.deliverBinary(
      metadataFrame({
        pageId: PAGE_ID,
        rows: [{ text: 'L000010' }, { text: 'L000011' }],
      }),
    )
    ws.deliverText({
      jsonrpc: '2.0',
      id: req.id,
      result: {
        pageId: PAGE_ID,
        start: 10,
        end: 12,
        floor: 0,
        more: false,
        durableThrough: null,
      },
    })
    await pending

    expect(rows).toEqual([
      {
        pageId: PAGE_ID,
        rows: [{ text: 'L000010' }, { text: 'L000011' }],
      },
    ])
  })

  it('does not steal a screen frame: the frame document routes to onScreenFrame', async () => {
    const { session, ws } = await connectedSession()

    const pages: unknown[] = []
    const frames: unknown[] = []
    session.onHistoryPageRows((page) => pages.push(page))
    session.onScreenFrame((frame) => frames.push(frame))

    ws.deliverBinary(
      metadataFrame({
        revision: 7,
        geometry: { cols: 80, rows: 24, cellWidthPx: 10, cellHeightPx: 20, revision: 7 },
        cursor: { x: 0, y: 0, visible: true },
        rows: [],
      }),
    )

    expect(pages).toEqual([])
    expect(frames).toHaveLength(1)
    expect(MSG_TYPE_METADATA).toBeDefined()
  })
})
