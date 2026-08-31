/**
 * ConnectionOverlay — the startup and transport-recovery surface.
 *
 * This is a kit component because a connection outage is one application-wide
 * condition, not a sentence each surface should invent. It is the application's
 * LOADING SCREEN: a full-bleed opaque ground with the app mark on it, one
 * sentence saying what is true, one dimmed line saying what happens next, and
 * the single action that can act in the state it is in.
 *
 * It is a sibling of `Dialog` rather than a mode of it. Both need the browser
 * top layer, and `createModalHost` is where that lives — but Dialog's own
 * vocabulary is a card (a title, a footer, a close control, a measured height
 * animation, a caret policy for the field a user is about to type in), and not
 * one of those applies to a ground with an icon on it. A `fullscreen` variant
 * of Dialog would have been a mode in which none of Dialog's props mean
 * anything.
 */
import { Show, createEffect, createSignal, onCleanup, untrack, type Accessor } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './button'
import { createModalHost } from './overlay/modal-host'

export type ConnectionOverlayState =
  | { kind: 'connecting' }
  | { kind: 'waiting'; nextAttemptInMs: number }
  | { kind: 'blocked'; message: string; remedy: string }
  | { kind: 'online' }

/**
 * How long the ground takes to leave.
 *
 * The overlay arrives instantly — an outage supplies enough motion of its own,
 * and a surface that fades IN delays telling somebody their terminal is not
 * connected. It leaves on a transition, because cutting a full-screen opaque
 * surface away in a single frame is a jolt. Exported so a test asserts the
 * same number the stylesheet transitions over.
 */
export const CONNECTION_OVERLAY_EXIT_MS = 180

export interface ConnectionOverlayProps {
  state: Accessor<ConnectionOverlayState>
  /** Invoked by Retry. The component decides nothing about when to retry. */
  onRetry: () => void
  /** Invoked after the overlay has finished leaving. */
  onHidden?: () => void
  /** Minimum visible time from first mount, in ms. */
  minimumVisibleMs?: number
}

/** A state the overlay can actually draw. `online` is the absence of one. */
type ShownState = Exclude<ConnectionOverlayState, { kind: 'online' }>

/** What is true right now. */
function headline(state: ShownState): string {
  switch (state.kind) {
    case 'connecting':
      return 'Connecting to nocx…'
    case 'waiting':
      // Not the countdown. The countdown was the headline once, set at the
      // largest size on the screen, so the biggest thing a person read was a
      // ticking number and the condition itself was stated nowhere.
      return 'Cannot reach the nocx backend'
    case 'blocked':
      return state.message
  }
}

/** What happens next — dimmed, under the headline. Empty while connecting. */
function detail(state: ShownState): string {
  switch (state.kind) {
    case 'connecting':
      return ''
    case 'waiting': {
      const seconds = Math.ceil(Math.max(0, state.nextAttemptInMs) / 1000)
      if (seconds === 0) return 'Retrying now'
      return `Next attempt in ${seconds} second${seconds === 1 ? '' : 's'}`
    }
    case 'blocked':
      return state.remedy
  }
}

/** Retry appears where it can act — never during an attempt already in flight. */
function canRetry(state: ShownState): boolean {
  return state.kind === 'waiting' || state.kind === 'blocked'
}

function ConnectionOverlayView(props: {
  state: Accessor<ShownState>
  visible: Accessor<boolean>
  exiting: Accessor<boolean>
  onRetry: () => void
}) {
  const host = createModalHost({
    open: () => props.visible(),
    dismissible: () => false,
    onClose: () => undefined,
  })

  return (
    <dialog
      ref={host.ref}
      class="ui-connection-overlay"
      data-state={props.state().kind}
      data-exiting={props.exiting() ? 'true' : undefined}
      onCancel={host.onCancel}
      onMouseDown={host.onMouseDown}
      role="alertdialog"
      aria-live="assertive"
      aria-label="Connection status"
    >
      <div class="ui-connection-overlay__content">
        <div class="ui-connection-overlay__mark">
          <img
            class="ui-connection-overlay__logo"
            src="/appicon-96.png"
            alt=""
            width="96"
            height="96"
          />
        </div>
        <p class="ui-connection-overlay__headline">{headline(props.state())}</p>
        {/* Always rendered, even empty: the line holds its own height, so the
            mark above it does not jump when `connecting` becomes a countdown. */}
        <p class="ui-connection-overlay__detail">{detail(props.state())}</p>
        <Show when={canRetry(props.state())}>
          <div class="ui-connection-overlay__action">
            <Button onClick={() => props.onRetry()} variant="primary">
              Retry
            </Button>
          </div>
        </Show>
      </div>
    </dialog>
  )
}

function ConnectionOverlay(props: ConnectionOverlayProps) {
  const minimumVisibleMs = Math.max(
    0,
    untrack(() => props.minimumVisibleMs ?? 1000),
  )
  const initialState = untrack(() => props.state())
  const initialVisible = minimumVisibleMs > 0 || initialState.kind !== 'online'
  let currentVisible = initialVisible
  let minimumElapsed = minimumVisibleMs === 0
  let minimumTimer: ReturnType<typeof setTimeout> | null = null
  let exitTimer: ReturnType<typeof setTimeout> | null = null
  const [visible, setVisible] = createSignal(initialVisible)
  const [exiting, setExiting] = createSignal(false)

  /**
   * The last state the overlay could draw.
   *
   * `online` is not a state the overlay has a face for — no sentence, no
   * activity — so while it is still up, it goes on showing the one it was in.
   * Without this, a socket that came up inside the startup minimum left a box
   * holding one motionless icon and no text for the rest of the second.
   */
  const [shown, setShown] = createSignal<ShownState>(
    initialState.kind === 'online' ? { kind: 'connecting' } : initialState,
  )

  const cancelExit = (): void => {
    if (exitTimer !== null) {
      clearTimeout(exitTimer)
      exitTimer = null
    }
    setExiting(false)
  }

  /** True under `prefers-reduced-motion: reduce` — leave without the fade. */
  const reducedMotion = (): boolean =>
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches

  const finishHiding = (): void => {
    exitTimer = null
    setExiting(false)
    currentVisible = false
    setVisible(false)
    const onHidden = untrack(() => props.onHidden)
    queueMicrotask(() => onHidden?.())
  }

  const show = (): void => {
    cancelExit()
    if (currentVisible) return
    currentVisible = true
    setVisible(true)
  }

  const hide = (): void => {
    if (!currentVisible || exitTimer !== null) return
    if (reducedMotion()) {
      finishHiding()
      return
    }
    setExiting(true)
    exitTimer = setTimeout(finishHiding, CONNECTION_OVERLAY_EXIT_MS)
  }

  createEffect(() => {
    const state = props.state()
    if (state.kind !== 'online') {
      setShown(state)
      show()
    } else if (minimumElapsed) {
      hide()
    }
  })

  if (!minimumElapsed) {
    minimumTimer = setTimeout(() => {
      minimumTimer = null
      minimumElapsed = true
      if (untrack(() => props.state()).kind === 'online') hide()
    }, minimumVisibleMs)
  }

  onCleanup(() => {
    if (minimumTimer !== null) clearTimeout(minimumTimer)
    if (exitTimer !== null) clearTimeout(exitTimer)
    minimumTimer = null
    exitTimer = null
  })

  return (
    <ConnectionOverlayView
      state={shown}
      visible={visible}
      exiting={exiting}
      onRetry={props.onRetry}
    />
  )
}

/**
 * Mount the overlay into an application-owned host. The host owns lifecycle;
 * destroying the returned handle disposes the Solid root and unregisters the
 * native dialog from the shared overlay stack through the modal host's cleanup.
 */
export function mountConnectionOverlay(
  host: HTMLElement,
  props: ConnectionOverlayProps,
): { destroy(): void } {
  const dispose = render(() => <ConnectionOverlay {...props} />, host)

  return { destroy: dispose }
}
