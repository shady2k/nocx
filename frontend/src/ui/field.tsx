/**
 * Field — label + description + error + control wrapper for form rows.
 *
 * Each form control (TextField, Select, Checkbox, etc.) inside a Field
 * gets its id from the `for` prop so label, description and error are
 * linked via `for` and `aria-describedby`.
 *
 * Justified by callers:
 * - connections.ts: div.cm-field > label + input (credential selector, jump host)
 * - settings.ts: st-control-col > label + select/input (all setting rows)
 *
 * Every form row should use this instead of ad-hoc markup.
 */

import { Show } from 'solid-js'
import type { JSX } from 'solid-js'
export interface FieldProps {
  /**
   * How prominent the label is. `secondary` is the form default — a quiet label above
   * a control. `primary` makes the label the row's main text, which is what a settings
   * row needs because the label IS the setting and the control is the answer to it.
   *
   * A horizontal Field defaults to `primary` because a horizontal field *is* a settings
   * row: the label is the setting and the control is the answer to it — whereas a vertical
   * field is a form field whose label captions its control. The prop still exists so a
   * caller can override, but the default follows orientation.
   *
   * This exists because settings.css was overriding `.ui-field label`'s size, weight
   * and colour from outside. Both stylesheets declared the same properties and the
   * surface won by specificity — dual ownership, the defect this migration exists to
   * remove, surviving inside the component that was supposed to end it (nocx-etu2).
   */
  labelProminence?: 'secondary' | 'primary'
  /**
   * A small mark rendered before the label text — a dot, a badge, a lock.
   *
   * A slot rather than something the surface reaches in for. Settings drew its
   * "modified" dot with `.ui-settings-row--modified > .ui-field > .ui-field-label-col
   * > label::before`, a selector that tunnels through two kit identities to decorate
   * a component's internals. Composition says the same thing without the tunnel
   * (nocx-etu2).
   */
  labelMarker?: JSX.Element
  /** The id of the control inside this field — used for label's `for` and
   *  description/error aria-describedby wiring. */
  for: string
  /**
   * Optional, and deliberately so: a control can legitimately carry a
   * description or an error and no label of its own. When it is absent the
   * `<label>` element is not rendered at all — an empty `<label for>` bound to
   * a control announces that control as unlabelled, which is worse than having
   * no label element (nocx-uxs5.5).
   */
  label?: string
  description?: string
  error?: string
  required?: boolean
  children: JSX.Element
  /**
   * Layout orientation:
   * - `vertical` (default): label above, children below — standard form rows.
   * - `horizontal`: label on the left, children on the right — settings rows.
   */
  orientation?: 'vertical' | 'horizontal'
  /**
   * Small element rendered inline after the label text — a storage-class tag,
   * a "beta" marker, and the like.
   *
   * It exists because such a tag belongs to the *label*, not to the control:
   * passed through `children` it lands in the control column and reads as part
   * of the affordance ("Public" sitting immediately left of a select looks like
   * one of its options).
   */
  labelAdornment?: JSX.Element
}

export function Field(props: FieldProps) {
  const descriptionId = () => (props.description ? `${props.for}__desc` : undefined)
  const errorId = () => (props.error ? `${props.for}__error` : undefined)

  return (
    <Show
      when={props.orientation === 'horizontal'}
      fallback={
        <div class="ui-field" data-label={props.labelProminence ?? 'secondary'}>
          <Show when={props.label !== undefined || props.labelMarker !== undefined}>
            <label for={props.for}>
              {props.labelMarker}
              {props.label}
              <Show when={props.required === true}>
                <span aria-hidden="true"> *</span>
              </Show>
            </label>
          </Show>
          <Show when={props.description !== undefined}>
            <p id={descriptionId()} class="ui-field-desc">
              {props.description}
            </p>
          </Show>
          {props.children}
          <Show when={props.error !== undefined}>
            <p id={errorId()} class="ui-field-error" role="alert">
              {props.error}
            </p>
          </Show>
        </div>
      }
    >
      <div class="ui-field ui-field-horizontal" data-label={props.labelProminence ?? 'primary'}>
        <div class="ui-field-label-col">
          <Show when={props.label !== undefined || props.labelMarker !== undefined}>
            <label for={props.for}>
              {props.labelMarker}
              {props.label}
              <Show when={props.required === true}>
                <span aria-hidden="true"> *</span>
              </Show>
              <Show when={props.labelAdornment}>{props.labelAdornment}</Show>
            </label>
          </Show>
          <Show when={props.description !== undefined}>
            <p id={descriptionId()} class="ui-field-desc">
              {props.description}
            </p>
          </Show>
          <Show when={props.error !== undefined}>
            <p id={errorId()} class="ui-field-error" role="alert">
              {props.error}
            </p>
          </Show>
        </div>
        <div class="ui-field-control-col">{props.children}</div>
      </div>
    </Show>
  )
}
