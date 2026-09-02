import { createSignal } from 'solid-js'

/**
 * The document's visibility is a window fact, not a panel fact. Panels receive
 * the composed SidebarViewProps.visible accessor; they must not read
 * document.hidden themselves or each invent a subtly different gate.
 *
 * This factory owns the one listener for one application mount and its teardown
 * so the signal is seeded from the document's current state before any panel
 * can start work. The desktop composition root mounts one sidebar for the
 * application lifetime; tests may create isolated mounts.
 * It is deliberately a fourth listener: dispatcher reconnect acceleration,
 * wake-report detection, and terminal-link disarming answer different lifecycle
 * questions and do not own panel-work visibility.
 */
export interface AppVisibility {
  visible: () => boolean
  destroy(): void
}

export function createAppVisibility(target: Document = document): AppVisibility {
  const [visible, setVisible] = createSignal(!target.hidden)
  const onVisibilityChange = (): void => {
    setVisible(!target.hidden)
  }
  let destroyed = false

  target.addEventListener('visibilitychange', onVisibilityChange)

  return {
    visible,
    destroy(): void {
      if (destroyed) return
      destroyed = true
      target.removeEventListener('visibilitychange', onVisibilityChange)
    },
  }
}
