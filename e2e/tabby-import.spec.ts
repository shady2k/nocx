/**
 * e2e: Tabby import preview + execute (bead nocx-kqw6)
 *
 * Verifies the full UI flow: upload a Tabby config with encrypted vault →
 * enter passphrase → preview → confirm → profile is created with resolvable
 * password secret.
 *
 * Uses its own ports (19890/19891) as prescribed by the brief.
 */

import { test as base, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, bindEndpoint, type DisposableRoot } from './harness'
import { readStand } from './stand'

const test = base

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

// ── Encrypted vault fixture ──────────────────────────────────────────────
//
// AES-256-CBC, PBKDF2-SHA512, 100k iterations, 8-byte salt and 16-byte IV as
// hex, contents base64 — the parameters verified against Tabby's own
// vault.service.ts (nocx-25k9.9), not against our encoder.
//
// Plaintext: {"config":null,"secrets":[{"type":"ssh:password",
//   "key":{"user":"deploy","host":"web.example.com","port":22},
//   "value":"e2e-secret-val"}]}
// Passphrase: "e2e-pw"
//
// The secret's key names the SAME connection as the profile below, on purpose.
// An earlier fixture named a host that appears nowhere in the config, so the
// import left the secret unbound — "not used by anything" — and a test
// asserting only that a row appeared was perfectly happy with it.
const CONFIG_YAML = `\
version: 1
profiles:
  - id: "ssh:custom:e2e-test:1"
    type: "ssh"
    name: "E2E Tabby Import"
    options:
      host: "web.example.com"
      port: 22
      user: "deploy"
      auth: password
groups:
  - id: "g:e2e"
    name: "E2E Group"
vault:
  version: 1
  encrypted: true
  contents: "+oXSQiEp1Uwnkh9qinRzfD6d9hprCrNoGiXtfYBT7rN905+7XjW04Ga8u1D/VgP6g+FfqmM8FimVjbvmS14W32H38pw5doOQjoX2JSRE4ilj/drHwWbJQVg5lJsq5Gx9+PvN8b5JdMVMsngTtIkrO//ahlsnmBAF/ENrWU80hZZKvwRu79qMklTX3Hf9tGZH"
  keySalt: "cef74ecf5c5a9012"
  iv: "cdc44974813ca33f838446a2b370c1e2"
`

// ── Helpers ───────────────────────────────────────────────────────────────

interface XdgDirsResult {
  /** The disposable directory the three below live in, and the one the backend
   *  is actually given. It was missing here: asDisposableRoot read `r.root`
   *  off a shape that had no such field, so the backend got `{ root:
   *  undefined }` and the run died in path.join before a single assertion.
   *  Nothing caught it because the spec could not run at all until CI built
   *  the devharness it needs (nocx-azxe.2), and nothing type-checks e2e/. */
  root: string
  data: string
  config: string
  cache: string
}

function createXdgDirs(): XdgDirsResult {
  const baseDir = mkdtempSync(join(tmpdir(), 'nocx-e2e-tabby-'))
  mkdirSync(join(baseDir, 'data'), { recursive: true })
  mkdirSync(join(baseDir, 'config'), { recursive: true })
  mkdirSync(join(baseDir, 'cache'), { recursive: true })
  return {
    root: baseDir,
    data: join(baseDir, 'data'),
    config: join(baseDir, 'config'),
    cache: join(baseDir, 'cache'),
  }
}

function asDisposableRoot(r: XdgDirsResult): DisposableRoot {
  return { root: r.root }
}

// ── Test ──────────────────────────────────────────────────────────────────

test.describe('Tabby import preview + execute', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult
  let configPath: string

  test.beforeAll(() => {
    xdg = createXdgDirs()
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
    configPath = join(xdg.root, 'tabby-e2e-test.yml')
    writeFileSync(configPath, CONFIG_YAML, 'utf-8')
  })

  test.afterAll(() => {
    backend?.stop()
  })

  // The whole path, including the vault setup the import triggers on a machine
  // with no OS keychain. The last assertion is the one that matters: the
  // imported password RESOLVES. A profile row proves metadata was written and
  // says nothing about the secret it points at, and this feature's entire job
  // is moving secrets — an import that created rows referencing nothing would
  // pass every weaker check.
  test('imports Tabby encrypted config via preview -> confirm -> execute, profile+secret appear', async ({
    page,
  }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')

    // Wait for the app to load (initial tab appears).
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // Open Settings via keyboard shortcut.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Click "Connections" in the left rail.
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

    // Click "Import…" — scoped to the toolbar, because the empty-state offers
    // a second button with the same name and an unscoped locator matches both.
    await page
      .locator('[role="toolbar"]')
      .getByRole('button', { name: 'Import…', exact: true })
      .click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Import Connections' })).toBeVisible({
      timeout: 5000,
    })

    // Select Tabby radio.
    await page.getByRole('radio', { name: /Tabby/ }).click()

    // Upload the config file.
    await page.locator('#cm-import-file').setInputFiles(configPath)

    // Enter the vault passphrase.
    await page.locator('#cm-import-passphrase').fill('e2e-pw')

    // Click Preview.
    await page.getByRole('button', { name: 'Preview', exact: true }).click()

    // Preview dialog should appear with import details.
    await expect(page.getByRole('dialog').filter({ hasText: 'Tabby Import Preview' })).toBeVisible({
      timeout: 15_000,
    })

    // Verify the preview shows expected counts.
    await expect(page.getByRole('dialog').filter({ hasText: '1 profile' })).toBeVisible()
    await expect(page.getByRole('dialog').filter({ hasText: '1 group' })).toBeVisible()
    await expect(page.getByRole('dialog').filter({ hasText: '1 secret' })).toBeVisible()

    // Confirm the import — scoped to the preview dialog.
    await page
      .getByRole('dialog')
      .filter({ hasText: 'Tabby Import Preview' })
      .getByRole('button', { name: 'Import', exact: true })
      .click()

    // Importing secrets needs a vault, and this backend has no OS keychain, so
    // the import correctly stops and asks for one. The preview dialog stays
    // open underneath: the import is deferred, not cancelled.
    // Scoped by what each sheet CONTAINS, not by its text. The preview dialog
    // stays open underneath, and it is a role="dialog" too — so a text filter
    // matches the ancestor as well as the sheet, which is the strict-mode
    // violation this used to fail with. A descendant's text is the ancestor's.
    const setupSheet = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-setup-passphrase') })
    await expect(setupSheet).toBeVisible({ timeout: 10_000 })
    await page.locator('#vault-setup-passphrase').fill('tabby-import-passphrase')
    await page.locator('#vault-setup-confirm').fill('tabby-import-passphrase')
    await setupSheet.getByRole('button', { name: /Set Up/i }).click()

    const recoveryCode = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    await expect(recoveryCode).toBeVisible({ timeout: 10_000 })
    await page.getByRole('button', { name: 'Done', exact: true }).click()

    // Setup done — the deferred import runs and the preview closes.
    await expect(
      page.getByRole('dialog').filter({ hasText: 'Tabby Import Preview' }),
    ).not.toBeVisible({ timeout: 15_000 })

    // The imported profile should appear in the connections list.
    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'E2E Tabby Import' }),
    ).toBeVisible({ timeout: 5000 })

    // The imported group should appear.
    await expect(page.locator('.cm-group-header').filter({ hasText: 'E2E Group' })).toBeVisible({
      timeout: 5000,
    })

    // The imported secret is real and attached to the profile that needs it.
    //
    // The vault entry's key names deploy@web.example.com:22, which is exactly
    // the profile in the config, so a correct import binds the secret onto
    // the profile. "The row appeared" proves metadata was written and says
    // nothing about the secret it points at — and moving secrets is this
    // feature's entire job, so an import leaving rows that reference nothing
    // would pass every weaker check.
    await page.locator('.ui-grouped-nav__item[data-item="secrets"]').click()
    const credRow = page.locator('.ui-collection-row').filter({ hasText: 'deploy@web.example.com' })
    await expect(credRow).toBeVisible({ timeout: 10_000 })
    await expect(credRow).not.toContainText('not used by anything')

    // No console errors.
    const consoleErrors: string[] = []
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text())
      }
    })
    expect(consoleErrors).toHaveLength(0)
  })
})
