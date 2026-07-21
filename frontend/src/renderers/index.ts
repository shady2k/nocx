import type { TerminalRenderer } from './types'
import { XtermRenderer } from './xterm'
import { WtermRenderer } from './wterm'

export type RendererName = 'xterm' | 'wterm'

const DEFAULT: RendererName = 'xterm'

export function createRenderer(name: RendererName): TerminalRenderer {
  switch (name) {
    case 'wterm':
      return new WtermRenderer()
    case 'xterm':
      return new XtermRenderer()
  }
}

// Picks the renderer used by all tabs in this window, via ?r=xterm|wterm.
// The tab bar is no longer a renderer bake-off — ADR-0001 settled on xterm.js.
// `?r=wterm` is kept as a diagnostics escape hatch for re-testing the
// switchable renderer seam (ADR-0001 contract).
export function resolveRendererName(): RendererName {
  const r = new URLSearchParams(location.search).get('r')
  return r === 'wterm' || r === 'xterm' ? r : DEFAULT
}
