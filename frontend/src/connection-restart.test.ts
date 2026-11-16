import { afterEach, expect, it, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { WSClient } from './ipc'
import { MockWebSocket } from './test-support/panes-fixtures'
import type { Open } from './generated/open'
import type { AttachResult } from './generated/attach'
import type { SessionsLiveResult } from './generated/sessions.live'
import type { Exit } from './generated/exit'

const SESSION_ID = '0123456789abcdef0011223344556677'
const OLD_IDENTITY = { instanceId: 'fedcba9876543210fedcba9876543210', sessionEpoch: 7 }
const LIVE_IDENTITY = { instanceId: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', sessionEpoch: 2 }
let client: WSClient | undefined

afterEach(() => {
  client?.close()
  vi.unstubAllGlobals()
})

async function reconnectingSession(interruptedBeforeRestart = false) {
  MockWebSocket.last = null
  vi.stubGlobal('WebSocket', MockWebSocket)
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  client = new WSClient(dispatcher)
  client.start()
  await vi.waitFor(() => expect(MockWebSocket.last).not.toBeNull())
  const first = MockWebSocket.last!
  first.serverAccepts()
  const opening = client.openSession({ cols: 80, rows: 24, xpixel: 0, ypixel: 0 })
  await vi.waitFor(() => expect(first.requests().find((r) => r.method === 'open')).toBeDefined())
  const open = first.requests().find((r) => r.method === 'open')!
  first.deliverText({
    jsonrpc: '2.0',
    id: open.id,
    result: {
      sessionId: SESSION_ID,
      ...OLD_IDENTITY,
      accessEpoch: 3,
      workspaceId: 'workspace-restart',
      cwd: '/home/test',
      integrationMode: 'raw',
      effectiveSize: { cols: 80, rows: 24, xpixel: 0, ypixel: 0 },
      parent: null,
      awaitsIntegration: false,
    } satisfies Open,
  })
  const session = await opening
  const exited = vi.fn()
  const attachment = vi.fn()
  session.onExit(exited)
  session.onAttachment(attachment)
  if (interruptedBeforeRestart) {
    first.deliverText({
      jsonrpc: '2.0',
      method: 'exit',
      params: {
        sessionId: SESSION_ID,
        ...OLD_IDENTITY,
        cause: 'interrupted',
      } satisfies Exit,
    })
    await vi.waitFor(() =>
      expect(exited).toHaveBeenCalledExactlyOnceWith({
        sessionId: SESSION_ID,
        ...OLD_IDENTITY,
        cause: 'interrupted',
      }),
    )
  }
  first.serverHangsUp()
  dispatcher.retryNow()
  await vi.waitFor(() => expect(MockWebSocket.last).not.toBe(first))
  const next = MockWebSocket.last!
  next.serverAccepts()
  await vi.waitFor(() => expect(next.requests().find((r) => r.method === 'attach')).toBeDefined())
  const attach = next.requests().find((r) => r.method === 'attach')!
  expect(attach.params).toMatchObject({ sessionId: SESSION_ID, ...OLD_IDENTITY })
  return { session, next, attach, exited, attachment }
}

it('delivers the backend restart verdict as an exit for the exact old session identity', async () => {
  const { next, attach, exited } = await reconnectingSession()
  next.deliverText({
    jsonrpc: '2.0',
    id: attach.id,
    error: {
      code: -32602,
      message: 'the old shell ended when its backend restarted',
      data: { reason: 'session-ended-after-restart' },
    },
  })
  await vi.waitFor(() =>
    expect(exited).toHaveBeenCalledExactlyOnceWith({
      sessionId: SESSION_ID,
      ...OLD_IDENTITY,
      cause: 'backend-restarted',
    }),
  )
  expect(next.requests().filter((r) => r.method === 'sessions.live')).toHaveLength(0)
})

it('reclaims a live session under its new backend identity without delivering an exit', async () => {
  const { next, attach, exited, attachment } = await reconnectingSession()
  next.deliverText({
    jsonrpc: '2.0',
    id: attach.id,
    error: {
      code: -32602,
      message: 'the binding belongs to the previous backend',
      data: { reason: 'foreign-instance' },
    },
  })
  await vi.waitFor(() =>
    expect(next.requests().find((r) => r.method === 'sessions.live')).toBeDefined(),
  )
  const inventory = next.requests().find((r) => r.method === 'sessions.live')!
  next.deliverText({
    jsonrpc: '2.0',
    id: inventory.id,
    result: {
      sessions: [
        {
          sessionId: SESSION_ID,
          ...LIVE_IDENTITY,
          paneId: 'pane-live',
          replayFrom: 512,
          attached: false,
        },
      ],
    } satisfies SessionsLiveResult,
  })
  await vi.waitFor(() =>
    expect(next.requests().filter((r) => r.method === 'attach')).toHaveLength(2),
  )
  const reclaim = next.requests().filter((r) => r.method === 'attach')[1]
  expect(reclaim.params).toEqual({
    sessionId: SESSION_ID,
    ...LIVE_IDENTITY,
    offset: 512,
    cols: 80,
    rows: 24,
    xpixel: 0,
    ypixel: 0,
  })
  next.deliverText({
    jsonrpc: '2.0',
    id: reclaim.id,
    result: {
      resumed: false,
      reset: true,
      from: 512,
      accessEpoch: 4,
      awaitsIntegration: false,
    } satisfies AttachResult,
  })
  await vi.waitFor(() => expect(attachment).toHaveBeenLastCalledWith({ kind: 'attached' }))
  expect(exited).not.toHaveBeenCalled()
})

it('keeps an interrupted shell identity until the restarted backend confirms it ended', async () => {
  const { next, attach, exited } = await reconnectingSession(true)
  next.deliverText({
    jsonrpc: '2.0',
    id: attach.id,
    error: {
      code: -32602,
      message: 'the old shell did not survive the backend restart',
      data: { reason: 'session-ended-after-restart' },
    },
  })
  await vi.waitFor(() => expect(exited).toHaveBeenCalledTimes(2))
  expect(exited).toHaveBeenNthCalledWith(1, {
    sessionId: SESSION_ID,
    ...OLD_IDENTITY,
    cause: 'interrupted',
  })
  expect(exited).toHaveBeenNthCalledWith(2, {
    sessionId: SESSION_ID,
    ...OLD_IDENTITY,
    cause: 'backend-restarted',
  })
  expect(next.requests().filter((r) => r.method === 'sessions.live')).toHaveLength(0)
})
