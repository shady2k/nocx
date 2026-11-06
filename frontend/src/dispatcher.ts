// Control-plane dispatcher: owns request-ID allocation, pending-request
// correlation, notification routing, disconnect/reconnect behaviour, and
// typed subscribe/unsubscribe.  WSClient and ProfileClient consume it.

import type {
  Endpoint,
  EndpointFailure,
  EndpointFailureKind,
  EndpointProvider,
  EndpointResult,
} from './endpoint'
import type { ControlSaturated } from './generated/control.saturated'
import type { TransportPingResult } from './generated/transport.ping'
import type { ControlSaturatedNotification } from './generated/control.saturated.notification'
import { log } from './log'
import type { MutationReplayed } from './generated/mutation.replayed'
import {
  MutationNotReplayedError,
  isMutationMethod,
  newMutationIdentity,
  replayedAssessment,
  resolveMutationOutcome,
} from './mutations'
export type NotificationHandler = (params: unknown) => void
export type LifecycleHandler = () => void

/**
 * RpcError carries a JSON-RPC error response intact: its `code` and, crucially,
 * its `data`.
 *
 * The dispatcher used to reject with a plain `Error` built from `message`
 * alone, which threw `data` away. That is fine while the only thing a surface
 * does with a failure is show its text, and wrong the moment the backend needs
 * to tell the UI *which action to offer* — "start a Secret Service", "unlock
 * your login keychain" and "retry" are three different remedies behind one
 * "provider unavailable" message. Recovering that distinction by matching on
 * message text is exactly the brittleness a machine-readable discriminator
 * exists to avoid.
 *
 * It extends Error, so every existing `catch` that reads `.message` or checks
 * `instanceof Error` keeps working unchanged.
 */
export class RpcError extends Error {
  constructor(
    message: string,
    readonly code?: number,
    readonly data?: unknown,
  ) {
    super(message)
    this.name = 'RpcError'
  }
}

/** A vault prompt was dismissed by the person who initiated the operation. */
export class VaultOperationCancelledError extends Error {
  constructor() {
    super('Vault operation cancelled')
    this.name = 'VaultOperationCancelledError'
  }
}

/** Who asked for a call (ADR-8eej3e66 §2).
 *
 *  A PERSON's call is one they pressed something for: a button, a row, a
 *  refresh. A BACKGROUND call is work a surface does on its own behalf — a
 *  store scoping itself to the tab in front, a watcher, a poll, a carrier —
 *  and it is the origin that decides whether a refusal may become anything a
 *  person sees. Absent, the origin is `person`: the side on which an error
 *  cannot be silenced without somebody having decided so.
 *
 *  Renderer only at this stage. The wire carries it once the operation
 *  catalogue exists (stage 2 of that record's migration), and until then the
 *  backend treats every request as a person's, exactly as absent means here.
 */
export type CallOrigin = 'person' | 'background'

/** What a caller may say about its invocation. `intent` is the vault's
 *  operation/report distinction (ADR-0032); `origin` is who is asking. */
export interface CallOptions {
  intent?: RpcIntent
  origin?: CallOrigin
}

/** The wire's fixed reason for a refused control request
 *  (contracts/control.saturated.schema.json). */
const SATURATION_REASON = 'control-saturated'

/**
 * WHAT THE REFUSAL WAS FOR, in the person's words (nocx-3an1s.1.1).
 *
 * A refusal carries `scope`: the backend admission that refused the work, set
 * from server vocabulary at the composition root and never from request text.
 * The owner met "The terminal is busy" while no command of theirs was running,
 * which is exactly what a background write in another surface looks like, so
 * naming the domain is the least a refusal can do.
 *
 * An unknown scope names no domain rather than inventing a cause; a new
 * admission is a new row here, deliberately.
 */
const REFUSAL_DOMAINS: Record<string, string> = {
  config: 'settings',
  session: 'terminal session',
  vault: 'vault',
  content: 'command record',
  git: 'git',
  filesystem: 'file',
  api: 'API',
  // The execution lane every admission-backed control method shares
  // (internal/transport/ws.go's control.NewSemaphore("control", …)). It is
  // the scope a stuck pane open takes eight of, and until it was named the
  // owner met exactly that with nothing they could name (nocx-398ca).
  control: 'terminal',
}

/**
 * The sentence a refused call rejects with (ADR-8eej3e66 §8, stage 1).
 *
 * It says what happened and what was at its limit, and nothing else: no
 * "busy", no "try again in a moment". The remedy belongs to the surface that
 * asked — its own refused state and its own retry — because a sentence shared
 * by every surface cannot know what the person was doing or whether asking
 * again is even theirs to do.
 *
 * This is the ONE owner of the refusal's words. A surface that renders
 * `err.message` therefore cannot inherit the wire's own "Control plane busy",
 * and two spellings of one fact cannot drift apart.
 */
export function refusalSentence(scope?: string): string {
  const domain = scope === undefined ? undefined : REFUSAL_DOMAINS[scope]
  return domain === undefined
    ? 'The request was refused — nocx was already running as many operations as it allows.'
    : `The request was refused — nocx was already running as many ${domain} operations as it allows.`
}

/** The control-plane notification a full refreshable outbound queue sends
 *  through its one reserved slot, after dropping the frame that overflowed it
 *  (outbound.StallNoticeMethod). It carries no params: the fact that frames
 *  were dropped is the whole message. */
const OUTBOUND_STALLED = 'outbound.stalled'

/**
 * True when `data` is the error payload of a refused control request
 * (contracts/control.saturated.schema.json). Only the fixed `reason`
 * discriminates, so a payload field may grow without the class being lost.
 */
function isSaturationData(data: unknown): data is Pick<ControlSaturated, 'reason'> {
  return (
    typeof data === 'object' &&
    data !== null &&
    'reason' in data &&
    data.reason === SATURATION_REASON
  )
}

/**
 * True when `err` is a control request the backend refused because the
 * control plane was saturated — transient by contract (`retryable`), so a
 * surface may offer the person the same action again.
 *
 * The dispatcher no longer shows anything for a refusal (ADR-8eej3e66 stage
 * 1): this is how a surface recognises the one class of failure whose remedy
 * is its own retry, through the wire's discriminator rather than a second
 * reading of the message.
 */
export function isSaturationRefusal(err: unknown): boolean {
  return err instanceof RpcError && isSaturationData(err.data)
}

// Reconnect backoff: start at 250 ms, double each attempt, cap at 5 s.
// Jitter of up to 50 % of the current backoff is added so a reload storm
// from many clients does not synchronise onto the server.
const MIN_BACKOFF_MS = 250
const MAX_BACKOFF_MS = 5000

/**
 * An idle control socket is probed after 15 s, long enough to avoid adding
 * chatter while a user is active. A response has 5 s to arrive; closing then
 * deliberately reuses the existing WebSocket close path.
 */
export const HEARTBEAT_IDLE_WINDOW_MS = 15_000
const HEARTBEAT_RESPONSE_TIMEOUT_MS = 5_000

/**
 * `connecting` has no face of its own — the overlay's `canRetry` draws Retry
 * only for `waiting` and `blocked` — because an attempt already in flight
 * has a person's own click to wait for. That is only true while something is
 * actually moving it: a coordinator-discovery call that never settles, or a
 * WebSocket that never fires `open`, `error` or `close`, left `connecting`
 * with no exit at all, Retry hidden by design and nothing else to move it.
 * The deadline is what the heartbeat and the ssh keepalive already do for
 * the same shape of problem — name the hang a loss instead of waiting on it
 * forever (nocx-y6fh7) — applied to the one place in this state machine that
 * still waited without one (nocx-oez54, nocx-61k0g).
 */
export const CONNECT_ATTEMPT_TIMEOUT_MS = 10_000

type TimerHandle = ReturnType<typeof setTimeout>

/** What the caller means by its request, in the vocabulary ADR-0032's
 *  amendment already gave the resolver: an OPERATION wants vault material and
 *  may raise the unlock, while a REPORT is a passive read whose sealed answer
 *  is a fact to show the person, never a prompt to interrupt them. Renderer
 *  only — the intent never crosses the control plane. */
export type RpcIntent = 'operation' | 'report'

interface PendingCall {
  resolve: (v: unknown) => void
  reject: (e: Error) => void
  // The request itself, so a sealed response can be re-sent verbatim (with a
  // fresh id) after the vault layer raises the unlock prompt.
  method: string
  params: unknown
  intent: RpcIntent
  /** Who asked. Carried on the call so a refusal can be recorded against the
   *  invocation that caused it, and so the paths that must stay quiet can
   *  recognise their own work (ADR-8eej3e66 §2). */
  origin: CallOrigin
  sealedRetried: boolean
  sent: boolean
  settled: boolean
  /** The identity this mutating call carries (ADR-8eej3e66 §4). Absent on a
   *  read: the catalogue decides, not the caller. */
  mutation?: string
  /** The id the Operations row was announced under; it survives a sealed
   *  retry, which changes currentID. */
  observeID: number
  /** The withdrawal the row's Cancel calls. */
  abort?: () => void
  /** Settlement is announced at most once. */
  settledNotified?: boolean
  /** The id the BACKEND is working on. A vault-sealed retry re-sends the
   *  request under a fresh id, and a withdrawal must name that one. */
  currentID: number
  signal?: AbortSignal
  onAbort?: () => void
  removeAbortListener?: () => void
}

/** One call a person made, as the Operations source sees it. `cancel` is the
 *  same withdrawal the caller's own AbortSignal performs. */
export interface PendingCallView {
  readonly id: number
  readonly method: string
  readonly startedAt: number
  /** The mutation identity the call carries, or absent for a read. A surface
   *  that shows what became of a call asks with this. */
  readonly mutation?: string
  cancel: () => void
}

/** How a person's call ended, as the Operations source needs to hear it. */
export type CallSettlement =
  /** The backend answered: a result or an error. */
  | 'answered'
  /** The renderer's own half ended with no answer — withdrawn, or the socket
   *  went. For a mutation this is the moment the question starts. */
  | 'no-answer'

function abortError(): Error {
  const err = new Error('The operation was aborted')
  err.name = 'AbortError'
  return err
}

/** One request frame. The mutation identity rides the ENVELOPE, beside the id
 *  and the method, because it is a fact about the invocation and not a param
 *  any handler should see (ADR-8eej3e66 §4). */
/** The data of the -32005 refusal, when this error is one. Code, not message
 *  text: the code is the wire's discriminator, the reason is the second half. */
function replayedPayload(code: number | undefined, data: unknown): MutationReplayed | null {
  if (code !== -32005 || data === null || typeof data !== 'object') return null
  const payload = data as { reason?: unknown; outcome?: unknown; operation?: unknown }
  if (payload.reason !== 'mutation-replayed') return null
  if (typeof payload.outcome !== 'string' || typeof payload.operation !== 'string') return null
  return {
    reason: 'mutation-replayed',
    outcome: payload.outcome as MutationReplayed['outcome'],
    operation: payload.operation,
  }
}

function frameFor(id: number, pending: PendingCall): string {
  const frame: Record<string, unknown> = {
    jsonrpc: '2.0',
    id,
    method: pending.method,
    params: pending.params,
  }
  if (pending.mutation !== undefined) frame.mutation = pending.mutation
  return JSON.stringify(frame)
}

export type ConnectionState =
  | { kind: 'connecting' }
  | { kind: 'online' }
  | { kind: 'waiting'; backoffMs: number }
  | { kind: 'blocked'; failure: EndpointFailure }

export class Dispatcher {
  private ws: WebSocket | null = null
  private nextID = 1
  private pending = new Map<number, PendingCall>()
  private subscribers = new Map<string, Set<NotificationHandler>>()

  // Lifecycle subscribers.
  private connectHandlers = new Set<LifecycleHandler>()
  private disconnectHandlers = new Set<LifecycleHandler>()
  private connectionStateHandlers = new Set<(state: ConnectionState) => void>()

  // Reconnect state.
  private _connectionState: ConnectionState = { kind: 'waiting', backoffMs: MIN_BACKOFF_MS }
  private _closingDeliberately = false
  private _backoffMs = MIN_BACKOFF_MS
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private _started = false
  private _attemptInFlight = false
  /** Bumped on every new attempt and on every give-up. A callback from an
   *  earlier attempt compares its captured value against this one to tell
   *  whether it still owns the state it is about to touch. */
  private _attemptEpoch = 0

  private _heartbeatIdleTimer: TimerHandle | null = null
  private _heartbeatResponseTimer: TimerHandle | null = null
  private _heartbeatSocket: WebSocket | null = null
  private _heartbeatRequestID: number | null = null

  constructor(private readonly provider: EndpointProvider) {
    this.subscribe('control.saturated', (params: unknown) => {
      // A refused NOTIFICATION: no id, so no response to carry the -32004
      // error, and the server says it here instead. Nobody's promise is
      // rejected for it and nobody pressed anything for it, so the whole
      // debt of this frame is the record (ADR-8eej3e66 stage 1 removed the
      // toast; the schema and this subscription go in its last stage).
      //
      // Consumes the generated params type — the contract file must stay
      // reachable from main(): `methodClass` and `scope` are the server's
      // coarse class and admission name, never request text.
      const note = params as ControlSaturatedNotification
      log.warn('nocx: control action refused', {
        methodClass: typeof note.methodClass === 'string' ? note.methodClass : '',
        scope: typeof note.scope === 'string' ? note.scope : '',
      })
    })
    this.subscribe(OUTBOUND_STALLED, () => {
      // The OUTBOUND twin of control.saturated, and it needs the opposite
      // remedy. A refused request has an answer — the caller is told no, and
      // the toast above makes that visible. A stalled outbound queue has
      // already DROPPED frames: the backend keeps one reserved slot to say so
      // and then goes on, so what this connection owes the renderer no longer
      // exists anywhere. There is nothing to retry and nothing to be told.
      //
      // So take the existing close path, exactly as the heartbeat's dead
      // socket does: it rejects what is pending, publishes waiting, schedules
      // the retry and fires onDisconnect — and the reconnect is what makes
      // every store re-read from the backend, which is the only thing that
      // recovers a dropped frame. internal/transport/outbound has said this
      // was the contract since the queue was written; nothing in the renderer
      // had ever subscribed, so the notice arrived, matched no handler and
      // was discarded in silence (nocx-mrtvy).
      const ws = this.ws
      if (ws === null || ws.readyState !== WebSocket.OPEN || this._closingDeliberately) return
      log.warn('nocx: the connection dropped frames; reconnecting to resync')
      ws.close()
    })
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this._onVisibilityChange)
    }
    if (typeof window !== 'undefined') {
      window.addEventListener('online', this._onOnline)
    }
  }

  // --- WebSocket lifecycle -------------------------------------------------

  /** Begin discovery and connection. Further attempts are scheduled by drops. */
  start(): void {
    if (this._started) return
    this._started = true
    this._closingDeliberately = false
    this._backoffMs = MIN_BACKOFF_MS
    void this._attemptConnection()
  }

  /** Cancel the scheduled wait, reset backoff, and attempt immediately. */
  retryNow(): void {
    if (!this._started) {
      this.start()
      return
    }
    if (
      this._attemptInFlight ||
      this._connectionState.kind === 'connecting' ||
      this._connectionState.kind === 'online'
    ) {
      return
    }
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer)
      this._reconnectTimer = null
    }
    this._backoffMs = MIN_BACKOFF_MS
    void this._attemptConnection()
  }

  private async _attemptConnection(): Promise<void> {
    if (this._attemptInFlight || this._closingDeliberately) return
    this._attemptInFlight = true
    const epoch = ++this._attemptEpoch
    this.setConnectionState({ kind: 'connecting' })
    const giveUpTimer = setTimeout(() => this._giveUpAttempt(epoch), CONNECT_ATTEMPT_TIMEOUT_MS)
    try {
      let result: EndpointResult
      try {
        result = await this.provider.resolve()
      } catch {
        if (epoch !== this._attemptEpoch) return
        this._scheduleReconnect()
        return
      }
      // The give-up may already have run while this call was in flight —
      // its resolution belongs to an attempt this dispatcher no longer owns.
      if (epoch !== this._attemptEpoch) return
      if (!result.ok) {
        // The desktop provider reports a stopped backend as `no-server`.
        // Unlike configuration/authentication failures, that condition can
        // clear without user action when the backend process comes back.
        // Keep probing on the normal backoff so an open page reconnects and
        // the onConnect attach pass can recover its live sessions.
        if (result.failure.kind === 'no-server') {
          this._scheduleReconnect()
        } else {
          this.setConnectionState({ kind: 'blocked', failure: result.failure })
        }
        return
      }
      try {
        await this._openSocket(result.endpoint)
        this._backoffMs = MIN_BACKOFF_MS
      } catch {
        if (epoch !== this._attemptEpoch) return
        if (!this._closingDeliberately) this._scheduleReconnect()
      }
    } finally {
      clearTimeout(giveUpTimer)
      // Only clear the flag if this is still the current attempt — a give-up
      // followed by a fresh attempt already owns it.
      if (epoch === this._attemptEpoch) this._attemptInFlight = false
    }
  }

  /**
   * Abandon an attempt that has not settled within the deadline. Its promise
   * chain may still be out there and may still resolve; bumping the epoch is
   * what makes that resolution inert, the same way a superseded socket's
   * late `open` already is (`_openSocket`, `this.ws !== ws`). A hung socket
   * is force-closed here for the same reason: a live one left open would
   * still fire `open` — spuriously reporting `online` after the state
   * machine has already given up on it and returned Retry to the person who
   * needs it.
   */
  private _giveUpAttempt(epoch: number): void {
    if (epoch !== this._attemptEpoch) return
    this._attemptEpoch++
    this._attemptInFlight = false
    const ws = this.ws
    if (ws !== null) {
      this.ws = null
      ws.onopen = null
      ws.onerror = null
      ws.close()
    }
    if (this._closingDeliberately) return
    this._scheduleReconnect()
  }

  private _openSocket(endpoint: Endpoint): Promise<void> {
    return new Promise((resolve, reject) => {
      const subprotocol = `nocx.token.${endpoint.token}`
      // traceparent is opt-in and absent in production (see Endpoint): the
      // desktop app has never had an exchange to continue before its first
      // socket. The e2e harness is the one caller that ever sets it, so a
      // failing Playwright test's backend lines carry the same trace id the
      // test itself is named by, over a query parameter the backend already
      // parses at the connection's entry (internal/transport/ws.go,
      // log.ContinueTrace).
      const url = endpoint.traceparent
        ? `ws://${endpoint.host}:${endpoint.port}/session?traceparent=${encodeURIComponent(endpoint.traceparent)}`
        : `ws://${endpoint.host}:${endpoint.port}/session`
      const ws = new WebSocket(url, subprotocol)
      ws.binaryType = 'arraybuffer'
      let settled = false
      const onMessage = (event: MessageEvent) => this._onSocketMessage(event, ws)

      ws.onopen = () => {
        // A retry can replace this socket before its handshake callback runs.
        // A superseded socket must not announce a connection.
        if (this.ws !== ws) return
        if (settled) return
        settled = true
        this.setConnectionState({ kind: 'online' })
        this._armHeartbeatIdle(ws)
        this.fireConnect()
        resolve()
      }
      ws.onerror = () => {
        if (this.ws !== ws || settled) return
        settled = true
        ws.removeEventListener('message', onMessage)
        this._clearHeartbeatTimers()
        this.ws = null
        reject(new Error('ws connection failed'))
      }

      ws.addEventListener('message', onMessage)
      ws.addEventListener(
        'close',
        () => {
          if (this.ws !== ws) return
          ws.removeEventListener('message', onMessage)
          this._clearHeartbeatTimers()
          this.ws = null
          if (!settled) {
            settled = true
            reject(new Error('ws closed'))
          }
          this.rejectAllPending('ws closed')
          // Decide the reconnect policy BEFORE the lifecycle event: a
          // subscriber reading `reconnectPending` at event time must see
          // the state that will hold — the sentence can say "reconnecting"
          // instead of guessing (nocx-gbhwh).
          if (!this._closingDeliberately) {
            this._scheduleReconnect()
          }
          this.fireDisconnect()
        },
        { once: true },
      )

      this.ws = ws
    })
  }

  close(): void {
    this._closingDeliberately = true
    this._started = false
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer)
      this._reconnectTimer = null
    }
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this._onVisibilityChange)
    }
    if (typeof window !== 'undefined') {
      window.removeEventListener('online', this._onOnline)
    }
    this._clearHeartbeatTimers()
    this.ws?.close()
    this.ws = null
    this.rejectAllPending('closed')
    this.subscribers.clear()
    this.connectHandlers.clear()
    this.disconnectHandlers.clear()
    this.connectionStateHandlers.clear()
  }

  // --- RPC -----------------------------------------------------------------

  /**
   * Installed by the vault layer. When set, a sealed response to an OPERATION
   * defers the caller's promise until this hook resolves (the vault was
   * unsealed), then retries the request exactly once. The hook may reject
   * (user cancelled) — that rejection replaces the caller's error.
   *
   * This is the ONE seam where a sealed vault raises the unlock prompt; no
   * call site wraps its own vault calls. A REPORT never reaches it: its
   * refusal goes back to the caller intact, because a surface being on screen
   * is not somebody asking to unlock anything (nocx-76wyh).
   */
  onVaultSealed?: (method: string) => Promise<void>

  /** Called when a person's operation call is on the wire. Background work
   *  never reaches it: it merges and is not shown. */
  onCallPending?: (call: PendingCallView) => void
  /** Called once when that call has settled, however it settled, with the
   *  one thing a row cannot derive: whether an answer ever arrived. */
  onCallSettled?: (id: number, settlement: CallSettlement) => void

  call<T = unknown>(
    method: string,
    params: unknown,
    signal?: AbortSignal,
    options: CallOptions = {},
  ): Promise<T> {
    const intent = options.intent ?? 'operation'
    const origin = options.origin ?? 'person'
    return new Promise((resolve, reject) => {
      const id = this.nextID++
      const pending: PendingCall = {
        resolve: resolve as (v: unknown) => void,
        reject,
        method,
        params,
        intent,
        origin,
        sealedRetried: false,
        sent: false,
        settled: false,
        observeID: id,
        currentID: id,
        // A MUTATION MINTS ITS IDENTITY HERE, once per call — not per attempt,
        // so a sanctioned retry names the same work (ADR-8eej3e66 §4).
        mutation:
          intent === 'operation' && isMutationMethod(method) ? newMutationIdentity() : undefined,
      }
      const abort = (): void => {
        if (pending.settled) return
        pending.settled = true
        const activeID = pending.currentID
        if (this.pending.get(activeID) === pending) this.pending.delete(activeID)
        pending.removeAbortListener?.()
        // NO ANSWER: for a mutation this is where the question starts, and
        // the surface asks operation.outcome rather than sending it again.
        this.notifyCallSettled(pending, 'no-answer')
        reject(abortError())
        const ws = this.ws
        if (pending.sent && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(
            JSON.stringify({ jsonrpc: '2.0', method: 'rpc.cancel', params: { id: activeID } }),
          )
          this._noteOutboundActivity(ws)
        }
      }
      pending.abort = abort
      this.pending.set(id, pending)
      if (signal?.aborted) {
        abort()
        return
      }
      if (signal) {
        pending.signal = signal
        pending.onAbort = abort
        signal.addEventListener('abort', abort, { once: true })
        pending.removeAbortListener = () => signal.removeEventListener('abort', abort)
      }
      const ws = this.ws
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        this.pending.delete(id)
        pending.settled = true
        pending.removeAbortListener?.()
        this.notifyCallSettled(pending, 'no-answer')
        reject(new Error('not connected'))
        return
      }
      pending.sent = true
      ws.send(frameFor(id, pending))
      this._noteOutboundActivity(ws)
      if (origin === 'person' && intent === 'operation') {
        this.onCallPending?.({
          id,
          method,
          startedAt: Date.now(),
          mutation: pending.mutation,
          cancel: () => this.cancel(id),
        })
      }
    })
  }

  /** Send a JSON-RPC notification (no id, no response expected). */
  notify(method: string, params: unknown): void {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ jsonrpc: '2.0', method, params }))
    this._noteOutboundActivity(ws)
  }

  // --- Notifications -------------------------------------------------------

  /** Subscribe to a notification method. Returns an unsubscribe function. */
  subscribe(method: string, handler: NotificationHandler): () => void {
    let set = this.subscribers.get(method)
    if (!set) {
      set = new Set()
      this.subscribers.set(method, set)
    }
    set.add(handler)
    return () => {
      set.delete(handler)
    }
  }

  // --- Lifecycle subscriptions ---------------------------------------------

  onConnect(handler: LifecycleHandler): () => void {
    this.connectHandlers.add(handler)
    return () => {
      this.connectHandlers.delete(handler)
    }
  }

  onDisconnect(handler: LifecycleHandler): () => void {
    this.disconnectHandlers.add(handler)
    return () => {
      this.disconnectHandlers.delete(handler)
    }
  }

  onConnectionStateChange(handler: (state: ConnectionState) => void): () => void {
    this.connectionStateHandlers.add(handler)
    return () => {
      this.connectionStateHandlers.delete(handler)
    }
  }

  // --- Accessors -----------------------------------------------------------

  get connectionState(): ConnectionState {
    return this._connectionState
  }

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  /** The current raw WebSocket, or null.  For data-plane binary frame
   *  handling in WSClient — the dispatcher only owns control-plane
   *  messages. */
  get socket(): WebSocket | null {
    return this.ws
  }

  /** For test introspection: the current reconnect backoff value. */
  get backoffMs(): number {
    return this._backoffMs
  }

  /** For test introspection: whether the reconnect timer is pending. */
  get reconnectPending(): boolean {
    return this._reconnectTimer !== null
  }

  // --- Internal message handling -------------------------------------------

  private _onSocketMessage = (ev: MessageEvent, source: WebSocket | null = this.ws): void => {
    if (source === null || source !== this.ws) return
    if (typeof ev.data !== 'string') return
    let msg: {
      id?: number
      result?: unknown
      error?: { code?: number; message?: string; data?: unknown }
      method?: string
      params?: unknown
    }
    try {
      msg = JSON.parse(ev.data) as typeof msg
    } catch {
      return
    }

    // Response to a heartbeat request. Its timer is transport-owned rather
    // than a PendingCall timeout, so ordinary RPCs keep their existing
    // lifetime semantics.
    if (msg.id !== undefined && this._heartbeatRequestID === msg.id) {
      if (
        typeof msg.result !== 'object' ||
        msg.result === null ||
        typeof (msg.result as TransportPingResult).serverTimeMs !== 'number'
      ) {
        return
      }
      this._clearHeartbeatResponse()
      this._armHeartbeatIdle(source)
      return
    }

    // Response to a pending request.
    if (msg.id !== undefined) {
      const p = this.pending.get(msg.id)
      if (!p) return
      this.pending.delete(msg.id)
      p.removeAbortListener?.()
      if (msg.error) {
        if (
          p.intent === 'operation' &&
          this.onVaultSealed &&
          !p.sealedRetried &&
          msg.error.data &&
          typeof msg.error.data === 'object' &&
          'reason' in msg.error.data &&
          msg.error.data.reason === 'vault-sealed'
        ) {
          // Keep the caller's promise pending: raise the unlock prompt (the
          // vault owns it), then re-send the request verbatim with a fresh
          // id. Exactly one retry — a second sealed error rejects as-is.
          //
          // A MUTATION ASKS FIRST (ADR-8eej3e66 §4). Re-sending is replaying,
          // and a replay is only safe when the backend says the first attempt
          // did not start. A sealed refusal normally is that answer — the
          // vault refused before anything was touched — but the renderer does
          // not get to assume it: the record does, and a record that says
          // anything else ends the call with one of §8's three answers.
          p.sealedRetried = true
          if (p.mutation !== undefined) {
            void this.replayMutationAfterUnlock(p)
            return
          }
          void this.onVaultSealed(p.method).then(
            () => {
              if (p.signal?.aborted) {
                // Withdrawn while the unlock prompt was up. The first request
                // has already been answered and the replacement was never
                // sent, so nothing is in flight to cancel.
                p.settled = true
                this.notifyCallSettled(p)
                p.reject(abortError())
                return
              }
              const id = this.nextID++
              p.currentID = id
              this.pending.set(id, p)
              const ws = this.ws
              if (!ws || ws.readyState !== WebSocket.OPEN) {
                this.pending.delete(id)
                p.settled = true
                this.notifyCallSettled(p)
                p.reject(new Error('not connected'))
                return
              }
              ws.send(frameFor(id, p))
              this._noteOutboundActivity(ws)
              // The listener was dropped with the first response; the caller's
              // signal still owns this call, so re-arm it against the new id.
              const signal = p.signal
              const onAbort = p.onAbort
              if (signal && onAbort) {
                signal.addEventListener('abort', onAbort, { once: true })
                p.removeAbortListener = () => signal.removeEventListener('abort', onAbort)
              }
            },
            (e: unknown) => {
              p.settled = true
              this.notifyCallSettled(p)
              p.reject(e instanceof Error ? e : new Error(String(e)))
            },
          )
          return
        }
        this.notifyCallSettled(p)
        // A replay the record refused. The answer is in the payload, so the
        // person is told which of the three it is rather than "already ran".
        const replayed = replayedPayload(msg.error.code, msg.error.data)
        if (replayed !== null) {
          p.reject(new MutationNotReplayedError(replayedAssessment(replayed)))
          return
        }
        if (
          msg.error.data &&
          typeof msg.error.data === 'object' &&
          'reason' in msg.error.data &&
          msg.error.data.reason === 'vault-operation-cancelled'
        ) {
          p.reject(new VaultOperationCancelledError())
          return
        }
        if (isSaturationData(msg.error.data)) {
          p.reject(refusedError(p.method, p.origin, msg.error.data, msg.error.code))
          return
        }
        p.reject(new RpcError(msg.error.message ?? 'rpc error', msg.error.code, msg.error.data))
      } else {
        this.notifyCallSettled(p)
        p.resolve(msg.result)
      }
      return
    }

    // Notification — route by method.
    if (msg.method !== undefined) {
      const handlers = this.subscribers.get(msg.method)
      if (handlers) {
        for (const h of handlers) {
          h(msg.params)
        }
      }
    }
  }

  // --- Reconnect plumbing --------------------------------------------------
  private _clearHeartbeatResponse(): void {
    if (this._heartbeatResponseTimer !== null) {
      clearTimeout(this._heartbeatResponseTimer)
      this._heartbeatResponseTimer = null
    }
    this._heartbeatRequestID = null
  }

  private _clearHeartbeatTimers(): void {
    if (this._heartbeatIdleTimer !== null) {
      clearTimeout(this._heartbeatIdleTimer)
      this._heartbeatIdleTimer = null
    }
    this._clearHeartbeatResponse()
    this._heartbeatSocket = null
  }

  // THE WINDOW IS MEASURED AGAINST WHAT THE CLIENT SENDS, and only that
  // (nocx-8jzbp). The server closes a connection that has sent it nothing for
  // heartbeatReadWindow — 30s, refreshed by client frames alone — so the only
  // thing that can stand in for a ping is another OUTBOUND frame, which is why
  // _noteOutboundActivity re-arms this and nothing on the receiving side does.
  //
  // Two readings of "the socket is busy" cost this a bug each. A pending
  // request used to stand the heartbeat down, on the reasoning that a call in
  // flight proves the connection alive: true of the connection, false of the
  // server's deadline, and a pending request is exactly the state in which the
  // client sends nothing — so a skills.audit, which is a model reading a whole
  // bundle, died at thirty seconds and took every other call on the socket with
  // it. A frame the server SENT used to re-arm it for the same reason and with
  // the same flaw. transport.ping rides the immediate submission and is
  // answered on the read loop, so it comes back while the control lane is busy;
  // there is nothing to stand down for.
  private _armHeartbeatIdle(ws: WebSocket): void {
    if (
      this.ws !== ws ||
      ws.readyState !== WebSocket.OPEN ||
      this._closingDeliberately ||
      this._heartbeatRequestID !== null
    ) {
      return
    }
    this._heartbeatSocket = ws
    if (this._heartbeatIdleTimer !== null) clearTimeout(this._heartbeatIdleTimer)
    this._heartbeatIdleTimer = setTimeout(() => {
      if (this._heartbeatSocket !== ws || this.ws !== ws || ws.readyState !== WebSocket.OPEN) return
      this._heartbeatIdleTimer = null
      const id = this.nextID++
      this._heartbeatRequestID = id
      ws.send(JSON.stringify({ jsonrpc: '2.0', id, method: 'transport.ping', params: {} }))
      this._heartbeatResponseTimer = setTimeout(() => {
        if (
          this._heartbeatSocket !== ws ||
          this.ws !== ws ||
          this._heartbeatRequestID !== id ||
          this._closingDeliberately
        ) {
          return
        }
        // Deliberately use the existing close path. It rejects pending calls,
        // publishes waiting, schedules the retry, and fires onDisconnect.
        ws.close()
      }, HEARTBEAT_RESPONSE_TIMEOUT_MS)
    }, HEARTBEAT_IDLE_WINDOW_MS)
  }

  private _noteOutboundActivity(ws: WebSocket): void {
    if (this.ws !== ws || ws.readyState !== WebSocket.OPEN || this._heartbeatRequestID !== null)
      return
    this._armHeartbeatIdle(ws)
  }

  private _scheduleReconnect(): void {
    if (this._reconnectTimer !== null || this._closingDeliberately) return
    const jitter = Math.random() * this._backoffMs * 0.5
    const delay = this._backoffMs + jitter
    this.setConnectionState({ kind: 'waiting', backoffMs: delay })
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null
      void this._attemptConnection()
    }, delay)
    this._backoffMs = Math.min(this._backoffMs * 2, MAX_BACKOFF_MS)
  }

  // --- Helpers -------------------------------------------------------------

  private setConnectionState(next: ConnectionState): void {
    if (this.sameConnectionState(this._connectionState, next)) return
    this._connectionState = next
    for (const handler of this.connectionStateHandlers) {
      handler(next)
    }
  }

  private sameConnectionState(a: ConnectionState, b: ConnectionState): boolean {
    if (a.kind !== b.kind) return false
    if (a.kind === 'waiting' && b.kind === 'waiting') return a.backoffMs === b.backoffMs
    if (a.kind === 'blocked' && b.kind === 'blocked') {
      const aFailureKind: EndpointFailureKind = a.failure.kind
      const bFailureKind: EndpointFailureKind = b.failure.kind
      return (
        aFailureKind === bFailureKind &&
        a.failure.message === b.failure.message &&
        a.failure.remedy === b.failure.remedy
      )
    }
    return true
  }

  private _onVisibilityChange = (): void => {
    if (this._connectionState.kind === 'waiting') this.retryNow()
  }

  private _onOnline = (): void => {
    if (this._connectionState.kind === 'waiting') this.retryNow()
  }

  /**
   * The one re-send a mutation may have: ADR-0032's unlock retry, gated on the
   * backend's record. It asks what became of the first attempt and re-sends
   * only when the answer is `not-started` — under the SAME identity, so a
   * second copy cannot slip past the record either.
   */
  private async replayMutationAfterUnlock(p: PendingCall): Promise<void> {
    const assessment = await resolveMutationOutcome(this, p.mutation ?? '')
    if (p.settled) return
    if (assessment.answer !== 'not-started') {
      // Nothing is re-sent. The person is told which of the three answers it
      // is, and a timeout that reached this point never implies a rollback.
      p.settled = true
      p.removeAbortListener?.()
      this.notifyCallSettled(p)
      p.reject(new MutationNotReplayedError(assessment))
      return
    }
    let unlocked = false
    try {
      await this.onVaultSealed?.(p.method)
      unlocked = true
    } catch (e) {
      p.settled = true
      this.notifyCallSettled(p)
      p.reject(e instanceof Error ? e : new Error(String(e)))
      return
    }
    if (!unlocked || p.settled) return
    if (p.signal?.aborted) {
      p.settled = true
      this.notifyCallSettled(p, 'no-answer')
      p.reject(abortError())
      return
    }
    const id = this.nextID++
    p.currentID = id
    this.pending.set(id, p)
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      this.pending.delete(id)
      p.settled = true
      this.notifyCallSettled(p, 'no-answer')
      p.reject(new Error('not connected'))
      return
    }
    ws.send(frameFor(id, p))
    this._noteOutboundActivity(ws)
    const signal = p.signal
    const onAbort = p.onAbort
    if (signal && onAbort) {
      signal.addEventListener('abort', onAbort, { once: true })
      p.removeAbortListener = () => signal.removeEventListener('abort', onAbort)
    }
  }

  /** Withdraw one call by the id its caller was given. Unknown ids are a
   *  normal race: the answer may have arrived first. */
  cancel(id: number): void {
    this.pending.get(id)?.abort?.()
  }

  private notifyCallSettled(p: PendingCall, settlement: CallSettlement = 'answered'): void {
    if (p.settledNotified) return
    p.settledNotified = true
    if (p.origin === 'person' && p.intent === 'operation') {
      this.onCallSettled?.(p.observeID, settlement)
    }
  }

  private rejectAllPending(reason: string): void {
    for (const p of this.pending.values()) {
      this.notifyCallSettled(p)
      p.reject(new Error(reason))
    }
    this.pending.clear()
  }

  private fireConnect(): void {
    for (const h of this.connectHandlers) {
      h()
    }
  }

  private fireDisconnect(): void {
    for (const h of this.disconnectHandlers) {
      h()
    }
  }
}

/**
 * The error one refused call rejects with, and the record of who caused it.
 *
 * Two things happen here and nowhere else (ADR-8eej3e66 stage 1):
 *
 * - The message becomes a sentence a person can read. The wire's own
 *   "Control plane busy" is a diagnostic, not an answer, and a surface that
 *   prints `err.message` — which most of them do — must not put it on screen.
 *   `data` is untouched, so `isSaturationRefusal` still recognises the class.
 * - The invocation is recorded with its origin, in the log. A background
 *   refusal is silent to the person by design, so the record is the only
 *   place it exists in the renderer; the backend's own `control action
 *   refused` line and Copy diagnostics hold the same fact with its holders.
 */
function refusedError(
  method: string,
  origin: CallOrigin,
  data: Pick<ControlSaturated, 'reason'>,
  code: number | undefined,
): RpcError {
  const scope =
    typeof (data as { scope?: unknown }).scope === 'string'
      ? ((data as { scope?: unknown }).scope as string)
      : ''
  log.warn('nocx: control request refused', { method, scope, origin })
  return new RpcError(refusalSentence(scope === '' ? undefined : scope), code, data)
}
