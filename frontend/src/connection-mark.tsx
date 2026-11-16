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
//
// IT DRAWS, IT DOES NOT DECIDE. The condition, the measurement and the
// observation's age all come from connection-condition.ts over the one
// observation the backend published; a second fold here was the defect this
// stage deletes (task 5.5).

import { createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import { connectionCondition, connectionRoundTripMs, observedAgo } from './connection-condition'
import type { PaneConnection } from './generated/pane.connection'
import type { ConnectionCondition } from './connection-condition'
import { ConnectionIndicator } from './ui/connection-indicator'

export interface ConnectionMark {
  /** Restate the observation and the condition the store folded for it (the
   *  store owns the session-ended overlay; the mark must not fold again).
   *  The mark redraws, or disappears. */
  set(observation: PaneConnection | null, condition?: ConnectionCondition): void
  dispose(): void
}

export function mountConnectionMark(target: HTMLElement): ConnectionMark {
  const [observation, setObservation] = createSignal<PaneConnection | null>(null)
  const [condition, setCondition] = createSignal<ConnectionCondition | undefined>(undefined)
  const host = document.createElement('div')
  host.className = 'nocx-connection-mark'
  target.appendChild(host)
  const dispose = render(
    () => (
      <ConnectionIndicator
        condition={condition() ?? connectionCondition(observation())}
        roundTripMs={connectionRoundTripMs(observation())}
        observedAgo={observedAgo(observation()?.observedAt)}
      />
    ),
    host,
  )
  return {
    set: (next, folded) => {
      setObservation(next)
      setCondition(folded)
    },
    dispose: () => {
      dispose()
      host.remove()
    },
  }
}
