/**
 * e2e: sidebar shield sandbox conversion (ADR-0043).
 *
 * The backend capability varies by runner. Visibility and removed entry
 * points are unconditional; conversion runs only when native enforcement is
 * available and the shell has reported a verified cwd.
 */
import type { Page } from '@playwright/test'
import { test, expect } from './harness'

const PALETTE_MOD = process.platform === 'darwin' ? 'Meta' : 'Control'
const SHIELD = '[data-testid="sandbox-shield"]'

async function openSettings(page: Page): Promise<void> {
  await page.keyboard.press(`${PALETTE_MOD}+,`)
  await expect(page.getByRole('searchbox', { name: 'Search settings' })).toBeVisible()
}

async function openPalette(page: Page): Promise<void> {
  await page.keyboard.press(`${PALETTE_MOD}+Shift+P`)
  await expect(page.getByRole('searchbox', { name: 'Command palette filter' })).toBeVisible()
}

async function setSandboxFlag(page: Page, on: boolean): Promise<void> {
  await openSettings(page)
  await page.getByRole('button', { name: /Experimental/ }).click()
  await expect(page.locator('[id="st-setting-sandbox.enabled"]')).toBeVisible()
  await page.evaluate((checked) => {
    const el = document.querySelector<HTMLInputElement>(
      '[id="st-setting-sandbox.enabled"] input[type="checkbox"]',
    )
    if (!el) throw new Error('sandbox.enabled toggle not found')
    if (el.checked !== checked) {
      el.checked = checked
      el.dispatchEvent(new Event('change', { bubbles: true }))
    }
  }, on)
  const row = page.locator('[id="st-setting-sandbox.enabled"]')
  if (on) await expect(row).toHaveClass(/ui-settings-row--modified/)
  else await expect(row).not.toHaveClass(/ui-settings-row--modified/)
}

test.describe('sandbox shield', () => {
  test.setTimeout(120_000)

  test('flag off hides the shield and the command palette has no sandbox row', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1, { timeout: 20_000 })
    await setSandboxFlag(page, false)
    await page.getByRole('tab').first().click()
    await expect(page.locator(SHIELD)).toHaveCount(0)
    await openPalette(page)
    await expect(page.getByText('Sandboxed shell…', { exact: true })).toHaveCount(0)
  })

  test('flag on exposes exactly one shield and never restores palette or More-menu launch', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1, { timeout: 20_000 })
    await setSandboxFlag(page, true)
    await page.getByRole('tab').first().click()

    const shield = page.locator(SHIELD)
    await expect(shield).toHaveCount(1)
    await expect(page.locator('.activity-bar-top').locator(SHIELD)).toHaveCount(1)
    await expect(page.locator('.ui-sidebar-view__actions').locator(SHIELD)).toHaveCount(0)
    await expect(shield).toHaveAttribute(
      'title',
      /Convert this tab to a sandboxed shell|Sandbox unavailable|Wait for the shell/,
    )

    await openPalette(page)
    await expect(page.getByText('Sandboxed shell…', { exact: true })).toHaveCount(0)
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: 'More' }).click()
    await expect(page.getByText('New sandboxed tab', { exact: true })).toHaveCount(0)
    await page.keyboard.press('Escape')

    if (await shield.isEnabled()) {
      const before = await page.getByRole('tab').count()
      await shield.click()
      await expect(page.getByRole('heading', { name: 'Sandbox permissions' })).toBeVisible()
      await page.getByRole('button', { name: 'Open sandboxed tab' }).click()
      await expect(page.getByRole('tab')).toHaveCount(before)
      const selected = page.getByRole('tab', { selected: true })
      await expect(selected.locator('.nocx-tab-line > :first-child')).toHaveClass(
        /nocx-tab-sandboxed-marker/,
      )
      await expect(shield).toHaveAttribute('aria-selected', 'true')
      await expect(shield).toHaveAttribute('title', /Remove sandbox from this tab/)

      await shield.click()
      await expect(page.getByRole('tab')).toHaveCount(before)
      await expect(
        page.getByRole('tab', { selected: true }).locator('.nocx-tab-sandboxed-marker'),
      ).toHaveCount(0)
      await expect(shield).not.toHaveAttribute('aria-selected', 'true')
    }
  })
})
