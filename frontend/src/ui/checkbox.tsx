/**
 * Checkbox — boolean toggle, wired to the change event.
 *
 * Justified by callers:
 * - settings.ts: standalone input[type=checkbox] for toggle controls; filter checkbox in label
 * - connections.ts: checkboxField() helper — div.cm-field > label + input[type=checkbox]
 * - export-section.ts: show/hide password checkbox
 */
import { Show } from 'solid-js'

export interface CheckboxProps {
  class?: string
  checked: boolean
  onChange: (checked: boolean) => void
  label?: string
  ariaLabel?: string
  disabled?: boolean
}

export function Checkbox(props: CheckboxProps) {
  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    props.onChange(target.checked)
  }

  return (
    <label class={props.class ?? ''}>
      <input
        type="checkbox"
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
