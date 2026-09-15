/**
 * Spinner — the kit's loading indicator (nocx-wzc4.9).
 *
 * Loading is a state that must be announced, and a surface that needs one
 * uses this component rather than hand-rolling a second vocabulary for it:
 * `role="status"` + `aria-label` carries the accessible name, the rotation is
 * the motion, and the animation is gated on `prefers-reduced-motion` like
 * every other kit animation (a reduced-motion user gets a static ring).
 *
 * Surfaces place it (a loading row, an empty state's icon slot) and never
 * repaint it.
 */

export type SpinnerSize = 'sm' | 'md'

export interface SpinnerProps {
  /** Accessible name for the loading state, announced via role="status". */
  label: string
  size?: SpinnerSize
}

export function Spinner(props: SpinnerProps) {
  return (
    <span
      class="ui-spinner"
      role="status"
      aria-label={props.label}
      data-size={props.size ?? 'md'}
    />
  )
}
