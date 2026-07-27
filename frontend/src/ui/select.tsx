/**
 * Select — dropdown option picker, wired to the change event.
 *
 * Justified by callers:
 * - settings.ts: <select> with options, onChange fires on user selection
 * - connections.ts: credential selector and jump-host selector with inline styles
 *
 * Per ADR-0014: native <select> with appearance: none on the closed control.
 * The open popup is platform-owned and unstylable — accepted tradeoff.
 */

import { For, Show } from 'solid-js'

export interface SelectOption {
  value: string
  label: string
}

export interface SelectProps {
  class?: string
  value: string
  onChange: (value: string) => void
  options: SelectOption[]
  /** Optional first option that reads as "— None —" or similar. */
  placeholder?: string
  placeholderValue?: string
  disabled?: boolean
}

export function Select(props: SelectProps) {
  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLSelectElement
    props.onChange(target.value)
  }

  return (
    <select
      class={props.class ?? ''}
      value={props.value}
      disabled={props.disabled === true}
      onChange={onChange}
    >
      <Show when={props.placeholder !== undefined}>
        <option value={props.placeholderValue ?? ''}>{props.placeholder}</option>
      </Show>
      <For each={props.options}>{(opt) => <option value={opt.value}>{opt.label}</option>}</For>
    </select>
  )
}
