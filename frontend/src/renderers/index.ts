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

// Picks the tab that opens first, via ?r=xterm|wterm. Every renderer is
// reachable from the tab bar regardless — this only sets the entry point.
export function resolveRendererName(): RendererName {
  const r = new URLSearchParams(location.search).get('r')
  return r === 'wterm' || r === 'xterm' ? r : DEFAULT
}
