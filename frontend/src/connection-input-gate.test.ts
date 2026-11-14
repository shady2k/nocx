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
import { TerminalContent, type TerminalContentHooks } from './terminal-content'
import {
  anchoredPane,
  makeBanner,
  makeClient,
  makeClipboard,
  makeSession,
  lifecycleHandler,
  type ClientFake,
} from './test-support/panes-fixtures'
import type { PaneConnection } from './generated/pane.connection'
import type { SessionHandle, WSClient } from './ipc'

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
let epoch = 0

async function mount(hooks: TerminalContentHooks = {}): Promise<void> {
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
    {
      adoptReplacement: async (wanted: string) => {
        // THE PRODUCTION ADOPTION PATH: the live inventory names the
        // replacement, then the claim takes it back with replay.
        const listed = await client.listLiveSessions()
        const entry = listed.find((candidate) => candidate.sessionId === wanted)
        if (entry === undefined) return null
        return (await client.reclaimSession(entry)) as unknown as SessionHandle
      },
      ...hooks,
    },
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
  epoch = 0
})

/** The next observation of this pane, in the order the backend mints them. */
function publish(over: Partial<PaneConnection> = {}): void {
  epoch += 1
  client._firePaneConnection(connection({ connectionEpoch: epoch, ...over }))
}

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

  it('turns an exit into the session’s end, never the connection’s verdict', async () => {
    await mount()
    const session = client._sessions[0]
    publish({ sessionId })
    const exit = session.onExit.mock.calls[0]?.[0] as (e: {
      sessionId: string
      cause: 'interrupted'
    }) => void
    exit({ sessionId: session.sessionId, cause: 'interrupted' })
    expect(content.connectionCondition()).toBe('reachable')
    expect(tab.pane.querySelector('.nocx-tab-warning')).toBeNull()
  })

  it('stops a queued intent when the gate closes behind an outstanding answer', async () => {
    await mount()
    const session = client._sessions[0]
    publish({ sessionId })
    let release!: (result: { state: 'executed'; bytesWritten: number; fenceAfter: number }) => void
    session.intent.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ state: 'executed', bytesWritten: 1, fenceAfter: 0 })
        }),
    )
    session.intent.mockClear()
    const send = content as unknown as {
      sendIntent(i: { kind: 'text'; payload: string } | { kind: 'key'; payload: string }): boolean
    }
    expect(send.sendIntent({ kind: 'text', payload: 'first' })).toBe(true)
    expect(session.intent).toHaveBeenCalledWith('text', 'first')
    // Queued behind the answer that has not come back.
    send.sendIntent({ kind: 'text', payload: 'second' })
    send.sendIntent({ kind: 'key', payload: 'Enter' })
    // The gate closes while the first call is outstanding.
    publish({ sessionId, transport: { state: 'waiting_person', person: 'password', stateMs: 0 } })
    release({ state: 'executed', bytesWritten: 1, fenceAfter: 0 })
    await Promise.resolve()
    await Promise.resolve()
    expect(session.intent).toHaveBeenCalledTimes(1)
    expect(session.intent).not.toHaveBeenCalledWith('text', 'second')
    expect(session.intent).not.toHaveBeenCalledWith('key', 'Enter')
  })

  it('holds type-ahead until a shell exists, even after the transport connects', async () => {
    await mount()
    const session = client._sessions[0]
    session.intent.mockClear()
    // No session yet: the shell has not been adopted, but the transport is
    // already connected.
    ;(content as unknown as { session: unknown }).session = null
    publish({ transport: { state: 'opening', stage: 'shell', stateMs: 0 } })
    expect(press('x')).toBe(true)
    publish({ transport: { state: 'connected', stateMs: 10, plainShells: 1 } })
    expect(session.intent).not.toHaveBeenCalled()

    // The backend adopts the shell; the held key goes to the composer draft
    // because this pane is integrated.
    const editor = (
      content as unknown as { editor: { show(): void; getDoc(): string; isVisible: boolean } }
    ).editor
    const replacement = makeSession({ awaitsIntegration: true })
    replacement.sessionId = 'replacement-shell'
    client._sessions.push(replacement)
    client._addLiveSession({
      sessionId: 'replacement-shell',
      instanceId: 'instance-gate',
      sessionEpoch: 1,
      paneId: 'pane-gate',
      replayFrom: 0,
      attached: false,
    })
    publish({ sessionId: 'replacement-shell' })
    await vi.waitFor(() =>
      expect(
        (content as unknown as { session: { sessionId: string } | null }).session?.sessionId,
      ).toBe('replacement-shell'),
    )
    // A further connected observation is NOT the shell's verdict: the session
    // awaits integration, so the keys stay held through it.
    publish({ sessionId: 'replacement-shell' })
    expect(editor.getDoc()).toBe('')
    expect(replacement.intent).not.toHaveBeenCalledWith('key', 'x')
    // The replacement shell proves itself integrated; only then do held keys
    // belong in the composer.
    lifecycleHandler(client)({
      lane: 'lane-1',
      lifecycle: 'prompt_ready',
      domain: 'd1',
      epoch: 1,
    })
    await vi.waitFor(() => expect(editor.getDoc()).toContain('x'))
    expect(replacement.intent).not.toHaveBeenCalledWith('key', 'x')
  })

  it('presses reconnect without closing or releasing the held session, and adopts the replacement', async () => {
    await mount()
    const first = client._sessions[0]
    first.close.mockClear()
    first.detach.mockClear()
    publish({ sessionId: first.sessionId })
    publish({
      sessionId: first.sessionId,
      transport: { state: 'failed', cause: 'auth-rejected', stateMs: 10 },
      reconnect: { endsShells: 1 },
    })
    expect(await content.pressReconnect()).toBe(true)
    // The renderer let the backend decide; it did not close the shell whose
    // reconnect the backend could still refuse.
    expect(first.close).not.toHaveBeenCalled()
    expect(first.detach).not.toHaveBeenCalled()
    await vi.waitFor(() => expect(client._sessions.length).toBe(2))
    const replacement = client._sessions[1]
    await vi.waitFor(() =>
      expect(
        (content as unknown as { session: { sessionId: string } | null }).session?.sessionId,
      ).toBe(replacement.sessionId),
    )
    expect(tab.pane.querySelector('[data-reconnect-boundary]')).not.toBeNull()
  })

  it('sends nothing into a healthy connection whose shell failed, and offers the shell retry', async () => {
    await mount()
    const session = client._sessions[0]
    session.intent.mockClear()
    publish({ sessionId })
    publish({
      transport: { state: 'connected', stateMs: 500, plainShells: 0 },
      shellFailure: { code: -32603, message: 'the shell could not start' },
      reconnect: { endsShells: 0 },
    })
    expect(press()).toBe(false)
    expect(session.intent).not.toHaveBeenCalled()
    const head = tab.pane.querySelector('[data-testid="connection-head"]')
    expect(head?.getAttribute('data-shell-failure')).toBe('true')
    expect(head?.querySelector('.ui-button')?.textContent).toBe('Retry shell')
  })

  it('adopts an automatic replacement without a press', async () => {
    await mount()
    const first = client._sessions[0]
    publish({ sessionId: first.sessionId })
    publish({
      sessionId: first.sessionId,
      transport: { state: 'reconnecting', attempt: 1, stateMs: 0 },
    })
    const automatic = makeSession()
    automatic.sessionId = 'automatic-shell'
    client._sessions.push(automatic)
    client._addLiveSession({
      sessionId: 'automatic-shell',
      instanceId: 'instance-gate',
      sessionEpoch: 1,
      paneId: 'pane-gate',
      replayFrom: 0,
      attached: false,
    })
    publish({ sessionId: 'automatic-shell' })
    await vi.waitFor(() =>
      expect(
        (content as unknown as { session: { sessionId: string } | null }).session?.sessionId,
      ).toBe('automatic-shell'),
    )
    expect(tab.pane.querySelector('[data-reconnect-boundary]')).not.toBeNull()
  })
})
