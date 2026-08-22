import { decodeFrame, encodeFrame, isSessionID } from './frame'
import { Dispatcher } from './dispatcher'
import { historyOutbox } from './history-client'
import type { Exit } from './generated/exit'
import type { Open } from './generated/open'
import type { SessionLiveness } from './generated/session.liveness'
import type { SecretsPaneClosed } from './generated/secrets.paneClosed'
import type { SandboxStatus } from './generated/sandbox.status'
import type { SandboxAccessChanged } from './generated/sandbox.access.changed'
import type {
  SandboxAccessList,
  Event as SandboxAccessEvent,
} from './generated/sandbox.access.list'
import type { SandboxAccessResolve } from './generated/sandbox.access.resolve'
import type { SandboxAccessStatus } from './generated/sandbox.access.status'

export type {
  SandboxStatus,
  SandboxAccessChanged,
  SandboxAccessEvent,
  SandboxAccessList,
  SandboxAccessResolve,
  SandboxAccessStatus,
}

export type SessionSandboxInfo = NonNullable<Open['sandbox']>

export interface SandboxLaunch {
  readonly settingsRevision: number
  readonly addWritable: string[]
  readonly removeWritable: string[]
  readonly addReadOnly: string[]
  readonly removeReadOnly: string[]
}

export interface SandboxRequest extends SandboxLaunch {
  readonly workspace: string
}

/** The open ack's wire shape (contracts/open.schema.json): the server
 *  assigns the session id (AD-7), and the resolved destination mode rides the
 *  same ack so the tab's capability control starts from the backend's own
 *  resolution (nocx-mlm7).
 *
 *  shellIntegrationReason is deliberately absent (nocx-dvql). It answered
 *  "is this session integrated" once, at open, and the two failures that
 *  matter most arrive later — a handshake that expires ten seconds in, and a
 *  channel lost mid-session. The session.integrationChanged notification
 *  answers it as a state instead; two answers would be the defect AD-8
 *  names. */
type OpenResult = {
  sessionId?: string
  instanceId?: string
  sessionEpoch?: number
  cwd?: string
  desiredMode?: Open['desiredMode']
  /** The workspace the backend RESOLVED for this session (nocx-fraus). It is
   *  derived, never sent: the renderer names a pane and the backend walks
   *  pane -> tab -> workspace itself, so this ack is the only place the
   *  renderer can learn where the session landed — the default workspace
   *  never renders, so the renderer has no name of its own for it. */
  workspaceId?: string
  /** The opener the backend ADMITTED, or null for a root session
   *  (nocx-9hu9d). Read rather than dropped because it is the only place the
   *  renderer ever learns it: the edge is written once, at open, and the ack
   *  is the one message that carries it. PROVENANCE ONLY — see
   *  SessionHandle.parent. */
  parent?: Open['parent']
  sandbox?: Open['sandbox']
}

/**
 * What an open carries beyond its geometry and its destination.
 *
 * NAMED rather than positional, for the reason TerminalContentHooks is named
 * (see terminal-content.ts): `user` and `paneId` are both optional strings in
 * adjacent slots on openSSHSessionByHost, so two bare positionals would let
 * every misalignment type-check — which is exactly the defect that put
 * onSetupVault into the onAdoptabilityChange slot.
 */
export interface OpenAnchor {
  /**
   * The pane this session is the pipe of: the renderer-minted UUIDv7 the
   * layout chain stores (design §7). The renderer is the only end that knows
   * it — the backend walks pane -> tab -> workspace from here, and told
   * nothing it can only answer "the default", which leaves every block this
   * session records anchored on nothing (nocx-rtg0.29).
   *
   * ABSENT AND EMPTY ARE DIFFERENT ANSWERS on the wire. validateOpenRaw
   * treats an absent paneId as legitimate — a session attached to no
   * recorded pane — and refuses a malformed one with -32602. An empty string
   * is the second, so the openers below drop the key rather than send it
   * blank.
   */
  paneId?: string
  /** Canonical local cwd for a replacement shell; absent uses backend default. */
  cwd?: string
}

/** Optional ordinary-open facts as the wire wants them. */
function paneParam(anchor: OpenAnchor): { paneId?: string; cwd?: string } {
  return {
    ...(anchor.paneId ? { paneId: anchor.paneId } : {}),
    ...(anchor.cwd ? { cwd: anchor.cwd } : {}),
  }
}

// Ack throttle: at most one ack per session per ~100 ms. Per-frame acks on
// a fast-scrolling terminal would flood the control plane with thousands of
// tiny JSON-RPC notifications every second for no benefit: the ring is
// 256 KB and backpressure from a full ring kicks in at ~8 ms of 32 KB/ms
// output, so a 100 ms interval drains ~12 ring cycles per ack — well within
// the AD-9 trimming budget without needless chatter.
const ACK_INTERVAL_MS = 100

// UTF8StreamDecoder is a zero-delay replacement for TextDecoder with
// stream:true. It decodes and emits every COMPLETE UTF-8 character
// immediately — no timer, no buffering of trailing bytes that are already
// a valid character. Only genuinely incomplete multi-byte sequences (a
// leading byte without enough continuation bytes at the frame boundary)
// are held for the next frame.
//
// TextDecoder in stream:true mode can hold back the final byte(s) of a
// frame indefinitely in WebKitGTK, making the last typed character
// invisible until more output arrives. This decoder eliminates that class
// of bug by construction: if the bytes form a complete character, it is
// returned now, not later.
class UTF8StreamDecoder {
  private tail: number[] = [] // incomplete multi-byte leftovers (0–3 bytes)

  decode(input: Uint8Array): string {
    if (input.length === 0 && this.tail.length === 0) return ''

    // Merge any leftover bytes from the previous chunk with the new input
    // into a single flat byte array so we can scan it linearly.
    const all = new Uint8Array(this.tail.length + input.length)
    if (this.tail.length > 0) all.set(this.tail)
    all.set(input, this.tail.length)
    this.tail = []

    let out = ''
    let i = 0
    const len = all.length

    while (i < len) {
      const b0 = all[i]

      if (b0 < 0x80) {
        // ASCII — one byte, one character. Emit immediately.
        out += String.fromCharCode(b0)
        i++
        continue
      }

      // Determine the expected sequence length and continuation mask.
      let seqLen: number
      if ((b0 & 0xe0) === 0xc0) seqLen = 2
      else if ((b0 & 0xf0) === 0xe0) seqLen = 3
      else if ((b0 & 0xf8) === 0xf0) seqLen = 4
      else {
        // Invalid leading byte — emit U+FFFD and skip.
        out += '\uFFFD'
        i++
        continue
      }

      if (i + seqLen > len) {
        // Not enough continuation bytes in this chunk — save the partial
        // sequence for the next decode() call. This is the ONLY case
        // where bytes are held back, and it only happens when a frame
        // boundary splits a multi-byte character, which is rare.
        this.tail = Array.from(all.slice(i))
        break
      }

      // Validate continuation bytes (must be 10xxxxxx).
      let valid = true
      for (let j = 1; j < seqLen; j++) {
        if ((all[i + j] & 0xc0) !== 0x80) {
          valid = false
          break
        }
      }
      if (!valid) {
        out += '\uFFFD'
        i++
        continue
      }

      // Decode the codepoint and emit.
      let cp: number
      if (seqLen === 2) {
        cp = ((b0 & 0x1f) << 6) | (all[i + 1] & 0x3f)
      } else if (seqLen === 3) {
        cp = ((b0 & 0x0f) << 12) | ((all[i + 1] & 0x3f) << 6) | (all[i + 2] & 0x3f)
      } else {
        cp =
          ((b0 & 0x07) << 18) |
          ((all[i + 1] & 0x3f) << 12) |
          ((all[i + 2] & 0x3f) << 6) |
          (all[i + 3] & 0x3f)
      }

      // Surrogate pairs for codepoints > 0xFFFF.
      if (cp > 0xffff) {
        cp -= 0x10000
        out += String.fromCharCode(0xd800 + (cp >> 10), 0xdc00 + (cp & 0x3ff))
      } else {
        out += String.fromCharCode(cp)
      }
      i += seqLen
    }

    return out
  }

  // reset clears any held partial bytes. Called when the stream position
  // jumps (reattach reset) so stale leading bytes are not spliced onto a
  // new stream position.
  reset(): void {
    this.tail = []
  }
}

interface AttachResult {
  resumed?: boolean
  reset?: boolean
  from: number
}

interface SessionState {
  decoder: UTF8StreamDecoder

  // The session's incarnation identity (nocx-3oupk), as minted by the
  // backend at open (AD-7): instanceId + sessionEpoch. Every observation
  // of this session — exit, integrationChanged, lifecycle.changed — is
  // compared against the pair, so a message out of a previous backend
  // instance is refused instead of applied. Always present: the open
  // contract requires the pair, and _registerHandle refuses an ack
  // without it.
  instanceId: string
  sessionEpoch: number
  // Monotonic byte offset — the total count of payload bytes received for
  // this session. Counted as frame.payload.byteLength, NOT decoded string
  // length, because a multi-byte rune is several bytes and one character.
  offset: number

  // dataCallback receives decoded PTY output for the caller (Tab → renderer).
  // May be null briefly between session creation (open response) and the
  // first onSessionData() call — binary frames arriving in that window are
  // buffered in pendingData and flushed when the callback is attached.
  dataCallback: ((data: string) => void) | null

  // pendingData holds decoded output that arrived before dataCallback was
  // registered. The server starts ringToConn immediately after the open
  // response, so the initial shell prompt can race the caller's
  // session.onData() — without this buffer the prompt is silently lost.
  pendingData: string
  // exitCallback receives the wire Exit (contracts/exit.schema.json): the
  // closed-set cause separating an authoritative shell exit (with its
  // status) from a loss. The failed-reattach path delivers a loss with the
  // same object, so a tab that lost its backend session is treated exactly
  // like a tab whose channel died — marked, never destroyed (nocx-ictcq).
  exitCallback: ((exit: Exit) => void) | null
  resetCallback: (() => void) | null

  // Fires when the backend reports that this session's write queue refused
  // a frame — the channel has stopped accepting bytes and the keystrokes
  // are being dropped. Once per stall, not once per key.
  inputStalledCallback: (() => void) | null

  // Fires when the backend revises what it believes about REACHING this
  // session: alive, or unknown when the host has stopped answering and
  // nothing has ended (contracts/session.liveness.schema.json). The
  // terminal half of that vocabulary never arrives here — a session that
  // ended is the exit notification's news.
  livenessCallback: ((liveness: SessionLiveness) => void) | null

  // The livenessEpoch of the last observation applied to this session. An
  // observation whose epoch is not GREATER describes an older moment,
  // whatever order it arrived in, and is dropped — the receiving half of
  // the rule the backend's record keeps (nocx-iarf9).
  livenessEpoch: number
}

// Per-session ack throttle state, tracked outside SessionState so the timer
// cancel/restart logic is self-contained.
interface AckThrottle {
  timer: ReturnType<typeof setTimeout> | null
  pendingOffset: number
}

export class SessionHandle {
  constructor(
    private client: WSClient,
    readonly sessionId: string,
    /** Where the shell started, ~-abbreviated. Names the tab until a program
     *  sets a title; does not follow `cd` (that needs OSC 7, nocx-5mn.2). */
    readonly cwd: string,
    /** The resolved destination mode the backend stamped at open
     *  (nocx-mlm7): the connection-scope default the tab's capability
     *  control starts from. Never proof integration succeeded — the reason
     *  field and the arrival of markers confirm or downgrade it. */
    readonly desiredMode: Open['desiredMode'] = 'script',
    /** The session that opened this one, as the backend ADMITTED it, or null
     *  for a root session (nocx-9hu9d). The full identity, never a bare id:
     *  an id alone re-resolves to whatever holds it now.
     *
     *  PROVENANCE ONLY (nocx-wtv3p, ADR-0020 §5). It says "A created B" and
     *  confers nothing: no surface may read it to decide that one tab may
     *  observe, drive or close another, and the backend refuses such an
     *  attempt whatever the renderer believes
     *  (internal/transport/ws_lineage_prohibitions_test.go). The one thing it
     *  is read for is the ASK in PaneManager.closePane — naming what a close
     *  would leave running, which is the opposite of acting on them. */
    readonly parent: Open['parent'] = null,
    /** The workspace the backend resolved this session into (nocx-fraus).
     *
     *  IT CARRIES NO BEHAVIOUR, and the contract says so: nothing reads
     *  authority, addressability or reachability from it, and §5.5 forbids
     *  any surface before the fence epic from describing a workspace as
     *  safe, isolated or contained. It is read as PROVENANCE — what the
     *  backend resolved from the pane the renderer named — which is why it
     *  is decoded here rather than dropped: this ack is the only message
     *  that carries it. */
    readonly workspaceId: string = '',
    /** Immutable realized policy for a sandboxed local session. */
    readonly sandbox?: SessionSandboxInfo,
  ) {}

  send(data: string): void {
    this.client.sendToSession(this.sessionId, data)
  }

  sendResize(cols: number, rows: number): void {
    this.client.sendResize(this.sessionId, cols, rows)
  }

  close(): void {
    this.client.closeSession(this.sessionId)
  }

  onData(cb: (data: string) => void): void {
    this.client.onSessionData(this.sessionId, cb)
  }

  onExit(cb: (exit: Exit) => void): void {
    this.client.onSessionExit(this.sessionId, cb)
  }

  // onReset registers a callback that fires when a reattach returns
  // {reset:true} — the ring has advanced past the client offset and the
  // renderer must clear its display before new data arrives.
  onReset(cb: () => void): void {
    this.client.onSessionReset(this.sessionId, cb)
  }

  // onInputStalled registers a callback for the backend's report that this
  // session is dropping the input sent to it.
  onInputStalled(cb: () => void): void {
    this.client.onSessionInputStalled(this.sessionId, cb)
  }

  /** Registers a callback for the backend's revised belief about reaching
   *  this session (nocx-iarf9). Fires on a CHANGE — the backend publishes
   *  when the value changes, not once per probe — so a handler may treat
   *  every call as news. */
  onLiveness(cb: (liveness: SessionLiveness) => void): void {
    this.client.onSessionLiveness(this.sessionId, cb)
  }
}

export class WSClient {
  private sessions = new Map<string, SessionState>()
  // Ack throttle: one per session.
  private acks = new Map<string, AckThrottle>()
  // Reattach-outcome subscribers (nocx-gbhwh): the notice consumes the
  // aggregate of one reconnect's session reattach pass.
  private reconnectResultHandlers = new Set<(r: { resumed: number; lost: number }) => void>()

  constructor(private readonly dispatcherImpl: Dispatcher) {
    // Wire binary frame handling and session reattach on every connect/reconnect.
    this.dispatcher.onConnect(() => {
      // A socket came back, so anything the outbox kept can go now
      // (nocx-rtg0.4). Fire-and-forget: a drain that fails leaves the queue
      // exactly as it was and the next connect tries again, which is the
      // whole point of keeping it.
      void historyOutbox.drain()
      const ws = this.dispatcher.socket!
      ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          const frame = decodeFrame(event.data)
          if (frame) {
            const state = this.sessions.get(frame.sessionId)
            if (state) {
              // Count payload bytes for the per-session offset (AD-9
              // reconnect). Use byteLength, not decoded string length,
              // because every byte counts on the wire.
              state.offset += frame.payload.byteLength
              const text = state.decoder.decode(new Uint8Array(frame.payload))
              if (state.dataCallback) {
                state.dataCallback(text)
              } else {
                // dataCallback is not registered yet — the server starts
                // ringToConn immediately after the open response, so the
                // initial shell prompt can arrive before the caller has
                // a chance to call session.onData(). Buffer until the
                // callback is attached, then flush.
                state.pendingData += text
              }
              this._scheduleAck(frame.sessionId, state.offset)
            }
          }
        }
      }

      // Reattach every session the client still knows about. Each attach
      // carries the last received byte offset so the server can replay
      // what the ring still holds. The outcomes are aggregated so a
      // listener can state what became of the sessions on this reconnect
      // (nocx-gbhwh): resumed, or gone — the backend no longer has them.
      const reattached = [...this.sessions.entries()].map(([sid, state]) =>
        this._sendAttach(sid, state.offset)
          .then((result) => {
            if (result.reset) {
              state.offset = result.from ?? 0
              // A reset means the client fell out of the ring — there
              // is a byte gap in the stream. If the last frame before
              // the drop ended mid-rune, the decoder is holding the
              // leading bytes of a multi-byte character. Reusing those
              // bytes would splice stale leading bytes onto bytes from
              // a different stream position, producing a wrong character
              // or U+FFFD (bead nocx-ao7 reborn).
              // Reset the decoder so the stream starts clean.
              state.decoder.reset()
              state.resetCallback?.()
            }
            return 'resumed' as const
          })
          .catch(() => {
            // A reattach that failed is a loss, not an exit: the backend no
            // longer holds the session, and the tab must be marked, never
            // destroyed (nocx-ictcq). Delivered through the SAME callback
            // as the backend's exit notification, with the same closed-set
            // cause, so a fix that only touches the notification's sender
            // leaves this path uncaused and the tab still dies on it.
            state.exitCallback?.({
              sessionId: sid,
              cause: 'interrupted',
              // The identity this session was opened with (nocx-3oupk):
              // echoed, never minted (AD-7) — the backend's pair is the
              // only one that exists.
              instanceId: state.instanceId,
              sessionEpoch: state.sessionEpoch,
            })
            this.sessions.delete(sid)
            return 'lost' as const
          }),
      )
      // Report once every attach has settled — the socket is back before
      // this fires, never the other way round. Zero sessions resolves
      // immediately with { resumed: 0, lost: 0 }.
      void Promise.all(reattached).then((outcomes) => {
        const resumed = outcomes.filter((o) => o === 'resumed').length
        const lost = outcomes.length - resumed
        for (const h of this.reconnectResultHandlers) {
          h({ resumed, lost })
        }
      })
    })

    // Handle server-initiated exit notifications. The cause is the closed
    // set of contracts/exit.schema.json: "exited" (the shell exited, with
    // its status) or "interrupted" (a loss). A cause that is neither — or
    // absent, from a peer that predates the contract — is treated as a
    // loss, deliberately: a wrongly-marked tab is recoverable, a wrongly
    // destroyed tab is lost work, so the safe direction is to never close
    // on ambiguous data.
    this.dispatcher.subscribe('exit', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      // The session's own identity (nocx-3oupk): the wire's pair is the
      // backend's word (AD-7), and the identity learned at open is the
      // fallback for a notification that predates the fields — the renderer
      // never mints one. With neither, the notification cannot be told from
      // a previous incarnation and is dropped rather than applied.
      const state = this.sessions.get(sid)
      const instanceId = typeof raw.instanceId === 'string' ? raw.instanceId : state?.instanceId
      const sessionEpoch =
        typeof raw.sessionEpoch === 'number' ? raw.sessionEpoch : state?.sessionEpoch
      if (typeof instanceId !== 'string' || typeof sessionEpoch !== 'number') {
        return
      }
      const isExited = raw.cause === 'exited' && Number.isInteger(raw.status)
      // A loss never carries a status; an exited event missing its status is
      // malformed and reads as a loss too (the schema's exited branch
      // requires it).
      const exit: Exit = isExited
        ? {
            sessionId: sid,
            cause: 'exited',
            status: raw.status as number,
            instanceId,
            sessionEpoch,
          }
        : { sessionId: sid, cause: 'interrupted', instanceId, sessionEpoch }
      this._flushAck(sid)
      state?.exitCallback?.(exit)
      this.sessions.delete(sid)
    })

    // The backend revised what it believes about reaching a session
    // (nocx-iarf9). Two refusals before anything is delivered, and both are
    // the point rather than defensiveness:
    //
    //   - the incarnation must be the one this tab opened. A report naming
    //     another backend instance, or another epoch of this session id, is
    //     about a different session that merely shares the id (nocx-3oupk).
    //   - the observation must be NEWER than the last one applied. A report
    //     delayed behind a faster path would otherwise revive a belief the
    //     backend has already moved on from, purely by arriving last.
    this.dispatcher.subscribe('session.liveness', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      const epoch = raw.livenessEpoch
      if (typeof epoch !== 'number' || epoch <= state.livenessEpoch) return
      const value = raw.liveness
      if (value !== 'alive' && value !== 'unknown') return
      const observedAt = raw.observedAt
      if (typeof observedAt !== 'string') return
      state.livenessEpoch = epoch
      state.livenessCallback?.({
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
        liveness: value,
        livenessEpoch: epoch,
        observedAt,
      })
    })

    // The backend dropped input for a session: its write queue is full,
    // so the channel underneath has stopped accepting bytes.
    this.dispatcher.subscribe('inputStalled', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const sid = (params as Record<string, unknown>).sessionId
      if (typeof sid !== 'string') return
      this.sessions.get(sid)?.inputStalledCallback?.()
    })
  }

  /** The shared control-plane dispatcher (the sealed-access seam installed
   *  at the app root). Exposed for server-initiated notification
   *  subscriptions (lifecycle.changed) that ride the same socket; RPC stays
   *  behind the typed methods above. */
  get dispatcher(): Dispatcher {
    return this.dispatcherImpl
  }

  // connect resolves when the WebSocket handshake completes. Sessions are
  // not open yet — call openSession() next to get a SessionHandle. The host
  // defaults to loopback (the Wails shell serves the page locally); the
  // plain-browser dev path overrides it with the page's own hostname.
  // The token is the per-launch capability carried in Sec-WebSocket-Protocol.
  connect(port: number, host = '127.0.0.1', token = ''): Promise<void> {
    this.sessions.clear()
    this.acks.clear()
    return this.dispatcher.connect(port, host, token)
  }

  /** Call a control-plane method and resolve with its typed result. The
   *  control plane carries JSON-RPC (AD-1); session-bound calls (open,
   *  resize, close) go through the dedicated methods above so their
   *  correlation with the binary data plane stays owned here. */
  call<T = unknown>(method: string, params: unknown): Promise<T> {
    return this.dispatcher.call<T>(method, params)
  }

  /** Tell the backend a pane closed, so its PENDING CAPTURES die with it
   *  (nocx-tsajw). The paneId is the pane's one identity — the same UUIDv7
   *  the layout chain stores and history.record carries — declared once in
   *  contracts/secrets.paneClosed.schema.json (SecretsPaneClosed is generated
   *  from it). Fire-and-forget: a lost notification is covered by the
   *  transport disconnect, which is the same destruction the pane's death
   *  implies.
   *
   *  NOT the same act as panes.close, and the method was renamed to say so
   *  (nocx-isoph.4): this closes a capture scope and touches no store, while
   *  panes.close removes the pane from the durable chain and can mint a
   *  replacement tab. A pane the user closes sends both. */
  notifyPaneClosed(paneId: string): void {
    const params: SecretsPaneClosed = { paneId }
    this.dispatcher.notify('secrets.paneClosed', params)
  }

  // --- ack plumbing -------------------------------------------------------

  // _scheduleAck posts a throttled ack for the session. If an ack is already
  // pending (timer running), the pending offset is updated but the timer is
  // not reset — this batches multiple frames into one ack per ACK_INTERVAL_MS.
  private _scheduleAck(sessionId: string, offset: number): void {
    let ack = this.acks.get(sessionId)
    if (!ack) {
      ack = { timer: null, pendingOffset: 0 }
      this.acks.set(sessionId, ack)
    }
    ack.pendingOffset = offset
    if (ack.timer !== null) return

    const throttled = ack
    throttled.timer = setTimeout(() => {
      throttled.timer = null
      this._sendAck(sessionId, throttled.pendingOffset)
    }, ACK_INTERVAL_MS)
  }

  private _flushAck(sessionId: string): void {
    const ack = this.acks.get(sessionId)
    if (!ack) return
    if (ack.timer !== null) {
      clearTimeout(ack.timer)
      ack.timer = null
    }
    this.acks.delete(sessionId)
  }

  private _sendAck(sessionId: string, offset: number): void {
    this.dispatcher.notify('ack', { sessionId, offset })
  }

  // --- session opening ----------------------------------------------------

  // openSession sends the JSON-RPC open request and resolves with a
  // SessionHandle carrying the server-assigned sessionId. Per AD-7, the
  // server assigns the authoritative id — nothing may be sent on the data
  // plane for this session before this resolves.
  //
  // Whether the session tries to become integrated is NOT ours to say
  // (nocx-tr2n): the backend asks for every session and falls back to a
  // plain terminal where it cannot. It used to be an `enhanced` argument
  // here, which both ssh openers below silently omitted — so an ssh tab
  // never established a lifecycle channel and could never show a block. The
  // renderer still wires the editor BEFORE this call, so no invisible
  // prompt gap can occur (nocx-4ff.10).
  openSession(cols: number, rows: number, anchor: OpenAnchor = {}): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result))
  }

  openSandboxedSession(
    cols: number,
    rows: number,
    request: SandboxRequest,
    anchor: OpenAnchor,
  ): Promise<SessionHandle> {
    const sandbox: {
      workspace: string
      settingsRevision: number
      addWritable?: string[]
      removeWritable?: string[]
      addReadOnly?: string[]
      removeReadOnly?: string[]
    } = {
      workspace: request.workspace,
      settingsRevision: request.settingsRevision,
    }
    if (request.addWritable.length > 0) sandbox.addWritable = [...request.addWritable]
    if (request.removeWritable.length > 0) sandbox.removeWritable = [...request.removeWritable]
    if (request.addReadOnly.length > 0) sandbox.addReadOnly = [...request.addReadOnly]
    if (request.removeReadOnly.length > 0) sandbox.removeReadOnly = [...request.removeReadOnly]
    return this.dispatcher
      .call<OpenResult>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        ...paneParam(anchor),
        sandbox,
      })
      .then((result) => this._registerHandle(result))
  }

  sandboxStatus(): Promise<SandboxStatus | null> {
    return this.dispatcher.call<SandboxStatus | null>('sandbox.status', {})
  }

  sandboxAccessStatus(): Promise<SandboxAccessStatus | null> {
    return this.dispatcher.call<SandboxAccessStatus | null>('sandbox.access.status', {})
  }

  sandboxAccessList(limit = 200): Promise<SandboxAccessList> {
    return this.dispatcher.call<SandboxAccessList>('sandbox.access.list', { limit })
  }

  sandboxAccessResolve(
    eventId: string,
    decision: 'dismiss' | 'globalReadOnly' | 'globalReadWrite',
  ): Promise<SandboxAccessResolve> {
    return this.dispatcher.call<SandboxAccessResolve>('sandbox.access.resolve', {
      eventId,
      decision,
    })
  }

  onSandboxAccessChanged(callback: (change: SandboxAccessChanged) => void): () => void {
    return this.dispatcher.subscribe('sandbox.access.changed', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const revision = (params as Record<string, unknown>).revision
      if (typeof revision !== 'number' || !Number.isSafeInteger(revision) || revision < 0) return
      callback({ revision })
    })
  }

  // openSSHSession opens an SSH session via a profile ID. The backend
  // resolves host, auth and jump host from the profile store.
  // Passwords are never sent over the wire.
  openSSHSession(
    cols: number,
    rows: number,
    profileId: string,
    anchor: OpenAnchor = {},
  ): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        kind: 'ssh',
        profileId,
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result))
  }

  // openSSHSessionByHost opens a direct SSH session by hostname/alias,
  // resolved through ~/.ssh/config on the backend. No saved profile needed.
  openSSHSessionByHost(
    cols: number,
    rows: number,
    host: string,
    user?: string,
    anchor: OpenAnchor = {},
  ): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        kind: 'ssh',
        host,
        user,
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result))
  }

  /** The open ack's wire shape (contracts/open.schema.json). Every open —
   *  local, profile SSH, direct-host SSH — carries the resolved launch
   *  policy and the refusal reason alongside the id and cwd. */
  private _registerHandle(result: OpenResult): SessionHandle {
    const sid = result?.sessionId
    if (!sid || !isSessionID(sid)) {
      throw new Error(`nocx: invalid session-id from server: ${sid}`)
    }
    // The identity is required by the contract and never minted here
    // (AD-7): an ack without it cannot tell this session from a restored
    // record, so it is refused like an invalid id (nocx-3oupk).
    const { instanceId, sessionEpoch } = result
    if (typeof instanceId !== 'string' || typeof sessionEpoch !== 'number') {
      throw new Error(`nocx: invalid session identity from server: ${sid}`)
    }
    this.sessions.set(sid, {
      decoder: new UTF8StreamDecoder(),
      offset: 0,
      dataCallback: null,
      pendingData: '',
      exitCallback: null,
      resetCallback: null,
      inputStalledCallback: null,
      livenessCallback: null,
      livenessEpoch: 0,
      instanceId,
      sessionEpoch,
    })
    return new SessionHandle(
      this,
      sid,
      result?.cwd ?? '',
      result?.desiredMode ?? 'script',
      result?.parent ?? null,
      result?.workspaceId ?? '',
      result?.sandbox,
    )
  }

  // --- reattach -----------------------------------------------------------

  private _sendAttach(sessionId: string, offset: number): Promise<AttachResult> {
    return this.dispatcher.call<AttachResult>('attach', { sessionId, offset })
  }

  // --- data plane ---------------------------------------------------------

  // sendToSession frames raw PTY input for one session. Drops silently if
  // the session is not in the map (AD-7: the client MUST NOT send data
  // frames for a session before the open result arrives, or after exit).
  sendToSession(sessionId: string, data: string): void {
    const ws = this.dispatcher.socket
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (!this.sessions.has(sessionId)) return
    const payload = new TextEncoder().encode(data)
    const frame = encodeFrame(sessionId, payload)
    ws.send(frame)
  }

  sendResize(sessionId: string, cols: number, rows: number): void {
    const ws = this.dispatcher.socket
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (!this.sessions.has(sessionId)) return
    // Fire-and-forget — response is silently dropped.
    void this.dispatcher
      .call('resize', { sessionId, cols, rows, xpixel: 0, ypixel: 0 })
      .catch(() => {})
  }

  closeSession(sessionId: string): void {
    const ws = this.dispatcher.socket
    if (ws && ws.readyState === WebSocket.OPEN) {
      void this.dispatcher.call('close', { sessionId }).catch(() => {})
    }
    this._flushAck(sessionId)
    this.sessions.delete(sessionId)
  }

  // --- session callbacks --------------------------------------------------

  onSessionData(sessionId: string, cb: (data: string) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.dataCallback = cb
      // Flush any output that arrived between session creation (open
      // response) and this call — the initial shell prompt can race
      // onSessionData() because the server starts ringToConn immediately.
      if (state.pendingData) {
        const buffered = state.pendingData
        state.pendingData = ''
        cb(buffered)
      }
    }
  }

  onSessionExit(sessionId: string, cb: (exit: Exit) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.exitCallback = cb
    }
  }

  onSessionReset(sessionId: string, cb: () => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.resetCallback = cb
    }
  }

  onSessionInputStalled(sessionId: string, cb: () => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.inputStalledCallback = cb
    }
  }

  onSessionLiveness(sessionId: string, cb: (liveness: SessionLiveness) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.livenessCallback = cb
    }
  }

  /** Report the aggregate outcome of one reconnect's session-reattach pass:
   *  how many known sessions came back and how many are gone (the backend
   *  no longer has them). Fires once per onConnect, after every attach
   *  attempt has settled — never before the socket is back (nocx-gbhwh).
   *  Returns an unsubscribe. */
  onReconnectResult(cb: (r: { resumed: number; lost: number }) => void): () => void {
    this.reconnectResultHandlers.add(cb)
    return () => {
      this.reconnectResultHandlers.delete(cb)
    }
  }

  // --- accessors ----------------------------------------------------------

  get connected(): boolean {
    return this.dispatcher.connected
  }

  // For test introspection only: the current reconnect backoff value.
  get backoffMs(): number {
    return this.dispatcher.backoffMs
  }

  // For test introspection only: whether the reconnect timer is pending.
  get reconnectPending(): boolean {
    return this.dispatcher.reconnectPending
  }

  close(): void {
    this.dispatcher.close()
    this.sessions.clear()
    for (const ack of this.acks.values()) {
      if (ack.timer !== null) clearTimeout(ack.timer)
    }
    this.acks.clear()
  }
}
