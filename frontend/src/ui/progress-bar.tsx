/**
 * ProgressBar — the kit's "how far along is this" bar. DETERMINATE, and
 * only determinate: a fraction that is known, drawn as a fraction.
 *
 * There is no indeterminate mode and that is a decision, not an omission.
 * A 20-minute upload with a permanently animating bar puts motion in
 * somebody's peripheral vision for 20 minutes; the kit already has Spinner
 * for "something is happening and nobody can say how far", and a caller
 * that cannot name a fraction wants that instead.
 *
 * Two callers today and they are the same concept at two scales — one
 * operation's own progress in a row, and the aggregate under the activity
 * bar's operations indicator. Which is exactly why it is one component: two
 * bars drawn by two surfaces would disagree first about what "done" looks
 * like, and a person reading both at once would see two answers.
 *
 * Surfaces place it (its width comes from where it sits) and never repaint
 * it.
 */

export interface ProgressBarProps {
  /** How far along, 0..1. Values outside the range are clamped rather than
   *  refused: a byte count that briefly overshoots its declared total is a
   *  measurement, not a reason to stop drawing. NaN reads as 0. */
  value: number
  /** Required — a bar with no accessible name announces a number about
   *  nothing. */
  ariaLabel: string
}

/** 0..1 clamped, NaN folded to 0. Exported for nobody: the component is the
 *  only owner of what its own `value` means. */
function fraction(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(1, Math.max(0, value))
}

export function ProgressBar(props: ProgressBarProps) {
  // Read inside the JSX, never hoisted: `props.value` is reactive and a
  // captured const would freeze the bar at its first render.
  return (
    <div
      class="ui-progress-bar"
      role="progressbar"
      aria-label={props.ariaLabel}
      aria-valuemin={0}
      aria-valuemax={100}
      // Rounded for the announcement only. Screen readers read this aloud,
      // and "41.837 percent" is noise; the fill below keeps the precision.
      aria-valuenow={Math.round(fraction(props.value) * 100)}
    >
      <div class="ui-progress-bar__fill" style={{ width: `${fraction(props.value) * 100}%` }} />
    </div>
  )
}
