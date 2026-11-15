/** A sealed vault offers unlock from the API header secret picker. */
import { expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  VaultBackend,
  appReadyForInput,
  bindEndpoint,
  ensureAssistantVault,
} from './harness'
import { readStand } from './stand'

const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const test = base

let backend: VaultBackend
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-y7fg-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

test('typing a bare @ in a sealed header value shows the unlock offer', async ({ page }) => {
  await openApp(page)
  await ensureAssistantVault(page, endpoint, 'sealed-header-passphrase')
  await page.getByRole('button', { name: 'Lock now', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Lock now', exact: true })).not.toBeVisible()
  await page.locator('.activity-bar button[data-action="api"]').click()
  const workbench = page.locator('.api-workbench')
  await expect(workbench).toBeVisible({ timeout: 15_000 })
  const requestRow = workbench.locator('.api-tree__row').filter({ hasText: 'Zen' })
  await expect(requestRow).toBeVisible({ timeout: 15_000 })
  await requestRow.click()
  await workbench.getByRole('tab', { name: /^Headers\b/ }).click()
  await workbench.getByRole('button', { name: 'Add header', exact: true }).click()
  const headerValue = workbench.locator('#api-header-value-0')
  await headerValue.press('@')

  const picker = page.getByRole('listbox', { name: 'vault secrets' })
  await expect(picker).toBeVisible({ timeout: 10_000 })
  await expect(
    picker.getByRole('option', { name: 'Unlock the vault to use its secrets' }),
  ).toBeVisible()
})
