/**
 * ResizeHandle — the kit's "drag to resize" separator (nocx-qmcu). The one
 * vocabulary for an edge the user drags between two panes: a focusable
 * `separator` (WAI-ARIA) that answers the mouse AND the keyboard, reports
 * its value through aria-valuenow, and never lets an interaction escape the
 * caller's bounds.
 *
 * Why a separator rather than a bare hit-area div: a drag handle that only
 * responds to a mouse excludes the keyboard. The separator role with
 * aria-valuenow/min/max is the standard operable-by-keyboard resize control;
 * ArrowRight/ArrowUp grow, ArrowLeft/ArrowDown shrink, Home/End jump to the
 * bounds.
 *
 * Why a new kit component and not an extension of an existing seam (AD-8):
 * the only other "edge beside the panes" in the shell is the terminal's
 * sibling gutter (`frontend/src/gutter.ts`), and it is not a resize owner at
 * all — a pointer-events:none glyph overlay for command records, with no
 * drag, no keyboard and no value. There is no existing vocabulary for "drag
 * to resize" to extend, so this is the first one, in the kit where the next
 * pane edge can reuse it.
 *
 * The component is controlled at rest and self-sufficient while dragging:
 * `value` is the settled value between interactions, and the live position
 * during a drag is tracked internally so aria-valuenow stays honest even
 * when the caller applies changes imperatively (the sidebar does). Callers
 * receive `onChange` for every live movement and `onCommit` once per settled
 * interaction — a drag end or one keyboard step — which is the moment to
 * persist. An interaction that ends where it started, or a step already at
 * a bound, fires nothing: an idle gesture must not churn the caller's
 * persistence or revision.
 */
import { createEffect, createSignal, on, untrack } from 'solid-js'

export interface ResizeHandleProps {
  /**
   * Which edge this is, in the separator role's own vocabulary — and with
   * it, the axis the drag reads.
   *
   * `vertical` (the default, and what the sidebar has always been) is a
   * vertical line between a left pane and a right one: the drag reads
   * clientX, and ArrowRight/ArrowLeft step it. `horizontal` is a horizontal
   * line between a top pane and a bottom one: the drag reads clientY, and
   * ArrowDown/ArrowUp step it — DOWN grows, because what the value measures
   * is the pane above.
   *
   * A variant rather than a second component, because everything else about
   * a resize edge — the capture, the clamping, the commit-once rule, the
   * idle-gesture suppression — is identical, and two of them would be two
   * owners of one behaviour.
   */
  orientation?: 'vertical' | 'horizontal'
  /** The settled value between interactions (px). */
  value: number
  /** Hard floor — a drag or a step can never produce less. */
  min: number
  /** Hard ceiling — a drag or a step can never produce more. */
  max: number
  /** Keyboard step for ArrowLeft/ArrowRight. */
  step?: number
  /** Accessible name — a separator has no visible label. REQUIRED. */
  ariaLabel: string
  /** Live change: every pointer movement while dragging, one per key step. */
  onChange: (value: number) => void
  /** A settled change: the drag end (pointerup/cancel) or one key step. */
  onCommit: (value: number) => void
  /** Drag lifecycle — true from pointerdown until pointerup/cancel. The
   *  caller uses it to stand down a value refetcher that would otherwise
   *  clobber the live position mid-drag. */
  onDragStateChange?: (dragging: boolean) => void
}

export function ResizeHandle(props: ResizeHandleProps) {
  const clamp = (v: number): number => {
    if (!Number.isFinite(v)) return props.value
    return Math.min(props.max, Math.max(props.min, v))
  }

  // The live value: props.value at rest (synced by the effect below), the
  // pointer position while dragging. Rendering reads it, so aria-valuenow
  // is always the truth even when the caller never re-renders us. Both
  // initializers are one-shot reads — untracked, because the reactive
  // contract is the `on` sync effect below, not these first paints.
  const [value, setValue] = createSignal(untrack(() => clamp(props.value)))
  const [dragging, setDragging] = createSignal(false)
  let live = untrack(() => clamp(props.value))
  let startValue = 0
  let startPos = 0
  let captureEl: HTMLElement | null = null
  let capturePointerId: number | null = null
  // Whether THIS interaction has produced any change — the drag-end commit
  // is gated on it, so a click without a drag persists nothing.
  let interactionChanged = false

  // Sync from a new controlled value while at rest (the caller re-renders
  // us with the settled value after a commit, or pushes one from outside —
  // a settings refetch). While dragging the live position is the truth.
  createEffect(
    on(
      () => props.value,
      (next) => {
        if (dragging()) return
        live = clamp(next)
        setValue(live)
      },
    ),
  )

  const report = (next: number, commit: boolean): void => {
    const clamped = clamp(next)
    if (clamped === live) return
    live = clamped
    interactionChanged = true
    setValue(clamped)
    props.onChange(clamped)
    if (commit) props.onCommit(clamped)
  }

  /** Where the pointer is ALONG THIS HANDLE'S AXIS. The one place the
   *  orientation reaches the drag maths; everything below is axis-blind. */
  const positionOf = (e: PointerEvent): number =>
    props.orientation === 'horizontal' ? e.clientY : e.clientX

  const endDrag = (position: number): void => {
    if (!dragging()) return
    const final = clamp(startValue + (position - startPos))
    // The release itself can move the pointer past the last reported point,
    // so recompute from the FINAL event's position; a no-op here means the
    // last move already reported it, and the commit below still fires once.
    if (final !== live) {
      live = final
      setValue(final)
      props.onChange(final)
    }
    if (interactionChanged) props.onCommit(live)
    setDragging(false)
    // Capture ends with the interaction — the pointerup that ends a drag
    // releases implicitly, but a pointercancel does not, and an explicit
    // release keeps both paths identical.
    if (captureEl && capturePointerId !== null) {
      captureEl.releasePointerCapture?.(capturePointerId)
    }
    captureEl = null
    capturePointerId = null
    props.onDragStateChange?.(false)
  }

  const onPointerDown = (e: PointerEvent): void => {
    startValue = live
    startPos = positionOf(e)
    interactionChanged = false
    captureEl = e.currentTarget as HTMLElement
    capturePointerId = e.pointerId
    setDragging(true)
    // Capture so the drag follows the pointer past the handle's own box and
    // the move/up listeners receive every event. jsdom lacks capture — the
    // optional call keeps the component testable there, and tests that fire
    // on the handle itself are exactly what capture makes equivalent.
    captureEl.setPointerCapture?.(e.pointerId)
    props.onDragStateChange?.(true)
  }

  const onPointerMove = (e: PointerEvent): void => {
    if (!dragging()) return
    report(startValue + (positionOf(e) - startPos), false)
  }

  const onPointerUp = (e: PointerEvent): void => endDrag(positionOf(e))

  const onKeyDown = (e: KeyboardEvent): void => {
    const step = props.step ?? 8
    const horizontal = props.orientation === 'horizontal'
    let next: number | null = null
    switch (e.key) {
      // The growing key is the one that points AWAY from the pane being
      // measured: right for a left pane, down for a top one. A horizontal
      // edge deliberately does not answer Left/Right at all — a key that
      // moves nothing is better than a key that moves the wrong edge.
      case 'ArrowRight':
        if (horizontal) return
        next = live + step
        break
      case 'ArrowLeft':
        if (horizontal) return
        next = live - step
        break
      case 'ArrowDown':
        next = horizontal ? live + step : live - step
        break
      case 'ArrowUp':
        next = horizontal ? live - step : live + step
        break
      case 'Home':
        next = props.min
        break
      case 'End':
        next = props.max
        break
      default:
        return
    }
    e.preventDefault()
    report(next, true)
  }

  return (
    <div
      role="separator"
      aria-label={props.ariaLabel}
      aria-orientation={props.orientation ?? 'vertical'}
      aria-valuenow={value()}
      aria-valuemin={props.min}
      aria-valuemax={props.max}
      tabIndex={0}
      class="ui-resize-handle"
      data-orientation={props.orientation ?? 'vertical'}
      data-dragging={dragging() ? 'true' : undefined}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={(e: PointerEvent) => endDrag(positionOf(e))}
      onKeyDown={onKeyDown}
    />
  )
}
