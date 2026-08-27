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

/**
 * Take a row of the panel, the way the panel answers a mouse.
 *
 * NOT `row.click()`, and the difference is the whole of nocx-vzdna's second
 * half. `FloatingPanel` activates a row on **mousedown**
 * (floating-panel.ts, renderRow), and activation removes the row: the picker
 * closes and the list's nodes go with it. Playwright's `click()` is hover,
 * then mousedown, then mouseup on the SAME node — so between its own down and
 * up the node is gone, it reports `element was detached from the DOM` and
 * restarts the whole action. On the retry there is nothing left to click, and
 * what it prints is whatever the stale hit test found: `<html … > intercepts
 * pointer events`, which is an echo of a click that had already succeeded and
 * not a diagnosis of one that failed. Measured on the tape: mousedown -> pick
 * -> activate -> requestCreate -> the host's prompt -> close, all inside 3ms,
 * all correct, all before Playwright's mouseup.
 *
 * So the helper presses the way the product listens. Down and up, at one
 * point, no move between them: that is also a real `click` for a browser, so
 * this keeps working unchanged if the row ever moves to activating on click
 * (see the note beside `onPick` — it should).
 *
 * AND IT MUST NOT PASS WHEN THE ROW STOPS ACTIVATING, which hand-driven mouse
 * coordinates would otherwise allow — a press into empty space raises nothing
 * and throws nothing. Two assertions close that, and they close it together:
 *
 *   BEFORE — the point about to be pressed belongs to the row. This is the
 *     hit test `click()` would have done, kept rather than dropped, so a panel
 *     that is off-screen, inert, or covered still fails here.
 *   AFTER — the row is gone. For a list row that happens only through
 *     `activate()`, which closes the picker on every branch that acts.
 *
 * Neither alone is enough: the first passes if the press does nothing, the
 * second passes if the press missed the row and dismissed the surface
 * underneath instead. Together they say the press landed on the row and the
 * row answered.
 */
async function activateRow(page: Page, row: Locator): Promise<void> {
  // Playwright's own actionability check — visible, stable, enabled, and
  // receiving pointer events — plus the mouse move a person makes first.
  await row.hover()
  const box = await row.boundingBox()
  expect(box, 'the row has no box to press').not.toBeNull()
  const x = box!.x + box!.width / 2
  const y = box!.y + box!.height / 2

  const owner = await page.evaluate(
    ({ px, py }) => {
      const hit = document.elementFromPoint(px, py)
      const rowEl = hit?.closest('.ui-floating-panel__row') ?? null
      return {
        ownedByARow: rowEl !== null,
        hit:
          hit === null
            ? 'null'
            : `${hit.tagName}${hit.id ? `#${hit.id}` : ''}.${String(hit.className)}`,
      }
    },
    { px: x, py: y },
  )
  expect(owner.ownedByARow, `the press point belongs to ${owner.hit}, not to a panel row`).toBe(
    true,
  )

  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.up()

  await expect(row, 'the row was pressed and did not activate').toHaveCount(0, { timeout: 10_000 })
}

/** Press the lock and take the row offering to keep exactly what the field
 *  holds. This is what a standalone "Store" button was. */
export async function storeFieldValue(page: Page, field: Locator, value: string): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, `Store "${value}" in the vault…`)
  await expect(row).toBeVisible({ timeout: 5000 })
  await activateRow(page, row)
}

/** Press the lock over an EMPTY field and take the plain create row. An empty
 *  field has nothing to store, so the panel is exactly the '@' panel and this
 *  row is what is left to take. */
export async function addSecretFromLock(page: Page, field: Locator): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, 'Add a secret…')
  await expect(row).toBeVisible({ timeout: 5000 })
  await activateRow(page, row)
}

/** Bind a field to an existing secret through the lock. */
export async function bindSecretFromLock(page: Page, field: Locator, name: string): Promise<void> {
  await pressLock(field)
  const row = panelRow(page, name)
  await expect(row).toBeVisible({ timeout: 10_000 })
  await activateRow(page, row)
  await expect(field).toHaveValue(/^\{\{secret:.+\}\}$/, { timeout: 5000 })
}
