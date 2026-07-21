import './console-filter'
import './style.css'
import { GetWSPort } from '../wailsjs/go/main/WailsApp'
import { TabManager } from './tabs'
import { resolveRendererName } from './renderers'

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

  // ?r=ghostty|xterm|wterm picks which tab opens first; the others mount on
  // demand when clicked (or Cmd/Ctrl+1..3).
  const tabs = new TabManager(bar, panes, port)
  await tabs.activate(resolveRendererName())
}

main().catch(console.error)
