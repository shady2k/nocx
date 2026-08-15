// ── TabManager test fixtures ──────────────────────────────────────────────
//
// Centralised factories, constants and helpers so that adding a field to the
// real SessionHandle (or changing a default title) requires editing exactly
// ONE place in test-support instead of chasing N copies through the suite.
//
// See AD-7: sessionId is server-authoritative, cwd is set once at session
// open. The fake must carry both.
import { vi, type Mock } from 'vitest'
import type {
  CommandMarkerCallback,
  CwdCallback,
  DataCallback,
  RenderFenceCallback,
  RenderFenceEvent,
  ResizeCallback,
  TitleCallback,
  TerminalRenderer,
} from '../renderers/types'
import { CommandSnapshotStore } from '../command-snapshot'
import type { ClipboardAccess } from '../clipboard'
import type { ClipboardGate } from '../clipboard'
import type { ClipboardBanner } from '../banner'
import type { TabManager } from '../tabs'
import type { DesiredMode } from '../capability'

// ═══════════════════════════════════════════════════════════════════════════
// Constants — every assertion must derive from these, never repeat the literal.
// ═══════════════════════════════════════════════════════════════════════════

/** The cwd every session reports by default. */
export const FIXTURE_CWD = '~/Documents/repos/nocx'

/** The tab label produced by directoryLabel(FIXTURE_CWD). */
export const FIXTURE_DIRECTORY_LABEL = 'repos/nocx'

// ═══════════════════════════════════════════════════════════════════════════
/** The renderer mock's live-content-height measurer: a spy the tests
 *  program to stand in for the real renderer measuring the grid (the
 *  TerminalRenderer interface types it as a plain function). */
export type LiveContentHeightSpy = Mock<() => number>
export interface RendererMock extends TerminalRenderer {
  /** This tab's OSC 636 store — XtermRenderer owns one, so the mock must too. */
  snapshotStore: CommandSnapshotStore
  _cbs: {
    onData?: DataCallback
    onResize?: ResizeCallback
    onTitle?: TitleCallback
    onCwd?: CwdCallback
    onCommandMarker?: CommandMarkerCallback
    onBell?: () => void
    onBufferChange?: (type: 'normal' | 'alternate') => void
    onSelectionChange?: (text: string) => void
    onClipboardWrite?: (text: string) => void
  }
  _fireBufferChange(type: 'normal' | 'alternate'): void
  _fireTitle(title: string): void
  _fireCwd(host: string, path: string): void
  _fireCommandMarker(marker: Parameters<CommandMarkerCallback>[0]): void
  /** Fire an OSC 1337 in-band READY (nocx-ynsx). */
  _fireInBandReady(): void
  _fireBell(): void
  /** Fire a selection event — used by clipboard policy tests. */
  _fireSelectionChange(text: string): void
  /** Fire an OSC 52 write event — used by clipboard policy tests. */
  _fireClipboardWrite(text: string): void
  /** Fire a recovery-fence sighting (ADR-0024 decision 8). */
  _fireRecoveryFence(hex: string): void
  /** Fire a render-fence sighting (ADR-0024 §7 carve-out, u7uh.8). */
  _fireRenderFence(ev: RenderFenceEvent): void
  /** Fire a keystroke reaching the grid in raw mode (nocx-yb5y). */
  _fireData(data: string): void
}

/**
 * Creates a single renderer mock with stored callbacks.
 * Used as the implementation of the mocked createRenderer() so each Tab
 * gets its own independent mock.
 */
export function createRendererMock(): RendererMock {
  const cbs: RendererMock['_cbs'] = {}
  const recoverySubs: Array<(hex: string) => void> = []
  const fenceSubs: Array<(ev: RenderFenceEvent) => void> = []
  const mock: Record<string, unknown> = {
    mount: vi.fn().mockResolvedValue(undefined),
    write: vi.fn(),
    reset: vi.fn(),
    dispose: vi.fn(),
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
    onRecoveryFence: vi.fn((cb: (hex: string) => void) => {
      recoverySubs.push(cb)
    }),
    onRenderFence: vi.fn((cb: RenderFenceCallback) => {
      fenceSubs.push(cb)
    }),
    onSelectionChange: vi.fn((cb: (text: string) => void) => {
      cbs.onSelectionChange = cb
    }),
    onClipboardWrite: vi.fn((cb: (text: string) => void) => {
      cbs.onClipboardWrite = cb
    }),
    // A paste IS input: xterm's term.paste() writes the payload (bracketed
    // when the program asked for it) through the same onData every keystroke
    // takes — which is how a submitted command reaches the pty at all. A mock
    // that only recorded the call made the command invisible to anything
    // watching onData, and hid a defect that put the command BEHIND the keys
    // typed after it until the e2e suite found it (nocx-yb5y).
    paste: vi.fn((text: string) => {
      cbs.onData?.(text)
    }),
    setReadOnly: vi.fn(),
    refreshAtlas: vi.fn(),
    focus: vi.fn(),
    fitViewport: vi.fn(),
    registerMarker: vi.fn().mockReturnValue(undefined),
    cellHeight: 16,
    viewportTopLine: 0,
    cellWidth: 8,
    onCellDimsChange: vi.fn(),
    onScroll: vi.fn(),
    onRender: vi.fn(),
    paneElement: document.createElement('div'),
    getBufferLine: vi.fn().mockReturnValue(undefined),
    cursorLine: vi.fn().mockReturnValue(0),
    clearViewport: vi.fn(),
    // Zero means "cannot measure", which the caller treats as "keep the current
    // height" — so a fixture that does not care about live-region sizing gets
    // the same behaviour as before this method existed.
    liveContentHeight: vi.fn().mockReturnValue(0),
    cols: 80,
    rows: 24,
    // A REAL store, not a stub: the composition point hands
    // renderer.snapshotStore to the editor and to the scrollback's frozen
    // headers, so a mock without one crashes the CM6 plugin at mount. It is
    // per mock, exactly like the per-renderer instance it stands in for —
    // tests that want a snapshot ingest into this one.
    snapshotStore: new CommandSnapshotStore(),
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
    _fireSelectionChange(text: string) {
      cbs.onSelectionChange?.(text)
    },
    _fireClipboardWrite(text: string) {
      cbs.onClipboardWrite?.(text)
    },
    /** Fire a recovery-fence sighting (ADR-0024 decision 8). */
    _fireRecoveryFence(hex: string) {
      for (const sub of recoverySubs) sub(hex)
    },
    _fireRenderFence(ev: RenderFenceEvent) {
      for (const sub of fenceSubs) sub(ev)
    },
    /** A keystroke reaching the grid in raw mode — what xterm's onData
     *  fires once stdin is enabled. The real renderer drops these while
     *  disableStdin is set; the mock does not model that, so a test that
     *  cares asserts on setReadOnly instead. */
    _fireData(data: string) {
      cbs.onData?.(data)
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
  /** The resolved destination mode from the open ack (nocx-mlm7). */
  desiredMode: DesiredMode
  send: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onData: ReturnType<typeof vi.fn>
  onExit: ReturnType<typeof vi.fn>
  onReset: ReturnType<typeof vi.fn>
  onInputStalled: ReturnType<typeof vi.fn>
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
    desiredMode: 'script',
    send: vi.fn(),
    sendResize: vi.fn(),
    close: vi.fn(),
    onData: vi.fn((cb: (data: string) => void) => {
      dataCb = cb
    }),
    onExit: vi.fn(),
    onReset: vi.fn(),
    onInputStalled: vi.fn(),
    fireData: (data: string) => {
      dataCb?.(data)
    },
    ...overrides,
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// WSClient fake
// ═══════════════════════════════════════════════════════════════════════════
/** The narrow dispatcher seam TerminalContent's lifecycle wiring touches:
 *  subscribe(method, handler) returns the unsubscribe, and tests capture
 *  the handler to deliver published facts (lifecycle.changed). `call` is
 *  the request half (lifecycle.submitAttempt): it resolves by default so a
 *  submit at a live prompt proceeds to the pty write; tests override it to
 *  control or record the attempt-open ordering. Not exported — it is a
 *  shape of ClientFake, never named by consumers. */
interface DispatcherFake {
  subscribe: ReturnType<typeof vi.fn>
  call: ReturnType<typeof vi.fn>
}

export interface ClientFake {
  connect: ReturnType<typeof vi.fn>
  openSession: ReturnType<typeof vi.fn>
  openSSHSession: ReturnType<typeof vi.fn>
  openSSHSessionByHost: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  sendToSession: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  closeSession: ReturnType<typeof vi.fn>
  onSessionData: ReturnType<typeof vi.fn>
  onSessionExit: ReturnType<typeof vi.fn>
  onSessionReset: ReturnType<typeof vi.fn>
  /** Control-plane calls (history.record, history.query, …). Rejects by
   *  default — the no-store state, which the recall overlay labels
   *  source=session. */
  call: ReturnType<typeof vi.fn>
  readonly connected: boolean
  /** Sessions created by openSession calls, in order. */
  _sessions: SessionFake[]
  /** The narrow dispatcher seam TerminalContent's lifecycle wiring touches:
   *  subscribe(method, handler) returns the unsubscribe, and tests capture
   *  the handler to deliver published facts (lifecycle.changed). */
  dispatcher: DispatcherFake
}
/** The handler a surface registered for one server-initiated method, so a
 *  test can deliver a published fact through the REAL subscription seam
 *  rather than reaching into the surface. Fails loudly when nothing
 *  subscribed: a silent no-op would let a test pass over a subscription
 *  that was never made, which is the defect class the lifecycle branch
 *  already shipped once (a dropped subscription, found by diffing). */
function notificationHandler(client: ClientFake, method: string): (params: unknown) => void {
  const call = client.dispatcher.subscribe.mock.calls.find((c: unknown[]) => c[0] === method) as
    [string, (params: unknown) => void] | undefined
  if (!call) throw new Error(`nothing subscribed to ${method}`)
  return call[1]
}

/** The session.integrationChanged handler (nocx-dvql). */
export function integrationHandler(client: ClientFake): (params: unknown) => void {
  return notificationHandler(client, 'session.integrationChanged')
}

/** The lifecycle.changed handler with the wire's server-authoritative session
 *  address. Tests describe the fact body; this helper supplies the exact
 *  transport envelope that routes it to the mounted tab. */
export function lifecycleHandler(
  client: ClientFake,
  sessionId = client._sessions[0]?.sessionId,
): (params: unknown) => void {
  if (!sessionId) throw new Error('no session available for lifecycle.changed')
  const deliver = notificationHandler(client, 'lifecycle.changed')
  return (params: unknown): void => {
    if (!params || typeof params !== 'object') {
      deliver(params)
      return
    }
    deliver({ ...params, sessionId })
  }
}

/**
 * Create a fake WSClient whose openSession() returns a new makeSession()
 * on every call and records it in _sessions for test inspection.
 */
export function makeClient(overrides?: Partial<ClientFake>): ClientFake {
  const sessions: SessionFake[] = []
  const newSession = (): SessionFake => {
    const s = makeSession()
    sessions.push(s)
    return s
  }
  const client: ClientFake = {
    connect: vi.fn().mockResolvedValue(undefined),
    openSession: vi.fn(() => Promise.resolve(newSession())),
    openSSHSession: vi.fn(() => Promise.resolve(newSession())),
    openSSHSessionByHost: vi.fn(() => Promise.resolve(newSession())),
    close: vi.fn(),
    sendToSession: vi.fn(),
    sendResize: vi.fn(),
    closeSession: vi.fn(),
    onSessionData: vi.fn(),
    onSessionExit: vi.fn(),
    onSessionReset: vi.fn(),
    dispatcher: {
      subscribe: vi.fn(() => () => undefined),
      // A live prompt opens the attempt before the pty write; the default
      // resolves so the write always proceeds (fail-open).
      call: vi.fn().mockResolvedValue({
        id: 'att-0',
        domain: 'd1',
        state: 'open',
        command: '',
        cwd: '',
        host: '',
        origin: 'app',
        startedAt: '2026-08-08T12:00:00Z',
      }),
    },
    call: vi.fn().mockRejectedValue(new Error('no store wired (fake)')),
    get connected() {
      return true
    },
    _sessions: sessions,
    ...overrides,
  }
  return client
}

// ═══════════════════════════════════════════════════════════════════════════
// Clipboard fake — injectable into TabManager for policy-layer tests.
// ═══════════════════════════════════════════════════════════════════════════

export interface ClipboardFake extends ClipboardAccess {
  readText: ReturnType<typeof vi.fn>
  writeText: ReturnType<typeof vi.fn>
}

/**
 * Create a fake clipboard whose readText / writeText are vitest spies.
 * Used by clipboard policy tests in tabs.test.ts.
 */
export function makeClipboard(overrides?: Partial<ClipboardFake>): ClipboardFake {
  return {
    readText: vi.fn().mockResolvedValue(''),
    writeText: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Banner fake — injectable into TabManager for gate-layer tests.
// ═══════════════════════════════════════════════════════════════════════════

export interface BannerFake extends ClipboardBanner {
  shown: boolean
  show: ReturnType<typeof vi.fn>
}

/**
 * Create a fake banner whose show() returns a controllable promise.
 * Override shown / show per-test to drive the gate policy.
 */
export function makeBanner(overrides?: Partial<BannerFake>): BannerFake {
  return {
    shown: false,
    show: vi.fn().mockResolvedValue('dismiss' as const),
    ...overrides,
  }
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
 * Full setup: create DOM, construct TabManager, and open the initial tab.
 * Callers must await; the returned manager has one terminal tab active.
 */
export async function mountTabManager(
  client?: ClientFake,
  clipboard?: ClipboardFake,
  gate?: ClipboardGate,
  banner?: BannerFake,
): Promise<{
  bar: HTMLElement
  panes: HTMLElement
  manager: TabManager
  client: ClientFake
  clipboard: ClipboardFake
  gate: ClipboardGate
  banner: BannerFake
  tabStrip: import('../tab-strip').TabStrip
}> {
  const { bar, panes } = setupTabBarDOM()
  const c = client ?? makeClient()
  const cb = clipboard ?? makeClipboard()
  const g = gate ?? new (await import('../clipboard')).ClipboardGate()
  const bn = banner ?? makeBanner()
  const pc = {
    listProfiles: vi.fn().mockResolvedValue([]),
    listGroups: vi.fn().mockResolvedValue([]),
  }
  const { TabManager } = await import('../tabs')
  const { HorizontalTabStrip } = await import('../tab-strip')
  const tabStrip = new HorizontalTabStrip()
  const manager = new TabManager(
    bar,
    bar,
    panes,
    c as unknown as import('../ipc').WSClient,
    cb,
    g,
    bn,
    pc as unknown as import('../profiles').ProfileClient,
    tabStrip,
  )
  // Open the initial tab explicitly — the constructor mounts nothing.
  await manager.openInitialTab()
  return { bar, panes, manager, client: c, clipboard: cb, gate: g, banner: bn, tabStrip }
}
