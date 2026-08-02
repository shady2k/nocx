/**
 * Toast — a transient notification that does not occupy layout.
 *
 * Built because every surface was inventing one. The export page kept a
 * `.st-export-status` div under each action: it reserved a line of vertical space
 * whether or not there was anything to say, and when there was, the message pushed
 * the controls below it down. A notification is not part of the document flow — it
 * is an overlay that appears, is read, and goes away.
 *
 * ## Levels and dismissal
 *
 * Two dismissal modes, both required by real callers:
 *
 * - **auto** — the toast disappears after `duration` ms. This is the default and
 *   the right answer for an outcome the user asked for and can see the result of
 *   ("Exported — file downloaded", the file lands in Downloads).
 * - **sticky** — `duration: 0`. The toast stays until the user closes it. This is
 *   the right answer for anything that failed, because a failure the user did not
 *   happen to be looking at is a failure they never saw.
 *
 * The per-level defaults encode exactly that: `info` and `success` auto-dismiss,
 * `warning` gets longer because it usually says something the user must act on,
 * and `danger` is sticky. An explicit `duration` always wins over the default,
 * including `duration: 0` on a `success`.
 *
 * ## Why a module-level queue rather than a context
 *
 * The app has several Solid roots — the shell plus one per tab surface (see
 * App.tsx) — and a toast raised inside a per-tab root has to render in the shell's
 * host, above everything. A context would have to be threaded through every
 * mount() boundary between them; module state crosses those boundaries for free
 * and Solid's reactivity works across roots. The cost is that it is a singleton,
 * which is what a screen-level notification area actually is.
 */
import { Dynamic, Portal } from 'solid-js/web'
import { For, Show, createSignal, onCleanup, type Component } from 'solid-js'
import { topOverlayElement } from './overlay/stack'
import { IconButton } from './icon-button'
import { CloseIcon, InfoIcon, CheckCircleIcon, AlertTriangleIcon, AlertCircleIcon } from './icons'

export type ToastLevel = 'info' | 'success' | 'warning' | 'danger'

export interface ToastOptions {
  message: string
  level?: ToastLevel
  /**
   * Milliseconds before the toast dismisses itself. `0` keeps it up until the
   * user closes it. Omit to take the level's default.
   */
  duration?: number
}

export interface Toast {
  id: number
  message: string
  level: ToastLevel
  duration: number
}

/** Per-level auto-dismiss defaults, in ms. `0` is sticky — see the doc comment. */
const DEFAULT_DURATION: Record<ToastLevel, number> = {
  info: 4000,
  success: 4000,
  warning: 8000,
  danger: 0,
}

const [toasts, setToasts] = createSignal<Toast[]>([])

/** Read-only view of the queue, for the host and for tests. */
export { toasts }

let nextId = 1
const timers = new Map<number, ReturnType<typeof setTimeout>>()

/**
 * Raise a toast. Returns its id so a caller that owns a long-running operation can
 * dismiss its own message early.
 */
export function showToast(options: ToastOptions): number {
  const level = options.level ?? 'info'
  const duration = options.duration ?? DEFAULT_DURATION[level]
  const toast: Toast = { id: nextId++, message: options.message, level, duration }
  setToasts((current) => [...current, toast])
  if (duration > 0) {
    timers.set(
      toast.id,
      setTimeout(() => dismissToast(toast.id), duration),
    )
  }
  return toast.id
}

export function dismissToast(id: number): void {
  const timer = timers.get(id)
  if (timer !== undefined) {
    clearTimeout(timer)
    timers.delete(id)
  }
  setToasts((current) => current.filter((t) => t.id !== id))
}

/** Drops every toast and its pending timer. For tests and for teardown. */
export function clearToasts(): void {
  for (const timer of timers.values()) clearTimeout(timer)
  timers.clear()
  setToasts([])
}

/**
 * The level says itself, in a glyph as well as in a colour. Colour alone fails
 * anyone who cannot tell the accent blue from the success green, and it fails
 * everyone at a glance — a shape is read faster than a hue.
 */
const LEVEL_ICON: Record<ToastLevel, Component> = {
  info: InfoIcon,
  success: CheckCircleIcon,
  warning: AlertTriangleIcon,
  danger: AlertCircleIcon,
}

/**
 * ToastHost — the fixed overlay the toasts render into. Mounted once, in the app
 * shell. Mounting it twice would render every toast twice.
 *
 * `aria-live="polite"` on the region rather than `role="alert"` per toast: the
 * region is announced as its contents change, which is one announcement per toast
 * instead of one per re-render of the list.
 */
export function ToastHost() {
  onCleanup(clearToasts)
  // Rendered into the topmost open overlay when there is one, and into the body
  // otherwise. A modal `<dialog>` lives in the browser's top layer, which is
  // above every z-index in the normal layer — so while one was open, every
  // toast was painted UNDER it and its scrim: the message arrived, was
  // announced to a screen reader, and was invisible to everyone else. Being on
  // top of a top-layer element is not a number, it is a parent.
  //
  // `keyed` is load-bearing: Portal reads `mount` once, so the host has to be
  // rebuilt when the topmost overlay changes. The toasts themselves live in a
  // module signal and survive that.
  return (
    <Show when={topOverlayElement()} keyed fallback={renderHost()}>
      {(el) => <Portal mount={el}>{renderHost()}</Portal>}
    </Show>
  )
}

function renderHost() {
  return (
    <div class="ui-toast-host" role="status" aria-live="polite">
      <For each={toasts()}>
        {(toast) => (
          <div class="ui-toast" data-level={toast.level}>
            <span class="ui-toast__icon" aria-hidden="true">
              <Dynamic component={LEVEL_ICON[toast.level]} />
            </span>
            <span class="ui-toast__message">{toast.message}</span>
            <IconButton
              size="xs"
              ariaLabel="Dismiss notification"
              onClick={() => dismissToast(toast.id)}
            >
              <CloseIcon />
            </IconButton>
          </div>
        )}
      </For>
    </div>
  )
}
