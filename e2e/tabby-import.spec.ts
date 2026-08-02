/**
 * e2e: Tabby import preview + execute (bead nocx-kqw6)
 *
 * Verifies the full UI flow: upload a Tabby config with encrypted vault →
 * enter passphrase → preview → confirm → profile is created with resolvable
 * password secret.
 *
 * Uses its own ports (19890/19891) as prescribed by the brief.
 */

import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, type BackendEndpoint, type DisposableRoot } from './harness'

const test = base

const DEVHARNESS_BIN = process.env.NOCX_VAULT_BIN ?? '/tmp/nocx-devharness'
const FIRST_PORT = 19890

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
    data: join(baseDir, 'data'),
    config: join(baseDir, 'config'),
    cache: join(baseDir, 'cache'),
  }
}

function asDisposableRoot(r: XdgDirsResult): DisposableRoot {
  return { root: r.root }
}

/**
 * Inject Wails stubs pointing at the given backend endpoint.
 * Must run before page.goto('/') so the bindings exist at app init.
 */
async function injectWailsBindings(page: Page, endpoint: BackendEndpoint): Promise<void> {
  await page.addInitScript(
    (opts: { p: number; t: string }) => {
      ;(window as unknown as { go: unknown }).go = {
        main: {
          WailsApp: {
            GetWSPort: () => Promise.resolve(opts.p),
            GetWSToken: () => Promise.resolve(opts.t),
            CheckForUpdate: () => Promise.resolve(null),
            ReportHealthy: () => Promise.resolve(),
            ApplyUpdate: () => Promise.resolve(),
          },
        },
      }
    },
    { p: endpoint.port, t: endpoint.token },
  )
}

// ── Test ──────────────────────────────────────────────────────────────────

test.describe('Tabby import preview + execute', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult
  let configPath: string

  test.beforeAll(() => {
    xdg = createXdgDirs()
    backend = new VaultBackend(DEVHARNESS_BIN, asDisposableRoot(xdg), true)
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
    const ep = await backend.start(FIRST_PORT)
    await injectWailsBindings(page, ep)
    await page.goto('/')

    // Wait for the app to load (initial tab appears).
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // Open Settings via keyboard shortcut.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Click "Connections" in the left rail.
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
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
    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible({
      timeout: 10_000,
    })
    await page.locator('#vault-setup-passphrase').fill('tabby-import-passphrase')
    await page.locator('#vault-setup-confirm').fill('tabby-import-passphrase')
    await page
      .getByRole('dialog')
      .filter({ hasText: 'Set Up Vault' })
      .getByRole('button', { name: /Set Up/i })
      .click()
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })
    await page
      .getByRole('dialog')
      .filter({ hasText: 'Recovery Code' })
      .getByRole('button', { name: 'Done', exact: true })
      .click()

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
    await page.locator('.ui-settings-section-nav-item[data-section="Secrets"]').click()
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
