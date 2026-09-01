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

/**
 * One visible saturation toast per episode, not one per refusal. The bounded
 * executor refuses a burst of requests while the control plane is saturated;
 * stacking a toast per refusal would bury the message in itself. The first
 * toast is sticky (danger level), so the user sees it, and a refusal after
 * the window gets a fresh toast — the plane is saturated again, and the
 * user may have dismissed the first.
 *
 * 10 s: an episode (executor refusals with retryAfterMs ~250 ms) resolves in
 * seconds, so the window collapses the whole burst into one toast; a window
 * much shorter would re-toast a user who is still reading the first.
 */
export const SATURATION_TOAST_WINDOW_MS = 10_000

const SATURATION_TOAST_MESSAGE =
  'The terminal is busy — that action was refused. Try again in a moment.'

// null = no saturation toast raised yet. Nullable on purpose: a numeric
// sentinel of 0 would suppress the FIRST toast under a fake clock that
// starts at epoch (Date.now() === 0 in tests).
let lastSaturationToastAt: number | null = null

/**
 * Test/teardown hook: forget the last saturation toast, so a fresh test (or
 * a fresh app session) starts with a clean dedup window. The dedup state is
 * module-global by design — one window per process, exactly like the toast
 * queue itself — so it needs the same explicit reset the queue has.
 */
export function resetSaturationToastDedup(): void {
  lastSaturationToastAt = null
}

/**
 * True when `data` is the error payload of a refused control request
 * (contracts/control.saturated.schema.json). Only the fixed `reason`
 * discriminates: a payload field may grow without silencing the toast.
 */
function isSaturationData(data: unknown): data is Pick<ControlSaturated, 'reason'> {
  return (
    typeof data === 'object' &&
    data !== null &&
    'reason' in data &&
    data.reason === 'control-saturated'
  )
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

type TimerHandle = ReturnType<typeof setTimeout>

interface PendingCall {
  resolve: (v: unknown) => void
  reject: (e: Error) => void
  // The request itself, so a sealed response can be re-sent verbatim (with a
  // fresh id) after the vault layer raises the unlock prompt.
  method: string
  params: unknown
  sealedRetried: boolean
  sent: boolean
  settled: boolean
  /** The id the BACKEND is working on. A vault-sealed retry re-sends the
   *  request under a fresh id, and a withdrawal must name that one. */
  currentID: number
  signal?: AbortSignal
  onAbort?: () => void
  removeAbortListener?: () => void
}

function abortError(): Error {
  const err = new Error('The operation was aborted')
  err.name = 'AbortError'
  return err
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

  private _heartbeatIdleTimer: TimerHandle | null = null
  private _heartbeatResponseTimer: TimerHandle | null = null
  private _heartbeatSocket: WebSocket | null = null
  private _heartbeatRequestID: number | null = null

  constructor(private readonly provider: EndpointProvider) {
    this.subscribe('control.saturated', (params: unknown) => {
      // Consume the generated params type (the contract file must be
      // reachable from main() — dead-exports ratchet). The notification's
      // shape is the contract; the toast does not read it, so the cast is
      // the consumption.
      const _: ControlSaturatedNotification = params as ControlSaturatedNotification
      void _
      this.raiseSaturationToast()
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
    this.setConnectionState({ kind: 'connecting' })
    try {
      let result: EndpointResult
      try {
        result = await this.provider.resolve()
      } catch {
        this._scheduleReconnect()
        return
      }
      if (!result.ok) {
        this.setConnectionState({ kind: 'blocked', failure: result.failure })
        return
      }
      try {
        await this._openSocket(result.endpoint)
        this._backoffMs = MIN_BACKOFF_MS
      } catch {
        if (!this._closingDeliberately) this._scheduleReconnect()
      }
    } finally {
      this._attemptInFlight = false
    }
  }

  private _openSocket(endpoint: Endpoint): Promise<void> {
    return new Promise((resolve, reject) => {
      const subprotocol = `nocx.token.${endpoint.token}`
      const ws = new WebSocket(`ws://${endpoint.host}:${endpoint.port}/session`, subprotocol)
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
   * Installed by the vault layer. When set, a response reporting the vault
   * sealed defers the caller's promise until this hook resolves (the vault
   * was unsealed), then retries the request exactly once. The hook may
   * reject (user cancelled) — that rejection replaces the caller's error.
   * This is the ONE seam where a sealed vault raises the unlock prompt; no
   * call site wraps its own vault calls.
   */
  onVaultSealed?: (method: string) => Promise<void>

  call<T = unknown>(method: string, params: unknown, signal?: AbortSignal): Promise<T> {
    return new Promise((resolve, reject) => {
      const id = this.nextID++
      const pending: PendingCall = {
        resolve: resolve as (v: unknown) => void,
        reject,
        method,
        params,
        sealedRetried: false,
        sent: false,
        settled: false,
        currentID: id,
      }
      const abort = (): void => {
        if (pending.settled) return
        pending.settled = true
        const activeID = pending.currentID
        if (this.pending.get(activeID) === pending) this.pending.delete(activeID)
        pending.removeAbortListener?.()
        reject(abortError())
        const ws = this.ws
        if (pending.sent && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(
            JSON.stringify({ jsonrpc: '2.0', method: 'rpc.cancel', params: { id: activeID } }),
          )
          this._noteOutboundActivity(ws)
        }
      }
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
        reject(new Error('not connected'))
        return
      }
      pending.sent = true
      ws.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
      this._noteOutboundActivity(ws)
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
    this._noteInboundActivity(source)
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
          p.sealedRetried = true
          void this.onVaultSealed(p.method).then(
            () => {
              if (p.signal?.aborted) {
                // Withdrawn while the unlock prompt was up. The first request
                // has already been answered and the replacement was never
                // sent, so nothing is in flight to cancel.
                p.settled = true
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
                p.reject(new Error('not connected'))
                return
              }
              ws.send(JSON.stringify({ jsonrpc: '2.0', id, method: p.method, params: p.params }))
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
              p.reject(e instanceof Error ? e : new Error(String(e)))
            },
          )
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
        this.handleSaturationData(msg.error.data)
        p.reject(new RpcError(msg.error.message ?? 'rpc error', msg.error.code, msg.error.data))
      } else {
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
      if (this.pending.size !== 0) {
        this._armHeartbeatIdle(ws)
        return
      }
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

  private _noteInboundActivity(ws: WebSocket): void {
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

  private rejectAllPending(reason: string): void {
    for (const p of this.pending.values()) {
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

  /**
   * The global fallback for a refused control request. A surface that
   * forgets to show its refusal still degrades visibly — a soft degrade
   * must be visible in the product, not only in a log. Individual
   * surfaces may later disable an action or retry; this is what stops
   * silence.
   */
  private handleSaturationData(data: unknown): void {
    if (!isSaturationData(data)) return
    this.raiseSaturationToast()
  }

  /**
   * The deduplicated saturation toast, shared by the error path and the
   * control.saturated notification path (a refused notification has no id,
   * so the server emits the notification instead of an error — it must be
   * visible the same way).
   */
  private raiseSaturationToast(): void {
    const now = Date.now()
    if (
      lastSaturationToastAt !== null &&
      now - lastSaturationToastAt < SATURATION_TOAST_WINDOW_MS
    ) {
      return
    }
    lastSaturationToastAt = now
    // Lazy import, on purpose: the toast kit pulls in Solid's DOM runtime,
    // which must not load in the node-env unit suites that import the
    // dispatcher (vitest defaults to node; solid modules are jsdom-only by
    // convention). The import resolves once and is cached. The dedup state
    // above is set synchronously, so a burst cannot race the import. The
    // If the chunk fails to load there is no toast to fall back to, so the
    // failure is logged rather than swallowed. A refusal the user is never
    // told about is the exact degrade this whole surface exists to prevent —
    // it would be perverse for the notifier to go quiet without a trace.
    void import('./ui/toast')
      .then((m) => m.showToast({ level: 'danger', message: SATURATION_TOAST_MESSAGE }))
      .catch((err: unknown) => {
        log.error('saturation toast could not be shown', { error: String(err) })
      })
  }
}
