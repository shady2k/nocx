import { decodeFrame, encodeFrame, isSessionID } from './frame'

let requestID = 0

function nextID(): number {
  requestID++
  return requestID
}

// The control-plane messages we receive, mirroring the JSON-RPC 2.0 contract
// documented in internal/transport/frame.go. Every field is optional: this is
// untrusted input off the socket, narrowed at each use.
interface ControlMessage {
  id?: number
  method?: string
  params?: { sessionId?: string }
  result?: { sessionId?: string }
  error?: { code?: number; message?: string }
}

interface PendingOpen {
  resolve: (sessionId: string) => void
  reject: (err: Error) => void
}

interface SessionState {
  // Per-session streaming TextDecoder. A frame can end mid-rune; one shared
  // decoder would splice bytes across interleaved sessions and corrupt both
  // — that is bead nocx-ao7 reborn in a new form.
  decoder: TextDecoder
  dataCallback: ((data: string) => void) | null
  exitCallback: ((sessionId: string) => void) | null
}

export class SessionHandle {
  constructor(
    private client: WSClient,
    readonly sessionId: string,
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
}

export class WSClient {
  private ws: WebSocket | null = null
  private sessions = new Map<string, SessionState>()
  private pendingOpens = new Map<number, PendingOpen>()

  // connect resolves when the WebSocket handshake completes. Sessions are
  // not open yet — call openSession() next to get a SessionHandle.
  connect(port: number): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(`ws://127.0.0.1:${port}/session`)
      this.ws.binaryType = 'arraybuffer'
      this.sessions.clear()
      this.pendingOpens.clear()

      this.ws.onopen = () => resolve()
      this.ws.onerror = () => {
        this.rejectAllPending('ws connection failed')
        reject(new Error('ws connection failed'))
      }
      this.ws.onclose = () => {
        console.log('ws closed')
        this.rejectAllPending('ws closed')
      }

      this.ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          const frame = decodeFrame(event.data)
          if (frame) {
            const state = this.sessions.get(frame.sessionId)
            if (state) {
              const text = state.decoder.decode(frame.payload, { stream: true })
              state.dataCallback?.(text)
            }
          }
        } else if (typeof event.data === 'string') {
          this.handleControlMessage(event.data)
        }
      }
    })
  }

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
        this.sessions.get(sid)?.exitCallback?.(sid)
        this.sessions.delete(sid)
      }
      return
    }

    // Response to a pending openSession request.
    if (msg.id !== undefined) {
      const pending = this.pendingOpens.get(msg.id)
      if (pending) {
        if (msg.error) {
          pending.reject(new Error(msg.error.message || 'open failed'))
          this.pendingOpens.delete(msg.id)
          return
        }
        const sid = msg.result?.sessionId
        if (sid) {
          if (!isSessionID(sid)) {
            pending.reject(new Error(`nocx: invalid session-id from server: ${sid}`))
            this.pendingOpens.delete(msg.id)
            return
          }
          this.sessions.set(sid, {
            decoder: new TextDecoder(),
            dataCallback: null,
            exitCallback: null,
          })
          console.log('nocx: session opened:', sid)
          pending.resolve(sid)
          this.pendingOpens.delete(msg.id)
        }
      }
    }
  }

  private rejectAllPending(reason: string): void {
    for (const pending of this.pendingOpens.values()) {
      pending.reject(new Error(reason))
    }
    this.pendingOpens.clear()
  }

  // openSession sends the JSON-RPC open request and resolves with a
  // SessionHandle carrying the server-assigned sessionId. Per AD-7, the
  // server assigns the authoritative id — nothing may be sent on the data
  // plane for this session before this resolves.
  openSession(cols: number, rows: number): Promise<SessionHandle> {
    return new Promise((resolve, reject) => {
      const id = nextID()
      this.pendingOpens.set(id, {
        resolve: (sid: string) => resolve(new SessionHandle(this, sid)),
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
    this.sessions.delete(sessionId)
  }

  onSessionData(sessionId: string, cb: (data: string) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.dataCallback = cb
    }
  }

  onSessionExit(sessionId: string, cb: (sessionId: string) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.exitCallback = cb
    }
  }

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  close(): void {
    this.ws?.close()
    this.ws = null
    this.sessions.clear()
    this.pendingOpens.clear()
  }
}
