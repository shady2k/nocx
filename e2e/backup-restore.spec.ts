import { test, expect, settingsReady } from './harness'

/**
 * The backup surface must move non-empty user state through the real renderer
 * and control plane. Changing a persisted setting before and after creation
 * makes a successful no-op restore fail this acceptance check.
 */
test.describe('Backup & Restore', () => {
  test('creates, reads, previews and restores a backup after mutating state', async ({
    page,
  }, testInfo) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open settings.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)

    // Change a reachable persisted setting so restore has an observable effect.
    const placementNav = '.ui-grouped-nav__item[data-item="Interface"] button'
    const placementSelect = '.ui-settings-row[data-key="tab.placement"] select'
    await page.locator(placementNav).click()
    await expect(page.locator(placementSelect)).toBeVisible({ timeout: 5000 })
    await page.selectOption(placementSelect, 'vertical')
    await expect(page.locator(placementSelect)).toHaveValue('vertical')

    // Navigate to Backup & Restore and create a backup.
    await page.locator('.ui-grouped-nav__item[data-item="backup"] button').click()
    await expect(page.getByRole('heading', { name: 'Create backup' })).toBeVisible()

    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Create backup', exact: true }).click()
    const download = await downloadPromise
    const backupPath = testInfo.outputPath('backup.json')
    await download.saveAs(backupPath)

    // Mutate the setting after creation so restore must move it back.
    await page.locator(placementNav).click()
    await expect(page.locator(placementSelect)).toBeVisible({ timeout: 5000 })
    await page.selectOption(placementSelect, 'horizontal')
    await expect(page.locator(placementSelect)).toHaveValue('horizontal')

    // Go back to Backup & Restore, load the backup file and preview.
    await page.locator('.ui-grouped-nav__item[data-item="backup"] button').click()
    // The section is on screen before the file goes into it — the same wait
    // the first visit above already does. Without it this step races the
    // navigation, which is the family of spec defects nocx-rv53x cleared out.
    await expect(page.getByRole('heading', { name: 'Restore backup' })).toBeVisible()

    await page.locator('.ui-file-input__native').setInputFiles(backupPath)
    // THE APP'S OWN RECORD THAT IT TOOK THE FILE, asserted before the preview
    // that file is supposed to trigger (nocx-hphhh). This failed once on
    // webkit at the preview heading two steps below, and the trace said the
    // file had never been read at all: the surface still said "No file
    // selected" and the backend was never asked for a preview. An assertion
    // that far downstream reported the wrong step as the broken one, which is
    // most of what made that failure expensive to read.
    await expect(page.locator('.ui-file-input__name')).toHaveText('backup.json')
    await expect(page.getByRole('heading', { name: /Preview — merge/ })).toBeVisible({
      timeout: 10_000,
    })
    await expect(page.getByRole('button', { name: 'Merge backup', exact: true })).toBeEnabled()

    await page.getByRole('button', { name: 'Merge backup', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Merge', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Merge', exact: true }).click()
    await expect(page.getByText('Restore complete (merge).')).toBeVisible({ timeout: 10_000 })

    // The restored setting is visible again through the ordinary Settings seam.
    await page.locator(placementNav).click()
    await expect(page.locator(placementSelect)).toBeVisible({ timeout: 5000 })
    await expect(page.locator(placementSelect)).toHaveValue('vertical')
  })
})
