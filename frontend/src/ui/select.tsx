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

import { For, Show, createEffect } from 'solid-js'

export interface SelectOption {
  value: string
  label: string
}

export interface SelectProps {
  value: string
  onChange: (value: string) => void
  options: SelectOption[]
  /** Optional first option that reads as "— None —" or similar. */
  placeholder?: string
  placeholderValue?: string
  disabled?: boolean
  ariaLabel?: string
}

export function Select(props: SelectProps) {
  let ref: HTMLSelectElement | undefined

  createEffect(() => {
    // Re-apply the selection whenever the option set changes: options often
    // arrive asynchronously (vault rows, loaded lists), and a controlled
    // select whose options land after its value was set would otherwise sit
    // on the placeholder even though the value matches an option — the bound
    // secret would read as "None".
    void props.value
    void props.options
    if (ref) ref.value = props.value
  })

  const onChange = (e: Event) => {
    const target = e.currentTarget as HTMLSelectElement
    props.onChange(target.value)
  }

  return (
    <select
      ref={ref}
      class="ui-select"
      value={props.value}
      disabled={props.disabled === true}
      aria-label={props.ariaLabel ?? undefined}
      onChange={onChange}
    >
      <Show when={props.placeholder !== undefined}>
        <option value={props.placeholderValue ?? ''}>{props.placeholder}</option>
      </Show>
      <For each={props.options}>{(opt) => <option value={opt.value}>{opt.label}</option>}</For>
    </select>
  )
}
