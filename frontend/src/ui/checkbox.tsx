/**
 * Checkbox — boolean toggle, wired to the change event.
 *
 * Justified by callers:
 * - settings.ts: standalone input[type=checkbox] for toggle controls; filter checkbox in label
 * - connections.ts: checkboxField() helper — div.cm-field > label + input[type=checkbox]
 */
import { Show } from 'solid-js'

export interface CheckboxProps {
  checked: boolean
  onChange: (checked: boolean) => void
  label?: string
  ariaLabel?: string
  disabled?: boolean
  /**
   * Which affordance to draw. Both are the same `input[type=checkbox]` — this
   * only changes the shape, never the semantics or the events.
   *
   * - `checkbox` (default): selection and filtering, where the user is marking
   *   something and the effect is scoped to the view.
   * - `switch`: a setting that takes effect the moment it is flipped. A tick
   *   box reads as "chosen, pending save"; these have nothing to save.
   */
  variant?: 'checkbox' | 'switch'
}

export function Checkbox(props: CheckboxProps) {
  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    props.onChange(target.checked)
  }

  return (
    <label class="ui-checkbox" data-variant={props.variant ?? 'checkbox'}>
      <input
        class="ui-checkbox__control"
        type="checkbox"
        role={props.variant === 'switch' ? 'switch' : undefined}
        checked={props.checked}
        aria-label={props.ariaLabel ?? undefined}
        disabled={props.disabled === true}
        onChange={onChange}
      />
      <Show when={props.label !== undefined}>
        <span>{props.label}</span>
      </Show>
    </label>
  )
}
