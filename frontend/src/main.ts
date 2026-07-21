import './style.css'
import { GetWSPort } from '../wailsjs/go/main/WailsApp'
import { WSClient } from './ipc'
import { TabManager } from './tabs'

async function main() {
  const bar = document.getElementById('tabbar')
  const panes = document.getElementById('panes')
  if (!bar || !panes) throw new Error('#tabbar / #panes not found')

  // Bound Go method — no startup-event race. Guarded so the renderers still
  // mount without a Wails runtime (plain browser), where GetWSPort throws.
  let port = 9876
  try {
    port = await GetWSPort()
  } catch {
    console.warn('nocx: no Wails runtime, using fallback WS port', port)
  }

  const client = new WSClient()
  await client.connect(port)

  // TabManager opens the first tab and activates it in the constructor.
  // The renderer is selected via ?r=xterm|wterm inside TabManager.
  new TabManager(bar, panes, client)
}

main().catch(console.error)
