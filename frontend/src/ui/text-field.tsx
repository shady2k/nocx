/**
 * TextField — text, number, or password input.
 *
 * Composes Field for label, description, error, and required marker.
 * Only the input and its event wiring live here.
 *
 * Justified by callers:
 * - settings.ts: input[type=text] and input[type=number] with change event, min/max
 * - connections.ts: inputField() / textField() / numberField() — label + input with input event
 */
import { Show, Switch, Match, type JSX } from 'solid-js'
import { Field } from './field'
import { mirrorControlledValue } from './controlled-value'

export interface TextFieldProps {
  id?: string
  label?: string
  description?: string
  error?: string
  /** When true, renders a <textarea> instead of an <input>. */
  multiline?: boolean
  value: string | number
  /** Fires on every keystroke (input event). */
  onInput?: (value: string) => void
  /**
   * Fires when focus leaves the input.
   *
   * Exists for validation: a message must not appear while the user is still
   * typing the first character of an empty field, so `createFormValidation` marks
   * a field answered on blur rather than on input. See `ui/validation.ts`.
   */
  onBlur?: (value: string) => void
  /**
   * Fires when the person is FINISHED with the value: when focus leaves, and
   * on Enter in a single-line field.
   *
   * For a field whose value is WRITTEN rather than merely validated. The
   * Agent policy page's scope field is checked by `ParseEffectPolicy`, which
   * rejects a non-absolute path, so writing per keystroke would be a refused
   * write and a toast on every character of `/workspace`. Blur and Enter are
   * one gesture — "done" — and naming it here keeps every caller from pairing
   * `onBlur` with a hand-rolled keydown of its own.
   *
   * Enter does NOT commit a `multiline` field: there it inserts a newline,
   * and only blur means done.
   */
  onCommit?: (value: string) => void
  type?: 'text' | 'number' | 'password'
  placeholder?: string
  min?: number
  max?: number
  disabled?: boolean
  required?: boolean
  autoFocus?: boolean
  /**
   * Select the field's current text when it takes focus, so the first
   * keystroke replaces it.
   *
   * For a field that opens PRE-FILLED with a suggestion — a suggested
   * workspace name, a suggested filename — where the value is an offer rather
   * than a starting point to edit. Without it the caret lands at one end and
   * the person has to clear the suggestion before typing, which makes a
   * helpful default into a chore. Meaningless without `autoFocus`, and
   * deliberately not implied by it: most autofocused fields are empty or hold
   * a value the user is amending.
   */
  selectOnFocus?: boolean
  trailing?: JSX.Element
  /**
   * A numeric field's unit ('days', 'MiB'), rendered as a suffix inside the
   * control so the number and its unit read — and copy — as one thing.
   * Declared by the setting's NumberSpec, never invented by a screen.
   */
  unit?: string
  /**
   * A permanent caption beneath the control — a number field's allowed
   * range. When `error` is present it REPLACES the caption in this same
   * single-line slot, so the layout does not jump and the two never
   * compete. Without a caption the error renders through Field as before.
   */
  caption?: string
  /**
   * Which edge the caption is flush with — 'start' (the default) or 'end'.
   *
   * It follows the column the field sits in, not the field: on a settings
   * page the controls are pinned to the right of the row, so captions set
   * 'start' leave a ragged right edge down the whole section, while 'end'
   * lines every one of them up with the fields above and below it.
   */
  captionAlign?: 'start' | 'end'
}

export function TextField(props: TextFieldProps) {
  const inputId = () => props.id ?? ''
  const descriptionId = () => (props.description ? `${inputId()}__desc` : undefined)
  const errorId = () => (props.error ? `${inputId()}__error` : undefined)
  const ariaDescribedBy = () => [descriptionId(), errorId()].filter(Boolean).join(' ') || undefined

  const onInput = (e: Event) => {
    const target = e.currentTarget as HTMLInputElement
    props.onInput?.(target.value)
  }

  const onBlur = (e: FocusEvent) => {
    const target = e.currentTarget as HTMLInputElement
    props.onBlur?.(target.value)
    props.onCommit?.(target.value)
  }

  /** Enter commits a single-line field. Wired to the input only — in a
   *  textarea Enter is a newline, and stealing it would make the control
   *  unable to do the one thing multiline exists for. */
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== 'Enter') return
    props.onCommit?.((e.currentTarget as HTMLInputElement).value)
  }

  const inputElement = () => (
    <input
      class="ui-text-field__input"
      id={inputId() || undefined}
      type={props.type ?? 'text'}
      placeholder={props.placeholder ?? ''}
      min={props.min !== undefined ? String(props.min) : undefined}
      max={props.max !== undefined ? String(props.max) : undefined}
      disabled={props.disabled === true}
      required={props.required === true}
      aria-invalid={props.error !== undefined ? true : undefined}
      aria-describedby={ariaDescribedBy()}
      autofocus={props.autoFocus === true}
      ref={(element) => {
        // Read BEFORE the microtask, not inside it. A prop read inside a
        // deferred callback is a reactive read outside any tracked scope —
        // `solid/reactivity` refuses it, and it is right to: the value it
        // would see is whatever the prop happens to hold a tick later. Both
        // of these are answered at mount and never change afterwards.
        const focusOnMount = props.autoFocus === true
        const selectOnMount = props.selectOnFocus === true
        if (focusOnMount)
          queueMicrotask(() => {
            element.focus()
            if (selectOnMount) element.select()
          })
        // mirrorControlledValue reads the accessor inside its own createEffect
        // (a tracked scope); the gate cannot see across that helper boundary.
        // eslint-disable-next-line solid/reactivity -- helper-boundary contract
        mirrorControlledValue(element, () => props.value)
      }}
      onInput={onInput}
      onBlur={onBlur}
      onKeyDown={onKeyDown}
    />
  )

  const textareaElement = () => (
    <textarea
      class="ui-text-field__input"
      id={inputId() || undefined}
      placeholder={props.placeholder ?? ''}
      disabled={props.disabled === true}
      required={props.required === true}
      aria-invalid={props.error !== undefined ? true : undefined}
      aria-describedby={ariaDescribedBy()}
      autofocus={props.autoFocus === true}
      rows={4}
      ref={(element) => {
        // Read before the microtask — see the input above for why.
        const focusOnMount = props.autoFocus === true
        const selectOnMount = props.selectOnFocus === true
        if (focusOnMount)
          queueMicrotask(() => {
            element.focus()
            if (selectOnMount) element.select()
          })
        // eslint-disable-next-line solid/reactivity -- same helper-boundary contract.
        mirrorControlledValue(element, () => props.value)
      }}
      onInput={onInput}
      onBlur={onBlur}
    />
  )

  const input = () => (
    <>
      <div
        class="ui-text-field__control"
        data-trailing={props.trailing !== undefined ? 'true' : 'false'}
        data-unit={props.unit !== undefined && props.multiline !== true ? 'true' : undefined}
      >
        <Switch>
          <Match when={props.multiline === true}>{textareaElement()}</Match>
          <Match when={true}>{inputElement()}</Match>
        </Switch>
        <Show when={!props.multiline && props.trailing}>
          <span class="ui-text-field__trailing">{props.trailing}</span>
        </Show>
        <Show when={!props.multiline && props.unit !== undefined}>
          <span class="ui-text-field__unit">{props.unit}</span>
        </Show>
      </div>
      {/* One caption slot beneath the control: the permanent caption, or the
          error in its place — never both, never a second line, so the field's
          height does not change when a value goes out of range. */}
      <Show when={props.caption !== undefined}>
        <p
          class="ui-text-field__caption"
          data-align={props.captionAlign ?? 'start'}
          data-tone={props.error !== undefined ? 'error' : 'caption'}
          role={props.error !== undefined ? 'alert' : undefined}
        >
          {props.error ?? props.caption}
        </p>
      </Show>
    </>
  )

  // Whether Field has anything to draw around the control. An error counts
  // ONLY when there is no caption slot to put it in: with a caption the error
  // is rendered in that slot, so letting it pull in a Field wrapper would
  // change the DOM — and the height — the moment a value went out of range.
  // Measured in a real browser on 2026-08-01: the wrapper appearing on error
  // grew a bare number field from 48.7px to 52.7px, and the row under it
  // shifted, which is exactly what the single caption slot exists to prevent.
  const hasFieldContent = () =>
    props.label !== undefined ||
    props.description !== undefined ||
    (props.error !== undefined && props.caption === undefined) ||
    props.required === true

  return (
    <div class="ui-text-field" data-multiline={props.multiline ? 'true' : undefined}>
      <Show when={hasFieldContent()} fallback={input()}>
        {/* When a caption slot exists it OWNS the error (the error replaces the
            caption in that slot); Field must not render a second one. */}
        <Field
          for={inputId()}
          label={props.label}
          description={props.description}
          error={props.caption !== undefined ? undefined : props.error}
          required={props.required}
        >
          {input()}
        </Field>
      </Show>
    </div>
  )
}
