/**
 * Radio — a native radio input wrapped in a label, matching the pattern of Checkbox.
 *
 * Designed for radio groups used in connections.tsx (auth mode selection).
 * Consumers wrap this in a container with `role="radiogroup"` and manage
 * selection state externally.
 */
import { Show } from 'solid-js'

export interface RadioProps {
  class?: string
  value: string
  checked: boolean
  onChange: (value: string) => void
  label?: string
  ariaLabel?: string
  disabled?: boolean
  name?: string
}

export function Radio(props: RadioProps) {
  return (
    <label class={props.class ?? ''}>
      <input
        type="radio"
        value={props.value}
        checked={props.checked}
        name={props.name}
        aria-label={props.ariaLabel ?? undefined}
        disabled={props.disabled === true}
        onChange={() => props.onChange(props.value)}
      />
      <Show when={props.label !== undefined}>
        <span>{props.label}</span>
      </Show>
    </label>
  )
}
