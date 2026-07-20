import './style.css'
import { Terminal } from './terminal'
import { WSClient } from './ipc'

declare global {
  interface Window {
    runtime?: {
      EventsOn: (event: string, cb: (...args: unknown[]) => void) => void
    }
  }
}

async function main() {
  const container = document.getElementById('terminal')
  if (!container) throw new Error('#terminal not found')

  const terminal = new Terminal()
  const ws = new WSClient()

  const port = await new Promise<number>((resolve) => {
    const eventsOn = window.runtime?.EventsOn
    if (eventsOn) {
      eventsOn('ws:port', (p: unknown) => resolve(p as number))
    } else {
      resolve(9876)
    }
  })

  await terminal.mount(container)
  await ws.connect(port)

  ws.onData((data: string) => terminal.write(data))
  terminal.onData((data: string) => ws.send(data))
  terminal.onResize((cols: number, rows: number) => ws.sendResize(cols, rows))

  window.addEventListener('resize', () => terminal.fit())

  window.runtime?.EventsOn('window:resize', () => {
    ws.sendResize(terminal.cols, terminal.rows)
    terminal.fit()
  })

  console.log('nocx: terminal connected')
}

main().catch(console.error)
