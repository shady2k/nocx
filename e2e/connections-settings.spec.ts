/**
 * e2e: Connections page inside Settings.
 *
 * Walks from Settings gear → Connections rail nav → profile CRUD → Save →
 * Connect → verify new SSH tab. Requires the composition-root `onConnect`
 * wire (coordinator-applied line in main.tsx near SURFACE_ID_SETTINGS
 * registration). Without it the Connect button dispatches a no-op and the
 * tab assertion fails.
 */
import { test, expect } from './harness'

test.describe('Connections inside Settings', () => {
  test.use({ viewport: { width: 1280, height: 800 } })

  test('walks from Settings gear to a created SSH tab', async ({ page }) => {
    await page.goto('/')
    // Wait for the app to load.
    await expect(page.locator('.tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings via keyboard shortcut (Meta+,).
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Record tab count AFTER Settings is open (Settings is itself a tab).
    const tabsBeforeConnect = await page.locator('.tab-title').count()

    // Click "Connections" in the left rail.
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    // ConnectionsView renders its title and actions in a kit Toolbar, not the
    // old .cm-header the standalone surface had.
    await expect(page.locator('[role="toolbar"] h1')).toHaveText('Connections', {
      timeout: 5000,
    })

    // Click "+ New connection".
    await page.locator('[role="toolbar"] .cm-primary').click()
    await expect(page.locator('.cm-form')).toBeAttached()

    // Fill in the profile form (Name, Host).
    const inputs = page.locator('.cm-form input')
    await inputs.nth(0).fill('Test SSH')
    await inputs.nth(1).fill('localhost')

    // Save the profile first (calls createProfile), then Connect opens a tab.
    await page.locator('.cm-form-actions .cm-save').click()

    // Wait for the save to complete — the form should close and the profile
    // list should update. The selected profile form opens again after save.
    await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })

    // Now click "Connect" to create an SSH tab.
    await page.locator('.cm-form-actions .cm-connect').click()

    // A new SSH tab should have been created.
    const tabsAfterConnect = await page.locator('.tab-title').count()
    expect(tabsAfterConnect).toBe(tabsBeforeConnect + 1)

    // Verify the new tab is not called "Settings".
    const newTab = page.locator('.tab-title').last()
    await expect(newTab).not.toHaveText('Settings')
  })
})
