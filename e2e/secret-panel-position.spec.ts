/**
 * e2e: the '@' panel opens where a person can see it and click it
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
 * THIS IS THE ONLY HONEST CHECK OF IT. Every existing test drove the panel
 * with the keyboard — type `@name`, press Enter — which never asks where the
 * panel is; and the unit tests run in jsdom, which lays nothing out and calls
 * every element visible, so `toBeVisible()` passed for the whole time the
 * panel was invisible. The unit tests assert the placement ARITHMETIC
 * (frontend/src/ui/floating-panel.test.ts); only a real browser can say the
 * numbers land on the screen. `toBeInViewport` is the assertion that would
 * have caught the original defect: it is the exact condition ten clicks
 * failed on across chromium and webkit ("element is outside of the
 * viewport").
 *
 * NO VAULT NEEDED, deliberately. The panel opens on '@' in every vault state
 * — an offer row when the vault is uninitialized or sealed, the list when it
 * is open — and this spec is about WHERE it opens, not what it lists. That
 * keeps it on the shared stand and independent of what other specs left
 * behind.
 */
import type { Locator } from '@playwright/test'
import { test, expect, settingsReady, type Page } from './harness'

const SETTINGS_ENDPOINTS_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const OPEN_PANEL = '.ui-floating-panel[data-variant="secret"][data-open="true"]'

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

/** The panel's rect and its field's, in one evaluation so they describe the
 *  same frame. */
async function rects(
  page: Page,
  fieldId: string,
): Promise<{
  panel: { top: number; bottom: number; left: number; right: number }
  field: { top: number; bottom: number }
  viewport: { width: number; height: number }
}> {
  return page.evaluate((id) => {
    const panelEl = document.querySelector(
      '.ui-floating-panel[data-variant="secret"][data-open="true"]',
    )!
    const fieldEl = document.getElementById(id)!
    const p = panelEl.getBoundingClientRect()
    const f = fieldEl.getBoundingClientRect()
    return {
      panel: { top: p.top, bottom: p.bottom, left: p.left, right: p.right },
      field: { top: f.top, bottom: f.bottom },
      viewport: { width: window.innerWidth, height: window.innerHeight },
    }
  }, fieldId)
}

test.describe('the secret panel opens where a person can reach it', () => {
  test('a field panel is inside the viewport, and its rows are clickable', async ({ page }) => {
    const dialog = await openEndpointDialog(page)
    const panel = await triggerPanel(page, dialog.locator('#endpoint-key'))

    const r = await rects(page, 'endpoint-key')
    // Criterion 1: the whole panel is on screen.
    expect(r.panel.top).toBeGreaterThanOrEqual(0)
    expect(r.panel.bottom).toBeLessThanOrEqual(r.viewport.height)
    // Criterion 3: and it does not run off the right edge.
    expect(r.panel.left).toBeGreaterThanOrEqual(0)
    expect(r.panel.right).toBeLessThanOrEqual(r.viewport.width)

    // Anchored to THIS field: the panel touches it, above or below.
    const gap = Math.min(
      Math.abs(r.field.top - r.panel.bottom),
      Math.abs(r.panel.top - r.field.bottom),
    )
    expect(gap).toBeLessThanOrEqual(16)

    // The assertion the ten failures made: a row can actually be clicked.
    // "element is outside of the viewport" is what they said instead.
    const row = panel.locator('.ui-floating-panel__row').first()
    await expect(row).toBeInViewport()
  })

  test('a field further down the form opens its panel at ITS OWN offset', async ({ page }) => {
    const dialog = await openEndpointDialog(page)

    await triggerPanel(page, dialog.locator('#endpoint-key'))
    const first = await rects(page, 'endpoint-key')
    await page.keyboard.press('Escape')
    await expect(page.locator(OPEN_PANEL)).toHaveCount(0)

    await dialog.getByRole('button', { name: 'Add header' }).click()
    const header = dialog.locator('#endpoint-header-0-value')
    await expect(header).toBeVisible({ timeout: 10_000 })
    const panel = await triggerPanel(page, header)
    const second = await rects(page, 'endpoint-header-0-value')

    // Criterion 2: two fields at different offsets, two panels at different
    // offsets — neither of them at the top of the document.
    expect(second.field.top).toBeGreaterThan(first.field.top)
    expect(second.panel.top).not.toBe(first.panel.top)
    expect(second.panel.top).toBeGreaterThanOrEqual(0)
    expect(second.panel.bottom).toBeLessThanOrEqual(second.viewport.height)
    const gap = Math.min(
      Math.abs(second.field.top - second.panel.bottom),
      Math.abs(second.panel.top - second.field.bottom),
    )
    expect(gap).toBeLessThanOrEqual(16)
    await expect(panel.locator('.ui-floating-panel__row').first()).toBeInViewport()
  })
})
