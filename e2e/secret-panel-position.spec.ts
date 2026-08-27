/**
 * e2e: the '@' panel opens where a person can see it AND reach it
 * (nocx-vzdna).
 *
 * The owner's report was "typing '@' in a value field renders no panel". The
 * panel was not missing — it was rendered ABOVE THE TOP OF THE WINDOW. Its
 * CSS says `bottom: 100%`, which is correct for the terminal, where the panel
 * is a child of the command editor's `position: relative` root and the rule
 * means "just above the prompt". A field's panel is mounted on the body
 * instead (a plain input has no positioned root, and a form row would clip
 * it), so the same rule resolved against the initial containing block: the
 * panel's bottom edge at y=0 and the whole panel off-screen above it.
 *
 * TWO THINGS HAVE TO BE TRUE and the second one is not the first. Placement
 * put the panel in the viewport; that turned ten "element is outside of the
 * viewport" failures into ten `<html> intercepts pointer events` ones, on
 * rows Playwright called visible, enabled and stable. The cause is the other
 * half of the same move: a field's panel re-homes into the modal `<dialog>`
 * its field lives in, because the browser's top layer is a parent and not a
 * z-index — and the top layer governs PAINTING, not inheritance. These
 * dialogs are DOM descendants of a pane, an inactive pane is
 * `pointer-events: none` (base.css), and the panel inherited it. Painted,
 * visible, and not hit-testable, with everything outside the modal inert, so
 * the click fell through to the document element. The panel now declares
 * `pointer-events: auto` for itself, the way `.ui-toast` does inside the
 * `pointer-events: none` toast host.
 *
 * SO THIS SPEC ASKS BOTH QUESTIONS, and the second one with
 * `elementFromPoint` rather than with a real click: this runs on the shared
 * stand, and activating a row here would set up a vault every other spec on
 * that stand would then inherit. The hit test is the exact predicate
 * Playwright's click uses, without the side effect.
 *
 * The unit tests assert the placement ARITHMETIC
 * (frontend/src/ui/floating-panel.test.ts); jsdom lays nothing out, calls
 * every element visible and cannot hit-test at all, so both questions here
 * are ones only a browser can answer.
 *
 * NO VAULT NEEDED, deliberately. The panel opens on '@' in every vault state
 * — an offer row when the vault is uninitialized or sealed, the list when it
 * is open — and this spec is about WHERE it opens, not what it lists.
 *
 * NO Escape ANYWHERE, also deliberately. `ui/overlay/stack.ts` installs its
 * Escape handler on `document` in the CAPTURE phase, so Escape reaches the
 * topmost overlay before the field's own keydown ever runs: pressing it to
 * dismiss the panel closes the dialog under it instead, and the rest of the
 * test then waits for controls that are gone. A panel is dismissed here the
 * way the adapter dismisses it — by removing the '@' the trigger is made of.
 */
import type { Locator } from '@playwright/test'
import { test, expect, settingsReady, type Page } from './harness'

const SETTINGS_ENDPOINTS_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const OPEN_PANEL = '.ui-floating-panel[data-variant="secret"][data-open="true"]'

/** The New Endpoint dialog with its header row already added, so both fields
 *  this spec measures exist before anything is opened over them. */
async function openEndpointDialog(page: Page): Promise<Locator> {
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`${SETTINGS_ENDPOINTS_NAV} button`).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: '+ New endpoint' }).first().click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
  await expect(dialog).toBeVisible({ timeout: 10_000 })
  return dialog
}

/** Type a bare '@' into a field, the way a person does — never `fill`, which
 *  sets the value without the caret the adapter reads. */
async function triggerPanel(page: Page, field: Locator): Promise<Locator> {
  await field.click()
  await field.pressSequentially('@')
  const panel = page.locator(OPEN_PANEL)
  await expect(panel).toBeVisible({ timeout: 10_000 })
  return panel
}

/** Dismiss the panel the way the adapter does: the trigger word is the '@',
 *  and deleting it is what "I am not naming a secret" looks like. */
async function dismissPanel(page: Page, field: Locator): Promise<void> {
  await field.press('Backspace')
  await expect(page.locator(OPEN_PANEL)).toHaveCount(0, { timeout: 10_000 })
}

interface Measured {
  panel: { top: number; bottom: number; left: number; right: number }
  field: { top: number; bottom: number }
  viewport: { width: number; height: number }
  /** Does the open panel's first row own the point a click would land on?
   *  False is the `<html> intercepts pointer events` failure, in the same
   *  terms Playwright states it. */
  rowOwnsItsPoint: boolean
  /** What the hit test found instead, for a failure that has to be read. */
  hitTarget: string
}

/** The panel's rect, its field's, and the hit test at the first row's centre
 *  — one evaluation, so all three describe the same frame. */
async function measure(page: Page, fieldId: string): Promise<Measured> {
  return page.evaluate((id) => {
    const panelEl = document.querySelector<HTMLElement>(
      '.ui-floating-panel[data-variant="secret"][data-open="true"]',
    )!
    const fieldEl = document.getElementById(id)!
    const rowEl = panelEl.querySelector<HTMLElement>('.ui-floating-panel__row')!
    const p = panelEl.getBoundingClientRect()
    const f = fieldEl.getBoundingClientRect()
    const r = rowEl.getBoundingClientRect()
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    return {
      panel: { top: p.top, bottom: p.bottom, left: p.left, right: p.right },
      field: { top: f.top, bottom: f.bottom },
      viewport: { width: window.innerWidth, height: window.innerHeight },
      rowOwnsItsPoint: hit !== null && (rowEl.contains(hit) || hit.contains(rowEl)),
      hitTarget: hit === null ? 'null' : `${hit.tagName}.${hit.className}`,
    }
  }, fieldId)
}

/** Criterion 1 and 3: the whole panel is on screen, on both axes. */
function expectInsideViewport(m: Measured): void {
  expect(m.panel.top).toBeGreaterThanOrEqual(0)
  expect(m.panel.bottom).toBeLessThanOrEqual(m.viewport.height)
  expect(m.panel.left).toBeGreaterThanOrEqual(0)
  expect(m.panel.right).toBeLessThanOrEqual(m.viewport.width)
}

/** Anchored to its own field: the panel touches it, above or below. */
function expectAnchoredToField(m: Measured): void {
  const gap = Math.min(
    Math.abs(m.field.top - m.panel.bottom),
    Math.abs(m.panel.top - m.field.bottom),
  )
  expect(gap).toBeLessThanOrEqual(16)
}

test.describe('the secret panel opens where a person can reach it', () => {
  test('a field panel is inside the viewport, and its rows own their own point', async ({
    page,
  }) => {
    const dialog = await openEndpointDialog(page)
    const panel = await triggerPanel(page, dialog.locator('#endpoint-key'))

    const m = await measure(page, 'endpoint-key')
    expectInsideViewport(m)
    expectAnchoredToField(m)
    await expect(panel.locator('.ui-floating-panel__row').first()).toBeInViewport()
    // The second half: in a modal dialog, painted is not the same as
    // reachable. `hitTarget` is reported so a regression names the element
    // that took the click instead of the row.
    expect(m.rowOwnsItsPoint, `hit target was ${m.hitTarget}`).toBe(true)
  })

  test('a row still activates from the panel portion hanging past the dialog', async ({ page }) => {
    const dialog = await openEndpointDialog(page)
    const panel = await triggerPanel(page, dialog.locator('#endpoint-key'))
    const row = panel.locator('.ui-floating-panel__row').first()

    // The panel is re-homed into the dialog so it can paint in the top layer.
    // Keep this assertion: without the DOM relationship, this test would not
    // prove the dialog's bubbling light-dismiss path.
    expect(await panel.evaluate((el) => el.closest('dialog') !== null)).toBe(true)

    const rowBox = await row.boundingBox()
    const dialogPanelBox = await dialog.locator('.nocx-dialog__panel').boundingBox()
    expect(rowBox).not.toBeNull()
    expect(dialogPanelBox).not.toBeNull()
    if (rowBox === null || dialogPanelBox === null) throw new Error('missing geometry')

    // Use the intersection of the row and the region immediately past the
    // dialog panel's right boundary, rather than a viewport constant. This
    // point is inside the row but outside the dialog panel.
    const dialogRight = dialogPanelBox.x + dialogPanelBox.width
    const hangingLeft = Math.max(rowBox.x, dialogRight)
    const hangingRight = rowBox.x + rowBox.width
    expect(hangingRight).toBeGreaterThan(hangingLeft)
    const x = hangingLeft + (hangingRight - hangingLeft) / 2
    const y = rowBox.y + rowBox.height / 2

    await page.mouse.click(x, y)

    // The clicked offer row opens the vault setup surface. The endpoint
    // dialog must remain open underneath it; a light-dismiss would discard it.
    await expect(dialog).toBeVisible()
    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible()
  })

  test('a field further down the form opens its panel at ITS OWN offset', async ({ page }) => {
    const dialog = await openEndpointDialog(page)
    // The second field FIRST, while nothing floats over the dialog: the
    // header row's value field sits below the API key, which is what makes
    // the two offsets different.
    await dialog.getByRole('button', { name: 'Add header' }).click()
    const header = dialog.locator('#endpoint-header-0-value')
    await expect(header).toBeVisible({ timeout: 10_000 })
    const key = dialog.locator('#endpoint-key')

    await triggerPanel(page, key)
    const first = await measure(page, 'endpoint-key')
    await dismissPanel(page, key)

    await triggerPanel(page, header)
    const second = await measure(page, 'endpoint-header-0-value')

    // Criterion 2: two fields at different offsets, two panels at different
    // offsets — neither of them at the top of the document.
    expect(second.field.top).toBeGreaterThan(first.field.top)
    expect(second.panel.top).not.toBe(first.panel.top)
    expectInsideViewport(second)
    expectAnchoredToField(second)
    expect(second.rowOwnsItsPoint, `hit target was ${second.hitTarget}`).toBe(true)
  })
})
