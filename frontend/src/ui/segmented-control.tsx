/**
 * SegmentedControl — one choice from a small, fixed set, laid out as one row.
 *
 * The same job as a group of Radios and the same ARIA (`radiogroup` of
 * `radio`s, one tab stop, arrows to move within it). What differs is the cost
 * in space: five radios are five rows, and in a dialog that is most of a
 * section. It is a separate component rather than a Radio variant because a
 * radio owns its own row and its own label — a segment owns neither.
 *
 * Use it when the options are few, short, and mutually exclusive. When there
 * are many, or the labels are sentences, a column of Radios is still right —
 * segments that wrap onto a second line stop reading as one control.
 *
 * A single segment can be disabled while the rest of the group is live — see
 * `SegmentedOption.disabled`, which is what a promised-but-unbuilt mode
 * should look like.
 */

import { For, Show, type Component } from 'solid-js'
import { Dynamic } from 'solid-js/web'

export interface SegmentedOption {
  value: string
  label: string
  /** Longer text for the tooltip, when the segment's label is abbreviated. */
  title?: string
  /**
   * Drawn above the label. Decoration, not information — the label is always
   * present, so a glyph nobody recognises costs nothing but a moment.
   */
  icon?: Component
  /**
   * This one segment cannot be chosen, while the rest of the group can.
   *
   * Distinct from the group's own `disabled`, which says the whole choice
   * is unavailable. This says the choice is real and one of its answers is
   * not yet — a mode the product means to have and does not have here. It
   * is the honest rendering of a promised option: hiding it would leave the
   * control looking like the whole of what was ever intended, and offering
   * it live would be a control that does nothing.
   *
   * **A disabled segment must carry a `title` saying why**, or it teaches
   * the person the panel is broken. That is a rule this component cannot
   * enforce in the type system without making `title` required for
   * everybody, so it is stated here and asserted in the tests of the
   * surfaces that use it.
   *
   * Arrow keys skip it: a roving selection that can land on a value the
   * control will not accept is a keyboard user hitting the same wall
   * repeatedly with no way to tell why.
   */
  disabled?: boolean
}

export interface SegmentedControlProps {
  options: SegmentedOption[]
  value: string
  onChange: (value: string) => void
  ariaLabel?: string
  disabled?: boolean
}

export function SegmentedControl(props: SegmentedControlProps) {
  const move = (delta: number) => {
    const opts = props.options
    const selectable = opts.filter((o) => o.disabled !== true)
    // Every segment disabled is a group with nothing to move to. It is not
    // the same as the group being `disabled` — the caller may have disabled
    // them one at a time — and the arrow must do nothing rather than divide
    // by zero below.
    if (selectable.length === 0) return
    const i = selectable.findIndex((o) => o.value === props.value)
    // No current selection yet — or the current value is a segment that has
    // since been disabled: an arrow lands on the first selectable option
    // rather than doing nothing, which is what a keyboard user expects from
    // a group whose selection is not among its live answers.
    if (i < 0) {
      props.onChange(selectable[0].value)
      return
    }
    props.onChange(selectable[(i + delta + selectable.length) % selectable.length].value)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if (props.disabled === true) return
    switch (e.key) {
      case 'ArrowLeft':
      case 'ArrowUp':
        e.preventDefault()
        move(-1)
        break
      case 'ArrowRight':
      case 'ArrowDown':
        e.preventDefault()
        move(1)
        break
    }
  }

  return (
    <div
      class="ui-segmented-control"
      role="radiogroup"
      aria-label={props.ariaLabel ?? undefined}
      onKeyDown={onKeyDown}
    >
      <For each={props.options}>
        {(opt) => (
          <button
            type="button"
            class="ui-segmented-control__option"
            role="radio"
            aria-checked={props.value === opt.value}
            title={opt.title ?? opt.label}
            disabled={props.disabled === true || opt.disabled === true}
            // Roving tabindex: the group is one tab stop. With nothing chosen
            // the first segment carries it, or the group is unreachable by Tab.
            tabIndex={
              props.value === opt.value ||
              (props.options.every((o) => o.value !== props.value) &&
                props.options.find((o) => o.disabled !== true) === opt)
                ? 0
                : -1
            }
            onClick={() => {
              if (opt.disabled === true) return
              props.onChange(opt.value)
            }}
          >
            <Show when={opt.icon}>
              {(icon) => (
                <span class="ui-segmented-control__icon" aria-hidden="true">
                  <Dynamic component={icon()} />
                </span>
              )}
            </Show>
            <span class="ui-segmented-control__label">{opt.label}</span>
          </button>
        )}
      </For>
    </div>
  )
}
