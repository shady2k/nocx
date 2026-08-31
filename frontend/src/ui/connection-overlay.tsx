/**
 * ConnectionOverlay — the startup and transport-recovery surface.
 *
 * This is a kit component because a connection outage is one application-wide
 * condition, not a sentence each surface should invent. The native Dialog
 * supplies the top-layer and inertness contract; this component supplies only
 * the connection states, the one action that can act in each state, and a
 * notification when it leaves the top layer.
 */
import {
  Show,
  createEffect,
  createSignal,
  onCleanup,
  onMount,
  untrack,
  type Accessor,
  type Component,
} from 'solid-js'
import { render } from 'solid-js/web'
import { Stack } from './stack'

import { Button } from './button'
import { Dialog } from './dialog'
import { Spinner } from './spinner'

export type ConnectionOverlayState =
  | { kind: 'connecting' }
  | { kind: 'waiting'; nextAttemptInMs: number }
  | { kind: 'blocked'; message: string; remedy: string }
  | { kind: 'online' }

export interface ConnectionOverlayProps {
  state: Accessor<ConnectionOverlayState>
  /** Invoked by Retry. The component decides nothing about when to retry. */
  onRetry: () => void
  /** Invoked after the modal has transitioned from visible to hidden. */
  onHidden?: () => void
  /** Minimum visible time from first mount, in ms. */
  minimumVisibleMs?: number
}

const APP_ICON = '/appicon-96.png'

function sentence(state: ConnectionOverlayState): string {
  switch (state.kind) {
    case 'connecting':
      return 'Connecting to nocx…'
    case 'waiting': {
      const seconds = Math.ceil(Math.max(0, state.nextAttemptInMs) / 1000)
      if (seconds === 0) return 'Retrying now'
      return `Retrying in ${seconds} second${seconds === 1 ? '' : 's'}`
    }
    case 'blocked':
      return state.message
    case 'online':
      return ''
  }
}

function blockedState(
  state: ConnectionOverlayState,
): Extract<ConnectionOverlayState, { kind: 'blocked' }> | undefined {
  if (state.kind !== 'blocked') return undefined
  return state
}

const ConnectionOverlayView: Component<{
  state: Accessor<ConnectionOverlayState>
  visible: Accessor<boolean>
  onRetry: () => void
}> = (props) => (
  <Dialog open={props.visible()} onClose={() => undefined} onEscape={() => true}>
    <div
      class="ui-connection-overlay"
      data-state={props.state().kind}
      role="alertdialog"
      aria-live="assertive"
      aria-label="Connection status"
    >
      <img class="ui-connection-overlay__logo" src={APP_ICON} alt="" width="96" height="96" />
      <Stack gap="loose">
        <Show when={props.state().kind === 'connecting'}>
          <Spinner label="Connecting" />
        </Show>
        <p class="ui-connection-overlay__message">{sentence(props.state())}</p>
        <Show when={blockedState(props.state())}>
          {(state) => <p class="ui-connection-overlay__remedy">{state().remedy}</p>}
        </Show>
        <Show when={props.state().kind === 'waiting' || props.state().kind === 'blocked'}>
          <Button onClick={() => props.onRetry()} variant="primary">
            Retry
          </Button>
        </Show>
      </Stack>
    </div>
  </Dialog>
)

const ConnectionOverlay: Component<ConnectionOverlayProps> = (props) => {
  const minimumVisibleMs = Math.max(
    0,
    untrack(() => props.minimumVisibleMs ?? 1000),
  )
  const initialState = untrack(() => props.state())
  const initialVisible = minimumVisibleMs > 0 || initialState.kind !== 'online'
  let currentVisible = initialVisible
  let minimumElapsed = minimumVisibleMs === 0
  let minimumTimer: ReturnType<typeof setTimeout> | null = null
  const [visible, setVisible] = createSignal(initialVisible)

  const setVisibility = (nextVisible: boolean): void => {
    if (nextVisible === currentVisible) return
    const wasVisible = currentVisible
    currentVisible = nextVisible
    setVisible(nextVisible)
    if (wasVisible && !nextVisible) {
      const onHidden = untrack(() => props.onHidden)
      queueMicrotask(() => onHidden?.())
    }
  }

  createEffect(() => {
    if (props.state().kind === 'online') {
      if (minimumElapsed) setVisibility(false)
    } else {
      setVisibility(true)
    }
  })

  onMount(() => {
    if (minimumElapsed) return
    minimumTimer = setTimeout(() => {
      minimumTimer = null
      minimumElapsed = true
      if (props.state().kind === 'online') setVisibility(false)
    }, minimumVisibleMs)
  })

  onCleanup(() => {
    if (minimumTimer !== null) {
      clearTimeout(minimumTimer)
      minimumTimer = null
    }
  })

  return <ConnectionOverlayView state={props.state} visible={visible} onRetry={props.onRetry} />
}

/**
 * Mount the overlay into an application-owned host. The host owns lifecycle;
 * destroying the returned handle disposes the Solid root and unregisters the
 * native dialog from the shared overlay stack through Dialog's cleanup.
 */
export function mountConnectionOverlay(
  host: HTMLElement,
  props: ConnectionOverlayProps,
): { destroy(): void } {
  const dispose = render(() => <ConnectionOverlay {...props} />, host)

  return { destroy: dispose }
}
