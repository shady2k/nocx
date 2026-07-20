import './console-filter'
import './style.css'
import { Terminal } from './terminal'
import { WSClient } from './ipc'
import { GetWSPort } from '../wailsjs/go/main/WailsApp'

async function main() {
  const container = document.getElementById('terminal')
  if (!container) throw new Error('#terminal not found')

  const terminal = new Terminal()
  const ws = new WSClient()

  // Pull the backend WS port (bound Go method — no startup-event race).
  const port = await GetWSPort().catch(() => 9876)

  await terminal.mount(container)
  await ws.connect(port)

  // Wire terminal <-> PTY data both ways.
  ws.onData((data) => terminal.write(data))
  terminal.onData((data) => ws.send(data))

  // Keep the PTY window size in sync with the rendered grid.
  // The FitAddon drives resizes; we forward each one, plus the initial size.
  terminal.onResize((cols, rows) => ws.sendResize(cols, rows))
  ws.sendResize(terminal.cols, terminal.rows)

  console.log('nocx: terminal connected')
}

main().catch(console.error)
