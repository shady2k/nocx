import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import {
  standalone as test,
  expect,
  promptReady,
  clickIntoEditor,
  bindEndpoint,
  openControlPlane,
  endDaemonsUnder,
  VaultBackend,
} from './harness'
import { startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

for (const kind of ['local', 'ssh'] as const) {
  test(`closing a ${kind} tab ends its running process (nocx-wkjdb)`, async ({ page }) => {
    test.setTimeout(180_000)
    const home = { root: mkdtempSync(path.join(tmpdir(), 'nocx-close-session-')) }
    const backend = new VaultBackend(readStand().server, home)
    let fixture: SshdFixture | undefined
    let isolatedHome = ''
    try {
      const endpoint = await backend.start()
      isolatedHome = backend.isolatedHome
      await bindEndpoint(page, endpoint)
      await page.goto('/')
      await promptReady(page)
      if (kind === 'ssh') {
        const remoteHome = mkdtempSync(path.join(tmpdir(), 'nocx-close-remote-'))
        fixture = await startSshd({ home: remoteHome, cwd: remoteHome })
        const sshDir = path.join(isolatedHome, '.ssh')
        mkdirSync(sshDir, { recursive: true, mode: 0o700 })
        writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
        const wire = await openControlPlane(endpoint.port, endpoint.token)
        try {
          await wire.call('profiles.create', {
            type: 'ssh',
            name: 'Close session host',
            options: {
              host: fixture.host,
              port: fixture.port,
              user: fixture.user,
              keyPath: fixture.userKey,
              desiredMode: 'helper',
            },
          })
        } finally {
          wire.close()
        }
        await page.keyboard.press('Control+Shift+P')
        const search = page.locator('.quick-connect__search input')
        await expect(search).toBeVisible()
        await search.fill('Close session host')
        await expect(
          page.locator('.quick-connect__item', { hasText: 'Close session host' }),
        ).toBeVisible()
        await page.keyboard.press('Enter')
        const ask = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
        await expect(ask).toBeVisible({ timeout: 30_000 })
        await ask.getByRole('radio', { name: 'Helper' }).click()
        await ask.getByRole('button', { name: 'Continue' }).click()
        await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 30_000 })
        await promptReady(page)
      }
      // The pid belongs to the program on the execution host. The SSH fixture
      // runs on this machine too, so the kernel is an independent witness in
      // both cases. Split the marker so typed command echo cannot match it.
      await clickIntoEditor(page)
      await page.keyboard.type(`sh -c 'echo NOCXJOB=""$$; exec sleep 100000'`)
      await page.keyboard.press('Enter')
      const screen = page.locator('.pane.active .terminal-live-grid')
      await expect(screen).toContainText(/NOCXJOB=\d+/, { timeout: 30_000 })
      const pid = Number(/NOCXJOB=(\d+)/.exec((await screen.textContent()) ?? '')![1])
      expect(alive(pid)).toBe(true)
      await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(1)
      const tab = page.locator('.nocx-tab[aria-selected="true"]')
      await tab.hover()
      await tab.getByRole('button', { name: 'Close tab', exact: true }).click()
      const confirm = page.getByRole('dialog').filter({ hasText: 'sleep 100000' })
      await expect(confirm).toBeVisible()
      // Cancel leaves the program alive and the tab available for a new close.
      await confirm.getByRole('button', { name: 'Cancel', exact: true }).click()
      expect(alive(pid)).toBe(true)
      await tab.hover()
      await tab.getByRole('button', { name: 'Close tab', exact: true }).click()
      await expect(confirm).toBeVisible()
      await confirm.getByRole('button', { name: 'Close tab', exact: true }).click()
      await expect.poll(() => alive(pid), { timeout: 30_000 }).toBe(false)
    } finally {
      backend.stop()
      await endDaemonsUnder(isolatedHome)
      fixture?.proc.kill()
    }
  })
}

test('unattended-session days survive a coordinator restart (nocx-wkjdb)', async ({ page }) => {
  const home = { root: mkdtempSync(path.join(tmpdir(), 'nocx-session-days-')) }
  const backend = new VaultBackend(readStand().server, home)
  let isolatedHome = ''
  try {
    const endpoint = await backend.start()
    isolatedHome = backend.isolatedHome
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await promptReady(page)
    await page.keyboard.press('Meta+,')
    await page.locator('.ui-grouped-nav__item[data-item="Sessions"] button').click()
    const row = page.locator('.ui-settings-row[data-key="sessions.idleDays"]')
    const days = row.locator('input[type="number"]')
    await expect(days).toHaveValue('3')
    await expect(row).toContainText('newly opened sessions')
    await days.fill('7')
    await days.press('Tab')
    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      await expect
        .poll(async () => {
          const snapshot = (await wire.call('settings.getSnapshot', {})) as {
            values: Record<string, unknown>
          }
          return snapshot.values['sessions.idleDays']
        })
        .toBe(7)
    } finally {
      wire.close()
    }
    await page.keyboard.press('Meta+w')
    await promptReady(page)
    const next = await backend.restart()
    await bindEndpoint(page, next)
    await page.reload()
    await promptReady(page)
    await page.keyboard.press('Meta+,')
    await page.locator('.ui-grouped-nav__item[data-item="Sessions"] button').click()
    await expect(days).toHaveValue('7')
  } finally {
    backend.stop()
    await endDaemonsUnder(isolatedHome)
  }
})
