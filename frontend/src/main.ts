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

  ;(window as any).__term = terminal

  const port = await GetWSPort().catch(() => 9876)

  await terminal.mount(container)
  terminal.onResize((cols: number, rows: number) => ws.sendResize(cols, rows))
  terminal.onData((data: string) => ws.send(data))

  await ws.connect(port)

  if (ws.connected) {
    ws.sendResize(terminal.cols, terminal.rows)
  }

  ws.onData((data: string) => {
    console.debug('ws:onData', {
      len: data.length,
      alt: terminal.isAlternateScreen,
    })
    terminal.write(data)
  })

  terminal.attachCustomWheelEventHandler((event: WheelEvent) => {
    console.debug('wheel', {
      deltaY: event.deltaY,
      alt: terminal.isAlternateScreen,
      mouseTracking: terminal.hasMouseTracking,
    })
    if (!terminal.isAlternateScreen) return false
    if (terminal.hasMouseTracking) return false

    const delta = event.deltaY
    if (delta > 0) {
      ws.send('\x1bOB')
    } else if (delta < 0) {
      ws.send('\x1bOA')
    }
    return true
  })

  terminal.onBufferChange(() => {
    if (terminal.isAlternateScreen) {
      terminal.fit()
    }
  })

  ;(window as any).__diag = () => {
    const canvas = container.querySelector('canvas')
    if (!canvas) return { error: 'no canvas' }
    const ctx = (canvas as HTMLCanvasElement).getContext('2d')
    return {
      canvasCSS: { w: canvas.clientWidth, h: canvas.clientHeight },
      canvasPhysical: { w: (canvas as HTMLCanvasElement).width, h: (canvas as HTMLCanvasElement).height },
      container: { w: container.clientWidth, h: container.clientHeight },
      cols: terminal.cols,
      rows: terminal.rows,
      alt: terminal.isAlternateScreen,
      smoothing: ctx?.imageSmoothingEnabled,
      transform: ctx?.getTransform(),
    }
  }

  window.addEventListener('resize', () => terminal.fit())
  window.runtime?.EventsOn('window:resize', () => {
    terminal.fit()
  })

  console.log('nocx: terminal connected')
}

main().catch(console.error)
