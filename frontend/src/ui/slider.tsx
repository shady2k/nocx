/**
 * Slider — a magnitude chosen by eye, wired to the input event.
 *
 * The kit had no continuous control before this. A number the person TYPES is
 * TextField's job and stays there; a slider is for the case where the value
 * has no name — you drag until the thing on screen looks right, and which
 * number that turned out to be is a detail you read afterwards. Hence the
 * value is always shown beside the track: a slider that hides its number
 * cannot be reported, compared or set back.
 *
 * Justified by callers:
 * - settings.tsx: the `slider` variant of a number declaration (pets.size).
 */
import { Show } from 'solid-js'

export interface SliderProps {
  value: number
  min: number
  max: number
  /** Granularity of a drag. 1 unless the caller has a reason. */
  step?: number
  /** Fires continuously while dragging — the point of the control is that
   *  the value is visible during the drag, not after it. */
  onInput: (value: number) => void
  /**
   * Fires when the person is DONE — pointer released, or the keyboard has
   * settled on a value.
   *
   * Separate from onInput because a drag passes through every number between
   * where it started and where it stopped, and for a caller that PERSISTS the
   * value those are not choices — they are the journey. Without this the pet
   * slider wrote sixty settings a second, each one a revision every client
   * refetched.
   */
  onCommit?: (value: number) => void
  /** Suffix after the readout: "px", "days". Never inside the track. */
  unit?: string
  ariaLabel?: string
  disabled?: boolean
}

export function Slider(props: SliderProps) {
  const clamp = (n: number) => Math.min(props.max, Math.max(props.min, n))
  return (
    <div class="ui-slider">
      <input
        class="ui-slider__control"
        type="range"
        min={props.min}
        max={props.max}
        step={props.step ?? 1}
        value={String(clamp(props.value))}
        aria-label={props.ariaLabel ?? undefined}
        disabled={props.disabled === true}
        onInput={(e) => {
          const n = Number(e.currentTarget.value)
          if (!Number.isNaN(n)) props.onInput(clamp(n))
        }}
        onChange={(e) => {
          const n = Number(e.currentTarget.value)
          if (!Number.isNaN(n)) props.onCommit?.(clamp(n))
        }}
      />
      <span class="ui-slider__readout">
        {clamp(props.value)}
        <Show when={props.unit !== undefined}>
          <span class="ui-slider__unit">{props.unit}</span>
        </Show>
      </span>
    </div>
  )
}
