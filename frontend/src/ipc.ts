export class WSClient {
  private ws: WebSocket | null = null
  private onDataCallback: ((data: string) => void) | null = null
  private onOpenCallback: (() => void) | null = null
  private decoder = new TextDecoder()

  connect(port: number): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(`ws://127.0.0.1:${port}/session`)
      this.ws.binaryType = 'arraybuffer'
      // Fresh decoder per connection: it carries the partial-sequence state
      // below, which must not survive into a new session.
      this.decoder = new TextDecoder()

      this.ws.onopen = () => {
        this.onOpenCallback?.()
        resolve()
      }
      this.ws.onerror = () => reject(new Error('ws connection failed'))
      this.ws.onclose = () => console.log('ws closed')

      this.ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          // A PTY read ends wherever the kernel happened to stop, so a
          // multi-byte UTF-8 sequence routinely straddles two frames. Decoding
          // each frame with its own decoder severs those sequences and yields
          // U+FFFD on both halves — rendered as '?' in a diamond, identically
          // in every renderer, which makes it look like a font bug. One
          // streaming decoder holds the tail until its continuation arrives.
          const data = this.decoder.decode(event.data, { stream: true })
          this.onDataCallback?.(data)
        }
      }
    })
  }

  send(data: string): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(new TextEncoder().encode(data))
    }
  }

  sendResize(cols: number, rows: number): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  }

  onData(cb: (data: string) => void): void {
    this.onDataCallback = cb
  }

  onOpen(cb: () => void): void {
    this.onOpenCallback = cb
  }

  get connected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN
  }

  close(): void {
    this.ws?.close()
    this.ws = null
  }
}
