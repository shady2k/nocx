import './console-filter'
import './style.css'
import { Terminal } from './terminal'
import { WSClient } from './ipc'
import { GetWSPort } from '../wailsjs/go/main/WailsApp'

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

  const port = await GetWSPort().catch(() => 9876)

  await terminal.mount(container)
  // Wire resize BEFORE mount's rAF fit fires so the initial fit sends the size
  terminal.onResize((cols: number, rows: number) => ws.sendResize(cols, rows))
  terminal.onData((data: string) => ws.send(data))

  await ws.connect(port)

  // If mount's rAF fired before WS was ready, sync now
  if (ws.connected) {
    ws.sendResize(terminal.cols, terminal.rows)
  }

  ws.onData((data: string) => terminal.write(data))

  window.addEventListener('resize', () => terminal.fit())
  window.runtime?.EventsOn('window:resize', () => {
    terminal.fit()
  })

  // Alt-screen scroll: prevent ghostty-web viewport scroll; send arrow keys instead
  terminal.attachCustomWheelEventHandler((event: WheelEvent) => {
    if (!terminal.isAlternateScreen) return false // normal screen: let ghostty-web scroll
    if (terminal.hasMouseTracking) return false   // app handles mouse: let events through

    // Send arrow up/down to the PTY app
    const delta = event.deltaY
    if (delta > 0) {
      ws.send('\x1bOB') // arrow down
    } else if (delta < 0) {
      ws.send('\x1bOA') // arrow up
    }
    return true // prevent ghostty-web viewport scroll
  })

  // When entering alt screen, reset viewportY so writes don't snap to bottom
  terminal.onBufferChange(() => {
    if (terminal.isAlternateScreen) {
      terminal.fit()
    }
  })

  console.log('nocx: terminal connected')
}

main().catch(console.error)
