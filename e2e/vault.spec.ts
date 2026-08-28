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
import { VaultBackend, bindEndpoint, settingsReady, type DisposableRoot } from './harness'
import { readStand } from './stand'
import { addSecretFromLock, pressLock } from './secret-field'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

// Two distinct ports so restart never conflicts with the first instance's
// TIME_WAIT. Both are outside the ranges used by `wails dev` (34115), the
// e2e suite default (9876), `dev-web.sh` (9880/5180), and `npm run dev` (5173).

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

// ── Case 1: No keyring — full round trip ──────────────────────────────────

test.describe('Vault — no keyring, full round trip', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // `true` = no Secret Service for this backend, regardless of the session
    // the suite runs in. These two cases are ABOUT the passphrase path.
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('saves password, sets up vault, persists across restart, unlocks with passphrase, connects', async ({
    page,
  }) => {
    // ── Phase 1: first backend ──────────────────────────────────────────
    const ep = await backend.start()
    await bindEndpoint(page, ep)

    await page.goto('/')
    // Wait for the app to load and show the initial tab.
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings via keyboard shortcut.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)

    // Click "Connections" in the left rail.
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
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

    // ── Phase 2: the lock, and the vault setup it raises ────────────────
    // There is no "Set Password" button any more (nocx-3o0ed.4): the password
    // is ONE field, and the door onto the vault is the lock on it. Pressing
    // the lock is an explicit request (ui/secret-picker.ts), so an
    // uninitialized vault answers with the real setup surface rather than an
    // offer row — which is why the SETUP now comes before the password
    // dialog, and not after it as it did when "Set Password" opened the
    // PasswordEditor first and the mint discovered the missing vault.
    //
    // Identified by what it CONTAINS, not by its text. The connection form is
    // itself a role="dialog" and the setup sheet opens inside it, so both
    // `hasText` and `has:` match the ancestor as well as the sheet — a
    // descendant's text and contents are the ancestor's too. The overlay is
    // the sheet's own container and nothing wraps it.
    const pwField = page.locator('#profile-auth-password')
    await expect(pwField).toBeVisible({ timeout: 3000 })
    await pressLock(pwField)

    const setupDialog = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-setup-passphrase') })
    await expect(setupDialog).toBeVisible({ timeout: 10_000 })

    // Enter passphrase.
    await page.locator('#vault-setup-passphrase').fill('my-master-passphrase-42')
    await page.locator('#vault-setup-confirm').fill('my-master-passphrase-42')

    // Click "Set Up".
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()

    // Wait for the recovery code to appear — title changes to "Recovery Code".
    // The recovery step is asserted through the code block itself, not through
    // a "dialog named Recovery Code". By this point the setup overlay has
    // closed and the code is rendered in the form behind it, so an overlay
    // scope finds nothing and a role+text scope matches every ancestor that
    // contains the words. The code block is the thing under test.
    const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    await expect(codeBlock).toBeVisible({ timeout: 10_000 })

    // Capture the recovery code from the CodeBlock.
    const code = await codeBlock.textContent()
    expect(code).not.toBeNull()
    expect((code ?? '').length).toBeGreaterThan(10)

    // Click "Done" to close setup. The vault is open; nothing is minted yet.
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // ── Phase 2b: the password itself ───────────────────────────────────
    // The setup sheet took the surface, so the panel closed with it and the
    // person presses the lock again — this time onto an OPEN vault, which
    // answers with its list. The vault is empty, so the row left to take is
    // the create row, and on a connection's password field a new secret IS a
    // password: it raises the same generator dialog "Set Password" used to,
    // and what it makes is minted and bound by the same call.
    await addSecretFromLock(page, pwField)

    // The PasswordEditor's OWN field, by id. A bare
    // `[role="dialog"] input[type="password"]` used to be unique and stopped
    // being so: the vault setup sheet holds two password fields of its own,
    // so a bare selector resolved to those instead — and on webkit, which
    // gets there faster, it resolved to both and failed strict mode.
    const pwInput = page.locator('#password-value')
    await expect(pwInput).toBeVisible({ timeout: 3000 })
    await pwInput.fill('test-password-123')
    // Click the dialog's primary action button to confirm password.
    await page.getByRole('button', { name: 'OK' }).click()
    // Wait for the PasswordEditor to close.
    await expect(pwInput).not.toBeVisible({ timeout: 3000 })

    // The mint bound the row: the field holds the reference now, which is the
    // binding this phase exists to make and the state Phase 3 reloads. This
    // assertion is new — the old flow had nothing on screen to check it by,
    // and read the binding only from the restart that followed.
    await expect(pwField).toHaveValue(/^\{\{secret:.+\}\}$/, { timeout: 10_000 })

    // Back in the connection form, which never closed: save it.
    await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

    // Verify the profile appears in the connection list (evidence the save
    // went through).
    await expect(page.locator('.ui-collection-row').filter({ hasText: 'Vault Test' })).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 3: restart backend ────────────────────────────────────────
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()

    // Wait for app to reinitialize.
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Re-open Settings and navigate back to Connections.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
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
    // Identified by the field it holds, not by a title. The sheet reads
    // "Unlock the vault to open this connection" — it names the reason it is
    // asking, which is the point of it — so a filter on the words "Unlock
    // Vault" matched nothing and the test blamed the dialog for not appearing.
    const unlockDialog = unlockSheet(page)
    await expect(unlockDialog).toBeVisible({ timeout: 10_000 })

    // ── Phase 5: unlock with passphrase ─────────────────────────────────
    await page.getByRole('dialog').getByRole('button', { name: 'Passphrase', exact: true }).click()
    await page.locator('#vault-unlock-passphrase').fill('my-master-passphrase-42')
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    // Dialog closes after successful unseal.
    await expect(unlockDialog).not.toBeVisible({ timeout: 10_000 })

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
    backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg), true)
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('unseals with recovery code after restart', async ({ page }) => {
    // ── Phase 1: first backend ──────────────────────────────────────────
    const ep = await backend.start()
    await bindEndpoint(page, ep)

    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Open Settings, Connections, create profile with password (same as case 1).
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
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

    // The lock is the door onto the vault (nocx-3o0ed.4), and pressing it is
    // an explicit request — so an uninitialized vault answers with the setup
    // sheet FIRST, and the password dialog comes after it. Case 1 says the
    // same thing at length.
    const pwField = page.locator('#profile-auth-password')
    await expect(pwField).toBeVisible({ timeout: 3000 })
    await pressLock(pwField)

    const setupDialog = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-setup-passphrase') })
    await expect(setupDialog).toBeVisible({ timeout: 10_000 })
    await page.locator('#vault-setup-passphrase').fill('recovery-passphrase-99')
    await page.locator('#vault-setup-confirm').fill('recovery-passphrase-99')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()

    // Capture recovery code.
    // The recovery step is asserted through the code block itself, not through
    // a "dialog named Recovery Code". By this point the setup overlay has
    // closed and the code is rendered in the form behind it, so an overlay
    // scope finds nothing and a role+text scope matches every ancestor that
    // contains the words. The code block is the thing under test.
    const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    await expect(codeBlock).toBeVisible({ timeout: 10_000 })
    const code = await codeBlock.textContent()
    expect(code).not.toBeNull()
    expect((code ?? '').length).toBeGreaterThan(10)
    const recoveryCode = code ?? ''

    // Click "Done". The vault is open; the form is still open behind it.
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()

    // Now the password: the lock again, onto an open and empty vault, whose
    // create row raises the generator and whose result is minted and bound.
    await addSecretFromLock(page, pwField)
    const pwInput = page.locator('#password-value')
    await expect(pwInput).toBeVisible({ timeout: 3000 })
    await pwInput.fill('test-password-456')
    await page.getByRole('button', { name: 'OK' }).click()
    await expect(pwInput).not.toBeVisible({ timeout: 3000 })
    await expect(pwField).toHaveValue(/^\{\{secret:.+\}\}$/, { timeout: 10_000 })

    // Save the connection.
    await page.getByRole('button', { name: 'Create Connection', exact: true }).click()

    // Verify the profile appears in the connection list.
    await expect(
      page.locator('.ui-collection-row').filter({ hasText: 'Recovery Test' }),
    ).toBeVisible({
      timeout: 10_000,
    })

    // ── Phase 2: restart ────────────────────────────────────────────────
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

    // Navigate back to Settings → Connections.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
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

    // Identified by the field it holds, not by a title. The sheet reads
    // "Unlock the vault to open this connection" — it names the reason it is
    // asking, which is the point of it — so a filter on the words "Unlock
    // Vault" matched nothing and the test blamed the dialog for not appearing.
    const unlockDialog = unlockSheet(page)
    await expect(unlockDialog).toBeVisible({ timeout: 10_000 })

    // ── Phase 3: unlock with recovery code ──────────────────────────────
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Recovery code', exact: true })
      .click()
    await page.locator('#vault-unlock-recovery').fill(recoveryCode)
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()

    await expect(unlockDialog).not.toBeVisible({ timeout: 10_000 })

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
    const backend = new VaultBackend(devharnessBin(), asDisposableRoot(xdg))

    try {
      const ep = await backend.start()
      await bindEndpoint(page, ep)

      await page.goto('/')
      await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })

      // Open Settings → Connections → create connection with password.
      await page.keyboard.press('Meta+,')
      await settingsReady(page)
      await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
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

      // THE SILENT PATH, and it is the whole point of this case. The lock is
      // an explicit request onto an uninitialized vault, so it sets the vault
      // up — but a machine whose OS key can carry it is set up WITHOUT a
      // sheet, and the panel stays where the person is standing and lists
      // what the vault now holds (createVaultSecretSource.requestSetup).
      // Cases 1 and 2 get the sheet because they run with no Secret Service;
      // this one must not, and that difference is what is asserted below.
      //
      // So there is exactly one dialog in this flow, and it is the password
      // generator behind the panel's create row — the same one "Set Password"
      // used to raise directly (nocx-3o0ed.4).
      const pwField = page.locator('#profile-auth-password')
      await expect(pwField).toBeVisible({ timeout: 3000 })
      await addSecretFromLock(page, pwField)
      // The create row was reached, so the vault is already open — and the
      // sheet cases 1 and 2 meet at exactly this moment did not appear.
      await expect(
        page.locator('.ui-prompt-overlay').filter({ has: page.locator('#vault-setup-passphrase') }),
      ).not.toBeVisible()
      const pwInput = page.locator('#password-value')
      await expect(pwInput).toBeVisible({ timeout: 3000 })
      await pwInput.fill('keyring-password-789')
      await page.getByRole('button', { name: 'OK' }).click()
      await expect(pwInput).not.toBeVisible({ timeout: 3000 })
      await expect(pwField).toHaveValue(/^\{\{secret:.+\}\}$/, { timeout: 10_000 })

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
