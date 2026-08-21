// ── PaneManager test fixtures ──────────────────────────────────────────────
//
// Centralised factories, constants and helpers so that adding a field to the
// real SessionHandle (or changing a default title) requires editing exactly
// ONE place in test-support instead of chasing N copies through the suite.
//
// See AD-7: sessionId is server-authoritative, cwd is set once at session
// open. The fake must carry both.
import type { PaneIdentity } from '../terminal-content'
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
import { LayoutStore } from '../layout/layout-store'
import { UIStateClient } from '../uistate-client'
import type { UIState } from '../generated/uistate'
import type { Dispatcher } from '../dispatcher'
import type { LayoutClientLike } from '../layout/layout-client'
import type {
  Tab as LayoutTab,
  Pane as LayoutPane,
  Workspace as LayoutWorkspace,
} from '../generated/layout.read'
import type { ClipboardAccess } from '../clipboard'
import type { ClipboardGate } from '../clipboard'
import type { ClipboardBanner } from '../banner'
import type { SSHProfile } from '../profiles'
import type { PaneManager } from '../panes'
import type { DesiredMode } from '../capability'
import type { SessionLiveness } from '../generated/session.liveness'
import type { Open } from '../generated/open'

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
export type LiveContentHeightSpy = Mock<() => number | null>
export interface RendererMock extends TerminalRenderer {
  /** This tab's OSC 636 store — XtermRenderer owns one, so the mock must too. */
  snapshotStore: CommandSnapshotStore
  /** The mock's paste is a spy: tests program it to refuse the write. */
  paste: Mock<(text: string) => boolean>
  /** The mock's bracketed-paste-mode answer is a spy: tests flip mode 2004
   *  on and off. */
  bracketedPasteActive: Mock<() => boolean>
  /** The snippet-palette chord handler — stored, so a test can fire it the
   *  way xterm's custom key handler would. */
  onSnippetChord: Mock<(cb: (() => void) | null) => void>
  /** Fire the registered snippet chord handler (the xterm boundary's
   *  delegation). */
  _fireSnippetChord(): void
  _cbs: {
    onData?: DataCallback
    onResize?: ResizeCallback
    onTitle?: TitleCallback
    onCwd?: CwdCallback
    onCommandMarker?: CommandMarkerCallback
    onBell?: () => void
    // The real renderer fans out to a list; the mock mirrors it so a
    // subscriber is never hidden by a later one.
    onBufferChange?: Array<(type: 'normal' | 'alternate') => void>
    onSelectionChange?: (text: string) => void
    onClipboardWrite?: (text: string) => void
    onWriteParsed?: () => void
  }
  _fireBufferChange(type: 'normal' | 'alternate'): void
  _fireTitle(title: string): void
  _fireCwd(host: string, path: string): void
  _fireCommandMarker(marker: Parameters<CommandMarkerCallback>[0]): void
  /** Fire a parse settle — xterm's onWriteParsed, the event the live region
   *  measures the grid on. */
  _fireWriteParsed(): void
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
  let snippetChordCb: (() => void) | null = null
  let activeBuffer: 'normal' | 'alternate' = 'normal'
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
    // The real renderer fans out to a subscriber list; the mock must too
    // (the lane interactivity report subscribes beside the presentation
    // layer — a single-slot mock hides one of them).
    onBufferChange: vi.fn((cb: (type: 'normal' | 'alternate') => void) => {
      ;(cbs.onBufferChange ??= []).push(cb)
    }),
    // The current buffer kind, like the real renderer's (the report the
    // lane interactivity path reads at session open — ADR-0020 decision 3).
    activeBufferKind: vi.fn(() => activeBuffer),
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
      // The write happened: the mock's terminal is always mounted, and the
      // real renderer returns true whenever a terminal exists. A test
      // wanting the refusal path overrides this with mockReturnValue(false).
      return true
    }),
    // Mode 2004 off by default — a test that wants bracketed paste on
    // flips it with mockReturnValue(true) on this same vi.fn.
    bracketedPasteActive: vi.fn(() => false),
    onSnippetChord: vi.fn((cb: (() => void) | null) => {
      snippetChordCb = cb
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
    // The live region measures the grid from here, not from the write:
    // xterm parses asynchronously, so this is the event that says the rows
    // exist. Stored so a test can fire it the way a parse pass would.
    onWriteParsed: vi.fn((cb: () => void) => {
      cbs.onWriteParsed = cb
    }),
    paneElement: document.createElement('div'),
    getBufferLine: vi.fn().mockReturnValue(undefined),
    cursorLine: vi.fn().mockReturnValue(0),
    clearViewport: vi.fn(),
    // NULL means "cannot measure", which the caller treats as "keep the current
    // height" — so a fixture that does not care about live-region sizing gets
    // the same behaviour as before this method existed. Zero would be a
    // different statement: a grid nobody has written to.
    liveContentHeight: vi.fn().mockReturnValue(null),
    cols: 80,
    rows: 24,
    // A REAL store, not a stub: the composition point hands
    // renderer.snapshotStore to the editor and to the scrollback's frozen
    // headers, so a mock without one crashes the CM6 plugin at mount. It is
    // per mock, exactly like the per-renderer instance it stands in for —
    // tests that want a snapshot ingest into this one.
    snapshotStore: new CommandSnapshotStore(),
    _fireBufferChange(type: 'normal' | 'alternate') {
      activeBuffer = type
      for (const sub of cbs.onBufferChange ?? []) sub(type)
    },
    _fireWriteParsed() {
      cbs.onWriteParsed?.()
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
    /** Fire the snippet-palette chord the way xterm's custom key handler
     *  does when ⌥⌘P is pressed in the terminal (nocx-jj77). */
    _fireSnippetChord() {
      snippetChordCb?.()
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
  /** The opener the backend ADMITTED on the open ack, or null for a root
   *  session (nocx-9hu9d). Null by default: the product opens no session with
   *  a parent yet, so a fixture that claimed one would describe a tree the
   *  renderer cannot currently produce. A test that needs an edge sets it —
   *  `childOf(parent)` builds the whole value from the parent's own fake, so
   *  no test spells the identity by hand. */
  parent: Open['parent']
  send: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onData: ReturnType<typeof vi.fn>
  onExit: ReturnType<typeof vi.fn>
  onReset: ReturnType<typeof vi.fn>
  onInputStalled: ReturnType<typeof vi.fn>
  /** The reachability axis (nocx-iarf9): the backend's revised belief about
   *  reaching this session. */
  onLiveness: ReturnType<typeof vi.fn>
  /** Fire the registered data callback. */
  fireData(data: string): void
  /** Fire the registered liveness callback with one observation. */
  fireLiveness(liveness: 'alive' | 'unknown', livenessEpoch?: number): void
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
  let livenessCb: ((l: SessionLiveness) => void) | null = null
  const sessionId = `mock-sid-${++sessionCounter}`
  return {
    sessionId,
    cwd: FIXTURE_CWD,
    desiredMode: 'script',
    parent: null,
    send: vi.fn(),
    sendResize: vi.fn(),
    close: vi.fn(),
    onData: vi.fn((cb: (data: string) => void) => {
      dataCb = cb
    }),
    onExit: vi.fn(),
    onReset: vi.fn(),
    onInputStalled: vi.fn(),
    onLiveness: vi.fn((cb: (l: SessionLiveness) => void) => {
      livenessCb = cb
    }),
    fireData: (data: string) => {
      dataCb?.(data)
    },
    fireLiveness: (liveness: 'alive' | 'unknown', livenessEpoch = 2) => {
      livenessCb?.({
        sessionId,
        instanceId: 'fedcba9876543210fedcba9876543210',
        sessionEpoch: 1,
        liveness,
        livenessEpoch,
        observedAt: '2026-08-17T10:00:00Z',
      })
    },
    ...overrides,
  }
}

/**
 * The `parent` value a session opened BY `opener` would carry back on its ack
 * (nocx-9hu9d): the opener's full identity, never a bare id. Built from the
 * opener's own fake so the two agree by construction — an edge naming an id
 * the test invented would exercise a tree the backend would have refused.
 */
export function childOf(opener: SessionFake): Open['parent'] {
  return {
    sessionId: opener.sessionId,
    instanceId: FIXTURE_INSTANCE_ID,
    sessionEpoch: 1,
  }
}

/** The backend instance every fixture session belongs to. One value, because
 *  a lineage edge may only name the instance that minted it. Not exported:
 *  no test needs to spell it, and childOf is the only thing that should. */
const FIXTURE_INSTANCE_ID = 'fedcba9876543210fedcba9876543210'

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
  /** The pane.close notification (nocx-tsajw): records the wire identity of
   *  the closed pane so tests can assert the backend was told. */
  notifyPaneClosed: ReturnType<typeof vi.fn>
  /** The reconnect report (nocx-gbhwh): fires once per reconnect, after every
   *  attach has settled. A pane subscribes to retry work that could not reach
   *  the store while the socket was down — restoring its past is one. Returns
   *  an unsubscribe, like the real one. */
  onReconnectResult: ReturnType<typeof vi.fn>
  /** Fire that report at every subscriber, so a test can drive the retry the
   *  way a returning socket does. */
  _fireReconnect: (r?: { resumed: number; lost: number }) => void
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
 * A pane whose row the chain ALREADY HOLDS — what every test that is not
 * about the create race wants (nocx-rtg0.29).
 *
 * Spelled once rather than at each construction site: "registered" is the
 * readiness TerminalContent waits on before it names the pane on `open`, and
 * six literals would be six places to get the default wrong.
 */
export function anchoredPane(paneId = 'tab-wire-1'): PaneIdentity {
  return { paneId, registered: Promise.resolve(true) }
}

/**
 * Create a fake WSClient whose openSession() returns a new makeSession()
 * on every call and records it in _sessions for test inspection.
 */
export function makeClient(overrides?: Partial<ClientFake>): ClientFake {
  const sessions: SessionFake[] = []
  const reconnectHandlers = new Set<(r: { resumed: number; lost: number }) => void>()
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
    notifyPaneClosed: vi.fn(),
    onReconnectResult: vi.fn((cb: (r: { resumed: number; lost: number }) => void) => {
      reconnectHandlers.add(cb)
      return () => reconnectHandlers.delete(cb)
    }),
    _fireReconnect: (r = { resumed: 0, lost: 0 }) => {
      for (const cb of [...reconnectHandlers]) cb(r)
    },
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
// Clipboard fake — injectable into PaneManager for policy-layer tests.
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
// Banner fake — injectable into PaneManager for gate-layer tests.
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
// The layout chain, in memory
// ═══════════════════════════════════════════════════════════════════════════

/**
 * A LayoutClientLike backed by three arrays, standing in for the backend that
 * owns the chain (nocx-isoph.4).
 *
 * It implements the RULES, not just the calls, because the rules are what the
 * renderer is now built on: a create with content, positions written from the
 * order a reorder was given, a tab dissolved with its last pane, and the
 * replacement minted when a close would leave the application with nothing.
 * A fake that only recorded calls would let a test pass while the renderer
 * assumed a lifecycle the backend does not have.
 */
export function makeLayoutBackend(): LayoutClientLike & {
  rows: () => { tabs: LayoutTab[]; panes: LayoutPane[]; workspaces: LayoutWorkspace[] }
  fail: (method: keyof LayoutClientLike, err: Error) => void
} {
  const DEFAULT_WS = 'workspace:default'
  let tabs: LayoutTab[] = []
  let panes: LayoutPane[] = []
  /** The workspaces the user made. The DEFAULT is not in here: it is written
   *  when something first needs it, exactly as ensureDefaultWorkspace does,
   *  and `read` supplies it whenever a tab is in it. */
  let made: LayoutWorkspace[] = []
  const failures = new Map<string, Error>()

  const refuse = <T>(method: string): Promise<T> | null => {
    const err = failures.get(method)
    return err ? Promise.reject(err) : null
  }
  const tabRow = (id: string, over: Partial<LayoutTab> = {}): LayoutTab => ({
    id,
    workspaceId: DEFAULT_WS,
    parentId: null,
    name: null,
    colour: null,
    position: tabs.length,
    pinned: false,
    layout: 'row',
    seenAt: null,
    ...over,
  })
  const paneRow = (id: string, tabId: string, over: Partial<LayoutPane> = {}): LayoutPane => ({
    id,
    tabId,
    cwd: '',
    kind: 'local',
    endpoint: null,
    sizeShare: 1,
    ...over,
  })
  const patch = (id: string, over: Partial<LayoutTab>): LayoutTab => {
    const next = { ...tabs.find((t) => t.id === id)!, ...over }
    tabs = tabs.map((t) => (t.id === id ? next : t))
    return next
  }

  /** Every workspace a read would answer with: the default when a tab is in
   *  it, then the ones the user made, in position order. */
  const allWorkspaces = (): LayoutWorkspace[] => {
    const rows = [...made].sort((a, b) => a.position - b.position)
    return tabs.some((t) => t.workspaceId === DEFAULT_WS)
      ? [{ id: DEFAULT_WS, name: 'default', colour: null, position: 0 }, ...rows]
      : rows
  }

  return {
    rows: () => ({ tabs: [...tabs], panes: [...panes], workspaces: allWorkspaces() }),
    fail: (method, err) => failures.set(method, err),

    read: () =>
      refuse('read') ??
      Promise.resolve({
        defaultWorkspaceId: DEFAULT_WS,
        workspaces: allWorkspaces(),
        tabs: [...tabs],
        panes: [...panes],
      }),

    createWorkspace: (ws) => {
      const refused = refuse<never>('createWorkspace')
      if (refused) return refused
      // The backend refuses a workspace with no name, and so does this: a
      // fake that accepted one would let the renderer ship a blank workspace
      // and stay green.
      if (ws.name.trim() === '') return Promise.reject(new Error('name is required'))
      // The colour is stored as it ARRIVED, unjudged — the real store does
      // not police it either, and a fake that normalised it would hide a
      // renderer sending something the backend would have kept verbatim.
      const workspace = { id: ws.id, name: ws.name, colour: ws.colour, position: ws.position }
      const tab = tabRow(ws.firstTab.id, { workspaceId: ws.id, position: 0 })
      const first = paneRow(ws.firstPane.id, ws.firstTab.id, {
        cwd: ws.firstPane.cwd,
        kind: ws.firstPane.kind,
        endpoint: ws.firstPane.endpoint,
      })
      made = [...made, workspace]
      tabs = [...tabs, tab]
      panes = [...panes, first]
      return Promise.resolve({ workspace, firstTab: tab, firstPane: first, replayed: false })
    },

    recolourWorkspace: (id, colour) => {
      const refused = refuse<never>('recolourWorkspace')
      if (refused) return refused
      const target = made.find((w) => w.id === id)
      // A recolour NEVER creates — an id naming no workspace is refused,
      // exactly as the store refuses it, because a create is the only thing
      // that may fix an id.
      if (!target) return Promise.reject(new Error('no such workspace'))
      const workspace = { ...target, colour }
      made = made.map((w) => (w.id === id ? workspace : w))
      return Promise.resolve({ workspace })
    },

    closeWorkspace: (id, replacement) => {
      const refused = refuse<never>('closeWorkspace')
      if (refused) return refused
      // ONE transaction, cascading: the workspace, its tabs, their panes —
      // and the replacement when that leaves the application with no tab.
      // The default workspace is refused, as it is in the store.
      if (id === DEFAULT_WS) return Promise.reject(new Error('the default workspace is permanent'))
      const doomed = tabs.filter((t) => t.workspaceId === id).map((t) => t.id)
      tabs = tabs.filter((t) => t.workspaceId !== id)
      panes = panes.filter((p) => !doomed.includes(p.tabId))
      made = made.filter((w) => w.id !== id)
      mintIfEmpty(replacement)
      return Promise.resolve({ id })
    },

    renameWorkspace: (id, name) => {
      const refused = refuse<never>('renameWorkspace')
      if (refused) return refused
      // The backend refuses a blank workspace name, and so does this — the
      // same reason createWorkspace does: a fake that accepted one would let
      // the renderer ship a nameless workspace and stay green.
      if (name.trim() === '') return Promise.reject(new Error('name is required'))
      if (id === DEFAULT_WS) return Promise.reject(new Error('the default workspace has no name'))
      const workspace = made.find((w) => w.id === id)
      if (!workspace) return Promise.reject(new Error('no such workspace'))
      const renamed = { ...workspace, name }
      made = made.map((w) => (w.id === id ? renamed : w))
      return Promise.resolve({ workspace: renamed })
    },

    reorderWorkspaces: (ids) => {
      const refused = refuse<never>('reorderWorkspaces')
      if (refused) return refused
      // A PERMUTATION or nothing, exactly as content.ReorderWorkspaces
      // requires: a fake that accepted a subset would let the renderer send
      // one and never learn that the real store refuses it.
      const held = allWorkspaces().map((w) => w.id)
      const sorted = (xs: readonly string[]) => [...xs].sort()
      if (ids.length !== held.length || sorted(ids).some((id, i) => id !== sorted(held)[i])) {
        return Promise.reject(new Error('ids must be a permutation of the workspaces held'))
      }
      const byId = new Map(allWorkspaces().map((w) => [w.id, w]))
      const workspaces = ids.map((id, position) => ({ ...byId.get(id)!, position }))
      made = workspaces.filter((w) => w.id !== DEFAULT_WS)
      return Promise.resolve({ workspaces })
    },

    createTab: (t) => {
      const refused = refuse<never>('createTab')
      if (refused) return refused
      const tab = tabRow(t.id, { position: t.position, workspaceId: t.workspaceId })
      const first = paneRow(t.firstPane.id, t.id, {
        cwd: t.firstPane.cwd,
        kind: t.firstPane.kind,
        endpoint: t.firstPane.endpoint,
      })
      tabs = [...tabs, tab]
      panes = [...panes, first]
      return Promise.resolve({ tab, firstPane: first, replayed: false })
    },

    createPane: (p) => {
      const row = paneRow(p.id, p.tabId, { cwd: p.cwd, kind: p.kind, endpoint: p.endpoint })
      panes = [...panes, row]
      return Promise.resolve({ pane: row, replayed: false })
    },

    setPaneCwd: (id, cwd) => {
      const refusal = refuse<never>('setPaneCwd')
      if (refusal) return refusal
      panes = panes.map((p) => (p.id === id ? { ...p, cwd } : p))
      const row = panes.find((p) => p.id === id)
      if (!row) return Promise.reject(new Error(`no such pane: ${id}`))
      return Promise.resolve({ pane: row })
    },

    renameTab: (id, name) =>
      refuse<never>('renameTab') ?? Promise.resolve({ tab: patch(id, { name }) }),
    recolourTab: (id, colour) =>
      refuse<never>('recolourTab') ?? Promise.resolve({ tab: patch(id, { colour }) }),
    pinTab: (id, pinned) =>
      refuse<never>('pinTab') ?? Promise.resolve({ tab: patch(id, { pinned }) }),

    reorderTabs: (workspaceId, ids) => {
      const refused = refuse<never>('reorderTabs')
      if (refused) return refused
      // A permutation of that workspace's tabs, or nothing moves — the same
      // refusal the store makes, because a test about a refused reorder must
      // be refused for the real reason.
      const members = tabs.filter((t) => t.workspaceId === workspaceId).map((t) => t.id)
      if (ids.length !== members.length || !ids.every((id) => members.includes(id))) {
        return Promise.reject(new Error('ids must be a permutation of the workspace tabs'))
      }
      const reordered = ids.map((id, position) => patch(id, { position }))
      tabs = [...reordered, ...tabs.filter((t) => t.workspaceId !== workspaceId)]
      return Promise.resolve({ tabs: reordered })
    },

    closeTab: (id, replacement) => {
      tabs = tabs.filter((t) => t.id !== id)
      panes = panes.filter((p) => p.tabId !== id)
      mintIfEmpty(replacement)
      return Promise.resolve({ id })
    },

    closePane: (id, replacement) => {
      const refused = refuse<never>('closePane')
      if (refused) return refused
      const gone = panes.find((p) => p.id === id)
      panes = panes.filter((p) => p.id !== id)
      if (gone && !panes.some((p) => p.tabId === gone.tabId)) {
        const emptied = tabs.find((t) => t.id === gone.tabId)
        tabs = tabs.filter((t) => t.id !== gone.tabId)
        // A container exists only while it holds a member: the tab went with
        // its last pane, and its workspace goes with its last tab — except
        // the default, which is permanent.
        if (
          emptied &&
          emptied.workspaceId !== DEFAULT_WS &&
          !tabs.some((t) => t.workspaceId === emptied.workspaceId)
        ) {
          made = made.filter((w) => w.id !== emptied.workspaceId)
        }
      }
      mintIfEmpty(replacement)
      return Promise.resolve({ id })
    },
  }

  function mintIfEmpty(replacement: { tabId: string; paneId: string; cwd: string }): void {
    if (tabs.length > 0) return
    tabs = [tabRow(replacement.tabId, { position: 0 })]
    panes = [paneRow(replacement.paneId, replacement.tabId, { cwd: replacement.cwd })]
  }
}

/**
 * An in-memory UI-state document, and a real UIStateClient over it.
 *
 * The double is the SOCKET, never the client: what a restart test has to
 * watch is the renderer's own merge and the `uistate.get` on the way back, so
 * `newClient()` mints a fresh mirror over the same stored document — exactly
 * what a second launch has.
 */
export function makeUIStateBackend(seed?: Partial<UIState>): {
  newClient: () => UIStateClient
  stored: () => UIState
} {
  let doc: UIState = {
    sidebar: { collapsed: false, activeViewId: '', width: 240 },
    activeTab: '',
    ...seed,
  }
  const dispatcher = {
    call: vi.fn((method: string, params: unknown) => {
      if (method === 'uistate.get') return Promise.resolve(structuredClone(doc))
      if (method === 'uistate.set') {
        doc = structuredClone(params as UIState)
        return Promise.resolve(structuredClone(doc))
      }
      return Promise.reject(new Error(`unexpected method ${method}`))
    }),
  } as unknown as Dispatcher
  return {
    newClient: () => new UIStateClient(dispatcher),
    stored: () => structuredClone(doc),
  }
}

/** A real LayoutStore over the in-memory backend: the tests exercise the
 *  store's own rules, and only the socket is faked. */
export function makeLayoutStore(backend?: ReturnType<typeof makeLayoutBackend>): {
  store: LayoutStore
  backend: ReturnType<typeof makeLayoutBackend>
} {
  const b = backend ?? makeLayoutBackend()
  return { store: new LayoutStore(b), backend: b }
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
 * Full setup: create DOM, construct PaneManager, and open the initial tab.
 * Callers must await; the returned manager has one terminal tab active.
 */
export async function mountPaneManager(
  client?: ClientFake,
  clipboard?: ClipboardFake,
  gate?: ClipboardGate,
  banner?: BannerFake,
  layout?: ReturnType<typeof makeLayoutStore>,
  uiState?: UIStateClient,
  /** The saved connections `profiles.list` answers with. Empty is the
   *  ordinary case; a restore that must reconnect a stored endpoint through
   *  a saved connection is the test that needs one (nocx-9y4ku). */
  profiles?: SSHProfile[],
): Promise<{
  bar: HTMLElement
  panes: HTMLElement
  manager: PaneManager
  client: ClientFake
  clipboard: ClipboardFake
  gate: ClipboardGate
  banner: BannerFake
  tabStrip: import('../tab-strip').TabStrip
  layout: LayoutStore
  backend: ReturnType<typeof makeLayoutBackend>
  uiState: UIStateClient
  /** The profile client the manager was built with — `listProfiles` is a
   *  spy, so a test can assert the saved connections were read. */
  profileClient: { listProfiles: Mock; listGroups: Mock }
}> {
  const { bar, panes } = setupTabBarDOM()
  const c = client ?? makeClient()
  const cb = clipboard ?? makeClipboard()
  const g = gate ?? new (await import('../clipboard')).ClipboardGate()
  const bn = banner ?? makeBanner()
  const pc = {
    listProfiles: vi.fn().mockResolvedValue(profiles ?? []),
    listGroups: vi.fn().mockResolvedValue([]),
  }
  const l = layout ?? makeLayoutStore()
  // Read before the manager is built, exactly as the composition root does:
  // the tab that was in front is decided at boot, so the mirror has to be
  // warm before `openInitialPane` reads it.
  const ui = uiState ?? makeUIStateBackend().newClient()
  await ui.load()
  const { PaneManager } = await import('../panes')
  const { HorizontalTabStrip } = await import('../tab-strip')
  const tabStrip = new HorizontalTabStrip()
  const manager = new PaneManager(
    bar,
    bar,
    panes,
    c as unknown as import('../ipc').WSClient,
    cb,
    g,
    bn,
    pc as unknown as import('../profiles').ProfileClient,
    tabStrip,
    l.store,
    ui,
  )
  // Open the initial tab explicitly — the constructor mounts nothing.
  await manager.openInitialPane()
  return {
    bar,
    panes,
    manager,
    client: c,
    clipboard: cb,
    gate: g,
    banner: bn,
    tabStrip,
    layout: l.store,
    backend: l.backend,
    uiState: ui,
    profileClient: pc,
  }
}
