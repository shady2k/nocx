/**
 * e2e: Vault full-path coverage.
 *
 * Drives the real frontend against cmd/devharness, walking the exact path a
 * user walks — no mocked components, no unit-test stubs. Each case owns a
 * clean XDG directory so every run begins uninitialised.
 *
 * Cases 1 and 2 verify vault persistence across a backend restart, using
 * page.context().addInitScript to override the Wails bindings with the new
 * launch's token (which is minted fresh per `crypto/rand`).
 *
 * Case 3 requires gnome-keyring via nix-shell; it is skipped when the
 * daemon is unavailable and the reason is marked in the test name.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, type BackendEndpoint, type DisposableRoot } from './harness'

const DEVHARNESS_BIN = process.env.NOCX_VAULT_BIN ?? '/tmp/nocx-devharness'

// Two distinct ports so restart never conflicts with the first instance's
// TIME_WAIT. Both are outside the ranges used by `wails dev` (34115), the
// e2e suite default (9876), `dev-web.sh` (9880/5180), and `npm run dev` (5173).
const FIRST_PORT = 19876
const SECOND_PORT = 19877

// ── Helpers ────────────────────────────────────────────────────────────────

interface XdgDirsResult {
  root: string
  data: string
  config: string
  cache: string
}

/** Create a temp directory with data/config/cache subdirs for one test case. */
function createXdgDirs(): XdgDirsResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-vault-'))
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
 * Inject Wails stubs pointing at the given backend endpoint.
 *
 * Uses context-level addInitScript so it runs after the harness fixture's
 * page-level init (which carries dummy env-var values). Each navigation
 * re-evaluates all registered scripts in order; the last one to set the
 * stubs wins.
 */
async function bindEndpoint(page: Page, endpoint: BackendEndpoint): Promise<void> {
  await page.context().addInitScript(
    (opts: { p: number; t: string }) => {
      // window.go is a wails-injected namespace not present in DOM types.
      const w = window as unknown as { go?: Record<string, unknown> }
      w.go = {
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

const test = base

// ── Case 1: No keyring — full round trip ──────────────────────────────────

test.describe('Vault — no keyring, full round trip', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // `true` = no Secret Service for this backend, regardless of the session
    // the suite runs in. These two cases are ABOUT the passphrase path.
    backend = new VaultBackend(DEVHARNESS_BIN, asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('saves password, sets up vault, persists across restart, unlocks with passphrase, connects', async ({
    page,
  }) => {
    // ── Phase 1: first backend ──────────────────────────────────────────
    const ep = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep)

    await page.goto('/')
    // Wait for the app to load and show the initial tab.
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings via keyboard shortcut.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })

    // Click "Connections" in the left rail.
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })
    await expect(
      page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }),
    ).toBeVisible()

    // Click "+ New connection". This opens a quick-connect dialog asking for
    // a host or connection string.
    await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }).click()
    await expect(page.getByRole('dialog').filter({ hasText: 'New Connection' })).toBeVisible({
      timeout: 5000,
    })

    // Enter a connection string (just "localhost") and click "Next" to open
    // the full profile form.
    await page.getByRole('dialog').getByLabel('Host or connection string').fill('localhost')
    await page.getByRole('dialog').getByRole('button', { name: 'Next', exact: true }).click()
    await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })

    // Fill in the profile form (Name, Host) on the General tab.
    await page.locator('#profile-name').fill('Vault Test')
    await page.locator('#profile-host').fill('localhost')

    // Switch to the Authentication tab for user + password fields.
    await page.locator('.cm-form').getByRole('tab', { name: 'Authentication' }).click()
    await page.locator('#profile-auth-user').fill('vault-test-user')

    // Set auth mode to 'password' using the radiogroup's accessible label.
    await page
      .getByRole('radiogroup', { name: 'Authentication method' })
      .getByRole('radio', { name: 'Password' })
      .click()

    // Verify the radio is checked and the password action appeared.
    await expect(
      page
        .getByRole('radiogroup', { name: 'Authentication method' })
        .getByRole('radio', { name: 'Password' }),
    ).toHaveAttribute('aria-checked', 'true', { timeout: 3000 })

    // Click "Set Password" by its accessible name in the visible form.
    await page.getByRole('button', { name: /Set Password/i }).click()
    // The PasswordEditor is a Dialog (the kit Prompt) with a password input.
    const pwInput = page.locator('[role="dialog"] input[type="password"]')
    await expect(pwInput).toBeVisible({ timeout: 3000 })
    await pwInput.fill('test-password-123')
    // Click the dialog's primary action button to confirm password.
    await page.getByRole('button', { name: 'OK' }).click()
    // Wait for the PasswordEditor to close.
    await expect(pwInput).not.toBeVisible({ timeout: 3000 })

    // Click "Create Connection" in the Dialog footer. This triggers the
    // password save, which fails with vault-uninitialized and shows SetupDialog.
    await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

    // ── Phase 2: vault setup dialog ─────────────────────────────────────
    // The SetupDialog should appear with title "Set Up Vault".
    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible({
      timeout: 10_000,
    })

    // Enter passphrase.
    await page.locator('#vault-setup-passphrase').fill('my-master-passphrase-42')
    await page.locator('#vault-setup-confirm').fill('my-master-passphrase-42')

    // Click "Set Up".
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()

    // Wait for the recovery code to appear — title changes to "Recovery Code".
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })

    // Capture the recovery code from the CodeBlock.
    const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    const code = await codeBlock.textContent()
    expect(code).not.toBeNull()
    expect((code ?? '').length).toBeGreaterThan(10)

    // Click "Done" to close setup. This triggers the deferred save retry.
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // After vault setup, the form saves the profile and closes. Verify the
    // profile appears in the connection list (evidence the save went through).
    await expect(page.locator('.ui-collection-row').filter({ hasText: 'Vault Test' })).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 3: restart backend ────────────────────────────────────────
    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()

    // Wait for app to reinitialize.
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Re-open Settings and navigate back to Connections.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

    // The saved profile should be in the list.
    await expect(page.locator('.ui-collection-row').filter({ hasText: 'Vault Test' })).toBeVisible({
      timeout: 10_000,
    })

    // Capture tab count before Connect.
    const tabsBeforeConnect = await page.locator('.nocx-tab-title').count()

    // Click the row-level "Connect" button (ariaLabel="Connect to Vault Test").
    await page.getByRole('button', { name: 'Connect to Vault Test' }).click()

    // The UnlockDialog should appear (vault is sealed).
    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock Vault' })).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 5: unlock with passphrase ─────────────────────────────────
    await page.getByRole('dialog').getByRole('button', { name: 'Passphrase', exact: true }).click()
    await page.locator('#vault-unlock-passphrase').fill('my-master-passphrase-42')
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    // Dialog closes after successful unseal.
    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock Vault' })).not.toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 6: deferred Connect runs after unlock → new SSH tab ───────
    // ensureBeforeSave stored the pending action when Connect was clicked;
    // unlocking the vault fires it, creating a terminal tab.
    await expect(page.locator('.nocx-tab-title')).toHaveCount(tabsBeforeConnect + 1, {
      timeout: 15_000,
    })
    // Verify the new tab is not Settings.
    await expect(page.locator('.nocx-tab-title').last()).not.toHaveText('Settings')
  })
})

// ── Case 2: recovery code unseal ──────────────────────────────────────────

test.describe('Vault — recovery code unseal', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // `true` = no Secret Service for this backend, regardless of the session
    // the suite runs in. These two cases are ABOUT the passphrase path.
    backend = new VaultBackend(DEVHARNESS_BIN, asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('unseals with recovery code after restart', async ({ page }) => {
    // ── Phase 1: first backend ──────────────────────────────────────────
    const ep = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep)

    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings, Connections, create profile with password (same as case 1).
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })
    await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New connection' }).click()
    await expect(page.getByRole('dialog').filter({ hasText: 'New Connection' })).toBeVisible({
      timeout: 5000,
    })
    await page.getByRole('dialog').getByLabel('Host or connection string').fill('localhost')
    await page.getByRole('dialog').getByRole('button', { name: 'Next', exact: true }).click()
    await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })

    // Fill in Name, Host on the General tab, then switch to Authentication.
    await page.locator('#profile-name').fill('Recovery Test')
    await page.locator('#profile-host').fill('localhost')
    await page.locator('.cm-form').getByRole('tab', { name: 'Authentication' }).click()
    await page.locator('#profile-auth-user').fill('recovery-user')
    await page
      .getByRole('radiogroup', { name: 'Authentication method' })
      .getByRole('radio', { name: 'Password' })
      .click()

    await page.getByRole('button', { name: /Set Password/i }).click()
    const pwInput = page.locator('[role="dialog"] input[type="password"]')
    await expect(pwInput).toBeVisible({ timeout: 3000 })
    await pwInput.fill('test-password-456')
    await page.getByRole('button', { name: 'OK' }).click()
    await expect(pwInput).not.toBeVisible({ timeout: 3000 })

    // Click "Create Connection" to save. Triggers vault-uninitialized → SetupDialog.
    await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

    // Setup dialog.
    await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).toBeVisible({
      timeout: 10_000,
    })
    await page.locator('#vault-setup-passphrase').fill('recovery-passphrase-99')
    await page.locator('#vault-setup-confirm').fill('recovery-passphrase-99')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()

    // Capture recovery code.
    await expect(page.getByRole('dialog').filter({ hasText: 'Recovery Code' })).toBeVisible({
      timeout: 10_000,
    })
    const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    const code = await codeBlock.textContent()
    expect(code).not.toBeNull()
    expect((code ?? '').length).toBeGreaterThan(10)
    const recoveryCode = code ?? ''

    // Click "Done".
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // Verify the profile appears in the connection list.
    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'Recovery Test' }),
    ).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 2: restart ────────────────────────────────────────────────
    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Navigate back to Settings → Connections.
    await page.keyboard.press('Meta+,')
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })
    await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })

    // Verify the profile is in the list (persisted across restart).
    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'Recovery Test' }),
    ).toBeVisible({
      timeout: 10_000,
    })

    // Capture tab count before Connect.
    const tabsBeforeConnect = await page.locator('.nocx-tab-title').count()

    // Click the row-level Connect button.
    await page.getByRole('button', { name: 'Connect to Recovery Test' }).click()

    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock Vault' })).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 3: unlock with recovery code ──────────────────────────────
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Recovery code', exact: true })
      .click()
    await page.locator('#vault-unlock-recovery').fill(recoveryCode)
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    await expect(page.getByRole('dialog').filter({ hasText: 'Unlock Vault' })).not.toBeVisible({
      timeout: 10_000,
    })

    // Deferred Connect runs after unlock → new SSH tab.
    await expect(page.locator('.nocx-tab-title')).toHaveCount(tabsBeforeConnect + 1, {
      timeout: 15_000,
    })
    await expect(page.locator('.nocx-tab-title').last()).not.toHaveText('Settings')
  })
})

// ── Case 3: with keyring, setup is silent ─────────────────────────────────

test.describe('Vault — with keyring, silent setup', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  // This test exercises the OS-keychain path, which requires a running
  // gnome-keyring daemon. It is the most likely to rot silently (nocx-25k9)
  // and must report "skipped" rather than pretending to pass.
  //
  // The CI recipe is:
  //   eval "$(echo -n nocx-ci | gnome-keyring-daemon --daemonize --login)"
  //   echo -n nocx-ci | gnome-keyring-daemon --unlock
  //
  // gnome-keyring is available via `nix-shell -p gnome-keyring dbus`.

  test('silent vault setup with gnome-keyring', async ({ page }) => {
    // ── Check that gnome-keyring is reachable ───────────────────────────
    let hasKeyring = false
    try {
      execSync('gnome-keyring-daemon --version 2>/dev/null', { stdio: 'pipe' })
      execSync(
        'dbus-send --session --dest=org.freedesktop.secrets --type=method_call --print-reply /org/freedesktop/secrets org.freedesktop.DBus.Peer.Ping 2>/dev/null',
        { stdio: 'pipe' },
      )
      hasKeyring = true
    } catch {
      // gnome-keyring or dbus session not available
    }

    if (!hasKeyring) {
      test.skip()
      return
    }

    // ── Start the backend under the existing keyring session ────────────
    const xdg = createXdgDirs()
    const backend = new VaultBackend(DEVHARNESS_BIN, asDisposableRoot(xdg))

    try {
      const ep = await backend.start(FIRST_PORT)
      await bindEndpoint(page, ep)

      await page.goto('/')
      await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

      // Open Settings → Connections → create connection with password.
      await page.keyboard.press('Meta+,')
      await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5000 })
      await page.locator('.ui-settings-section-nav-item[data-section="Connections"]').click()
      await expect(page.locator('.cm-root')).toBeVisible({ timeout: 5000 })
      await page
        .locator('[role="toolbar"]')
        .getByRole('button', { name: '+ New connection' })
        .click()
      await expect(page.getByRole('dialog').filter({ hasText: 'New Connection' })).toBeVisible({
        timeout: 5000,
      })
      await page.getByRole('dialog').getByLabel('Host or connection string').fill('localhost')
      await page.getByRole('dialog').getByRole('button', { name: 'Next', exact: true }).click()
      await expect(page.locator('.cm-form')).toBeAttached({ timeout: 5000 })
      await page.locator('#profile-name').fill('Keyring Test')
      await page.locator('#profile-host').fill('localhost')
      await page.locator('.cm-form').getByRole('tab', { name: 'Authentication' }).click()
      await page.locator('#profile-auth-user').fill('keyring-user')
      await page
        .getByRole('radiogroup', { name: 'Authentication method' })
        .getByRole('radio', { name: 'Password' })
        .click()

      // Same locator as cases 1 and 2. `profile-password-action` is a Field's
      // `for=` — a label target, not an element id — so scoping to it matched
      // nothing and this case timed out looking for a button that was on screen.
      await page.getByRole('button', { name: /Set Password/i }).click()
      const pwInput = page.locator('[role="dialog"] input[type="password"]')
      await expect(pwInput).toBeVisible({ timeout: 3000 })
      await pwInput.fill('keyring-password-789')
      await page.getByRole('button', { name: 'OK' }).click()
      await expect(pwInput).not.toBeVisible({ timeout: 3000 })

      // Click "Create Connection". With a keyring, the vault setup should be
      // silent — no dialog should appear.
      await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

      // Verify NO vault dialog appeared: the connection list shows the profile.
      await expect(
        page.locator('.ui-collection-row').filter({ hasText: 'Keyring Test' }),
      ).toBeVisible({
        timeout: 10_000,
      })

      // Verify no setup/unlock dialog is visible.
      await expect(page.getByRole('dialog').filter({ hasText: 'Set Up Vault' })).not.toBeVisible({
        timeout: 3000,
      })
      await expect(page.getByRole('dialog').filter({ hasText: 'Unlock Vault' })).not.toBeVisible({
        timeout: 3000,
      })
    } finally {
      backend?.stop()
    }
  })
})
