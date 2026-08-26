/**
 * e2e: a sealed vault raises the unlock prompt and the original call
 * completes — the whole of nocx-y7fg in one journey (ADR-0032).
 *
 * The feature, stated as behaviour only: pressing Test on a saved AI
 * endpoint while the vault is sealed raises the unlock dialog instead of
 * failing with "vault is sealed", and once unlocked the probe completes the
 * original call. Dismissing the dialog fails the call cleanly — no failure
 * is painted for a choice the person made, and nothing hangs.
 *
 * The seam under test is the renderer's dispatcher (ADR-0032): the backend
 * normalizes any sealed-vault failure to code -32001 / reason vault-sealed
 * (internal/transport/vault_sealed.go) — one wrapper, every method — and
 * the dispatcher raises the vault-owned unlock dialog (vault.tsx, one
 * dialog coalescing concurrent sealed calls) and re-sends the request
 * verbatim exactly once. The re-sent request is a fresh submission, so the
 * operation's gates and lane permit, released when the failed attempt
 * returned, are free for it.
 *
 * The backend is THIS FILE'S OWN devharness on a disposable home with no
 * Secret Service (VaultBackend, `true`), so the passphrase journey is the
 * real one and the endpoint never leaks into the shared stand.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, bindEndpoint, settingsReady } from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
// The rail is selected by PAGE ID, not by display title (nocx-dgsp): the
// pages here are titled Endpoints (under the Assistant group) and Protection
// (under Vault), and neither rename touches these selectors.
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_VAULT_NAV = '.ui-grouped-nav__item[data-item="vault"]'

const test = base
const nonce = Date.now().toString(36)
const passphrase = `sealed-probe-pass-${nonce}`
const endpointName = `E2E Sealed Probe ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-y7fg-e2e-'))
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function openAIEndpoints(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_AI_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
}

async function openVaultPage(page: Page): Promise<void> {
  await page.locator(SETTINGS_VAULT_NAV).click()
  await expect(page.getByText('Where it is stored')).toBeVisible({ timeout: 10_000 })
}

/** The documented first-run journey: set up protection with a passphrase
 *  (the endpoints surface does not raise the setup sheet itself — reported
 *  in agent-ask.spec.ts — so this spec drives the surface that does). */
async function setupVault(page: Page): Promise<void> {
  await openVaultPage(page)
  await page.getByRole('button', { name: 'Set up protection', exact: true }).click()
  const setupDialog = page
    .locator('.ui-prompt-overlay')
    .filter({ has: page.locator('#vault-setup-passphrase') })
  await expect(setupDialog).toBeVisible({ timeout: 10_000 })
  await page.locator('#vault-setup-passphrase').fill(passphrase)
  await page.locator('#vault-setup-confirm').fill(passphrase)
  await page
    .getByRole('dialog')
    .getByRole('button', { name: /Set Up/i })
    .click()
  await expect(page.locator('.ui-vault-code-block-wrap .ui-code-block')).toBeVisible({
    timeout: 10_000,
  })
  await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()
  await expect(setupDialog).not.toBeVisible({ timeout: 10_000 })
}

/** Lock the vault from the Vault settings page. */
async function lockVault(page: Page): Promise<void> {
  await openVaultPage(page)
  await page.getByRole('button', { name: 'Lock now', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Lock now', exact: true })).not.toBeVisible({
    timeout: 10_000,
  })
}

/** Create the endpoint with a key, through the dialog a user uses. */
async function createEndpoint(page: Page): Promise<void> {
  await page.getByRole('button', { name: '+ New endpoint' }).first().click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
  await expect(dialog).toBeVisible()
  await dialog.locator('#endpoint-name').fill(endpointName)
  await dialog.locator('#endpoint-base-url').fill(fake.baseUrl())
  await dialog.locator('#endpoint-key').fill(`e2e-key-${nonce}`)
  await dialog.getByRole('button', { name: 'Add model' }).click()
  await dialog.locator('#endpoint-model-0-name').fill('e2e-model')
  await dialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()
  await expect(dialog).not.toBeVisible({ timeout: 10_000 })
}

/** The unlock sheet, identified by the sheet class AND the action every one
 *  of its tabs carries. The endpoint editor's native <dialog> is open
 *  underneath it, so a bare role=dialog filter would match both; the sheet
 *  itself is the ui-prompt section (vault.tsx UnlockDialog). */
function unlockSheet(page: Page) {
  return page
    .locator('.ui-prompt')
    .filter({ has: page.getByRole('button', { name: 'Unlock', exact: true }) })
}

/** Open the saved endpoint's edit dialog — the surface the Test button lives
 *  on. */
async function openEndpointEditor(page: Page): Promise<void> {
  await page.getByRole('button', { name: `Edit ${endpointName}`, exact: true }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
  await expect(dialog).toBeVisible({ timeout: 10_000 })
}

/** Open the editor over a LOCKED vault and decline the unlock it raises.
 *  This endpoint's key IS a vault row, so the form opens on "Use existing
 *  secret" with a picker on screen — and a picker renders secret NAMES,
 *  which is a request for vault data (ADR-0032, nocx-5ratm). The test below
 *  owns the assertion that it asks; here the point is to get past it, so
 *  what follows measures the Test button and nothing else. */
async function openEndpointEditorDecliningTheUnlock(page: Page): Promise<void> {
  await openEndpointEditor(page)
  const unlock = unlockSheet(page)
  await expect(unlock).toBeVisible({ timeout: 10_000 })
  await unlock.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(unlock).not.toBeVisible({ timeout: 10_000 })
}

test('a sealed vault raises the unlock and the probe completes after unlocking', async ({
  page,
}) => {
  await openApp(page)
  await openAIEndpoints(page)
  await setupVault(page)
  await openAIEndpoints(page)
  await createEndpoint(page)
  await expect(page.locator('.ep-root')).toContainText(endpointName)

  // The vault is sealed: pressing Test must raise the unlock, not fail with
  // "vault is sealed".
  await lockVault(page)
  await openAIEndpoints(page)
  await openEndpointEditorDecliningTheUnlock(page)
  await page.getByRole('button', { name: 'Test endpoint', exact: true }).click()

  const unlock = unlockSheet(page)
  await expect(unlock).toBeVisible({ timeout: 10_000 })

  // Unlock with the passphrase: the dialog closes, the original probe is
  // re-sent once the vault answers, and the verdict lands.
  await page.locator('#vault-unlock-passphrase').fill(passphrase)
  await unlock.getByRole('button', { name: 'Unlock', exact: true }).click()
  await expect(unlock).not.toBeVisible({ timeout: 10_000 })
  const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
  await expect(dialog).toContainText('e2e-model answered in', { timeout: 15_000 })
})

test('a bound key asks the vault as the editor opens, and is then named (nocx-5ratm)', async ({
  page,
}) => {
  await openApp(page)
  await openAIEndpoints(page)
  await lockVault(page)
  await openAIEndpoints(page)

  // The picker itself asks: it has to render the NAME of a secret, which
  // only the vault can answer, and this endpoint's key is a bound row so the
  // form opens with the picker already on screen. Before this, the editor
  // read the vault's state, decided not to ask, and showed the person the
  // row handle — `secrow:` plus 32 hex — where the name of their own key
  // belongs, with no way to tell why.
  await openEndpointEditor(page)
  const unlock = unlockSheet(page)
  await expect(unlock).toBeVisible({ timeout: 10_000 })

  await page.locator('#vault-unlock-passphrase').fill(passphrase)
  await unlock.getByRole('button', { name: 'Unlock', exact: true }).click()
  await expect(unlock).not.toBeVisible({ timeout: 10_000 })

  // The endpoint was saved with a typed key, so the vault holds it under the
  // name the mint gave it (capability/config.go endpointKeyName), and the
  // editor opens on "Use existing secret" with that row bound.
  const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
  const picker = dialog.locator('select').first()
  await expect(picker).toHaveValue(/^secrow:/)
  await expect(picker.locator('option:checked')).toHaveText(`${endpointName} API key`, {
    timeout: 10_000,
  })
})

test('dismissing the unlock fails the call cleanly — no failure is painted', async ({ page }) => {
  await openApp(page)
  await openAIEndpoints(page)
  await lockVault(page)
  await openAIEndpoints(page)
  await openEndpointEditorDecliningTheUnlock(page)

  await page.getByRole('button', { name: 'Test endpoint', exact: true }).click()
  const unlock = unlockSheet(page)
  await expect(unlock).toBeVisible({ timeout: 10_000 })

  // The person chose not to unlock: the dialog closes, the call fails
  // cleanly, and nothing hangs — no "Test failed" sentence appears, and the
  // editor stays usable.
  await unlock.getByRole('button', { name: 'Cancel', exact: true }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
  await expect(dialog).not.toContainText('Test failed', { timeout: 10_000 })
  // The editor still responds: the dismissal did not wedge the surface.
  await dialog.locator('#endpoint-name').fill(`${endpointName} (edited)`)
})
