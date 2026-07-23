import './style.css'
import { GetWSPort } from '../wailsjs/go/main/WailsApp'
import { WSClient } from './ipc'
import { TabManager } from './tabs'
import { createClipboardAccess, ClipboardGate } from './clipboard'
import { ClipboardBannerImpl } from './banner'

async function main() {
  const bar = document.getElementById('tabbar')
  const panes = document.getElementById('panes')
  if (!bar || !panes) throw new Error('#tabbar / #panes not found')

  // Clipboard access is constructed at the composition root and injected
  // down (AD-8). No consumer calls the factory — a test can inject a fake.
  const clipboard = createClipboardAccess()

  // OSC 52 gate — pure state, no DOM. Denied by default (Warp default).
  const gate = new ClipboardGate()

  // Banner — raised once on the first blocked OSC 52 write. The real
  // implementation manipulates the DOM; tests inject a fake.
  const banner = new ClipboardBannerImpl()

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
  new TabManager(bar, panes, client, clipboard, gate, banner)
}

main().catch(console.error)
