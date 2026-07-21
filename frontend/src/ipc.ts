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
  resolve: () => void
  reject: (err: Error) => void
  id: number
}

export class WSClient {
  private ws: WebSocket | null = null
  private onDataCallback: ((data: string) => void) | null = null
  private onExitCallback: ((sessionId: string) => void) | null = null
  private decoder = new TextDecoder()
  private sessionId: string | null = null
  private pendingOpen: PendingOpen | null = null

  // connect resolves when the WebSocket handshake completes. The session
  // is not open yet — call openSession() next to get the sessionId.
  connect(port: number): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(`ws://127.0.0.1:${port}/session`)
      this.ws.binaryType = 'arraybuffer'
      this.decoder = new TextDecoder()

      this.ws.onopen = () => resolve()
      this.ws.onerror = () => {
        this.rejectPending('ws connection failed')
        reject(new Error('ws connection failed'))
      }
      this.ws.onclose = () => {
        console.log('ws closed')
        this.rejectPending('ws closed')
      }

      this.ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          const frame = decodeFrame(event.data)
          if (frame && frame.sessionId === this.sessionId) {
            const data = this.decoder.decode(frame.payload, { stream: true })
            this.onDataCallback?.(data)
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
      if (sid && sid === this.sessionId) {
        this.onExitCallback?.(sid)
      }
      return
    }

    // Response to the pending openSession request.
    if (this.pendingOpen && msg.id === this.pendingOpen.id) {
      if (msg.error) {
        this.pendingOpen.reject(new Error(msg.error.message || 'open failed'))
        this.pendingOpen = null
        return
      }
      const sid = msg.result?.sessionId
      if (sid) {
        if (!isSessionID(sid)) {
          this.pendingOpen.reject(new Error(`nocx: invalid session-id from server: ${sid}`))
          this.pendingOpen = null
          return
        }
        this.sessionId = sid
        console.log('nocx: session opened:', this.sessionId)
        this.pendingOpen.resolve()
        this.pendingOpen = null
      }
    }
  }

  private rejectPending(reason: string): void {
    if (this.pendingOpen) {
      this.pendingOpen.reject(new Error(reason))
      this.pendingOpen = null
    }
  }

  // send queues raw PTY input for the open session. Drops silently if the
  // sessionId has not arrived yet (AD-7: the client MUST NOT send data frames
  // for a session before the open result arrives).
  send(data: string): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    if (!this.sessionId) return
    const payload = new TextEncoder().encode(data)
    const frame = encodeFrame(this.sessionId, payload)
    this.ws.send(frame)
  }

  sendResize(cols: number, rows: number): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    if (!this.sessionId) return
    const id = nextID()
    this.ws.send(
      JSON.stringify({
        jsonrpc: '2.0',
        id,
        method: 'resize',
        params: { sessionId: this.sessionId, cols, rows, xpixel: 0, ypixel: 0 },
      }),
    )
  }

  // openSession sends the JSON-RPC open request and resolves when the
  // server returns the authoritative sessionId. Per AD-7, nothing may be
  // sent on the data plane before this resolves.
  openSession(cols: number, rows: number): Promise<void> {
    return new Promise((resolve, reject) => {
      const id = nextID()
      this.pendingOpen = { resolve, reject, id }
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

  onData(cb: (data: string) => void): void {
    this.onDataCallback = cb
  }

  onExit(cb: (sessionId: string) => void): void {
    this.onExitCallback = cb
  }

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  get sid(): string | null {
    return this.sessionId
  }

  close(): void {
    if (this.ws && this.sessionId) {
      const id = nextID()
      this.ws.send(
        JSON.stringify({
          jsonrpc: '2.0',
          id,
          method: 'close',
          params: { sessionId: this.sessionId },
        }),
      )
    }
    this.ws?.close()
    this.ws = null
    this.sessionId = null
    this.pendingOpen = null
  }
}
