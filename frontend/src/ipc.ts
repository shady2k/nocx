import { decodeFrame, encodeFrame, isSessionID } from './frame'
import { Dispatcher } from './dispatcher'

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

  exitCallback: ((sessionId: string) => void) | null
  resetCallback: (() => void) | null
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

  onExit(cb: (sessionId: string) => void): void {
    this.client.onSessionExit(this.sessionId, cb)
  }

  // onReset registers a callback that fires when a reattach returns
  // {reset:true} — the ring has advanced past the client offset and the
  // renderer must clear its display before new data arrives.
  onReset(cb: () => void): void {
    this.client.onSessionReset(this.sessionId, cb)
  }
}

export class WSClient {
  private sessions = new Map<string, SessionState>()
  // Ack throttle: one per session.
  private acks = new Map<string, AckThrottle>()

  constructor(private dispatcher: Dispatcher) {
    // Wire binary frame handling and session reattach on every connect/reconnect.
    this.dispatcher.onConnect(() => {
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
      // what the ring still holds.
      for (const [sid, state] of this.sessions) {
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
          })
          .catch(() => {
            state.exitCallback?.(sid)
            this.sessions.delete(sid)
          })
      }
    })

    // Handle server-initiated exit notifications.
    this.dispatcher.subscribe('exit', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const sid = (params as Record<string, unknown>).sessionId
      if (typeof sid !== 'string') return
      this._flushAck(sid)
      this.sessions.get(sid)?.exitCallback?.(sid)
      this.sessions.delete(sid)
    })
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
  // enhanced tells the backend to spawn the shell in marker-only prompt mode
  // (ADR-0006); the frontend wires the editor BEFORE this call so no invisible
  // prompt gap can occur (nocx-4ff.10).
  openSession(cols: number, rows: number, enhanced: boolean): Promise<SessionHandle> {
    return this.dispatcher
      .call<{ sessionId?: string; cwd?: string }>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        enhanced,
      })
      .then((result) => {
        const sid = result?.sessionId
        if (!sid || !isSessionID(sid)) {
          throw new Error(`nocx: invalid session-id from server: ${sid}`)
        }
        this.sessions.set(sid, {
          decoder: new UTF8StreamDecoder(),
          offset: 0,
          dataCallback: null,
          pendingData: '',
          exitCallback: null,
          resetCallback: null,
        })
        return new SessionHandle(this, sid, result?.cwd ?? '')
      })
  }

  // openSSHSession opens an SSH session via a profile ID. The backend
  // resolves host, auth and jump host from the profile store.
  // Passwords are never sent over the wire.
  openSSHSession(cols: number, rows: number, profileId: string): Promise<SessionHandle> {
    return this.dispatcher
      .call<{ sessionId?: string; cwd?: string }>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        kind: 'ssh',
        profileId,
      })
      .then((result) => {
        const sid = result?.sessionId
        if (!sid || !isSessionID(sid)) {
          throw new Error(`nocx: invalid session-id from server: ${sid}`)
        }
        this.sessions.set(sid, {
          decoder: new UTF8StreamDecoder(),
          offset: 0,
          dataCallback: null,
          pendingData: '',
          exitCallback: null,
          resetCallback: null,
        })
        return new SessionHandle(this, sid, result?.cwd ?? '')
      })
  }

  // openSSHSessionByHost opens a direct SSH session by hostname/alias,
  // resolved through ~/.ssh/config on the backend. No saved profile needed.
  openSSHSessionByHost(
    cols: number,
    rows: number,
    host: string,
    user?: string,
  ): Promise<SessionHandle> {
    return this.dispatcher
      .call<{ sessionId?: string; cwd?: string }>('open', {
        cols,
        rows,
        xpixel: 0,
        ypixel: 0,
        kind: 'ssh',
        host,
        user,
      })
      .then((result) => {
        const sid = result?.sessionId
        if (!sid || !isSessionID(sid)) {
          throw new Error(`nocx: invalid session-id from server: ${sid}`)
        }
        this.sessions.set(sid, {
          decoder: new UTF8StreamDecoder(),
          offset: 0,
          dataCallback: null,
          pendingData: '',
          exitCallback: null,
          resetCallback: null,
        })
        return new SessionHandle(this, sid, result?.cwd ?? '')
      })
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

  onSessionExit(sessionId: string, cb: (sessionId: string) => void): void {
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
