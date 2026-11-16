/** An older installer gate must not own a newly integrated remote pane. */
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { once } from 'node:events'
import path from 'node:path'

import { appReadyForInput, test, expect, resolveBackend, promptReady } from './harness'
import { hostKeyFingerprint, recordHostMethod, startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'

for (const shell of ['bash', 'zsh']) {
  test(`remote ${shell} retires an installer-era loader and completes its first command`, async ({
    page,
  }) => {
    test.setTimeout(180_000)
    const home = mkdtempSync(path.join(tmpdir(), 'nocx-legacy-integration-'))
    const fixture = await startSshd({
      home,
      cwd: home,
      args: ['-legacy-integration', '-shell', shell],
    })
    let profile: string | undefined
    let shellExited = false
    let endpoint: { port: number; token: string } | undefined
    try {
      // The fixture calls the production installer for the gates, then derives
      // the previous acceptance rule from the installed script. These markers
      // guard the setup itself: a changed layout must fail, not test nothing.
      expect(fixture.lines()).toContain('LEGACY_INTEGRATION=43')
      const current = fixture.lines().find((line) => line.startsWith('SCRIPT_VERSION='))
      expect(current).toBeDefined()
      const version = current!.slice('SCRIPT_VERSION='.length)
      const legacyTarget = path.join(home, '.nocx', `shell-integration.${shell}`)
      expect(readFileSync(legacyTarget, 'utf8')).toContain('__nocx_script_version=43\n')
      const rc = path.join(home, shell === 'bash' ? '.bashrc' : '.zshrc')
      const startupBefore = readFileSync(rc)

      const sshDir = path.join(readStand().home, '.ssh')
      mkdirSync(sshDir, { recursive: true, mode: 0o700 })
      writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
      await page.goto('/')
      await appReadyForInput(page)
      endpoint = await resolveBackend(page)
      const name = `Previous integration ${shell}`
      const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
        type: 'ssh',
        name,
        options: {
          host: fixture.host,
          port: fixture.port,
          user: fixture.user,
          keyPath: fixture.userKey,
        },
      })
      profile = created.id
      await recordHostMethod(page, endpoint, hostKeyFingerprint(fixture.knownHosts), 'script')
      await page.keyboard.press('Control+Shift+P')
      const search = page.locator('.quick-connect__search input')
      await expect(search).toBeVisible()
      await search.fill(name)
      await expect(page.locator('.quick-connect__item', { hasText: name })).toBeVisible()
      await page.keyboard.press('Enter')
      await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 60_000 })
      await promptReady(page)

      const editor = page.locator('.pane.active .nocx-editor-input')
      await editor.click()
      // This is the FIRST command. Report the sourced script's own marker,
      // not the launcher's generation environment or the manifest's claim.
      // The output marker does not occur in the submitted command header.
      await editor.pressSequentially(
        `printf 'CURRENT-%s:%s\\n' "$__nocx_script_version" "$((21*2))"`,
      )
      await editor.press('Enter')
      const block = page
        .locator('.pane.active .cmd-block')
        .filter({ hasText: `CURRENT-${version}:42` })
      await expect(block).toBeVisible({ timeout: 60_000 })
      await expect(block).not.toHaveClass(/cmd-block-running/, { timeout: 60_000 })
      await promptReady(page)
      expect(readFileSync(rc)).toEqual(startupBefore)
      const manifest = JSON.parse(
        readFileSync(path.join(home, '.nocx', 'manifest.json'), 'utf8'),
      ) as { generation: string }
      const stub = readFileSync(legacyTarget, 'utf8')
      expect(stub).toContain(`neutralised by generation ${manifest.generation}`)
      expect(stub).not.toContain('__nocx_script_version=43')
      const exitsBefore = fixture.lines().filter((line) => line === 'PTY_EXIT').length
      await editor.click()
      await editor.pressSequentially('exit')
      await editor.press('Enter')
      await expect
        .poll(() => fixture.lines().filter((line) => line === 'PTY_EXIT').length)
        .toBeGreaterThan(exitsBefore)
      shellExited = true
    } finally {
      try {
        if (profile && endpoint) await rpc(page, endpoint, 'profiles.delete', { id: profile })
      } finally {
        if (fixture.proc.exitCode === null && fixture.proc.signalCode === null) {
          const stopped = once(fixture.proc, 'exit')
          fixture.proc.kill('SIGKILL')
          await stopped
        }
        // Never delete a directory its live shell may still be writing.
        // Preserve it on failure as disposable diagnostic state instead.
        if (shellExited) rmSync(home, { recursive: true, force: true })
      }
    }
  })
}
