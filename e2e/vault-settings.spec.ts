/**
 * e2e: Vault settings — actions that must not destroy secrets.
 *
 * Two cases, both asserting a secret still resolves after the operation,
 * never just that a dialog closed:
 *
 * 1. Change passphrase → restart → unseal with NEW passphrase →
 *    confirm the stored password is still readable.
 * 2. Regenerate recovery code → restart → unseal with NEW code →
 *    confirm the stored password is still readable.
 *
 * Uses its own ports (19880/19881) so it never conflicts with vault.spec.ts
 * or the default dev-server port.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, bindEndpoint, type DisposableRoot } from './harness'
import { readStand } from './stand'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

// ── Helpers ────────────────────────────────────────────────────────────────

interface XdgDirsResult {
  root: string
  data: string
  config: string
  cache: string
}

function createXdgDirs(): XdgDirsResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-vault-settings-'))
  for (const d of ['data', 'config', 'cache'] as const)
    mkdirSync(join(root, d), { recursive: true })
  return {
    root,
    data: join(root, 'data'),
    config: join(root, 'config'),
    cache: join(root, 'cache'),
  }
}

function asDisposableRoot(r: XdgDirsResult): DisposableRoot {
  return { root: r.root }
}

/**
 * Open the Settings page and navigate to the Vault section.
 * Returns after the section is visible.
 */
async function openVaultSettings(page: Page): Promise<void> {
  // Open Settings via keyboard shortcut (Cmd/Ctrl+,).
  const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
  await page.keyboard.press(`${mod}+,`)
  // Wait for the Settings search field to appear.
  await expect(page.getByRole('searchbox', { name: 'Search settings' })).toBeVisible({
    timeout: 5000,
  })
  // Click the vault page in the left rail by its page id — the id survives
  // the display-title rename to "Protection".
  await page.locator('.ui-grouped-nav__item[data-item="vault"]').click()
  // Wait for a section the Vault page always has, in every vault state.
  // This used to wait for a heading called "Status", which the page no longer
  // has: it now opens with the protection state itself ("Protection is not set
  // up yet" / the configured equivalent), so the wait never returned and every
  // test using this helper failed before it had done anything.
  await expect(page.getByText('Where it is stored')).toBeVisible({ timeout: 5000 })
}

/**
 * Set up the vault with a passphrase via the SetupDialog triggered by saving
 * a connection password. Returns the recovery code displayed after setup.
 */
async function setupVaultAndSavePassword(
  page: Page,
  passphrase: string,
  profileName: string,
): Promise<string> {
  // ── Create a connection with password ────────────────────────────────
  // Click "Connections" in the left rail.
  await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
  await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

  // Click "+ New connection".
  await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }).click()
  await expect(page.getByRole('dialog').filter({ hasText: 'New Connection' })).toBeVisible({
    timeout: 5000,
  })

  await page.getByRole('dialog').getByLabel('Host or connection string').fill('localhost')
  await page.getByRole('dialog').getByRole('button', { name: 'Next', exact: true }).click()
  await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })

  // Fill profile form.
  await page.locator('#profile-name').fill(profileName)
  await page.locator('#profile-host').fill('localhost')

  await page.locator('.cm-form').getByRole('tab', { name: 'Authentication' }).click()
  await page.locator('#profile-auth-user').fill('test-user')

  // Set auth mode to 'password'.
  await page
    .getByRole('radiogroup', { name: 'Authentication method' })
    .getByRole('radio', { name: 'Password' })
    .click()

  await expect(
    page
      .getByRole('radiogroup', { name: 'Authentication method' })
      .getByRole('radio', { name: 'Password' }),
  ).toHaveAttribute('aria-checked', 'true', { timeout: 3000 })

  // Click "Set Password".
  await page
    .locator('.cm-form')
    .getByRole('button', { name: /Set Password/i })
    .click()
  // The PasswordEditor's own field, by id: closing it opens a setup sheet that
  // holds two password fields of its own, so a bare
  // `[role="dialog"] input[type="password"]` stops being unique right here.
  const pwInput = page.locator('#password-value')
  await expect(pwInput).toBeVisible({ timeout: 3000 })
  await pwInput.fill('test-password-123')
  await page.getByRole('button', { name: 'OK' }).click()
  await expect(pwInput).not.toBeVisible({ timeout: 3000 })

  // ── Vault setup dialog ──────────────────────────────────────────────
  // It follows the password dialog directly: minting the secret is what needs
  // the vault. This used to click "Create Connection" first, on the older
  // arrangement where the save attempted the write and the setup came out of
  // its failure — which now aims at a button the sheet is already covering.
  //
  // Scoped to the prompt overlay, because the connection form is itself a
  // role="dialog" and the sheet opens inside it: both hasText and has: match
  // the ancestor as well.
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

  // The recovery step, asserted through the code block itself — by now the
  // setup overlay has closed and the code renders in the form behind it.
  const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
  await expect(codeBlock).toBeVisible({ timeout: 10_000 })
  const code = await codeBlock.textContent()
  expect(code).not.toBeNull()
  expect((code ?? '').length).toBeGreaterThan(10)

  // Click "Done" to close setup. The password is minted and bound.
  await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

  // Save the connection.
  await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

  // Verify profile was saved.
  await expect(page.locator('.ui-collection-row').filter({ hasText: profileName })).toBeVisible({
    timeout: 10_000,
  })

  return code ?? ''
}

/**
 * The unlock sheet, identified by the action every one of its tabs carries.
 *
 * Not by its title: it reads "Unlock the vault to open this connection", naming
 * the reason it is asking, and a filter on "Unlock Vault" matched nothing. Not
 * by #vault-unlock-passphrase either: that field belongs to ONE tab, so a
 * "the sheet closed" assertion written against it also passes when the user has
 * merely switched to Recovery code — true for the wrong reason.
 */
function unlockSheet(page: Page) {
  return page
    .getByRole('dialog')
    .filter({ has: page.getByRole('button', { name: 'Unlock', exact: true }) })
}

const test = base

// ── Case 1: Change passphrase ─────────────────────────────────────────────

test.describe('Vault settings — change passphrase', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('change passphrase then unseal with new passphrase and connect', async ({ page }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)

    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Phase 1: Set up vault + save password.
    await openVaultSettings(page)
    const recoveryCode = await setupVaultAndSavePassword(
      page,
      'original-passphrase',
      'change-pass-test',
    )
    expect(recoveryCode.length).toBeGreaterThan(0)

    // Phase 2: Navigate to Vault section and change passphrase.
    await openVaultSettings(page)

    // Click "Change passphrase" button.
    await page.getByRole('button', { name: 'Change passphrase' }).click()
    // Fill in the ChangePassphraseDialog.
    await page.locator('#vault-change-old-passphrase').fill('original-passphrase')
    await page.locator('#vault-change-new-passphrase').fill('new-passphrase-789')
    await page.locator('#vault-change-confirm-passphrase').fill('new-passphrase-789')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Change passphrase', exact: true })
      .click()

    // Wait for dialog to close (success).
    await expect(
      page.getByRole('dialog').filter({ hasText: 'Change vault passphrase' }),
    ).not.toBeVisible({
      timeout: 10_000,
    })
    // Capture tab count before restart.
    const tabsBefore = await page.locator('.nocx-tab-title').count()

    // Phase 3: Restart backend (vault seals).
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()

    // Wait for app to reinitialize.
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Phase 4: Connect to profile → vault sealed → unseal with NEW passphrase.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Go to Connections.
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

    // The saved profile should be in the list.
    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'change-pass-test' }),
    ).toBeVisible({ timeout: 10_000 })

    // Click Connect.
    await page.getByRole('button', { name: 'Connect to change-pass-test' }).click()

    // Unlock vault dialog should appear.
    await expect(unlockSheet(page)).toBeVisible({
      timeout: 10_000,
    })

    // Unseal with the NEW passphrase.
    await page.getByRole('dialog').getByRole('button', { name: 'Passphrase', exact: true }).click()
    await page.locator('#vault-unlock-passphrase').fill('new-passphrase-789')
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    // Dialog closes — vault unsealed.
    await expect(unlockSheet(page)).not.toBeVisible({
      timeout: 10_000,
    })

    // The assertion: a new SSH tab appears, proving the secret was resolved.
    await expect(page.locator('.nocx-tab-title')).toHaveCount(tabsBefore + 1, {
      timeout: 15_000,
    })
    await expect(page.locator('.nocx-tab-title').last()).not.toHaveText('Settings')
  })
})

// ── Case 2: Reissue recovery code ─────────────────────────────────────────

test.describe('Vault settings — reissue recovery code', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('reissue recovery code then unseal with new code and connect', async ({ page }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)

    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Phase 1: Set up vault + save password.
    await openVaultSettings(page)
    await setupVaultAndSavePassword(page, 'test-passphrase', 'recovery-test')

    // Phase 2: Navigate to Vault section and reissue recovery code.
    await openVaultSettings(page)

    // Click "Reissue recovery code" button.
    await page.getByRole('button', { name: 'Reissue recovery code' }).click()

    // Fill in passphrase in RecoveryCodeDialog.
    await page.locator('#vault-reissue-passphrase').fill('test-passphrase')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Generate new recovery code', exact: true })
      .click()

    // Read the new recovery code.
    await expect(page.getByRole('dialog').filter({ hasText: 'Reissue recovery code' })).toBeVisible(
      {
        timeout: 10_000,
      },
    )
    const newCodeBlock = page.locator('.ui-code-block')
    const newCode = await newCodeBlock.textContent()
    expect(newCode).not.toBeNull()
    expect((newCode ?? '').length).toBeGreaterThan(10)

    // Click Done.
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // Phase 3: Restart backend (vault seals).
    const tabsBefore = await page.locator('.nocx-tab-title').count()
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Phase 4: Connect → vault sealed → unseal with NEW recovery code.
    await page.keyboard.press('Meta+,')
    await expect(page.getByRole('searchbox', { name: 'Search settings' })).toBeVisible({
      timeout: 5000,
    })

    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'recovery-test' }),
    ).toBeVisible({ timeout: 10_000 })

    await page.getByRole('button', { name: 'Connect to recovery-test' }).click()

    await expect(unlockSheet(page)).toBeVisible({
      timeout: 10_000,
    })

    // Unseal with the recovery code.
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Recovery code', exact: true })
      .click()
    await page.locator('#vault-unlock-recovery').fill(newCode ?? '')
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    await expect(unlockSheet(page)).not.toBeVisible({
      timeout: 10_000,
    })

    // The assertion: a new SSH tab appears, proving the secret was resolved.
    await expect(page.locator('.nocx-tab-title')).toHaveCount(tabsBefore + 1, {
      timeout: 15_000,
    })
    await expect(page.locator('.nocx-tab-title').last()).not.toHaveText('Settings')
  })
})
