// ── PaneManager test fixtures ──────────────────────────────────────────────
//
// Centralised factories, constants and helpers so that adding a field to the
// real SessionHandle (or changing a default title) requires editing exactly
// ONE place in test-support instead of chasing N copies through the suite.
//
// See AD-7: sessionId is server-authoritative, cwd is set once at session
// open. The fake must carry both.
import type { PaneIdentity } from '../terminal-content'
import type { OpenAnchor, SessionIntentKind, SessionAttachment, WSClient } from '../ipc'
import { vi, type Mock } from 'vitest'
import type { SessionFrame } from '../generated/session.frame'
import type { SessionEffect } from '../generated/session.effect'
import type { SessionIntentResult } from '../generated/session.intent'
import type { SessionSignal } from '../generated/session.signal'
import type { SessionOutputGap } from '../generated/session.outputGap'
import type { SessionRecovery, SessionSize } from '../ipc'
import type { BlockOutputStart } from '../generated/block.outputStart'
import type { SessionIntegrationChanged } from '../generated/session.integrationChanged'
import { LayoutStore } from '../layout/layout-store'
import { UIStateClient } from '../uistate-client'
import type { UIState } from '../generated/uistate'
import { RpcError, type Dispatcher } from '../dispatcher'
import type { LayoutClientLike } from '../layout/layout-client'
import type {
  Tab as LayoutTab,
  Pane as LayoutPane,
  Workspace as LayoutWorkspace,
} from '../generated/layout.read'
import { type ClipboardAccess, type ClipboardGate } from '../clipboard'
import type { ClipboardBanner } from '../banner'
import type { SSHProfile } from '../profiles'
import type { PaneManager } from '../panes'
import type { DesiredMode } from '../capability'
import type { SessionLiveness } from '../generated/session.liveness'
import type { SessionEntry } from '../generated/sessions.inventory'
import type { SessionObservationChanged } from '../generated/session.observationChanged'
import type { DriverState } from '../pane-observation'
import type { WorkersTabCreated } from '../generated/workers.tabCreated'
import type { WorkersTabClosed } from '../generated/workers.tabClosed'
import type { Open } from '../generated/open'

// ═══════════════════════════════════════════════════════════════════════════
// Constants — every assertion must derive from these, never repeat the literal.
// ═══════════════════════════════════════════════════════════════════════════

/** The cwd every session reports by default. */
export const FIXTURE_CWD = '~/Documents/repos/nocx'

/** The tab label produced by directoryLabel(FIXTURE_CWD). */
export const FIXTURE_DIRECTORY_LABEL = 'repos/nocx'

/** One scripted socket for tests that must cross the real Dispatcher/WSClient
 * boundary. Keep this transport fake here so pane and IPC tests share one
 * wire harness rather than growing competing socket implementations. */
export class MockWebSocket {
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
  onmessage: ((event: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null

  private _listeners = new Map<string, Set<(...args: unknown[]) => void>>()

  addEventListener(type: string, fn: (...args: unknown[]) => void): void {
    let s = this._listeners.get(type)
    if (!s) {
      s = new Set()
      this._listeners.set(type, s)
    }
    s.add(fn)
  }

  removeEventListener(type: string, fn: (...args: unknown[]) => void): void {
    this._listeners.get(type)?.delete(fn)
  }

  private _fire(type: string, event?: unknown): void {
    for (const fn of this._listeners.get(type) ?? []) {
      fn(event)
    }
  }

  constructor(readonly url: string) {
    MockWebSocket.last = this
  }

  send(data: string | ArrayBuffer): void {
    this.sent.push(data)
  }

  close(): void {
    this.closeCalled = true
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  serverAccepts(): void {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
    this._fire('open')
  }

  serverFails(): void {
    this.onerror?.()
  }

  serverHangsUp(): void {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
    this._fire('close')
  }

  deliverText(payload: unknown): void {
    const event = { data: typeof payload === 'string' ? payload : JSON.stringify(payload) }
    this.onmessage?.(event)
    this._fire('message', event)
  }

  deliverBinary(data: ArrayBuffer): void {
    const event = { data }
    this.onmessage?.(event)
    this._fire('message', event)
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

// ═══════════════════════════════════════════════════════════════════════════
/** The live-content-height measurer used by scrollback controller tests. */
export type LiveContentHeightSpy = Mock<() => number | null>

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
  /** The open ack's own `awaitsIntegration` (nocx-ui8q6.6): whether this
   *  session has just entered the axis `starting`, before any
   *  session.integrationChanged fact exists. False by default — most
   *  fixtures are about something else and a session that defaulted to
   *  "waiting" would hide the grid every test that never touches this axis
   *  would otherwise not care about. A test proving the gap-close sets it
   *  true and fires `integrationHandler` afterward, exactly as production
   *  order requires. */
  awaitsIntegration: boolean
  send: ReturnType<typeof vi.fn>
  sendResize: Mock<(size: SessionSize) => boolean>
  resizeApplied: Mock<(size: SessionSize) => Promise<'applied' | 'superseded' | 'closed'>>
  /** Hand one session intent to the pane's helper (nocx-zg3k3.3.1). What a
   *  person typed, pasted or clicked lands here now, as the kind and payload
   *  the wire carries — never as bytes. Resolves `executed` by default: the
   *  case a test that is not about a refusal wants, and the same default
   *  `signal` uses. */
  intent: Mock<(kind: SessionIntentKind, payload: string) => Promise<SessionIntentResult>>
  /** Address a signal to the command running in this session (nocx-23rph).
   *  Resolves `delivered` by default — the case a test that is not about
   *  the refusal wants; a test that IS overrides the mock. */
  signal: Mock<(signal: SessionSignal['signal']) => Promise<SessionSignal>>
  detach: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onAttachment: (cb: (attachment: SessionAttachment) => void) => void
  fireAttachment(attachment: SessionAttachment): void
  retryAttach: Mock<() => Promise<boolean>>
  /** Subscribe to the latest integration fact, matching SessionHandle. */
  onIntegrationChanged: (handler: (fact: SessionIntegrationChanged) => void) => () => void
  /** Fire the integration fact seen by a mounted pane. */
  fireIntegrationChanged(fact: SessionIntegrationChanged): void
  onExit: ReturnType<typeof vi.fn>
  onReset: ReturnType<typeof vi.fn>
  onOutputGap: ReturnType<typeof vi.fn>
  /** Fire the registered output-gap callback. */
  fireOutputGap(gap: SessionOutputGap): void
  /** The recording's own revision of what it still holds
   *  (session.recoveryChanged, nocx-zg3k3.14.24). The handle owns the fact,
   *  so firing it REPLACES `recovered` before telling the surface — exactly
   *  what SessionHandle.onRecoveryChanged does, and what makes a card that
   *  reads `recovered` again the behaviour under test. */
  onRecoveryChanged: (cb: () => void) => () => void
  /** Fire one revision of the recovery account, as the backend would. */
  fireRecoveryChanged(recovery: SessionRecovery): void
  onProtocolError: ReturnType<typeof vi.fn>
  onInputStalled: ReturnType<typeof vi.fn>
  /** The reachability axis (nocx-iarf9): the backend's revised belief about
   *  reaching this session. */
  onLiveness: ReturnType<typeof vi.fn>
  /** What an enrolled pane's driver says its screen is inviting
   *  (nocx-szb40.3). */
  onObservation: ReturnType<typeof vi.fn>
  /** The screen plane (nocx-zg3k3.2.8): one parsed session.frame document
   *  per metadata frame the backend publishes. */
  onScreenFrame: ReturnType<typeof vi.fn>
  onEffect: ReturnType<typeof vi.fn>
  /** Fire the registered output-protocol error callback. */
  fireProtocolError(message: string): void
  /** Fire the registered runtime effect callback. */
  fireEffect(effect: SessionEffect): void
  /** Fire the registered screen-frame callback with one document. */
  fireScreenFrame(frame: SessionFrame): void
  /** The live tier's page seam (nocx-zg3k3.10.4): rows documents arrive
   *  on the carrier the surface registers here. */
  onHistoryPageRows: ReturnType<typeof vi.fn>
  /** One page of the live history, as session.historyPage answers it. The
   *  default is the empty head read of an empty history — inert to every
   *  pane behavior that does not scroll. */
  historyPage: ReturnType<typeof vi.fn>
  /** What a reclaim recovered before it attached (ipc.SessionRecovery), or
   *  null for a fresh handle that was opened rather than taken back. `size`
   *  is the grid the BACKEND says the session runs at — the one the recovered
   *  bytes were wrapped at (nocx-eidfb.3). Mirrors SessionHandle exactly. */
  recovered: SessionRecovery | null
  /** Fire the registered liveness callback with one observation. */
  fireLiveness(liveness: 'alive' | 'unknown', livenessEpoch?: number): void
  /** Fire a liveness observation whose round trip the BACKEND graded slow.
   *  Separate from fireLiveness because the grade is a different fact from
   *  the value, and a test asking for one should not have to state the
   *  other. */
  fireSlowLiveness(livenessEpoch?: number): void
  /** Fire the registered pane-observation callback with one classification. */
  fireObservation(state: DriverState, agent?: string): void
}

/**
 * Create a fake SessionHandle with sensible defaults.
 *
 * Override any property per-test — the default cwd comes from FIXTURE_CWD
 * so a test that just needs a differently-named directory can pass
 * `{ cwd: '~/other' }` without repeating every other field.
 */
export function makeSession(overrides?: Partial<SessionFake>): SessionFake {
  let screenCb: ((frame: SessionFrame) => void) | null = null
  let effectCb: ((effect: SessionEffect) => void) | null = null
  let protocolErrorCb: ((message: string) => void) | null = null
  let attachment: SessionAttachment = { kind: 'attached' }
  let attachmentCb: ((attachment: SessionAttachment) => void) | null = null
  let outputGapCb: ((gap: SessionOutputGap) => void) | null = null
  let integrationCb: ((fact: SessionIntegrationChanged) => void) | null = null
  let integrationFact: SessionIntegrationChanged | null = null
  let livenessCb: ((l: SessionLiveness) => void) | null = null
  let observationCb: ((o: SessionObservationChanged) => void) | null = null
  const sessionId = `mock-sid-${++sessionCounter}`
  let recoveryChangedCb: (() => void) | null = null
  const handle: SessionFake = {
    sessionId,
    cwd: FIXTURE_CWD,
    desiredMode: 'script',
    parent: null,
    awaitsIntegration: false,
    recovered: null,
    send: vi.fn(),
    sendResize: vi.fn<(size: SessionSize) => boolean>(),
    resizeApplied: vi.fn(() => Promise.resolve('applied' as const)),
    intent: vi.fn<(kind: SessionIntentKind, payload: string) => Promise<SessionIntentResult>>(() =>
      Promise.resolve({ state: 'executed', bytesWritten: 1, fenceAfter: 0 }),
    ),
    // The signal is ECHOED back, exactly as the wire echoes it: a fixture
    // that always answered 'interrupt' would let a caller that asked for
    // 'stop' pass unnoticed.
    signal: vi.fn<(signal: SessionSignal['signal']) => Promise<SessionSignal>>((signal) =>
      Promise.resolve({ signal, outcome: 'delivered' }),
    ),
    detach: vi.fn(),
    close: vi.fn(),
    onAttachment: (cb) => {
      attachmentCb = cb
      cb(attachment)
    },
    fireAttachment: (next) => {
      attachment = next
      attachmentCb?.(next)
    },
    retryAttach: vi.fn(() => Promise.resolve(true)),
    onIntegrationChanged: vi.fn((cb: (fact: SessionIntegrationChanged) => void) => {
      integrationCb = cb
      if (integrationFact) cb(integrationFact)
      return () => {
        if (integrationCb === cb) integrationCb = null
      }
    }),
    fireIntegrationChanged: (fact: SessionIntegrationChanged) => {
      integrationFact = fact
      integrationCb?.(fact)
    },
    onExit: vi.fn(),
    onReset: vi.fn(),
    onOutputGap: vi.fn((cb: (gap: SessionOutputGap) => void) => {
      outputGapCb = cb
    }),
    fireOutputGap: (gap: SessionOutputGap) => outputGapCb?.(gap),
    onRecoveryChanged: (cb: () => void) => {
      recoveryChangedCb = cb
      return () => {
        if (recoveryChangedCb === cb) recoveryChangedCb = null
      }
    },
    fireRecoveryChanged: (recovery: SessionRecovery) => {
      handle.recovered = recovery
      recoveryChangedCb?.()
    },
    onProtocolError: vi.fn((cb: (message: string) => void) => {
      protocolErrorCb = cb
    }),
    onInputStalled: vi.fn(),
    onLiveness: vi.fn((cb: (l: SessionLiveness) => void) => {
      livenessCb = cb
    }),
    onObservation: vi.fn((cb: (o: SessionObservationChanged) => void) => {
      observationCb = cb
    }),
    onScreenFrame: vi.fn((cb: (frame: SessionFrame) => void) => {
      screenCb = cb
    }),
    onEffect: vi.fn((cb: (effect: SessionEffect) => void) => {
      effectCb = cb
    }),
    fireEffect: (effect: SessionEffect) => effectCb?.(effect),
    fireProtocolError: (message: string) => protocolErrorCb?.(message),
    // The live tier's page seam (nocx-zg3k3.10.4): the default answers the
    // head read with the empty page of an empty history, so a pane test
    // that never scrolls never pages.
    onHistoryPageRows: vi.fn(),
    historyPage: vi.fn(() =>
      Promise.resolve({
        pageId: 'a1b2c3d4e5f6a7b8a1b2c3d4e5f6a7b8',
        start: 0,
        end: 0,
        floor: 0,
        more: false,
        durableThrough: null,
        bufferKind: 'normal',
        bufferInstance: '1',
        baseRow: 0,
        cols: 80,
        rows: 24,
      }),
    ),
    fireObservation: (state: DriverState, agent = 'claude') => {
      observationCb?.({
        sessionId,
        instanceId: 'fedcba9876543210fedcba9876543210',
        sessionEpoch: 1,
        agent,
        state,
        progress: 'moving',
      })
    },
    fireScreenFrame: (frame: SessionFrame) => {
      screenCb?.(frame)
    },
    fireLiveness: (liveness: 'alive' | 'unknown', livenessEpoch = 2) => {
      livenessCb?.({
        sessionId,
        instanceId: 'fedcba9876543210fedcba9876543210',
        sessionEpoch: 1,
        liveness,
        livenessEpoch,
        observedAt: '2026-08-17T10:00:00Z',
        roundTripMs: 12,
      })
    },
    fireSlowLiveness: (livenessEpoch = 2) => {
      livenessCb?.({
        sessionId,
        instanceId: 'fedcba9876543210fedcba9876543210',
        sessionEpoch: 1,
        liveness: 'alive',
        livenessEpoch,
        observedAt: '2026-08-17T10:00:00Z',
        slow: true,
        roundTripMs: 900,
      })
    },
    ...overrides,
  }
  return handle
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
  call: Mock<WSClient['call']>
}

export interface ClientFake {
  connect: ReturnType<typeof vi.fn>
  openSession: Mock<(size: SessionSize, anchor?: OpenAnchor) => Promise<SessionFake>>
  openSSHSession: ReturnType<typeof vi.fn>
  openSSHSessionByHost: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  sendToSession: ReturnType<typeof vi.fn>
  sendResize: ReturnType<typeof vi.fn>
  detachSession: ReturnType<typeof vi.fn>
  closeSession: ReturnType<typeof vi.fn>
  onSessionExit: ReturnType<typeof vi.fn>
  onSessionReset: ReturnType<typeof vi.fn>
  onBlockOutputStart: (cb: (anchor: BlockOutputStart) => void) => () => void
  _fireBlockOutputStart: (anchor: BlockOutputStart) => void
  /** Control-plane calls (history.record, history.query, …). Rejects by
   *  default — the no-store state, which the recall overlay labels
   *  source=session. */
  call: Mock<WSClient['call']>
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
  /** Another client took a session this window was holding (nocx-oevq4, D8).
   *  Returns an unsubscribe, like the real one. */
  onSessionDisplaced: ReturnType<typeof vi.fn>
  /** Deliver that news to every subscriber, so a test can drive the take the
   *  way a second window does. */
  _fireDisplaced: (d?: { sessionId: string; instanceId: string; sessionEpoch: number }) => void
  /** A worker participant's tab has appeared (nocx-ui8q6.3). Returns an
   *  unsubscribe, like the real one. */
  onWorkerTabCreated: ReturnType<typeof vi.fn>
  /** Deliver that fact to every subscriber, the way workers.tabCreated does
   *  off the real socket. */
  _fireWorkerTabCreated: (fact: WorkersTabCreated) => void
  /** A worker participant's tab has LEFT the window (nocx-xn63t.4.6), closed
   *  on the backend by workers.close. The real method's own signature, so the
   *  fixture cannot drift from what a surface is handed. */
  onWorkerTabClosed: (cb: (fact: WorkersTabClosed) => void) => () => void
  /** Deliver that fact to every subscriber, the way workers.tabClosed does
   *  off the real socket. */
  _fireWorkerTabClosed: (fact: WorkersTabClosed) => void
  readonly connected: boolean
  /** What the coordinator is still running, asked once before the chain is
   *  drawn (design D5). Answers an empty list by default, which is a cold
   *  backend and the ordinary case; a restore test that means to RECLAIM a
   *  pane overrides it with an entry naming that pane. */
  listLiveSessions: ReturnType<typeof vi.fn>
  /** Helper-owned inventory returned to the overview reader. */
  listHelperSessions: ReturnType<typeof vi.fn>
  /** Take one of those back. Answers a fresh session by default, so a pane
   *  that adopts one still has a handle to drive. */
  reclaimSession: Mock<(entry: Parameters<WSClient['reclaimSession']>[0]) => Promise<SessionFake>>
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

/** Deliver the latest integration fact through the session handle's subscription seam. */
export function integrationHandler(
  client: ClientFake,
  sessionOverride?: SessionFake,
): (params: unknown) => void {
  return (params) => {
    if (!params || typeof params !== 'object') {
      throw new Error('invalid session.integrationChanged fact')
    }
    const sessionId = (params as Record<string, unknown>).sessionId
    const session =
      client._sessions.find((candidate) => candidate.sessionId === sessionId) ??
      (sessionOverride?.sessionId === sessionId ? sessionOverride : undefined)
    if (!session)
      throw new Error(`no fake session for session.integrationChanged: ${String(sessionId)}`)
    session.fireIntegrationChanged(params as SessionIntegrationChanged)
  }
}

/** The session.signalUndelivered handler: the fact that a Stop the pane was
 *  told was held will not reach its command (nocx-zas0d). Tests describe the
 *  fact body; the framing is this helper's. */
export function signalUndeliveredHandler(client: ClientFake): (params: unknown) => void {
  return notificationHandler(client, 'session.signalUndelivered')
}

/** The session.publishFailed handler (nocx-1kejz.4): the backend's word that
 *  this pane's shell-integration bundle was not copied to the host. Tests
 *  describe the fact body; the framing is this helper's. */
export function publishFailedHandler(client: ClientFake): (params: unknown) => void {
  return notificationHandler(client, 'session.publishFailed')
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
/** The backend's block.closed (contracts/block.closed.schema.json): the one
 *  event a finished command's block closes on (nocx-2v80t.3.27). Tests name
 *  the entry; `kept` defaults to false — no rows to fetch — so a test that is
 *  not about stored rows does not have to fake a store read. */
export function blockClosedHandler(client: ClientFake): (entryId: string, kept?: boolean) => void {
  const deliver = notificationHandler(client, 'block.closed')
  return (entryId: string, kept = false): void => deliver({ entryId, kept })
}

/** The backend-owned history receipt notification. */
export function historyRecordedHandler(
  client: ClientFake,
  sessionId = client._sessions[0]?.sessionId,
): (params: unknown) => void {
  if (!sessionId) throw new Error('no session available for history.recorded')
  const deliver = notificationHandler(client, 'history.recorded')
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
  type DisplacedFact = { sessionId: string; instanceId: string; sessionEpoch: number }
  const displacedHandlers = new Set<(d: DisplacedFact) => void>()
  const workerTabCreatedHandlers = new Set<(fact: WorkersTabCreated) => void>()
  const workerTabClosedHandlers = new Set<(fact: WorkersTabClosed) => void>()
  const blockOutputStartHandlers = new Set<(anchor: BlockOutputStart) => void>()
  const newSession = (): SessionFake => {
    const s = makeSession()
    sessions.push(s)
    return s
  }
  const client: ClientFake = {
    connect: vi.fn().mockResolvedValue(undefined),
    openSession: vi.fn<(size: SessionSize, anchor?: OpenAnchor) => Promise<SessionFake>>(() =>
      Promise.resolve(newSession()),
    ),
    openSSHSession: vi.fn(() => Promise.resolve(newSession())),
    openSSHSessionByHost: vi.fn(() => Promise.resolve(newSession())),
    listLiveSessions: vi.fn(() => Promise.resolve([])),
    reclaimSession: vi.fn<
      (entry: Parameters<WSClient['reclaimSession']>[0]) => Promise<SessionFake>
    >(() => Promise.resolve(newSession())),
    listHelperSessions: vi.fn(() => Promise.resolve([] as SessionEntry[])),
    close: vi.fn(),
    sendToSession: vi.fn(),
    sendResize: vi.fn(),
    detachSession: vi.fn(),
    closeSession: vi.fn(),
    onSessionExit: vi.fn(),
    onSessionReset: vi.fn(),
    onBlockOutputStart: (cb) => {
      blockOutputStartHandlers.add(cb)
      return () => blockOutputStartHandlers.delete(cb)
    },
    _fireBlockOutputStart: (anchor) => {
      for (const cb of [...blockOutputStartHandlers]) cb(anchor)
    },
    notifyPaneClosed: vi.fn(),
    onReconnectResult: vi.fn((cb: (r: { resumed: number; lost: number }) => void) => {
      reconnectHandlers.add(cb)
      return () => reconnectHandlers.delete(cb)
    }),
    _fireReconnect: (r = { resumed: 0, lost: 0 }) => {
      for (const cb of [...reconnectHandlers]) cb(r)
    },
    onSessionDisplaced: vi.fn((cb: (d: DisplacedFact) => void) => {
      displacedHandlers.add(cb)
      return () => displacedHandlers.delete(cb)
    }),
    _fireDisplaced: (
      d = {
        sessionId: 'sess-0',
        instanceId: 'fedcba9876543210fedcba9876543210',
        sessionEpoch: 1,
      },
    ) => {
      for (const cb of [...displacedHandlers]) cb(d)
    },
    onWorkerTabCreated: vi.fn((cb: (fact: WorkersTabCreated) => void) => {
      workerTabCreatedHandlers.add(cb)
      return () => workerTabCreatedHandlers.delete(cb)
    }),
    _fireWorkerTabCreated: (fact: WorkersTabCreated) => {
      for (const cb of [...workerTabCreatedHandlers]) cb(fact)
    },
    onWorkerTabClosed: (cb: (fact: WorkersTabClosed) => void) => {
      workerTabClosedHandlers.add(cb)
      return () => workerTabClosedHandlers.delete(cb)
    },
    _fireWorkerTabClosed: (fact: WorkersTabClosed) => {
      for (const cb of [...workerTabClosedHandlers]) cb(fact)
    },
    dispatcher: {
      subscribe: vi.fn(() => () => undefined),
      // A live prompt opens the attempt before the pty write; the default
      // resolves so the write always proceeds (fail-open).
      call: vi.fn<WSClient['call']>().mockResolvedValue({
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
    // What the backend answers when no content store is wired: a JSON-RPC
    // -32601, not a transport failure — a reader that tells "the store
    // keeps nothing" from "the read failed" (restore-client's
    // blockRowsForEntry, nocx-2v80t.3.27) must see the same refusal here.
    call: vi
      .fn<WSClient['call']>()
      .mockRejectedValue(new RpcError('no store wired (fake)', -32601)),
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
  readText: Mock<ClipboardAccess['readText']>
  writeText: Mock<ClipboardAccess['writeText']>
}

/**
 * Create a fake clipboard whose readText / writeText are vitest spies.
 * Used by clipboard policy tests in tabs.test.ts.
 */
export function makeClipboard(overrides?: Partial<ClipboardFake>): ClipboardFake {
  return {
    readText: vi.fn<ClipboardAccess['readText']>().mockResolvedValue(''),
    writeText: vi.fn<ClipboardAccess['writeText']>().mockResolvedValue(undefined),
    ...overrides,
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Banner fake — injectable into PaneManager for gate-layer tests.
// ═══════════════════════════════════════════════════════════════════════════

export interface BannerFake extends ClipboardBanner {
  shown: boolean
  show: Mock<ClipboardBanner['show']>
}

/**
 * Create a fake banner whose show() returns a controllable promise.
 * Override shown / show per-test to drive the gate policy.
 */
export function makeBanner(overrides?: Partial<BannerFake>): BannerFake {
  return {
    shown: false,
    show: vi.fn<ClipboardBanner['show']>().mockResolvedValue('dismiss'),
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
      const tab = tabRow(t.id, { position: 0, workspaceId: t.workspaceId })
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
