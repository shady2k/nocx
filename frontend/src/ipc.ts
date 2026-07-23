import { decodeFrame, encodeFrame, isSessionID } from './frame'

let requestID = 0

function nextID(): number {
  requestID++
  return requestID
}

// Reconnect backoff: start at 250 ms, double each attempt, cap at 5 s.
// Jitter of up to 50 % of the current backoff is added so a reload storm
// from many clients does not synchronise onto the server.
const MIN_BACKOFF_MS = 250
const MAX_BACKOFF_MS = 5000

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

// The control-plane messages we receive, mirroring the JSON-RPC 2.0 contract
// documented in internal/transport/frame.go. Every field is optional: this is
// untrusted input off the socket, narrowed at each use.
interface ControlMessage {
  id?: number
  method?: string
  params?: { sessionId?: string }
  result?: {
    sessionId?: string
    cwd?: string
    resumed?: boolean
    reset?: boolean
    from?: number
  }
  error?: { code?: number; message?: string }
}

interface PendingOpen {
  resolve: (sessionId: string, cwd: string) => void
  reject: (err: Error) => void
}

interface PendingAttach {
  sessionId: string
  resolve: (result: { resumed?: boolean; reset?: boolean; from: number }) => void
  reject: (err: Error) => void
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
  private ws: WebSocket | null = null
  private sessions = new Map<string, SessionState>()
  private pendingOpens = new Map<number, PendingOpen>()
  private pendingAttaches = new Map<number, PendingAttach>()

  // Ack throttle: one per session.
  private acks = new Map<string, AckThrottle>()

  // Reconnect state.
  private _port = 0
  private _closingDeliberately = false
  private _backoffMs = MIN_BACKOFF_MS
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null

  // connect resolves when the WebSocket handshake completes. Sessions are
  // not open yet — call openSession() next to get a SessionHandle.
  connect(port: number): Promise<void> {
    this._port = port
    this._closingDeliberately = false
    this._backoffMs = MIN_BACKOFF_MS
    this.sessions.clear()
    this.acks.clear()
    return this._connectInternal()
  }

  private _connectInternal(): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(`ws://127.0.0.1:${this._port}/session`)
      this.ws.binaryType = 'arraybuffer'
      this.pendingOpens.clear()
      this.pendingAttaches.clear()

      this.ws.onopen = () => resolve()
      this.ws.onerror = () => {
        this.rejectAllPending('ws connection failed')
        reject(new Error('ws connection failed'))
      }
      this.ws.onclose = () => {
        this.rejectAllPending('ws closed')
        if (!this._closingDeliberately) {
          this._scheduleReconnect()
        }
      }

      this.ws.onmessage = (event: MessageEvent) => {
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
        } else if (typeof event.data === 'string') {
          this.handleControlMessage(event.data)
        }
      }
    })
  }

  // --- reconnect plumbing -------------------------------------------------

  private _scheduleReconnect(): void {
    if (this._reconnectTimer !== null) return
    const jitter = Math.random() * this._backoffMs * 0.5
    const delay = this._backoffMs + jitter
    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null
      void this._tryReconnect()
    }, delay)
    this._backoffMs = Math.min(this._backoffMs * 2, MAX_BACKOFF_MS)
  }

  private async _tryReconnect(): Promise<void> {
    try {
      await this._connectInternal()
      this._backoffMs = MIN_BACKOFF_MS

      // Reattach every session the client still knows about. Each attach
      // carries the last received byte offset so the server can replay
      // what the ring still holds.
      for (const [sid, state] of this.sessions) {
        this._sendAttach(sid, state.offset)
          .then((result) => {
            if (result.reset) {
              state.offset = result.from
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
    } catch {
      if (!this._closingDeliberately) {
        this._scheduleReconnect()
      }
    }
  }

  private _sendAttach(sessionId: string, offset: number): Promise<AttachResult> {
    return new Promise((resolve, reject) => {
      const id = nextID()
      this.pendingAttaches.set(id, { sessionId, resolve, reject })
      this.ws!.send(
        JSON.stringify({
          jsonrpc: '2.0',
          id,
          method: 'attach',
          params: { sessionId, offset },
        }),
      )
    })
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
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    this.ws.send(
      JSON.stringify({
        jsonrpc: '2.0',
        method: 'ack',
        params: { sessionId, offset },
      }),
    )
  }

  // --- control-plane handling ---------------------------------------------

  private handleControlMessage(data: string): void {
    let msg: ControlMessage
    try {
      msg = JSON.parse(data) as ControlMessage
    } catch {
      return
    }

    // exit notification (has method, no id).
    if (msg.method === 'exit' && msg.id === undefined) {
      const sid = msg.params?.sessionId
      if (sid) {
        this._flushAck(sid)
        this.sessions.get(sid)?.exitCallback?.(sid)
        this.sessions.delete(sid)
      }
      return
    }

    if (msg.id !== undefined) {
      // Pending openSession response.
      const pendingOpen = this.pendingOpens.get(msg.id)
      if (pendingOpen) {
        if (msg.error) {
          pendingOpen.reject(new Error(msg.error.message || 'open failed'))
          this.pendingOpens.delete(msg.id)
          return
        }
        const sid = msg.result?.sessionId
        if (sid) {
          if (!isSessionID(sid)) {
            pendingOpen.reject(new Error(`nocx: invalid session-id from server: ${sid}`))
            this.pendingOpens.delete(msg.id)
            return
          }
          this.sessions.set(sid, {
            decoder: new UTF8StreamDecoder(),
            offset: 0,
            dataCallback: null,
            pendingData: '',
            exitCallback: null,
            resetCallback: null,
          })
          pendingOpen.resolve(sid, msg.result?.cwd ?? '')
          this.pendingOpens.delete(msg.id)
        }
        return
      }

      // Pending attach response.
      const pendingAttach = this.pendingAttaches.get(msg.id)
      if (pendingAttach) {
        if (msg.error) {
          pendingAttach.reject(new Error(msg.error.message || 'attach failed'))
          this.pendingAttaches.delete(msg.id)
          return
        }
        const result = msg.result ?? {}
        pendingAttach.resolve({
          resumed: result.resumed,
          reset: result.reset,
          from: result.from ?? 0,
        })
        this.pendingAttaches.delete(msg.id)
        return
      }
    }
  }

  private rejectAllPending(reason: string): void {
    for (const pending of this.pendingOpens.values()) {
      pending.reject(new Error(reason))
    }
    this.pendingOpens.clear()
    for (const attach of this.pendingAttaches.values()) {
      attach.reject(new Error(reason))
    }
    this.pendingAttaches.clear()
  }

  // openSession sends the JSON-RPC open request and resolves with a
  // SessionHandle carrying the server-assigned sessionId. Per AD-7, the
  // server assigns the authoritative id — nothing may be sent on the data
  // plane for this session before this resolves.
  openSession(cols: number, rows: number): Promise<SessionHandle> {
    return new Promise((resolve, reject) => {
      const id = nextID()
      this.pendingOpens.set(id, {
        resolve: (sid: string, cwd: string) => resolve(new SessionHandle(this, sid, cwd)),
        reject,
      })
      this.ws!.send(
        JSON.stringify({
          jsonrpc: '2.0',
          id,
          method: 'open',
          params: { cols, rows, xpixel: 0, ypixel: 0 },
        }),
      )
    })
  }

  // sendToSession frames raw PTY input for one session. Drops silently if
  // the session is not in the map (AD-7: the client MUST NOT send data
  // frames for a session before the open result arrives, or after exit).
  sendToSession(sessionId: string, data: string): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    if (!this.sessions.has(sessionId)) return
    const payload = new TextEncoder().encode(data)
    const frame = encodeFrame(sessionId, payload)
    this.ws.send(frame)
  }

  sendResize(sessionId: string, cols: number, rows: number): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    if (!this.sessions.has(sessionId)) return
    const id = nextID()
    this.ws.send(
      JSON.stringify({
        jsonrpc: '2.0',
        id,
        method: 'resize',
        params: { sessionId, cols, rows, xpixel: 0, ypixel: 0 },
      }),
    )
  }

  closeSession(sessionId: string): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      const id = nextID()
      this.ws.send(
        JSON.stringify({
          jsonrpc: '2.0',
          id,
          method: 'close',
          params: { sessionId },
        }),
      )
    }
    this._flushAck(sessionId)
    this.sessions.delete(sessionId)
  }

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

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  // For test introspection only: the current reconnect backoff value.
  get backoffMs(): number {
    return this._backoffMs
  }

  // For test introspection only: whether the reconnect timer is pending.
  get reconnectPending(): boolean {
    return this._reconnectTimer !== null
  }

  close(): void {
    this._closingDeliberately = true
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer)
      this._reconnectTimer = null
    }
    this.ws?.close()
    this.ws = null
    this.sessions.clear()
    this.pendingOpens.clear()
    this.pendingAttaches.clear()
    for (const ack of this.acks.values()) {
      if (ack.timer !== null) clearTimeout(ack.timer)
    }
    this.acks.clear()
  }
}
