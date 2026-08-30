// Mounting the pane's connection mark.
//
// The same shape as mountIntegrationNotice: a Solid island the vanilla pane
// controller owns, created once and driven imperatively afterwards. It is a
// separate module rather than a method on the pane because the pane is not a
// Solid component and must not become one — AD-6's UI-layer corollary keeps
// framework effects out of the terminal controller.
//
// Unlike the integration notice this island is POSITIONED over the grid
// rather than inserted into the flow, so it takes no height and cannot
// reflow the terminal. That is the whole reason it is allowed to overlay at
// all: it costs the pane no layout and it is absent in the healthy state.

import { createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import {
  connectionCondition,
  connectionRoundTripMs,
  type ConnectionFacts,
} from './connection-condition'
import { ConnectionIndicator } from './ui/connection-indicator'

export interface ConnectionMark {
  /** Restate the facts; the mark redraws, or disappears. */
  set(facts: ConnectionFacts): void
  dispose(): void
}

export function mountConnectionMark(target: HTMLElement): ConnectionMark {
  const [facts, setFacts] = createSignal<ConnectionFacts>({ sessionLost: false, liveness: null })
  const host = document.createElement('div')
  host.className = 'nocx-connection-mark'
  target.appendChild(host)
  const dispose = render(
    () => (
      <ConnectionIndicator
        condition={connectionCondition(facts())}
        roundTripMs={connectionRoundTripMs(facts())}
        observedAgo={observedAgo(facts())}
      />
    ),
    host,
  )
  return {
    set: setFacts,
    dispose: () => {
      dispose()
      host.remove()
    },
  }
}

/** How old the belief is, in words. The renderer DISPLAYS observedAt and
 *  never subtracts it, says the contract — so this rounds to a coarse phrase
 *  rather than counting, and says nothing at all when the belief is fresh. */
function observedAgo(facts: ConnectionFacts): string | undefined {
  const at = facts.liveness?.observedAt
  if (!at) return undefined
  const ms = Date.now() - Date.parse(at)
  if (!Number.isFinite(ms) || ms < 60_000) return undefined
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 60) return `since ${minutes} min ago`
  return `since ${Math.floor(minutes / 60)} h ago`
}
