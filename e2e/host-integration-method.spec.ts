/**
 * e2e: the connection editor shows and changes the HOST's integration method,
 * and a NEW tab to that host opens by it (nocx-oalo1.11.2).
 *
 * # What a person can do that they could not before
 *
 * Open a saved connection and answer the connect-time ask with Raw, then open
 * the connection's editor, see that the machine is at Raw, change it to
 * Script, and open a new tab to the same host that comes up INTEGRATED — the
 * composer and blocks — without being asked again.
 *
 * # Why this is the assertion
 *
 * ADR-727g63tr decision 1 made the method a fact of the HOST, keyed by its
 * host-key fingerprint. A saved connection has no copy of it any more: the
 * editor reads and writes the host record (connections.hostMode /
 * connections.setHostMode), and a change applies to every later open of that
 * machine. This spec watches that whole path through the product's own seams —
 * the ask's dialog, the editor's Delivery mode select, and a second open.
 *
 * # No timing
 *
 * Every wait is on an observable state: the ask's dialog, the live grid's own
 * `live-unstructured` class and its prompt, the backend's answer through
 * connections.hostMode, the composer, and a command's block. Nothing is waited
 * out.
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { appReadyForInput, test, expect, resolveBackend, settingsReady, type Page } from './harness'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const LIVE = '.pane.active .terminal-live-container'
const EDITOR = '.pane.active .nocx-editor-input'
const ASK = 'Choose how nocx connects'
const DELIVERY_MODE = '#desired-mode'

function trustHostKey(fixture: SshdFixture): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
}

async function openFromPalette(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible()
  await search.fill(profileName)
  await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
    timeout: 10_000,
  })
  await page.keyboard.press('Enter')
}

/** The unstructured pane's readiness: the grid is live and the shell has
 *  painted a prompt. A raw open runs a plain login shell, so there is no
 *  composer and no marker — the prompt is the only cue a person (or this spec)
 *  has. */
async function waitForPlainPrompt(page: Page): Promise<void> {
  await expect
    .poll(
      async () =>
        page.evaluate(
          () =>
            document
              .querySelector('.pane.active .terminal-live-container')
              ?.classList.contains('live-unstructured') ?? false,
        ),
      { timeout: 60_000 },
    )
    .toBe(true)
  await expect
    .poll(
      async () =>
        page.evaluate(() =>
          Array.from(document.querySelectorAll('.pane.active .term-grid-row')).some((row) =>
            /[$#]\s*$/.test((row.textContent ?? '').trimEnd()),
          ),
        ),
      { timeout: 60_000 },
    )
    .toBe(true)
}

test('the editor changes the host method and the next tab opens by it', async ({ page }) => {
  test.setTimeout(240_000)
  const remoteHome = path.join(path.dirname(readStand().home), `host-mode-remote-${Date.now()}`)
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
  let profileId: string | null = null
  try {
    trustHostKey(fixture)
    await page.goto('/')
    await appReadyForInput(page)
    await expect(page.locator(TAB)).toHaveCount(1)

    const endpoint = await resolveBackend(page)
    const profileName = `e2e-host-mode-${Date.now()}`
    const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
      },
    })
    profileId = created.id

    // ── 1. The host nobody has answered: the ask decides ────────────────
    await openFromPalette(page, profileName)
    const ask = page.getByRole('dialog').filter({ hasText: ASK })
    await expect(ask).toBeVisible({ timeout: 30_000 })
    await ask.getByRole('radio', { name: 'Raw' }).click()
    await ask.getByRole('button', { name: 'Continue' }).click()
    await expect(ask).not.toBeVisible()
    await expect(page.locator(TAB)).toHaveCount(2)

    // Raw means what it says: a plain login shell, the grid takes the keys,
    // and a command is NOT a block.
    await waitForPlainPrompt(page)
    const grid = page.locator(LIVE)
    await grid.click()
    await page.keyboard.type('echo opened-by-raw')
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active')).toContainText('opened-by-raw', { timeout: 30_000 })
    await expect(page.locator('.pane.active .cmd-block')).toHaveCount(0)

    // The answer was recorded for the HOST, so the editor reads it back.
    await expect
      .poll(
        async () =>
          (await rpc<{ mode: string }>(page, endpoint, 'connections.hostMode', { profileId }))
            ?.mode,
        { timeout: 15_000 },
      )
      .toBe('raw')

    await page.locator('.nocx-tab[aria-selected="true"] [aria-label="Close tab"]').click()
    await expect(page.locator(TAB)).toHaveCount(1)

    // ── 2. The editor shows the host's method and changes it ────────────
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator('.ui-grouped-nav__item[data-item="connections"]').click()
    await expect(page.locator('.cm-root')).toBeVisible({ timeout: 10_000 })
    await page.locator(`[aria-label="Edit ${profileName}"]`).click()
    const editor = page.getByRole('dialog').filter({ hasText: 'Edit Connection' })
    await expect(editor).toBeVisible()
    await editor.getByRole('tab', { name: 'Advanced' }).click()

    const mode = page.locator(DELIVERY_MODE)
    await expect(mode).toHaveValue('raw', { timeout: 10_000 })
    await mode.selectOption('script')

    // The select writes the host record at once. The BACKEND's answer is the
    // observable: the editor shows the value it was told to show, and only the
    // record says the open that follows will use it.
    await expect
      .poll(
        async () =>
          (await rpc<{ mode: string }>(page, endpoint, 'connections.hostMode', { profileId }))
            ?.mode,
        { timeout: 15_000 },
      )
      .toBe('script')
    await editor.getByRole('button', { name: 'Save Connection', exact: true }).click()
    await expect(editor).not.toBeVisible()

    // ── 3. A NEW tab to that host opens by the chosen method ────────────
    await openFromPalette(page, profileName)
    // No ask: the host has answered, and script is an answer (ADR-0033). The
    // composer coming up IS the integrated state this open reached.
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 60_000 })
    await expect(page.getByRole('dialog').filter({ hasText: ASK })).toHaveCount(0)

    const composer = page.locator(EDITOR)
    await composer.click()
    await composer.pressSequentially('echo opened-by-script')
    await composer.press('Enter')
    const block = page.locator('.pane.active .cmd-block', { hasText: 'opened-by-script' })
    await expect(block.first()).toBeVisible({ timeout: 30_000 })
  } finally {
    try {
      const endpoint = await resolveBackend(page)
      if (profileId !== null) await rpc(page, endpoint, 'profiles.delete', { id: profileId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})
