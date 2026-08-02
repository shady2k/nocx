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
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings via keyboard shortcut (Meta+,).
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Record tab count AFTER Settings is open (Settings is itself a tab).
    const tabsBeforeConnect = await page.locator('.nocx-tab-title').count()

    // Click "Connections" in the left rail.
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    // The view is identified by its own root and its toolbar action, not by a
    // heading: inside Settings no section draws a title of its own — the rail
    // names the section, and the standalone surface's .cm-header > h1 went with
    // the migration to a kit Toolbar (nocx-pp3y.1).
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })
    await expect(
      page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }),
    ).toBeVisible()

    // Click "+ New connection". The form buttons are kit Buttons, which own their
    // own class and refuse one from a caller — so they are addressed by their
    // accessible name, which is what the user reads anyway (nocx-pp3y.1).
    await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }).click()
    await expect(page.locator('.cm-form')).toBeAttached()

    // Fill in the profile form (Name, Host, User).
    //
    // User is required whenever no secret is selected (nocx-74cn added the
    // rule; nocx-vjhz is this spec catching up). Leaving it empty does not fail
    // here — it fails 20 lines down, where Create is refused, no profile is
    // written and the tab count that Connect was supposed to change reads one
    // short. The only visible evidence was a "User is required" toast that
    // nothing asserted on.
    const inputs = page.locator('.cm-form input')
    await inputs.nth(0).fill('Test SSH')
    await inputs.nth(1).fill('localhost')
    await page.locator('#profile-auth-user').fill('tester')

    // Save the profile first (calls createProfile), then Connect opens a tab.
    await page
      .locator('.cm-form-actions')
      .getByRole('button', { name: 'Create', exact: true })
      .click()

    // Wait for the save to complete — the form should close and the profile
    // list should update. The selected profile form opens again after save.
    await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })

    // Now click "Connect" to create an SSH tab.
    await page
      .locator('.cm-form-actions')
      .getByRole('button', { name: 'Connect', exact: true })
      .click()

    // A new SSH tab should have been created. Asserted with a retrying
    // expectation, not a bare count(): opening the tab is a round trip, so a
    // count read on the next line races it and answers for the DOM as it was.
    await expect(page.locator('.nocx-tab-title')).toHaveCount(tabsBeforeConnect + 1, {
      timeout: 10_000,
    })

    // Verify the new tab is not called "Settings".
    const newTab = page.locator('.nocx-tab-title').last()
    await expect(newTab).not.toHaveText('Settings')
  })
})
