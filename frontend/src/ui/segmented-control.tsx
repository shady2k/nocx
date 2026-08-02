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
    const i = opts.findIndex((o) => o.value === props.value)
    // No current selection yet: an arrow lands on the first option rather than
    // doing nothing, which is what a keyboard user expects from an empty group.
    if (i < 0) {
      if (opts.length > 0) props.onChange(opts[0].value)
      return
    }
    props.onChange(opts[(i + delta + opts.length) % opts.length].value)
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
            disabled={props.disabled === true}
            // Roving tabindex: the group is one tab stop. With nothing chosen
            // the first segment carries it, or the group is unreachable by Tab.
            tabIndex={
              props.value === opt.value ||
              (props.options.every((o) => o.value !== props.value) && props.options[0] === opt)
                ? 0
                : -1
            }
            onClick={() => props.onChange(opt.value)}
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
