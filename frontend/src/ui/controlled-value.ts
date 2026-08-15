import { createEffect } from 'solid-js'

/**
 * Mirror a controlled value into an element, writing ONLY when it differs.
 *
 * The write is guarded because the callers round-trip the value: typing into
 * a field that sits in an `EditableRowList` updates the caller's draft,
 * which REPLACES the row object, which re-runs this effect with the string
 * the element already holds. An unguarded `value=` would assign that
 * identical string on every keystroke — a no-op per spec, but an assignment
 * a browser may still use to reset caret or selection, and the assignment
 * that originally motivated the guard: writing `input.value` closed the
 * native `<datalist>` popup, so a suggestion list shut itself on every
 * letter typed. Guarded, a controlled input never writes the value it is
 * not changing, and never fights the caret it is not moving.
 *
 * Call from the element's `ref`; the effect runs synchronously at creation,
 * so the initial value is in place before the caller sees the element.
 */
export function mirrorControlledValue(
  element: HTMLInputElement | HTMLTextAreaElement,
  getValue: () => string | number,
): void {
  createEffect(() => {
    const next = String(getValue())
    if (element.value !== next) element.value = next
  })
}
