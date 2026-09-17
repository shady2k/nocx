import type { Page } from '@playwright/test'
import { appReadyForInput, test, expect, settingsReady } from './harness'

const placementNav = '.ui-grouped-nav__item[data-item="Interface"] button'
const placementSelect = '.ui-settings-row[data-key="tab.placement"] select'
const placementMarker = '.ui-settings-row[data-key="tab.placement"] .ui-settings-modified-dot'

/**
 * Puts tab.placement at `value` and returns only once the BACKEND has accepted
 * the write (nocx-9ox05). The select shows the new value the instant it is
 * chosen; the write behind it holds the config gate until it commits, and
 * backup.create, backup.preview and backup.restore all wait on that gate for
 * at most a second. On a slow CI disk the write outlasted that wait, the
 * preview came back "Control plane busy", and the spec was reading a state
 * the product had not reached. The row's modified marker is set from the
 * accepted outcome, after the response, so it is the observable the next
 * step depends on: present for vertical, absent for the default horizontal.
 *
 * A write of the value already held is skipped rather than issued, because
 * the marker could not tell that write landing from it not having landed —
 * and the shared stand may well start where this spec left it.
 */
async function setPlacement(page: Page, value: 'vertical' | 'horizontal'): Promise<void> {
  await page.locator(placementNav).click()
  await expect(page.locator(placementSelect)).toBeVisible({ timeout: 5000 })
  if ((await page.locator(placementSelect).inputValue()) !== value) {
    await page.selectOption(placementSelect, value)
  }
  await expect(page.locator(placementSelect)).toHaveValue(value)
  if (value === 'vertical') {
    await expect(page.locator(placementMarker)).toHaveAttribute('data-modified', 'true')
  } else {
    await expect(page.locator(placementMarker)).not.toHaveAttribute('data-modified')
  }
}

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
    await appReadyForInput(page)

    // Open settings.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)

    // Change a reachable persisted setting so restore has an observable effect.
    await setPlacement(page, 'vertical')

    // Navigate to Backup & Restore and create a backup.
    await page.locator('.ui-grouped-nav__item[data-item="backup"] button').click()
    await expect(page.getByRole('heading', { name: 'Create backup' })).toBeVisible()

    const downloadPromise = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Create backup', exact: true }).click()
    const download = await downloadPromise
    const backupPath = testInfo.outputPath('backup.json')
    await download.saveAs(backupPath)

    // Mutate the setting after creation so restore must move it back.
    await setPlacement(page, 'horizontal')

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
