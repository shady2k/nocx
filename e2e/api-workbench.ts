import { expect, type Locator, type Page } from '@playwright/test'

import { bindEndpoint, type VaultBackend } from './harness'

export const PLAYGROUND = 'Playground'
export const ZEN = 'Zen'
export const RATE_LIMIT = 'Rate limit'

const SEEDED_ROWS = [PLAYGROUND, ZEN, RATE_LIMIT] as const

/** A request or collection row, addressed by the name the tree exposes. */
export function treeRow(page: Page, workbench: Locator, name: string): Locator {
  return workbench
    .locator('.api-tree__row')
    .filter({ has: page.locator(`.ui-tree-row__name[title="${name}"]`) })
}

/** Reach the API workbench through its activity-bar entry and wait for its seeded tree. */
export async function openWorkbench(
  page: Page,
  backend: VaultBackend,
  expectedRows: readonly string[] = SEEDED_ROWS,
): Promise<Locator> {
  const ep = await backend.start()
  await bindEndpoint(page, ep)
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

  await page.locator('.activity-bar button[data-action="api"]').click()
  const workbench = page.locator('.api-workbench')
  await expect(workbench).toBeVisible({ timeout: 15_000 })
  for (const name of expectedRows) {
    await expect(treeRow(page, workbench, name)).toBeVisible({ timeout: 15_000 })
  }
  return workbench
}
