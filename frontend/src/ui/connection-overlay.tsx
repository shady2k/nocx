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
import { createEffect, createSignal, onCleanup, untrack, type Accessor } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './button'
import { createModalHost } from './overlay/modal-host'

export type ConnectionOverlayState =
  | { kind: 'connecting' }
  /** `message` overrides the default sentence when the caller has a better one. */
  | { kind: 'waiting'; nextAttemptInMs: number; message?: string }
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

/**
 * How often the countdown is recomputed while an attempt is scheduled.
 *
 * Four times a second rather than once: the displayed number is a ceiling of
 * the remaining time, so a one-second tick that starts out of phase with the
 * deadline shows each value for anywhere between an instant and two seconds.
 * The published wait is not a whole number of seconds — the dispatcher adds up
 * to 50% jitter — so it never happens to be in phase.
 */
const COUNTDOWN_TICK_MS = 250

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

/**
 * What is true right now.
 *
 * `recovering` is what stops the headline strobing. Once an attempt has
 * failed, every following attempt in the same outage is a `connecting` state
 * lasting a few hundred milliseconds, and letting it retitle the screen made
 * the sentence flip between "Connecting to nocx…" and "Cannot reach the nocx
 * backend" for as long as the backend stayed down. The condition is what has
 * not changed; that an attempt is in flight is what the ring is for.
 */
function headline(state: ShownState, recovering: boolean): string {
  switch (state.kind) {
    case 'connecting':
      return recovering ? 'Cannot reach the nocx backend' : 'Connecting to nocx…'
    case 'waiting':
      // Not the countdown. The countdown was the headline once, set at the
      // largest size on the screen, so the biggest thing a person read was a
      // ticking number and the condition itself was stated nowhere.
      return state.message ?? 'Cannot reach the nocx backend'
    case 'blocked':
      return state.message
  }
}

/** What happens next — dimmed, under the headline. */
function detail(state: ShownState, recovering: boolean, remainingMs: number): string {
  switch (state.kind) {
    case 'connecting':
      return recovering ? 'Reconnecting…' : ''
    case 'waiting': {
      const seconds = Math.ceil(Math.max(0, remainingMs) / 1000)
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
  recovering: Accessor<boolean>
  remainingMs: Accessor<number>
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
        <p class="ui-connection-overlay__headline">{headline(props.state(), props.recovering())}</p>
        {/* Always rendered, even empty: the line holds its own height, so the
            mark above it does not jump when `connecting` becomes a countdown. */}
        <p class="ui-connection-overlay__detail">
          {detail(props.state(), props.recovering(), props.remainingMs())}
        </p>
        {/* The action row is rendered in EVERY state and the button inside it
            is hidden rather than removed when it cannot act. A row that comes
            and goes changes the group's height, and the group is centred, so
            the mark moved 28px up and down on every attempt — measured in a
            browser, and the reason the owner reported the icon twitching.
            `visibility: hidden` reserves exactly the right height with no
            number to keep in step, and takes the control out of the tab order
            and the accessibility tree, so nothing offers a Retry that would do
            nothing. */}
        <div
          class="ui-connection-overlay__action"
          data-available={canRetry(props.state()) ? 'true' : 'false'}
        >
          <Button onClick={() => props.onRetry()} variant="primary">
            Retry
          </Button>
        </div>
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
  /** True once an attempt in this episode has failed — see `headline`. */
  const [recovering, setRecovering] = createSignal(false)
  const [remainingMs, setRemainingMs] = createSignal(0)
  let deadline: number | null = null
  let countdownTimer: ReturnType<typeof setInterval> | null = null

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

  /**
   * Count the wait down instead of restating it.
   *
   * The dispatcher publishes a wait ONCE, when it schedules the attempt, and
   * the number it publishes grows with the backoff — so a screen that renders
   * the published value shows 1, then 2, then 3, then 6 seconds, counting UP
   * in jumps and never reaching the attempt it is describing. The deadline is
   * what the caller actually stated; the remaining time is derived from the
   * clock.
   */
  const stopCountdown = (): void => {
    if (countdownTimer !== null) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
    deadline = null
  }

  const startCountdown = (nextAttemptInMs: number): void => {
    deadline = Date.now() + Math.max(0, nextAttemptInMs)
    setRemainingMs(Math.max(0, nextAttemptInMs))
    if (countdownTimer !== null) return
    countdownTimer = setInterval(() => {
      if (deadline === null) return
      setRemainingMs(Math.max(0, deadline - Date.now()))
    }, COUNTDOWN_TICK_MS)
  }

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
    // The episode is over. The next time the overlay comes up it is a fresh
    // connect again, and says so.
    setRecovering(false)
    stopCountdown()
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
    if (state.kind === 'online') {
      if (minimumElapsed) hide()
      return
    }
    setShown(state)
    if (state.kind === 'waiting') {
      setRecovering(true)
      startCountdown(state.nextAttemptInMs)
    } else {
      if (state.kind === 'blocked') setRecovering(true)
      stopCountdown()
    }
    show()
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
    stopCountdown()
  })

  return (
    <ConnectionOverlayView
      state={shown}
      visible={visible}
      exiting={exiting}
      recovering={recovering}
      remainingMs={remainingMs}
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
