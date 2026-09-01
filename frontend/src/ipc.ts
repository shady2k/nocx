import { decodeFrame, encodeFrame, isSessionID } from './frame'
import { Dispatcher } from './dispatcher'
import { historyOutbox } from './history-client'
import type { AttachResult } from './generated/attach'
import type { Exit } from './generated/exit'
import type { Open } from './generated/open'
import type { SessionEntry, SessionsInventoryResult } from './generated/sessions.inventory'
import type { LiveSession, SessionsLiveResult } from './generated/sessions.live'
import type {
  EffectiveSize as SessionSize,
  Gap as SessionOutputGap,
  Run as WireRun,
  SessionOutput,
} from './generated/session.output'
import type { SessionDisplaced } from './generated/session.displaced'
import type { SessionLiveness } from './generated/session.liveness'
import type { SessionObservationChanged } from './generated/session.observationChanged'
import { isDriverState } from './pane-observation'
import type { SessionSignal } from './generated/session.signal'
import type { SecretsPaneClosed } from './generated/secrets.paneClosed'

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
}

/** The paneId as the wire wants it: the key, or no key at all. One helper for
 *  all three openers, because "how an absent pane is expressed" is one fact
 *  and three copies of `...(x ? {paneId: x} : {})` would be three. */
function paneParam(anchor: OpenAnchor): { paneId?: string } {
  return anchor.paneId ? { paneId: anchor.paneId } : {}
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
/** One contiguous stretch of a session's recorded output, and where it sits
 *  in the stream. The offset is carried rather than implied: it is the only
 *  thing that says whether two runs are adjacent or have a hole between
 *  them, and that is the difference between joining bytes and inventing
 *  them.
 *
 *  Not exported: it is reached as the element type of SessionRecording.runs,
 *  which is, and a second exported name for one shape is a second thing to
 *  keep in step. */
interface RecordedRun {
  offset: number
  body: Uint8Array
}

/** What a read of a session's recording came back with (nocx-22k1c.2). The
 *  runs are what survives, the gaps are what does not, and `produced` is the
 *  recording's end — the offset a client attaches at once it has read this
 *  far, so the recording and the ring meet with nothing between them. */
export interface SessionRecording {
  from: number
  produced: number
  runs: RecordedRun[]
  gaps: SessionOutputGap[]
  /** The size the BACKEND decided this session runs at (nocx-eidfb.1).
   *  Bytes alone are not a screen — the same stream wraps differently at two
   *  widths — so a surface that renders a recovered recording at its own
   *  guess would disagree with the client that watched it live. */
  size: SessionSize
}

/** One recorded run off the wire, turned into bytes. Its own function so the
 *  wire shape is named exactly once on this side of the socket: base64 in,
 *  bytes out, and the offset carried through untouched. */
function decodeRun(run: WireRun): RecordedRun {
  return { offset: run.offset, body: decodeBase64(run.body) }
}

/** What a reclaim recovered before it attached, for the surface that draws
 *  the pane to say. `gaps` is the whole of "what is missing" over the
 *  recovered span: the ranges the retention bound dropped, plus — when the
 *  recording ends before the ring's window begins — the stretch nothing
 *  kept at all. */
export interface SessionRecovery {
  /** Bytes handed to the terminal ahead of the live stream. */
  bytes: number
  gaps: SessionOutputGap[]
  /** The size the recovered bytes were produced at. A surface rendering them
   *  at anything else is drawing a different screen from the one the session
   *  printed. */
  size: SessionSize
}

/** What a read that could not happen came back with. A named constant, not
 *  an inline literal, because the degrade has to be the SAME shape as a
 *  successful read of a session that printed nothing — a reclaim must not be
 *  able to tell them apart, or the failure path would grow a branch. */
const EMPTY_RECORDING: SessionRecording = {
  from: 0,
  produced: 0,
  runs: [],
  gaps: [],
  // The same named default a session with no client attached holds
  // (nocx-eidfb.1). Not zero: a size of zero is not a size, and a surface
  // that had to branch on one would be branching on a degrade rather than
  // rendering an empty recording.
  size: { cols: 80, rows: 24, xpixel: 0, ypixel: 0 },
}

/** The gap reason for a stretch that neither the recording nor the ring
 *  holds. The renderer DERIVES this one because only it can: `produced`
 *  belongs to session.output and `replayFrom` to sessions.live, and neither
 *  owner can see the other's number.
 *
 *  The backend mints the same word for a range nobody recorded on its side
 *  (internal/content's GapReasonUnrecorded, nocx-k6p18.2), and that is
 *  deliberate rather than a second owner: it is one fact — nobody has these
 *  bytes — arrived at from two directions, and one fact told to a user in two
 *  vocabularies is the defect. Keep them equal. What must stay distinct is
 *  this and `cap`: the cap dropped bytes that were here, and telling a person
 *  the cap took bytes it never had would send them to a limit that did
 *  nothing.
 *
 *  EXPORTED so the surface that reports the hole (recovery-notice.tsx) reads
 *  the word this file mints rather than spelling a second copy of it
 *  (nocx-fz4qa). Two string literals that must agree and only meet at runtime
 *  is the same delay-fused shape, one layer up. */
export const UNRECORDED = 'unrecorded'

/** base64 → bytes. atob is the platform's, and its output is one byte per
 *  code unit by definition, so the copy below is exact rather than a
 *  re-encoding. */
function decodeBase64(b64: string): Uint8Array {
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

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

  // The geometry THIS client last reported for this session — what it told
  // the backend at open, and every resize since. It is kept because the
  // report has to survive the socket: the backend returns a session to its
  // named default the moment its last client detaches (nocx-eidfb.2), so a
  // reconnecting client that said nothing would leave a live window's
  // terminal running at 80x24. It rides the attach, which re-takes the
  // session and its size together.
  //
  // Null for a session this client has never reported a size for — a pane
  // reclaimed by a window that has not laid it out yet. That is not a report
  // of nothing: the attach then carries no geometry at all and the backend
  // leaves the session at the size it is running at.
  reported: { cols: number; rows: number } | null

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
  // What an ENROLLED agent pane's screen is currently inviting, as the
  // agent's driver classified it in the backend (nocx-szb40.3). Null for
  // every pane nobody enrolled, which is almost all of them.
  observationCallback: ((observation: SessionObservationChanged) => void) | null

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
    /** What a reclaim recovered from the backend's recording before it
     *  attached (nocx-22k1c.2), or null for a handle that was never
     *  reclaimed — an open starts an empty session and has nothing to
     *  recover.
     *
     *  It is READ BY THE SURFACE THAT DRAWS THE PANE, which is the only
     *  thing that can say what is missing where a person will see it. The
     *  bytes are already queued for the terminal by the time this handle
     *  exists; what this carries is the part the terminal cannot show —
     *  the ranges nothing kept. A recovered scrollback with a silent hole
     *  in it is the one outcome worse than a short one. */
    readonly recovered: SessionRecovery | null = null,
  ) {}

  send(data: string): void {
    this.client.sendToSession(this.sessionId, data)
  }

  sendResize(cols: number, rows: number): void {
    this.client.sendResize(this.sessionId, cols, rows)
  }

  /** Address a signal to the command running in this session (nocx-23rph).
   *  See WSClient.signalSession — this is the same call, spelled where a
   *  surface that already holds the handle can reach it. */
  signal(signal: SessionSignal['signal']): Promise<SessionSignal> {
    return this.client.signalSession(this.sessionId, signal)
  }

  close(): void {
    this.client.closeSession(this.sessionId)
  }

  detach(): void {
    this.client.detachSession(this.sessionId)
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
  onObservation(cb: (observation: SessionObservationChanged) => void): void {
    this.client.onSessionObservation(this.sessionId, cb)
  }

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
  // Displacement subscribers (nocx-oevq4, D8): another client took a session
  // this one was holding. A client-level set rather than a per-session
  // callback because the surface that has to react — the pane holding the
  // session — is found BY the session id, and a pane that never registered a
  // callback is exactly the one that would otherwise go on advertising a
  // terminal it no longer has.
  private displacedHandlers = new Set<(displaced: SessionDisplaced) => void>()

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
        this._sendAttach(sid, state.offset, state, state.reported)
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
    // What an enrolled agent pane's screen is inviting (nocx-szb40.3).
    //
    // Guarded at the boundary like every other unsolicited notification, and
    // bound to the INCARNATION here rather than downstream: this is the one
    // place that already holds the (instanceId, sessionEpoch) pair the open
    // ack minted, so an observation out of a previous incarnation is refused
    // once, for every consumer, instead of each of them remembering to.
    //
    // The state is checked against the closed set on the way in. A value
    // nobody wrote a branch for would otherwise reach the indicator and land
    // in whichever branch was written last — and every consumer of this
    // treats what it cannot read as busy, which only works if what it cannot
    // read never gets through.
    this.dispatcher.subscribe('session.observationChanged', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      const agent = raw.agent
      if (typeof agent !== 'string' || agent === '') return
      const paneState = raw.state
      if (!isDriverState(paneState)) return
      state.observationCallback?.({
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
        agent,
        state: paneState,
      })
    })

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
      // The MEASUREMENT travels with the belief, and both halves of it.
      //
      // This rebuilds the fact rather than passing the payload on — the
      // identity fields are taken from the session's own record, not from the
      // wire — and that is exactly how these two came to be dropped: they are
      // the only fields with no local counterpart to copy from, so a
      // reconstruction written field by field simply never named them. The
      // renderer then saw slow === undefined for every host and drew nothing,
      // however slow the link got (nocx-y3i0s).
      //
      // Absent stays absent. The contract says absent and zero are one
      // statement — "nothing was measured" — so a host that never answered
      // must not arrive looking like one that answered instantly, and a
      // default of 0 or false here would be exactly that lie.
      const roundTripMs = typeof raw.roundTripMs === 'number' ? raw.roundTripMs : undefined
      const slow = typeof raw.slow === 'boolean' ? raw.slow : undefined
      state.livenessEpoch = epoch
      state.livenessCallback?.({
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
        liveness: value,
        livenessEpoch: epoch,
        observedAt,
        ...(roundTripMs !== undefined ? { roundTripMs } : {}),
        ...(slow !== undefined ? { slow } : {}),
      })
    })

    // Another client took a session this one was holding (D8). The take has
    // already happened on the backend — no output is coming and no input will
    // be accepted — so this is not a request to give it up, it is the news
    // that it is gone.
    //
    // The session is dropped from the map BEFORE the handlers run, and that
    // order is the point: a handler that reacted while the entry was still
    // there could send one more keystroke into a stream this client no longer
    // owns, and sendToSession's only guard is the map.
    this.dispatcher.subscribe('session.displaced', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      // Unknown, or a previous incarnation of this id: not this client's
      // session, so nothing is dropped and nobody is told. The identity is
      // checked exactly as every other session-scoped notification checks it
      // (nocx-3oupk) — the renderer never mints one and never assumes one.
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      this._flushAck(sid)
      this.sessions.delete(sid)
      const displaced: SessionDisplaced = {
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
      }
      for (const h of this.displacedHandlers) h(displaced)
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

  // Start discovery and connection. Sessions are not open yet — call
  // openSession() next to get a SessionHandle. The Dispatcher asks its
  // EndpointProvider for the endpoint on every attempt.
  start(): void {
    this.sessions.clear()
    this.acks.clear()
    this.dispatcher.start()
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
      .then((result) => this._registerHandle(result, { cols, rows }))
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
      .then((result) => this._registerHandle(result, { cols, rows }))
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
      .then((result) => this._registerHandle(result, { cols, rows }))
  }

  /** The open ack's wire shape (contracts/open.schema.json). Every open —
   *  local, profile SSH, direct-host SSH — carries the resolved launch
   *  policy and the refusal reason alongside the id and cwd. */
  private _registerHandle(
    result: OpenResult,
    reported: { cols: number; rows: number },
  ): SessionHandle {
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
    this._registerSession(sid, { instanceId, sessionEpoch }, 0, reported)
    return new SessionHandle(
      this,
      sid,
      result?.cwd ?? '',
      result?.desiredMode ?? 'script',
      result?.parent ?? null,
      result?.workspaceId ?? '',
    )
  }

  /** Mint the per-session state this client keeps. ONE PLACE, because an open
   *  and a reclaim are two ways of coming to hold the same thing, and a second
   *  literal would be the field the next callback is added to and forgotten
   *  in. The offset differs: an open starts at zero, a reclaim starts where
   *  the backend says the replay does. */
  private _registerSession(
    sessionId: string,
    identity: { instanceId: string; sessionEpoch: number },
    offset: number,
    reported: { cols: number; rows: number } | null = null,
  ): void {
    this.sessions.set(sessionId, {
      decoder: new UTF8StreamDecoder(),
      offset,
      reported,
      dataCallback: null,
      pendingData: '',
      exitCallback: null,
      resetCallback: null,
      inputStalledCallback: null,
      livenessCallback: null,
      observationCallback: null,
      livenessEpoch: 0,
      instanceId: identity.instanceId,
      sessionEpoch: identity.sessionEpoch,
    })
  }

  // --- reattach -----------------------------------------------------------

  /** The claim, in the one shape both callers use (contracts/attach.schema
   *  .json). The identity travels with it because a claim names a session
   *  COMPLETELY — instanceId and sessionEpoch, never a bare id — so a binding
   *  written against a backend that has since restarted is refused as what it
   *  is rather than answered "no such session". */
  private _sendAttach(
    sessionId: string,
    offset: number,
    identity: { instanceId: string; sessionEpoch: number },
    reported: { cols: number; rows: number } | null = null,
  ): Promise<AttachResult> {
    return this.dispatcher.call<AttachResult>('attach', {
      sessionId,
      offset,
      instanceId: identity.instanceId,
      sessionEpoch: identity.sessionEpoch,
      // The claiming client's own geometry, when it has one. A claim takes
      // the session AND its size — the client that attached last is the one
      // the shared channel follows (nocx-eidfb.2) — so a reconnect that
      // omitted it would leave the terminal at the default the backend put
      // it on when this client went away. Omitted entirely, not sent as
      // zeroes, when this client has never laid the session out: the backend
      // then leaves the size alone rather than reading it as "no client".
      ...(reported ? { cols: reported.cols, rows: reported.rows, xpixel: 0, ypixel: 0 } : {}),
    })
  }

  // --- reclaim -------------------------------------------------------------

  /** What the BACKEND is holding right now: every live session with the pane
   *  it is the pipe of and the offset its replay starts at
   *  (contracts/sessions.live.schema.json).
   *
   *  This is the answer `this.sessions` cannot give. That Map is renderer
   *  process memory, so a window that has just started knows nothing, and the
   *  reconnect pass below reattaches only what is in it — which is why live
   *  PTYs were orphaned by closing the window. The list replaces the memory;
   *  it does not replace the reattach. */
  listLiveSessions(): Promise<LiveSession[]> {
    return this.dispatcher
      .call<SessionsLiveResult>('sessions.live', {})
      .then((result) => result.sessions)
  }

  /** The sessions currently held by authenticated helper generations known
   *  to the coordinator (contracts/sessions.inventory.schema.json). */
  listHelperSessions(): Promise<SessionEntry[]> {
    return this.dispatcher
      .call<SessionsInventoryResult>('sessions.inventory', {})
      .then((result) => result.sessions)
  }

  // --- the recording -------------------------------------------------------

  /** Everything the backend recorded for one session, read back by OFFSET
   *  (contracts/session.output.schema.json).
   *
   *  This is what the replay ring cannot give. The ring is 256 KiB of
   *  transport-side buffering (AD-9) and deliberately not scrollback, so a
   *  window opened an hour into a run used to see about ten screens of the
   *  hour. The recording holds the rest.
   *
   *  It PAGES, because one answer is bounded: the target is the `produced`
   *  the FIRST answer reported, never the latest — the session is live and
   *  its own end keeps moving, so chasing it would never terminate. Whatever
   *  arrives after that offset is the ring's to replay, which is the point of
   *  attaching at it.
   *
   *  The bytes stay bytes. They are decoded by the SESSION's decoder, at the
   *  call site that owns it, because a UTF-8 rune can straddle any boundary
   *  the recorder happened to write at — including the boundary between the
   *  recording and the ring's first frame. */
  readSessionOutput(
    sessionId: string,
    identity?: { instanceId: string; sessionEpoch: number },
    from = 0,
  ): Promise<SessionRecording> {
    const claim = identity
      ? { instanceId: identity.instanceId, sessionEpoch: identity.sessionEpoch }
      : {}
    const runs: RecordedRun[] = []
    const gaps: SessionOutputGap[] = []
    let produced = 0
    let size: SessionSize = EMPTY_RECORDING.size

    const page = (at: number, target: number | null): Promise<SessionRecording> =>
      this.dispatcher
        .call<SessionOutput>('session.output', { sessionId, ...claim, from: at })
        .then((answer) => {
          produced = answer.produced
          size = answer.effectiveSize
          const end = target ?? answer.produced
          for (const gap of answer.gaps) gaps.push(gap)
          let cursor = at
          for (const run of answer.runs) {
            const decoded = decodeRun(run)
            runs.push(decoded)
            cursor = decoded.offset + decoded.body.length
          }
          // No progress means the recording holds nothing more at this
          // offset, whatever `produced` says — stop rather than ask the same
          // question again for ever.
          if (cursor <= at || cursor >= end) return { from, produced, runs, gaps, size }
          return page(cursor, end)
        })

    return page(from, null)
  }

  /** Feed a recording to the session's own decoder, in stream order.
   *
   *  The decoder is RESET wherever a run does not continue the one before
   *  it. Bytes are missing there, so a partial rune held from before the
   *  hole can never be completed by what comes after — the same reason the
   *  reset path resets it, and without it the first character past a hole
   *  would be a replacement glyph. A run boundary that IS adjacent — the
   *  seam between two pages of one unbroken recording — is not a hole and
   *  must not reset anything, or a rune split across the page boundary would
   *  be lost. */
  private _decodeRecording(state: SessionState, rec: SessionRecording): string {
    let text = ''
    let next: number | null = null
    for (const run of rec.runs) {
      if (next !== null && run.offset !== next) state.decoder.reset()
      text += state.decoder.decode(run.body)
      next = run.offset + run.body.length
    }
    return text
  }

  /** Take a live session back: register it as if this client had opened it,
   *  then attach at the offset the backend named.
   *
   *  The handle it returns knows the session and NOT the pane's own facts —
   *  no cwd, no mode, no workspace — because those belong to the pane, which
   *  is the renderer's durable identity and is read from the layout store.
   *  The backend owns the live binding and the renderer owns the pane; a
   *  reclaim is where the two are joined, not where either learns the other's
   *  half.
   *
   *  A reset here is not a loss to report: this client has drawn nothing yet,
   *  so it simply starts at the offset the backend resumed from. */
  reclaimSession(entry: LiveSession): Promise<SessionHandle> {
    const identity = { instanceId: entry.instanceId, sessionEpoch: entry.sessionEpoch }
    // The recording is read BEFORE the claim, and a failure to read it never
    // fails the claim. Recovering the scrollback is what makes the reclaimed
    // pane worth looking at; taking the session back is the job, and a
    // backend with no content store wired answers this method "not found"
    // (registration.go) on an otherwise perfectly reclaimable session.
    return this.readSessionOutput(entry.sessionId, identity)
      .catch(() => EMPTY_RECORDING)
      .then((recording) => {
        // Attach at the LATER of the two, which is what makes the two halves
        // meet. While recording is on the ring may not free a byte the
        // recorder has not passed, so `produced` is at or past the ring's
        // window and attaching there resumes exactly where the recording
        // stopped. With recording off the recording stands still while acks
        // go on freeing the ring, `replayFrom` overtakes it, and attaching
        // below the window would be answered with a reset — losing the
        // recording as well as the gap.
        const attachAt = Math.max(recording.produced, entry.replayFrom)
        this._registerSession(entry.sessionId, identity, attachAt)
        const state = this.sessions.get(entry.sessionId)
        const gaps = [...recording.gaps]
        let recovered = ''
        if (state) {
          recovered = this._decodeRecording(state, recording)
          // Queued, not delivered: the surface has not registered its data
          // callback yet, and pendingData is the buffer that already exists
          // for exactly this window. The ring's own replay lands behind it,
          // in stream order, and both flush together on onData.
          state.pendingData = recovered + state.pendingData
          if (attachAt !== recording.produced) {
            // The live stream does not continue the recording — the attach
            // starts past its end. A partial rune the decoder is holding from
            // the recording's last bytes can never be completed by what comes
            // next, so it is dropped here rather than fused onto the first
            // byte of the ring's replay and drawn as a character neither half
            // contained.
            state.decoder.reset()
          }
        }
        if (recording.produced < entry.replayFrom) {
          // Neither owner holds this stretch. Said, not swallowed: a shorter
          // scrollback nobody mentioned is indistinguishable from a shorter
          // session.
          gaps.push({ start: recording.produced, end: entry.replayFrom, reason: UNRECORDED })
        }
        return this._sendAttach(entry.sessionId, attachAt, identity)
          .then((result) => {
            const attached = this.sessions.get(entry.sessionId)
            if (attached) attached.offset = result.from
            // No cwd, no mode, no parent, no workspace: every one of those is
            // a fact of the PANE or of the open that made the session, and
            // the reclaiming window reads them from the layout store it
            // already has. Inventing them here would be the second owner
            // (AD-8) — and the one that is wrong, because it would be
            // guessing.
            return new SessionHandle(this, entry.sessionId, '', 'script', null, '', {
              bytes: recovered.length,
              gaps,
              size: recording.size,
            })
          })
          .catch((err) => {
            // A refused claim leaves NOTHING behind: the map must not hold a
            // session this client does not have, or its next reconnect would
            // reattach to it and its input would be sent into a stream it
            // never owned.
            this.sessions.delete(entry.sessionId)
            throw err
          })
      })
  }

  /** Fires when another client takes a session this one was holding (D8,
   *  contracts/session.displaced.schema.json). The session is already gone
   *  from this client's map when a handler runs: the backend has stopped
   *  sending its output and refuses its input, so the only honest state here
   *  is not holding it. Returns an unsubscribe. */
  onSessionDisplaced(cb: (displaced: SessionDisplaced) => void): () => void {
    this.displacedHandlers.add(cb)
    return () => this.displacedHandlers.delete(cb)
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
    // Recorded before it is sent, and recorded whatever the backend answers:
    // this is what THIS client measured, and it is what the next attach has
    // to report to take the session's size back (nocx-eidfb.2).
    const state = this.sessions.get(sessionId)
    if (state) state.reported = { cols, rows }
    // Fire-and-forget — response is silently dropped.
    void this.dispatcher
      .call('resize', { sessionId, cols, rows, xpixel: 0, ypixel: 0 })
      .catch(() => {})
  }

  /**
   * Address a signal to the command running in one session (nocx-23rph).
   *
   * NOT fire-and-forget, unlike resize: the answer is the whole point. A
   * signal sent to a pane sitting at a prompt is refused honestly in the
   * RESULT (`outcome`), because it is not a caller error and there is
   * nothing for the transport to report — and a control that silently does
   * nothing is indistinguishable from a broken one. The caller reads the
   * outcome and says so.
   *
   * A closed socket rejects rather than resolving a fiction: the renderer
   * cannot know whether the command is still running, and a made-up
   * "delivered" is the one answer that must never be given.
   */
  signalSession(sessionId: string, signal: SessionSignal['signal']): Promise<SessionSignal> {
    return this.dispatcher.call<SessionSignal>('session.signal', { sessionId, signal })
  }

  detachSession(sessionId: string): void {
    const ws = this.dispatcher.socket
    if (ws && ws.readyState === WebSocket.OPEN) {
      void this.dispatcher.call('detach', { sessionId }).catch(() => {})
    }
    this._flushAck(sessionId)
    this.sessions.delete(sessionId)
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

  onSessionObservation(
    sessionId: string,
    cb: (observation: SessionObservationChanged) => void,
  ): void {
    const state = this.sessions.get(sessionId)
    if (state) state.observationCallback = cb
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
