// @vitest-environment jsdom
//
// The pane sends nothing into a connection the backend has declared unusable,
// and says it once (nocx-oalo1.5.1; design §5.8).
//
// The owner saw the failure on 2026-11-13 with htop in an SSH tab: the tab said
// the session was gone, and the pane kept sending input to it — a full-screen
// program with mouse reporting produces bytes on its own — so every refusal
// raised its own danger toast, one per keystroke, until the stack covered the
// window. The gate below is the fix, and this file proves both directions: a
// healthy connection still carries the person's input, and a lost one carries
// none of it.
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ClipboardGate } from './clipboard'
import { Pane } from './panes'
import { SURFACE_TERMINAL } from './pane-content'
import { showToast } from './ui/toast'
import { TerminalContent } from './terminal-content'
import {
  anchoredPane,
  makeBanner,
  makeClient,
  makeClipboard,
  makeSession,
  type ClientFake,
} from './test-support/panes-fixtures'
import type { PaneConnection } from './generated/pane.connection'
import type { WSClient } from './ipc'

vi.mock('./ui/toast', () => ({
  showToast: vi.fn(),
}))

function connection(over: Partial<PaneConnection> = {}): PaneConnection {
  return {
    paneId: 'pane-gate',
    connectionId: 'conn-gate',
    connectionEpoch: 1,
    instanceId: 'instance-gate',
    transport: { state: 'connected', stateMs: 10, plainShells: 1 },
    attempt: { shown: 'none', candidate: 'none', reason: '' },
    observedAt: new Date().toISOString(),
    ...over,
  }
}

let client: ClientFake
let content: TerminalContent
let tab: Pane
let sessionId = ''

async function mount(): Promise<void> {
  client = makeClient()
  content = new TerminalContent(
    client as unknown as WSClient,
    { ...anchoredPane('pane-gate'), registered: Promise.resolve(true) },
    makeClipboard(),
    new ClipboardGate(),
    makeBanner(),
    null,
    () => {},
    { profileId: 'p1', host: 'h' },
  )
  tab = new Pane(
    content,
    {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      defaultTitle: 'Terminal',
    },
    1,
    'tab-gate',
  )
  const parent = document.createElement('div')
  parent.append(tab.pane)
  document.body.append(parent)
  await tab.start()
  sessionId = (client._sessions[0] ?? makeSession()).sessionId
}

/** The private intent path a physical key takes, called where the pane's
 *  input element calls it. */
function press(payload = 'a'): boolean {
  return (
    content as unknown as { onSessionIntent(intent: { kind: 'key'; payload: string }): boolean }
  ).onSessionIntent({ kind: 'key', payload })
}

beforeEach(() => {
  document.body.replaceChildren()
  vi.mocked(showToast).mockClear()
})

describe('the connection input gate (nocx-oalo1.5.1)', () => {
  it('carries input while the connection is usable', async () => {
    await mount()
    const session = client._sessions[0]
    session.intent.mockClear()
    client._firePaneConnection(connection({ sessionId }))
    expect(press()).toBe(true)
    await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('key', 'a'))
  })

  it('sends nothing after the loss, and shows the one notice instead of a toast', async () => {
    await mount()
    const session = client._sessions[0]
    session.intent.mockClear()
    client._firePaneConnection(connection({ sessionId }))
    client._firePaneConnection(
      connection({
        connectionEpoch: 2,
        sessionId,
        transport: { state: 'ended', cause: 'lost-link', stateMs: 10 },
        reconnect: { endsShells: 1 },
      }),
    )
    // A pointer report and a key, the two sources the owner's htop produced.
    expect(press('mouse')).toBe(false)
    expect(press()).toBe(false)
    await Promise.resolve()
    expect(session.intent).not.toHaveBeenCalled()
    expect(showToast).not.toHaveBeenCalled()
    // ONE notice, and it is the head.
    expect(tab.pane.querySelectorAll('[data-testid="connection-head"]')).toHaveLength(1)
    expect(document.querySelectorAll('.ui-status-card')).toHaveLength(0)
  })
})
