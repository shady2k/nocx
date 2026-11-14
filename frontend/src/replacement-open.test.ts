// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { ClipboardGate } from './clipboard'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { WSClient } from './ipc'
import { Pane } from './panes'
import { SURFACE_TERMINAL } from './pane-content'
import { TerminalContent } from './terminal-content'
import {
  anchoredPane,
  makeBanner,
  makeClipboard,
  MockWebSocket,
} from './test-support/panes-fixtures'
import { encodeFrame, MSG_TYPE_METADATA } from './frame'
import type { SessionFrame } from './generated/session.frame'
import type { Open } from './generated/pane.opened'

let client: WSClient | undefined
let content: TerminalContent | undefined
afterEach(() => {
  content?.dispose()
  client?.close()
  vi.unstubAllGlobals()
  document.body.replaceChildren()
})

it('binds replacement open facts and the immediately following frame through the real socket', async () => {
  vi.stubGlobal('WebSocket', MockWebSocket)
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  client = new WSClient(dispatcher)
  client.start()
  await vi.waitFor(() => expect(MockWebSocket.last).not.toBeNull())
  const ws = MockWebSocket.last!
  ws.serverAccepts()
  content = new TerminalContent(
    client,
    anchoredPane('pane-wire'),
    makeClipboard(),
    new ClipboardGate(),
    makeBanner(),
    null,
    () => {},
    { profileId: 'p1', host: 'h' },
  )
  const tab = new Pane(
    content,
    {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      defaultTitle: 'Terminal',
    },
    1,
    'tab-wire',
  )
  document.body.append(tab.pane)
  const starting = tab.start()
  await vi.waitFor(() => expect(ws.requests().some((r) => r.method === 'open')).toBe(true))
  const identity = { instanceId: 'fedcba9876543210fedcba9876543210', sessionEpoch: 1 }
  const first = '0123456789abcdef0011223344556677'
  const second = 'ffffffffffffffffffffffffffffffff'
  const third = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  const open = (sessionId: string): Open => ({
    sessionId,
    ...identity,
    cwd: '/backend/replacement',
    desiredMode: 'helper',
    workspaceId: 'workspace-real',
    awaitsIntegration: true,
    accessEpoch: 17,
    effectiveSize: { cols: 80, rows: 24, xpixel: 0, ypixel: 0 },
    parent: null,
  })
  ws.deliverText({
    jsonrpc: '2.0',
    id: ws.requests().find((r) => r.method === 'open')!.id,
    result: open(first),
  })
  await starting
  const opened = (sessionId: string, generation: number) =>
    ws.deliverText({
      jsonrpc: '2.0',
      method: 'pane.opened',
      params: { paneId: 'pane-wire', generation, open: open(sessionId) },
    })
  opened(second, 2)
  const frame: SessionFrame = {
    revision: 7,
    geometry: { cols: 80, rows: 24, cellWidthPx: 9, cellHeightPx: 18, revision: 1 },
    cursor: { x: 0, y: 0, visible: true },
    state: {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: false,
      pointerShape: null,
      title: null,
      cwd: null,
    },
    rows: Array.from({ length: 24 }, (_, i) => ({ text: i === 0 ? 'replacement prompt' : '' })),
  }
  // No promise turn between the notification and the frame. The production
  // bind must already have installed its callbacks at this point.
  ws.deliverBinary(
    encodeFrame(second, new TextEncoder().encode(JSON.stringify(frame)), MSG_TYPE_METADATA),
  )
  const pane = content as unknown as {
    session: {
      sessionId: string
      cwd: string
      desiredMode: string
      workspaceId: string
      awaitsIntegration: boolean
    }
    _awaitsIntegration: boolean
    _cellModel: { current(): { revision: number } | null }
  }
  expect(pane.session).toMatchObject({
    sessionId: second,
    cwd: '/backend/replacement',
    desiredMode: 'helper',
    workspaceId: 'workspace-real',
    awaitsIntegration: true,
  })
  expect(pane._awaitsIntegration).toBe(true)
  ws.deliverText({
    jsonrpc: '2.0',
    method: 'lifecycle.changed',
    params: {
      sessionId: second,
      lane: 'lane-real',
      lifecycle: 'prompt_ready',
      domain: 'd-real',
      epoch: 1,
    },
  })
  expect(
    (content as unknown as { lifecycle: { state: { kind: string } } }).lifecycle.state.kind,
  ).toBe('prompt_ready')
  await vi.waitFor(() => expect(pane._cellModel.current()?.revision).toBe(7))
  opened(third, 3)
  opened(second, 2)
  expect(pane.session.sessionId).toBe(third)
  expect(ws.requests().filter((r) => ['sessions.live', 'attach'].includes(r.method ?? ''))).toEqual(
    [],
  )
})
