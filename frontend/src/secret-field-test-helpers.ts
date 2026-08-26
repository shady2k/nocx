import { fireEvent, waitFor } from '@solidjs/testing-library'

/**
 * Driving the ONE secret field the way a person does (nocx-3o0ed.4).
 *
 * These replace `secret-source-test-helpers.ts`, which drove the segmented
 * control's bound SuggestionField. The control is gone and so is that helper;
 * what a test proved through it — a value bound to a vault row, named rather
 * than handled — is proved through the field's own two doors instead:
 *
 *   '@'  — the passive door. Type it and narrow by name, exactly as an
 *          @-mention works, and take the row.
 *   lock — the explicit door. Press it and take a row from the panel it
 *          raises, including the row offering to STORE what the field holds.
 *
 * The panel mounts on `document.body` (secret-picker-field.ts), so its rows
 * are looked up globally rather than inside a container.
 */

/** A field is an <input> or, when it is multiline, a <textarea>. Both carry a
 *  value and a selection, which is all these helpers touch. */
type FieldElement = HTMLInputElement | HTMLTextAreaElement

const panelRows = (): HTMLElement[] =>
  Array.from(document.querySelectorAll<HTMLElement>('.ui-floating-panel__row'))

const rowLabel = (row: HTMLElement): string =>
  row.querySelector('.ui-collection-row__info')?.textContent?.trim() ?? ''

/** Every row the panel is currently offering, in order. */
export function offeredSecretRows(): string[] {
  return panelRows().map(rowLabel)
}

/** Wait for a row with this exact text and take it. A mousedown IS the pick
 *  (floating-panel.ts) and the picker treats it as Enter on that row. */
export async function takePanelRow(label: string): Promise<void> {
  const row = await waitFor(() => {
    const found = panelRows().find((candidate) => rowLabel(candidate) === label)
    if (!found)
      throw new Error(`panel row "${label}" is not offered; got ${offeredSecretRows().join(' | ')}`)
    return found
  })
  fireEvent.mouseDown(row)
}

/** Bind a field to a secret through the '@' door: type the trigger, narrow by
 *  the start of the name, take the row. Resolves once the field holds the
 *  reference — the opaque handle, never the name. */
export async function bindSecretByTyping(
  input: FieldElement,
  filter: string,
  name: string,
): Promise<void> {
  input.focus()
  fireEvent.input(input, { target: { value: `@${filter}` } })
  await takePanelRow(name)
  await waitFor(() => {
    if (!/^\{\{secret:.+\}\}$/.test(input.value)) {
      throw new Error(`field did not bind to ${name}; it holds "${input.value}"`)
    }
  })
}

/** Press the field's lock — the explicit door. `selection` defaults to the
 *  whole value, which is what a caret with nothing selected means. */
export function pressLock(input: FieldElement, selection?: { start: number; end: number }): void {
  input.focus()
  const start = selection?.start ?? input.value.length
  const end = selection?.end ?? input.value.length
  input.setSelectionRange(start, end)
  const lock = input
    .closest('.ui-text-field__control')
    ?.querySelector<HTMLElement>('[aria-label="Store in vault"]')
  if (!lock) throw new Error('the field has no lock')
  fireEvent.click(lock)
}

/** Bind a field to a secret through the LOCK: press it, take the named row. */
export async function bindSecretFromLock(input: FieldElement, name: string): Promise<void> {
  pressLock(input)
  await takePanelRow(name)
  await waitFor(() => {
    if (!/^\{\{secret:.+\}\}$/.test(input.value)) {
      throw new Error(`field did not bind to ${name}; it holds "${input.value}"`)
    }
  })
}
