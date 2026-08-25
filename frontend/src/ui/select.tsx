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

import { Index, Show, createEffect } from 'solid-js'

export interface SelectOption {
  value: string
  label: string
}

export interface SelectProps {
  /**
   * The control's accessible name when nothing visible labels it.
   *
   * Same rule as TextField's: for a control whose surroundings already say
   * what it is — the environment beside the Send it governs, a method beside
   * the URL it applies to — where a visible label would be a second word for
   * a thing the person is looking straight at. A select with neither a
   * `<label for>` nor this is announced as unnamed.
   */
  ariaLabel?: string
  /**
   * The id the control answers to — what a `Field`'s `for` points at, and
   * what a surface addresses it by.
   *
   * TextField has carried one since it was written and this did not, so a
   * `Field for="…"` wrapped round a Select bound its `<label>` to nothing:
   * the row read as labelled and the control was announced as unnamed. It is
   * optional for the same reason TextField's is — a select whose
   * surroundings already say what it is needs no id at all.
   */
  id?: string
  value: string
  onChange: (value: string) => void
  options: SelectOption[]
  /** Optional first option that reads as "— None —" or similar. */
  placeholder?: string
  placeholderValue?: string
  disabled?: boolean
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
      id={props.id}
      aria-label={props.ariaLabel}
      value={props.value}
      disabled={props.disabled === true}
      onChange={onChange}
    >
      <Show when={props.placeholder !== undefined}>
        <option value={props.placeholderValue ?? ''}>{props.placeholder}</option>
      </Show>
      {/* KEYED BY POSITION. Callers build `options` inline — `.map()` over a
          list that is itself derived — so the array and every option object
          in it are new on each render. Keyed by reference, every `<option>`
          was destroyed and rebuilt each time, which a native select in a
          webview pays for in layout and in the popup it is drawing. Same
          contract as row-list and tabs: the element survives, the value and
          the label update in place. */}
      <Index each={props.options}>
        {(opt) => <option value={opt().value}>{opt().label}</option>}
      </Index>
    </select>
  )
}
