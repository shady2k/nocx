// ── TabManager test fixtures ──────────────────────────────────────────────
//
// Centralised factories, constants and helpers so that adding a field to the
// real SessionHandle (or changing a default title) requires editing exactly
// ONE place in test-support instead of chasing N copies through the suite.
//
// See AD-7: sessionId is server-authoritative, cwd is set once at session
// open. The fake must carry both.

import { vi, expect } from 'vitest'
import type {
  CommandMarkerCallback,
  CwdCallback,
  DataCallback,
  ResizeCallback,
  TitleCallback,
  TerminalRenderer,
} from '../renderers/types'
import type { TabManager } from '../tabs'

// ═══════════════════════════════════════════════════════════════════════════
// Constants — every assertion must derive from these, never repeat the literal.
// ═══════════════════════════════════════════════════════════════════════════

/** The cwd every session reports by default. */
export const FIXTURE_CWD = '~/Documents/repos/nocx'

/** The tab label produced by directoryLabel(FIXTURE_CWD). */
export const FIXTURE_DIRECTORY_LABEL = 'repos/nocx'

// ═══════════════════════════════════════════════════════════════════════════
// Renderer mock — factory called once per tab by createRenderer().
// ═══════════════════════════════════════════════════════════════════════════

export interface RendererMock extends TerminalRenderer {
  _cbs: {
    onData?: DataCallback
    onResize?: ResizeCallback
    onTitle?: TitleCallback
    onCwd?: CwdCallback
    onCommandMarker?: CommandMarkerCallback
    onBell?: () => void
    onBufferChange?: (type: 'normal' | 'alternate') => void
  }
  _fireBufferChange(type: 'normal' | 'alternate'): void
  _fireTitle(title: string): void
  _fireCwd(host: string, path: string): void
  _fireCommandMarker(marker: Parameters<CommandMarkerCallback>[0]): void
  _fireBell(): void
}

/**
 * Creates a single renderer mock with stored callbacks.
 * Used as the implementation of the mocked createRenderer() so each Tab
 * gets its own independent mock.
 */
export function createRendererMock(): RendererMock {
  const cbs: RendererMock['_cbs'] = {}
  const mock: Record<string, unknown> = {
    mount: vi.fn().mockResolvedValue(undefined),
    write: vi.fn(),
    reset: vi.fn(),
    onData: vi.fn((cb: DataCallback) => {
      cbs.onData = cb
    }),
    onResize: vi.fn((cb: ResizeCallback) => {
      cbs.onResize = cb
    }),
    onTitle: vi.fn((cb: TitleCallback) => {
      cbs.onTitle = cb
    }),
    onCwd: vi.fn((cb: CwdCallback) => {
      cbs.onCwd = cb
    }),
    onCommandMarker: vi.fn((cb: CommandMarkerCallback) => {
      cbs.onCommandMarker = cb
    }),
    onBell: vi.fn((cb: () => void) => {
      cbs.onBell = cb
    }),
    onBufferChange: vi.fn((cb: (type: 'normal' | 'alternate') => void) => {
      cbs.onBufferChange = cb
    }),
    refreshAtlas: vi.fn(),
    focus: vi.fn(),
    cols: 80,
    rows: 24,
    _cbs: cbs,
    _fireBufferChange(type: 'normal' | 'alternate') {
      cbs.onBufferChange?.(type)
    },
    _fireTitle(title: string) {
      cbs.onTitle?.(title)
    },
    _fireCwd(host: string, path: string) {
      cbs.onCwd?.({ host, path })
    },
    _fireCommandMarker(marker: Parameters<CommandMarkerCallback>[0]) {
      cbs.onCommandMarker?.(marker)
    },
    _fireBell() {
      cbs.onBell?.()
    },
  }
  return mock as unknown as RendererMock
}

// ═══════════════════════════════════════════════════════════════════════════
// SessionHandle fake
// ═══════════════════════════════════════════════════════════════════════════

let sessionCounter = 0

/** Reset the session-id counter between tests. */
export function resetSessionCounter(): void {
  sessionCounter = 0
}

export interface SessionFake {
  sessionId: string
  cwd: string
  send: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onData: ReturnType<typeof vi.fn>
  onExit: ReturnType<typeof vi.fn>
  onReset: ReturnType<typeof vi.fn>
  /** Fire the registered data callback. */
  fireData(data: string): void
}

/**
 * Create a fake SessionHandle with sensible defaults.
 *
 * Override any property per-test — the default cwd comes from FIXTURE_CWD
 * so a test that just needs a differently-named directory can pass
 * `{ cwd: '~/other' }` without repeating every other field.
 */
export function makeSession(overrides?: Partial<SessionFake>): SessionFake {
  let dataCb: ((data: string) => void) | null = null
  return {
    sessionId: `mock-sid-${++sessionCounter}`,
    cwd: FIXTURE_CWD,
    send: vi.fn(),
    sendResize: vi.fn(),
    close: vi.fn(),
    onData: vi.fn((cb: (data: string) => void) => {
      dataCb = cb
    }),
    onExit: vi.fn(),
    onReset: vi.fn(),
    fireData: (data: string) => {
      dataCb?.(data)
    },
    ...overrides,
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// WSClient fake
// ═══════════════════════════════════════════════════════════════════════════

export interface ClientFake {
  connect: ReturnType<typeof vi.fn>
  openSession: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  sendToSession: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  closeSession: ReturnType<typeof vi.fn>
  onSessionData: ReturnType<typeof vi.fn>
  onSessionExit: ReturnType<typeof vi.fn>
  onSessionReset: ReturnType<typeof vi.fn>
  readonly connected: boolean
  /** Sessions created by openSession calls, in order. */
  _sessions: SessionFake[]
}

/**
 * Create a fake WSClient whose openSession() returns a new makeSession()
 * on every call and records it in _sessions for test inspection.
 */
export function makeClient(overrides?: Partial<ClientFake>): ClientFake {
  const sessions: SessionFake[] = []
  const client: ClientFake = {
    connect: vi.fn().mockResolvedValue(undefined),
    openSession: vi.fn(() => {
      const s = makeSession()
      sessions.push(s)
      return Promise.resolve(s)
    }),
    close: vi.fn(),
    sendToSession: vi.fn(),
    sendResize: vi.fn(),
    closeSession: vi.fn(),
    onSessionData: vi.fn(),
    onSessionExit: vi.fn(),
    onSessionReset: vi.fn(),
    get connected() {
      return true
    },
    _sessions: sessions,
    ...overrides,
  }
  return client
}

// ═══════════════════════════════════════════════════════════════════════════
// DOM setup helpers
// ═══════════════════════════════════════════════════════════════════════════

/** Create the bare bar + panes container elements and append them to body. */
export function setupTabBarDOM(): { bar: HTMLElement; panes: HTMLElement } {
  document.body.innerHTML = ''
  const bar = document.createElement('div')
  const panes = document.createElement('div')
  document.body.append(bar, panes)
  return { bar, panes }
}

/**
 * Full setup: create DOM, construct TabManager with the given client
 * (or a fresh makeClient() if none provided), and wait until the initial
 * session has been opened.
 */
export async function mountTabManager(client?: ClientFake): Promise<{
  bar: HTMLElement
  panes: HTMLElement
  manager: TabManager
  client: ClientFake
}> {
  const { bar, panes } = setupTabBarDOM()
  const c = client ?? makeClient()
  const { TabManager } = await import('../tabs')
  const manager = new TabManager(bar, panes, c as unknown as import('../ipc').WSClient)
  await vi.waitFor(() => {
    expect(c.openSession).toHaveBeenCalled()
  })
  return { bar, panes, manager, client: c }
}
