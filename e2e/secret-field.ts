/**
 * Driving the ONE secret field, from a Playwright spec (nocx-3o0ed.4).
 *
 * The connections editor, the endpoints page and the API workbench's Auth tab
 * all place the same control now: a text field whose value says where its
 * value comes from. A whole `{{secret:secrow:…}}` reference means the vault's
 * row, and the field draws that reference as a chip carrying the secret's
 * NAME; anything else is a literal nobody has stored yet.
 *
 * The segmented "Type a new one / Use existing secret" control these helpers
 * replace is gone, and with it the `#…-secret` comboboxes. What is left is two
 * doors onto the vault, which differ only in what they do to a LOCKED one
 * (frontend/src/ui/secret-picker.ts):
 *
 *   '@'  — passive. Typed into the field, it filters and only ever offers.
 *   lock — explicit. Pressing it IS the request, so a sealed vault is asked to
 *          unlock and an uninitialized one is set up, rather than being drawn
 *          as an offer row.
 *
 * The panel mounts on document.body, so its rows are addressed from `page`,
 * never from inside the dialog that owns the field.
 */
import { expect, type Locator, type Page } from '@playwright/test'

/** The panel's rows, in the order it offers them. */
export function panelRows(page: Page): Locator {
  return page.locator('.ui-floating-panel[data-variant="secret"] .ui-floating-panel__row')
}

/** One row of the panel, by the text it shows. */
export function panelRow(page: Page, text: string | RegExp): Locator {
  return panelRows(page).filter({ hasText: text })
}

/**
 * The chip a bound field draws over its reference — the secret's name.
 *
 * This is where a name-vs-handle assertion belongs now. The old bound
 * combobox held the name as its DOM VALUE, so specs asserted
 * `toHaveValue(name)` and `not.toHaveValue(/secrow:/)`; the field's value is
 * the opaque reference itself, and the NAME is what is painted over it.
 */
export function fieldChip(field: Locator): Locator {
  return field.locator('xpath=..').locator('.ui-text-field__mark')
}

/**
 * Press a field's lock.
 *
 * The lock renders only while the field has focus (ui/text-field.tsx), so this
 * focuses first. The button keeps that focus on mousedown, which is why
 * clicking it does not close what it just opened.
 */
export async function pressLock(field: Locator): Promise<void> {
  await field.click()
  const lock = field.locator('xpath=..').getByRole('button', { name: 'Store in vault' })
  await expect(lock).toBeVisible({ timeout: 5000 })
  await lock.click()
}

/** Press the lock and take the row offering to keep exactly what the field
 *  holds. This is what a standalone "Store" button was. */
export async function storeFieldValue(page: Page, field: Locator, value: string): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, `Store "${value}" in the vault…`)
  await expect(row).toBeVisible({ timeout: 5000 })
  await row.click()
}

/** Press the lock over an EMPTY field and take the plain create row. An empty
 *  field has nothing to store, so the panel is exactly the '@' panel and this
 *  row is what is left to take. */
export async function addSecretFromLock(page: Page, field: Locator): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, 'Add a secret…')
  await expect(row).toBeVisible({ timeout: 5000 })
  await row.click()
}

/** Bind a field to an existing secret through the lock. */
export async function bindSecretFromLock(page: Page, field: Locator, name: string): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, name)
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.click()
  await expect(field).toHaveValue(/^\{\{secret:.+\}\}$/, { timeout: 5000 })
}
