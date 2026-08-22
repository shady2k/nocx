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
import { For, Show, Switch, Match, type JSX } from 'solid-js'
import { Field } from './field'
import { mirrorControlledValue } from './controlled-value'

/** One marked span of a field's text. `to` is exclusive. */
export interface TextFieldMark {
  from: number
  to: number
  /**
   * What the mark says about the span — `reference` (the default) is "this
   * is a reference", `secret` is "and what it stands for is not readable
   * here", `unknown` is "and nothing answers it".
   *
   * Two tones and not a boolean because the third state is real and must not
   * be drawn as either: a surface that does not yet KNOW whether a name is
   * answered passes `reference`, and a warning colour appears only once
   * somebody can say it is warranted. Crying wolf while a listing is in
   * flight is how a person learns to ignore the colour.
   */
  tone?: 'reference' | 'secret' | 'unknown'
}

export interface TextFieldProps {
  id?: string
  label?: string
  /**
   * The control's accessible name when it has NO visible label.
   *
   * For a field whose surroundings already say what it is — a filter at the
   * head of the list it filters, a search box in a toolbar — where a visible
   * label would be a second word for a thing the person is looking straight
   * at. It is not an alternative to `label`: a field that has one does not
   * need this, and a field that has neither is a control assistive tech
   * announces as unnamed, which is the defect Field's own `label` comment
   * describes (nocx-uxs5.5).
   */
  ariaLabel?: string
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
   * Spans of the value the reader must see as something other than plain
   * text — a `{{variable}}` inside a URL.
   *
   * The caller says WHICH spans and the kit says what they look like, and
   * that split is the whole point: which characters are a reference is the
   * API domain's grammar (it must agree, character for character, with the
   * backend that substitutes them), while how a marked span is painted is
   * one decision for the whole product. A surface that highlighted its own
   * field would be repainting a kit control, which is the thing the kit
   * exists to stop.
   *
   * Offsets are UTF-16 code units into `value`, half-open [from, to), in
   * order and non-overlapping. A mark outside the value is ignored rather
   * than clamped: a stale mark is a caller that has not caught up, and
   * painting it somewhere it does not belong would be worse than not
   * painting it.
   *
   * Single-line fields only. A textarea scrolls in two dimensions and the
   * ink layer follows one, so `multiline` ignores this.
   */
  marks?: readonly TextFieldMark[]
  /**
   * What a click on a marked span does. Absent means a mark is decoration
   * and the click falls through to the field, which is what a caret needs.
   *
   * THE MARK TAKES THE CLICK when this is present: the span is the one place
   * on the line where the pointer means "tell me about this", and the rest of
   * the field still places the caret. It is a trade rather than a free win —
   * clicking the middle of `{{baseUrl}}` no longer puts the caret there — and
   * it is the same one Postman makes for the same reason.
   */
  onMarkClick?: (mark: TextFieldMark, at: { x: number; y: number }) => void
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
      aria-label={props.ariaLabel}
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
      onScroll={followScroll}
    />
  )

  const textareaElement = () => (
    <textarea
      class="ui-text-field__input"
      id={inputId() || undefined}
      placeholder={props.placeholder ?? ''}
      disabled={props.disabled === true}
      required={props.required === true}
      aria-label={props.ariaLabel}
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

  /**
   * The value split into plain and marked runs.
   *
   * Built from the marks the caller gave, in order, skipping anything that
   * does not land inside the value. The runs are what the ink layer draws:
   * the SAME characters the input holds, in the same font at the same
   * position, so the two layers cannot drift — only the paint differs. That
   * is why a mark is a highlight and not a widget: a chip of a different
   * width would put every character after it in a different place than the
   * caret believes it is.
   */
  const runs = (): Array<{
    text: string
    marked: boolean
    tone?: 'reference' | 'secret' | 'unknown'
    mark?: TextFieldMark
  }> => {
    const text = String(props.value)
    const out: Array<{
      text: string
      marked: boolean
      tone?: 'reference' | 'secret' | 'unknown'
      mark?: TextFieldMark
    }> = []
    let at = 0
    for (const m of props.marks ?? []) {
      if (m.from < at || m.to > text.length || m.from >= m.to) continue
      if (m.from > at) out.push({ text: text.slice(at, m.from), marked: false })
      out.push({
        text: text.slice(m.from, m.to),
        marked: true,
        tone: m.tone ?? 'reference',
        mark: m,
      })
      at = m.to
    }
    if (at < text.length) out.push({ text: text.slice(at), marked: false })
    return out
  }

  const inked = () => props.multiline !== true && (props.marks?.length ?? 0) > 0

  // The ink follows the input's own horizontal scroll. A URL longer than the
  // field scrolls under the caret, and a layer that stayed put would sit a
  // word to the left of the text it is marking.
  let ink: HTMLDivElement | undefined
  const followScroll = (e: Event): void => {
    const el = e.currentTarget as HTMLInputElement
    if (ink) ink.style.transform = `translateX(${-el.scrollLeft}px)`
  }

  const input = () => (
    <>
      <div
        class="ui-text-field__control"
        data-trailing={props.trailing !== undefined ? 'true' : 'false'}
        data-unit={props.unit !== undefined && props.multiline !== true ? 'true' : undefined}
        data-ink={inked() ? 'true' : undefined}
      >
        <Switch>
          <Match when={props.multiline === true}>{textareaElement()}</Match>
          <Match when={true}>{inputElement()}</Match>
        </Switch>
        {/* ABOVE the input and inert: it paints the same characters the input
            holds, while the input keeps the caret, the selection and every
            gesture. aria-hidden because it is the same text twice — a reader
            announcing both would hear the value stutter. */}
        <Show when={inked()}>
          <div class="ui-text-field__ink" aria-hidden="true" ref={ink}>
            <For each={runs()}>
              {(run) => (
                <Show when={run.marked} fallback={<span>{run.text}</span>}>
                  <span
                    class="ui-text-field__mark"
                    data-tone={run.tone}
                    data-interactive={props.onMarkClick ? 'true' : undefined}
                    onClick={(e: MouseEvent) => {
                      const mark = run.mark
                      if (!mark) return
                      props.onMarkClick?.(mark, { x: e.clientX, y: e.clientY })
                    }}
                  >
                    {run.text}
                  </span>
                </Show>
              )}
            </For>
          </div>
        </Show>
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
