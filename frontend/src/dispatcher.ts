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
import { callOperationDeclaration } from './generated/operations'
import type { TransportCreditResult } from './generated/transport.credit'
import type { MutationReplayed } from './generated/mutation.replayed'
import {
  MutationNotReplayedError,
  isMutationMethod,
  newMutationIdentity,
  replayedAssessment,
  resolveMutationOutcome,
} from './mutations'
import { log } from './log'
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

/** One caller waiting on a slot. Several members share a slot when they made
 *  the same mergeable call; each keeps its own promise, signal and origin. */
interface CallMember {
  resolve: (v: unknown) => void
  reject: (e: Error) => void
  intent: RpcIntent
  /** Who asked. The strongest origin on the slot decides how it queues and
   *  whether it is shown (ADR-8eej3e66 §2). */
  origin: CallOrigin
  settled: boolean
  signal?: AbortSignal
  onAbort?: () => void
  removeAbortListener?: () => void
}

/** One invocation on its way to the backend. It may hold a credit and be on
 *  the wire, or wait in this renderer's queue for one (ADR-8eej3e66 §7). Its
 *  identity is stable across a sealed retry, which changes only `wireID`. */
interface CallSlot {
  /** Stable identity: the Operations row's id and what cancel() names. */
  observeID: number
  method: string
  params: unknown
  intent: RpcIntent
  origin: CallOrigin
  /** The merge key, or null for a declaration whose merge is `none`. */
  key: string | null
  members: Set<CallMember>
  /** Whether the Operations source was told about this slot. */
  announced: boolean
  startedAt: number
  state: 'queued' | 'sent' | 'done'
  /** The id the BACKEND is working on; null while queued or awaiting an
   *  unlock. A sealed retry sends under a fresh id. */
  wireID: number | null
  sealedRetried: boolean
  /** The `turn` deadline while this slot waits here for credit. */
  deadline: ReturnType<typeof setTimeout> | null
  /** The identity this mutating call carries (ADR-8eej3e66 §4). Minted once
   *  per slot — not per attempt — so a sanctioned retry names the same work.
   *  Absent on a read: the catalogue decides, not the caller. */
  mutation?: string
}

/** A call that waited for credit past its declared turn and was never sent.
 *  Its outcome is the record's `not-started`: nothing took effect. Surfaces
 *  recognise it by `name`, the same stable token the existing AbortError and
 *  VaultOperationCancelledError use. */
class CallNotStartedError extends Error {
  constructor(readonly method: string) {
    super('The operation timed out before it started')
    this.name = 'CallNotStartedError'
  }
}

/** The credit advertisement the backend answers transport.credit with
 *  (contracts/transport.credit.schema.json). */
type CreditAdvertisement = TransportCreditResult

/**
 * The key two invocations must share to join one another. A declaration whose
 * merge is `none` never joins: two mutations are two effects even when their
 * params are identical. For `same-key` and `latest-value` the key is the
 * method plus its canonical params — that is what makes two actions the SAME
 * action rather than two calls that resemble each other.
 */
function mergeKey(
  method: string,
  params: unknown,
  intent: RpcIntent,
  origin: CallOrigin,
): string | null {
  const declaration = callOperationDeclaration(method)
  // ONLY A READ MERGES. `latest-value` is a state replacement (resize): a
  // later value replaces a QUEUED one, which is a different operation with
  // its own completion to report, and a sent resize must never be collapsed
  // with the one after it — the receipt chain depends on both being applied
  // in order. `none` is every mutation, and two effects are two effects.
  if (declaration === undefined || declaration.merge !== 'same-key') return null
  // INTENT IS PART OF THE KEY. A report is a passive read whose sealed answer
  // is a fact to show, while an operation wants vault material and may raise
  // the unlock; joining them would hand one caller the other's semantics.
  //
  // AND SO IS ORIGIN (ADR-8eej3e66 §2). Origin is who the answer is owed to:
  // a background poll's refusal is recorded quietly and a person's is shown.
  // A person joining a background call that is already on the wire would take
  // that call's quiet verdict for the person's action, which is the silence
  // §2 exists to end — so two origins are two invocations, and only the
  // same origin, intent and params share one.
  return method + '\n' + intent + '\n' + origin + '\n' + canonicalJSON(params)
}

/** The bound a queued call waits under before it is answered "not started".
 *
 *  A PERSON'S CALL GETS ITS DECLARATION'S TURN — thirty seconds — because the
 *  wait must be long enough to be seen: the row appears past two seconds with
 *  Cancel, and a refusal after one second would be the busyness answer in a
 *  new place (ADR-8eej3e66 §8, and the catalogue's personTurnMillis).
 *
 *  A BACKGROUND CALL KEEPS THE OLD ONE SECOND. Nobody is watching it, it is
 *  never shown as a row, and a queue made of polls must still end: a poll
 *  that never got credit is not worth thirty seconds of a window.
 */
const BACKGROUND_QUEUE_TURN_MS = 1000

function queueTurnMs(slot: CallSlot, declaredTurnMs: number): number {
  if (slot.origin === 'person') return declaredTurnMs
  return Math.min(declaredTurnMs, BACKGROUND_QUEUE_TURN_MS)
}

/** JSON with object keys sorted, so two equal params produce one key. */
function canonicalJSON(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? 'null'
  if (Array.isArray(value)) return '[' + value.map(canonicalJSON).join(',') + ']'
  const record = value as Record<string, unknown>
  return (
    '{' +
    Object.keys(record)
      .sort()
      .map((key) => JSON.stringify(key) + ':' + canonicalJSON(record[key]))
      .join(',') +
    '}'
  )
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

/** How a call left the renderer's hands (ADR-8eej3e66 §8). */
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

/** One request frame, from one owner: the mutation identity rides the
 *  ENVELOPE beside the id and the method, because it is a fact about the
 *  invocation and not a param any handler should see (ADR-8eej3e66 §4), and
 *  the origin travels with it for the same reason (§2). */
function frameFor(id: number, slot: CallSlot): string {
  const frame: Record<string, unknown> = {
    jsonrpc: '2.0',
    id,
    method: slot.method,
    params: slot.params,
    origin: slot.origin,
  }
  if (slot.mutation !== undefined) frame.mutation = slot.mutation
  return JSON.stringify(frame)
}

export type ConnectionState =
  | { kind: 'connecting' }
  | { kind: 'online' }
  | { kind: 'waiting'; backoffMs: number }
  | { kind: 'blocked'; failure: EndpointFailure }

export class Dispatcher {
  private ws: WebSocket | null = null
  /** Call ids, starting at 1: a caller's first request is id 1, which is the
   *  number the wire and the tests already speak. */
  private nextID = 1
  /** Infrastructure ids, kept below zero so the handshake and the heartbeat
   *  never consume a call's id. JSON-RPC ids are opaque to the backend. */
  private infraID = -1
  /** The id an Operations row and cancel() name. It is deliberately separate
   *  from the wire id: a sealed retry changes the wire id and must not change
   *  what the person's Cancel names. Both counters start at 1; the maps they
   *  key are separate. */
  private nextObserveID = 1
  /** Wire id → the slot the backend is working on. */
  private pending = new Map<number, CallSlot>()
  /** Every live slot, queued or sent, by the id its row and cancel() use. */
  private slots = new Map<number, CallSlot>()
  /** Slots waiting for credit, in arrival order. */
  private queue: CallSlot[] = []
  /** Merge key → the live slot a further identical call joins. */
  private groups = new Map<string, CallSlot>()
  private subscribers = new Map<string, Set<NotificationHandler>>()

  // Credit window state (ADR-8eej3e66 §7). `null` means the backend has not
  // advertised yet: ordinary calls wait rather than risk sending more than
  // they were allowed. Infinity means the peer does not advertise at all, so
  // holding calls would invent a bound nobody enforces.
  private creditWindow: number | null = null
  private creditExempt = new Set<string>()
  private creditReserved = new Set<string>()
  private creditInFlight = 0
  private creditReservedInFlight = new Map<string, number>()
  private creditRequestID: number | null = null

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
        // THE WINDOW IS THIS CONNECTION'S (ADR-8eej3e66 §7). Forget the old
        // one, ask for this one, and let onConnect fire immediately: a
        // surface's calls queue until the answer arrives rather than risk
        // sending more than the connection allows.
        this.resetCredit()
        this.requestCredit(ws)
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
  /** Called once when that call has settled, however it settled. */
  onCallSettled?: (id: number, settlement: CallSettlement) => void

  call<T = unknown>(
    method: string,
    params: unknown,
    signal?: AbortSignal,
    options: CallOptions = {},
  ): Promise<T> {
    const intent = options.intent ?? 'operation'
    const origin = options.origin ?? 'person'
    return new Promise<T>((resolve, reject) => {
      const ws = this.ws
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        reject(new Error('not connected'))
        return
      }
      const member: CallMember = {
        resolve: resolve as (v: unknown) => void,
        reject,
        intent,
        origin,
        settled: false,
      }
      // A MERGEABLE CALL JOINS THE ONE PENDING (ADR-8eej3e66 §7). Background
      // work merges on its key so a poll storm cannot fill the window with
      // one question, and a person's further identical action joins too
      // rather than making the backend answer the same thing twice.
      const key = mergeKey(method, params, intent, origin)
      const joined = key === null ? undefined : this.groups.get(key)
      if (joined !== undefined && joined.state !== 'done') {
        this.attach(joined, member, signal)
        return
      }
      const slot: CallSlot = {
        observeID: this.nextObserveID++,
        method,
        params,
        intent,
        origin,
        key,
        members: new Set(),
        announced: false,
        startedAt: Date.now(),
        state: 'queued',
        wireID: null,
        sealedRetried: false,
        deadline: null,
        // A MUTATION MINTS ITS IDENTITY HERE, once per call — not per attempt,
        // so a sanctioned retry names the same work (ADR-8eej3e66 §4).
        mutation:
          intent === 'operation' && isMutationMethod(method) ? newMutationIdentity() : undefined,
      }
      if (key !== null) this.groups.set(key, slot)
      this.slots.set(slot.observeID, slot)
      this.attach(slot, member, signal)
      if (slot.state === 'done') return
      this.announce(slot)
      this.enqueue(slot)
    })
  }

  // --- The credit queue (ADR-8eej3e66 §7) --------------------------------

  /** Add one caller to a slot, and wire its withdrawal. A slot's origin is
   *  the strongest of its members': a person joining background work makes
   *  the whole call a person's for queueing and for the row. */
  private attach(slot: CallSlot, member: CallMember, signal?: AbortSignal): void {
    slot.members.add(member)
    if (member.origin === 'person' && slot.origin !== 'person') {
      slot.origin = 'person'
      slot.startedAt = Date.now()
      this.announce(slot)
    }
    if (signal === undefined) return
    const abort = (): void => this.detach(slot, member)
    if (signal.aborted) {
      member.settled = true
      slot.members.delete(member)
      member.reject(abortError())
      if (slot.members.size === 0 && slot.state !== 'done') this.withdraw(slot)
      return
    }
    member.signal = signal
    member.onAbort = abort
    signal.addEventListener('abort', abort, { once: true })
    member.removeAbortListener = () => signal.removeEventListener('abort', abort)
  }

  /** One caller withdraws. If it was the last one, the whole slot goes; a
   *  joiner leaving must not cancel the request the others still want. */
  private detach(slot: CallSlot, member: CallMember): void {
    if (member.settled) return
    member.settled = true
    member.removeAbortListener?.()
    slot.members.delete(member)
    member.reject(abortError())
    if (slot.state !== 'done' && slot.members.size === 0) this.withdraw(slot)
  }

  /** Tell the Operations source about a person's operation call. It is told
   *  the moment the person acts, not when credit arrives: the wait the row
   *  reports is exactly the wait for credit. */
  private announce(slot: CallSlot): void {
    if (slot.announced || slot.state === 'done') return
    if (slot.intent !== 'operation' || slot.origin !== 'person') return
    slot.announced = true
    this.onCallPending?.({
      id: slot.observeID,
      method: slot.method,
      startedAt: slot.startedAt,
      mutation: slot.mutation,
      cancel: () => this.cancel(slot.observeID),
    })
  }

  /** Put a slot where it belongs: on the wire if it may go now, otherwise in
   *  the queue under its `turn` deadline. Unknown methods bypass the queue:
   *  the backend does not count a method it does not know. */
  private enqueue(slot: CallSlot): void {
    if (slot.state === 'done') return
    const declaration = callOperationDeclaration(slot.method)
    if (declaration === undefined || this.isExempt(slot.method)) {
      this.send(slot)
      return
    }
    if (this.creditWindow !== null && this.hasCredit(slot)) {
      this.send(slot)
      return
    }
    slot.state = 'queued'
    this.queue.push(slot)
    if (slot.deadline === null) {
      slot.deadline = setTimeout(() => this.expire(slot), queueTurnMs(slot, declaration.turnMs))
    }
  }

  private expire(slot: CallSlot): void {
    slot.deadline = null
    if (slot.state !== 'queued') return
    this.removeFromQueue(slot)
    this.settleSlot(slot, new CallNotStartedError(slot.method))
  }

  private isExempt(method: string): boolean {
    return this.creditExempt.has(method)
  }

  private isReserved(method: string): boolean {
    return this.creditReserved.has(method)
  }

  private hasCredit(slot: CallSlot): boolean {
    if (this.isReserved(slot.method)) {
      return (this.creditReservedInFlight.get(slot.method) ?? 0) < 1
    }
    const window = this.creditWindow
    return window !== null && this.creditInFlight < window
  }

  /** Send one slot now. The credit is taken here and released by whichever
   *  comes first: the response, the withdrawal, or the task ending. */
  private send(slot: CallSlot): void {
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      this.settleSlot(slot, new Error('not connected'))
      return
    }
    this.removeFromQueue(slot)
    this.clearDeadline(slot)
    const id = this.nextID++
    slot.wireID = id
    slot.state = 'sent'
    if (!this.isExempt(slot.method)) {
      if (this.isReserved(slot.method)) {
        this.creditReservedInFlight.set(
          slot.method,
          (this.creditReservedInFlight.get(slot.method) ?? 0) + 1,
        )
      } else {
        this.creditInFlight++
      }
    }
    this.pending.set(id, slot)
    ws.send(frameFor(id, slot))
    this._noteOutboundActivity(ws)
  }

  private releaseCredit(slot: CallSlot): void {
    if (this.isExempt(slot.method)) return
    if (this.isReserved(slot.method)) {
      const held = this.creditReservedInFlight.get(slot.method) ?? 0
      if (held <= 1) this.creditReservedInFlight.delete(slot.method)
      else this.creditReservedInFlight.set(slot.method, held - 1)
      return
    }
    this.creditInFlight = Math.max(0, this.creditInFlight - 1)
  }

  /** Fill the window from the queue, person first. Background work never
   *  takes the credit a person's call is waiting for (ADR-8eej3e66 §6). */
  private pump(): void {
    if (this.creditWindow === null) return
    for (;;) {
      if (this.queue.length === 0) return
      let next = this.queue.findIndex((slot) => slot.origin === 'person' && this.hasCredit(slot))
      if (next < 0) next = this.queue.findIndex((slot) => this.hasCredit(slot))
      if (next < 0) return
      const slot = this.queue.splice(next, 1)[0]
      this.send(slot)
    }
  }

  /** Settle one slot and every caller waiting on it. There is no second
   *  settlement: the state check is what makes a response arriving after a
   *  withdrawal inert. */
  private settleSlot(
    slot: CallSlot,
    error?: Error,
    value?: unknown,
    settlement: CallSettlement = 'answered',
  ): void {
    if (slot.state === 'done') return
    slot.state = 'done'
    this.clearDeadline(slot)
    this.removeFromQueue(slot)
    if (slot.key !== null && this.groups.get(slot.key) === slot) this.groups.delete(slot.key)
    this.slots.delete(slot.observeID)
    if (slot.wireID !== null) {
      // A sent slot that settles without an answer (withdrawal, expire,
      // disconnect) still owns its credit; free it here. The normal response
      // path has already released and cleared it, so this is the other half
      // of the same invariant rather than a second release.
      this.releaseCredit(slot)
      this.pending.delete(slot.wireID)
      slot.wireID = null
    }
    for (const member of slot.members) {
      if (member.settled) continue
      member.settled = true
      member.removeAbortListener?.()
      if (error !== undefined) member.reject(error)
      else member.resolve(value)
    }
    slot.members.clear()
    this.notifyCallSettled(slot, settlement)
    this.pump()
  }

  /** Withdraw every caller of one slot. A queued call is simply dropped; a
   *  sent one is cancelled BY THE ID THE BACKEND IS WORKING ON. */
  private withdraw(slot: CallSlot): void {
    const wireID = slot.wireID
    const onTheWire = wireID !== null && !this.isExempt(slot.method)
    // A SENT CALL ENDS WITH NO ANSWER, and for a mutation that is the moment
    // the question starts (ADR-8eej3e66 §4 and §8): the record has to be
    // asked, because nothing here knows whether the work began.
    this.settleSlot(slot, abortError(), undefined, onTheWire ? 'no-answer' : 'answered')
    if (!onTheWire || wireID === null) return
    const ws = this.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ jsonrpc: '2.0', method: 'rpc.cancel', params: { id: wireID } }))
    this._noteOutboundActivity(ws)
  }

  private removeFromQueue(slot: CallSlot): void {
    const index = this.queue.indexOf(slot)
    if (index >= 0) this.queue.splice(index, 1)
  }

  private clearDeadline(slot: CallSlot): void {
    if (slot.deadline !== null) {
      clearTimeout(slot.deadline)
      slot.deadline = null
    }
  }

  /** Forget the previous connection's window. The next one advertises its
   *  own; until then nothing ordinary is sent. */
  private resetCredit(): void {
    this.creditWindow = null
    this.creditExempt = new Set()
    this.creditReserved = new Set()
    this.creditInFlight = 0
    this.creditReservedInFlight.clear()
    this.creditRequestID = null
  }

  /** Ask the connection for its window. It is the first frame sent, so the
   *  answer is on its way before any surface's onConnect handler can make a
   *  call; those calls wait in the queue until it lands. */
  private requestCredit(ws: WebSocket): void {
    const id = this.infraID--
    this.creditRequestID = id
    ws.send(JSON.stringify({ jsonrpc: '2.0', id, method: 'transport.credit', params: {} }))
    this._noteOutboundActivity(ws)
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

    // Response to the credit advertisement (transport.credit).
    if (msg.id !== undefined && this.creditRequestID === msg.id) {
      const answer =
        msg.error === undefined ? (msg.result as Partial<CreditAdvertisement>) : undefined
      if (
        answer !== undefined &&
        typeof answer.window === 'number' &&
        answer.window > 0 &&
        Array.isArray(answer.exempt) &&
        Array.isArray(answer.reserved)
      ) {
        this.creditWindow = answer.window
        this.creditExempt = new Set(answer.exempt)
        this.creditReserved = new Set(answer.reserved)
      } else {
        // No advertisement: this peer does not enforce a window, so holding
        // calls would impose a bound nobody asked for. One line, then send.
        this.creditWindow = Number.POSITIVE_INFINITY
        log.warn('nocx: the backend did not advertise a credit window')
      }
      this.creditRequestID = null
      this.pump()
      return
    }

    // Response to a pending request.
    if (msg.id !== undefined) {
      const slot = this.pending.get(msg.id)
      if (!slot) return
      this.pending.delete(msg.id)
      this.releaseCredit(slot)
      slot.wireID = null
      if (msg.error) {
        if (this.retrySealed(slot, msg.error)) return
        this.settleSlot(slot, this.errorFor(slot, msg.error))
        return
      }
      this.settleSlot(slot, undefined, msg.result)
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
      const id = this.infraID--
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

  /** Withdraw one call by the id its caller was given. Unknown ids are a
   *  normal race: the answer may have arrived first. */
  cancel(id: number): void {
    const slot = this.slots.get(id)
    if (slot !== undefined) this.withdraw(slot)
  }

  /** One sealed refusal becomes one unlock and one re-send (ADR-0032). The
   *  replacement goes back through the queue, so an unlock answered while
   *  the window is full waits for credit like any other call rather than
   *  jumping it — except vault.unseal, which has its reserved credit. */
  private retrySealed(
    slot: CallSlot,
    error: { code?: number; message?: string; data?: unknown },
  ): boolean {
    if (slot.intent !== 'operation' || this.onVaultSealed === undefined || slot.sealedRetried) {
      return false
    }
    if (
      !error.data ||
      typeof error.data !== 'object' ||
      !('reason' in error.data) ||
      error.data.reason !== 'vault-sealed'
    ) {
      return false
    }
    slot.sealedRetried = true
    // A MUTATION ASKS FIRST (ADR-8eej3e66 §4). Re-sending is replaying, and a
    // replay is only safe when the backend says the first attempt did not
    // start. A sealed refusal normally is that answer — the vault refused
    // before anything was touched — but the renderer does not get to assume
    // it: the record does, and a record that says anything else ends the call
    // with one of §8's three answers.
    if (slot.mutation !== undefined) {
      void this.replayMutationAfterUnlock(slot)
      return true
    }
    slot.state = 'queued'
    void this.onVaultSealed(slot.method).then(
      () => {
        if (slot.state === 'done') return
        this.enqueue(slot)
      },
      (e: unknown) => this.settleSlot(slot, e instanceof Error ? e : new Error(String(e))),
    )
    return true
  }

  /**
   * The one re-send a mutation may have: ADR-0032's unlock retry, gated on the
   * backend's record. It asks what became of the first attempt and re-sends
   * only when the answer is `not-started` — under the SAME identity, so a
   * second copy cannot slip past the record either — and the re-send goes back
   * through the queue, so it waits for credit like any other call.
   */
  private async replayMutationAfterUnlock(slot: CallSlot): Promise<void> {
    const assessment = await resolveMutationOutcome(this, slot.mutation ?? '')
    if (slot.state === 'done') return
    if (assessment.answer !== 'not-started') {
      // Nothing is re-sent. The person is told which of the three answers it
      // is, and a timeout that reached this point never implies a rollback.
      this.settleSlot(slot, new MutationNotReplayedError(assessment))
      return
    }
    let unlocked = false
    try {
      await this.onVaultSealed?.(slot.method)
      unlocked = true
    } catch (e) {
      this.settleSlot(slot, e instanceof Error ? e : new Error(String(e)))
      return
    }
    // The awaits above let the slot settle under us (a withdrawal while the
    // prompt was up), and TypeScript cannot see that: it narrowed the union
    // to the two live states after the guard at the top.
    if (!unlocked || (slot.state as CallSlot['state']) === 'done') return
    slot.state = 'queued'
    this.enqueue(slot)
  }

  private errorFor(
    slot: CallSlot,
    error: { code?: number; message?: string; data?: unknown },
  ): Error {
    // A REPLAY THE RECORD REFUSED. The answer is in the payload, so the
    // person is told which of the three it is rather than "already ran".
    const replayed = replayedPayload(error.code, error.data)
    if (replayed !== null) {
      return new MutationNotReplayedError(replayedAssessment(replayed))
    }
    if (
      error.data &&
      typeof error.data === 'object' &&
      'reason' in error.data &&
      error.data.reason === 'vault-operation-cancelled'
    ) {
      return new VaultOperationCancelledError()
    }
    if (isSaturationData(error.data)) {
      return refusedError(slot.method, slot.origin, error.data, error.code)
    }
    return new RpcError(error.message ?? 'rpc error', error.code, error.data)
  }

  private notifyCallSettled(slot: CallSlot, settlement: CallSettlement = 'answered'): void {
    if (slot.announced) this.onCallSettled?.(slot.observeID, settlement)
  }

  /** A lost connection settles everything: sent calls have no answer coming,
   *  queued calls never started, and the next connection advertises a new
   *  window. */
  private rejectAllPending(reason: string): void {
    // Forget the window FIRST: settling a slot pumps the queue, and pumping
    // on a dead connection must not try to send anything.
    this.resetCredit()
    this.queue = []
    for (const slot of [...this.slots.values()]) {
      this.settleSlot(slot, new Error(reason), undefined, 'no-answer')
    }
    this.slots.clear()
    this.pending.clear()
    this.groups.clear()
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
