export class WSClient {
  private ws: WebSocket | null = null
  private onDataCallback: ((data: string) => void) | null = null
  private onOpenCallback: (() => void) | null = null

  connect(port: number): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(`ws://127.0.0.1:${port}/session`)
      this.ws.binaryType = 'arraybuffer'

      this.ws.onopen = () => {
        this.onOpenCallback?.()
        resolve()
      }
      this.ws.onerror = () => reject(new Error('ws connection failed'))
      this.ws.onclose = () => console.log('ws closed')

      this.ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          const data = new TextDecoder().decode(event.data)
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
