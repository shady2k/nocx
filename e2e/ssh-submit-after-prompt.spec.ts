/**
 * e2e: a submit queued during an ssh open reaches the far shell only AFTER its
 * prompt — never while the startup chain still owns the terminal
 * (nocx-evj4b.1.1).
 *
 * # The finding
 *
 * The backend's input quarantine opens when the launch carrier prints its
 * outcome token, and the carrier prints it BEFORE it execs the shell. Until the
 * shell's first prompt the far pty is still in its startup state —
 * ICANON|ECHO|ISIG|IXON — and the shell's own startup files can read stdin. A
 * submit released in that interval can be cut by a startup read, or edited by
 * the line discipline (control bytes eaten, \r turned into \n, MAX_CANON
 * splits).
 *
 * # What this asserts, and how the far side says it
 *
 * The remote home's `.bashrc` (the shell is integrated, so the launcher's hooks
 * are appended to it) first waits for a release file the spec creates, then
 * READS ONE LINE from the terminal with a two-second bound, and prints what it
 * got as `EARLY:<...>`. Then the shell reaches its prompt.
 *
 * The spec types its line into the startup composer while `.bashrc` is blocked
 * and only then releases it. From that moment to the prompt there is nothing
 * on the far side that writes stdin, so the shell's read is the only consumer
 * of the terminal:
 *
 *   - the read sees NOTHING (`EARLY:<none>`), and the line runs afterwards, as
 *     a command of its own, when the prompt is there;
 *   - if the product wrote it early, the read consumes it and the marker names
 *     it — the bytes went to the startup chain, which is the defect.
 *
 * The fixture's own input log says the same thing from the other side: after
 * the open's bootstrap frames it must be exactly the typed line and its Enter,
 * and nothing else.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, appReadyForInput, promptReady, resolveBackend, test } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const PANE = '.pane.active'
const EDITOR = `${PANE} .nocx-editor-input`
const LINE = 'echo after-the-prompt'

/** Where the open's bootstrap frames end (the frame protocol:
 *  internal/shellintegration/carrier.go — `NOCX1 <seq> <len>` and its body). */
function bootstrapFramesEnd(bytes: Buffer): number | null {
  let offset = 0
  for (const seq of [1, 2]) {
    const header = bytes.subarray(offset, offset + 17).toString('latin1')
    const m = /^NOCX1 (\d) (.{8})\n$/.exec(header)
    if (m === null) return null
    const length = Number(m[2].trim())
    if (!Number.isFinite(length) || length <= 0 || length > 32768) {
      throw new Error(`bootstrap frame ${seq} declares a length outside 1..32768: ${length}`)
    }
    offset += 17 + length
  }
  return offset <= bytes.length ? offset : null
}

test('a submit queued during an ssh open waits for the shell’s prompt (nocx-evj4b.1.1)', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const logRoot = mkdtempSync(path.join(tmpdir(), 'nocx-after-prompt-log-'))
  const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-after-prompt-host-'))
  const remoteHome = path.join(hostRoot, 'home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const release = path.join(remoteHome, '.startup-release')
  // The startup file reads the terminal. It is sourced by the LOGIN shell the
  // launcher execs, before the first prompt, with the pty still cooked.
  writeFileSync(
    path.join(remoteHome, '.bashrc'),
    [
      `while [ ! -e "${release}" ]; do sleep 0.05; done`,
      // Two seconds of "whoever writes stdin now is writing to the startup
      // chain"; the spec's line must NOT be among it.
      `if IFS= read -r -t 2 early; then printf 'EARLY:<%s>\\n' "$early"; else printf 'EARLY:<none>\\n'; fi`,
      '',
    ].join('\n'),
  )
  const fixture: SshdFixture = await startSshd({
    home: remoteHome,
    cwd: remoteHome,
    args: ['-input-log', logRoot, '-shell', 'bash'],
  })
  let profileId: string | null = null
  try {
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

    await page.goto('/')
    await appReadyForInput(page)
    const info = await resolveBackend(page)
    const profileName = `e2e-after-prompt-${Date.now()}`
    const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'e2e',
        keyPath: fixture.userKey,
        desiredMode: 'script',
      },
    })
    profileId = created.id

    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible({ timeout: 30_000 })
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 30_000,
    })
    await page.keyboard.press('Enter')

    // The startup composer is on screen while the shell is proving itself.
    const editor = page.locator(EDITOR)
    await expect(editor).toBeVisible({ timeout: 180_000 })
    await editor.click()
    await editor.pressSequentially(LINE)
    await expect(editor).toHaveText(LINE)
    await editor.press('Enter')
    // Taken by the host, not sent: the draft empties.
    await expect(editor).toHaveText('')

    // NOW the startup chain may run — and it reads the terminal.
    writeFileSync(release, '')

    const pane = page.locator(PANE)
    // The far side's own account: the startup read saw nothing. If the line had
    // been written into the cooked tty before the prompt, this would name it.
    await expect(pane).toContainText('EARLY:<none>', { timeout: 120_000 })

    // The prompt arrives, and only then does the queued line run.
    await promptReady(page)
    const block = pane.locator('.cmd-block', { hasText: 'after-the-prompt' })
    await expect(block.first()).toBeVisible({ timeout: 60_000 })

    // And the far side received exactly the typed line and its Enter, after the
    // bootstrap frames.
    const logLine = fixture.lines().find((line) => line.startsWith('INPUTLOG='))
    expect(logLine, 'the fixture never announced an input log').toBeDefined()
    const logPath = logLine!.slice('INPUTLOG='.length).split(' ')[0]
    await expect
      .poll(
        () => {
          const all = readFileSync(logPath)
          const end = bootstrapFramesEnd(all)
          if (end === null) return null
          return all.subarray(end).toString('utf8')
        },
        { timeout: 60_000, intervals: [200] },
      )
      .toBe(`${LINE}\r`)
  } finally {
    try {
      if (profileId !== null) {
        const info = await resolveBackend(page)
        await rpc(page, info, 'profiles.delete', { id: profileId })
      }
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})

/**
 * THE CONVENTIONAL CASE (owner, 2026-11-10; nocx-evj4b.1.8).
 *
 * A plain sh on the far side never announces a prompt mark, so the startup
 * queue's promise cannot be kept. A line submitted while the shell is still
 * starting stays the person's: it comes back to the composer after the startup
 * settles, and NOTHING is written into the pty — the far side is still cooked
 * and its startup chain can read stdin. Only the person's own submit at the
 * prompt they can see reaches it.
 */
test('a submit during a plain sh startup stays in the composer until the person submits at the prompt', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const logRoot = mkdtempSync(path.join(tmpdir(), 'nocx-conventional-log-'))
  const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-conventional-host-'))
  const remoteHome = path.join(hostRoot, 'home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  // The prompt is HELD at the shell: everything before it runs at full speed,
  // and the spec decides when the shell becomes a running /bin/sh.
  const holdPath = path.join(hostRoot, 'release-shell')
  const holdBin = path.join(hostRoot, 'bin', 'sh')
  mkdirSync(path.dirname(holdBin), { recursive: true })
  writeFileSync(
    holdBin,
    `#!/bin/sh\nwhile [ ! -e '${holdPath}' ]; do sleep 0.01; done\nexec /bin/sh -l\n`,
    { mode: 0o755 },
  )
  const fixture: SshdFixture = await startSshd({
    home: remoteHome,
    cwd: remoteHome,
    args: ['-input-log', logRoot, '-shell', holdBin],
  })
  let profileId: string | null = null
  try {
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

    await page.goto('/')
    await appReadyForInput(page)
    const info = await resolveBackend(page)
    const profileName = `e2e-conventional-${Date.now()}`
    const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'nocx-e2e-long-bound',
        keyPath: fixture.userKey,
        desiredMode: 'script',
      },
    })
    profileId = created.id

    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible({ timeout: 30_000 })
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 30_000,
    })
    await page.keyboard.press('Enter')

    const editor = page.locator(EDITOR)
    const checking = page.getByRole('status', { name: 'Checking this shell…' })
    await expect(editor).toBeVisible({ timeout: 180_000 })
    await expect(checking).toBeVisible({ timeout: 30_000 })
    await editor.click()
    await editor.pressSequentially(LINE)
    await expect(editor).toHaveText(LINE)
    await editor.press('Enter')

    // The shell may start; the queue must not write the line.
    writeFileSync(holdPath, '')

    const logLine = () => fixture.lines().find((line) => line.startsWith('INPUTLOG=')) ?? ''
    await expect.poll(logLine, { timeout: 60_000 }).not.toBe('')
    const logPath = logLine().slice('INPUTLOG='.length).split(' ')[0]
    // The settled-conventional card: the startup is over.
    await expect(page.locator(`${PANE} .nocx-integration-notice`)).toBeVisible({ timeout: 60_000 })

    // THE LINE IS STILL THE PERSON'S: no prompt mark is coming, so the queue
    // returned it to the composer, and the far side read nothing.
    await expect(editor).toHaveText(LINE)
    expect(bootstrapFramesEnd(readFileSync(logPath))).not.toBeNull()
    const framesEnd = bootstrapFramesEnd(readFileSync(logPath))!
    expect(
      readFileSync(logPath).subarray(framesEnd).toString('utf8'),
      'the line reached the far pty before the person submitted after the prompt',
    ).toBe('')

    // The restored draft owns the keyboard even after a click on live output.
    // Keep typing as a person would: the suffix must extend that draft, not
    // bypass it into the shell or replace it during a late restoration.
    const suffix = ' && echo continued'
    await page.locator(`${PANE} .terminal-live-container`).click()
    await page.keyboard.type(suffix)
    await expect(editor).toHaveText(`${LINE}${suffix}`)
    expect(readFileSync(logPath).subarray(framesEnd).toString('utf8')).toBe('')
    await page.keyboard.press('Enter')
    await expect
      .poll(() => readFileSync(logPath).subarray(framesEnd).toString('utf8'), {
        timeout: 60_000,
        intervals: [200],
      })
      .toBe(`${LINE}${suffix}\r`)

    // Once that draft is submitted, the conventional shell owns raw input.
    await expect(editor).not.toBeVisible()
    const rawLine = 'echo raw-after-draft'
    await page.locator(`${PANE} .terminal-live-container`).click()
    await page.keyboard.type(rawLine)
    await page.keyboard.press('Enter')
    await expect
      .poll(() => readFileSync(logPath).subarray(framesEnd).toString('utf8'), {
        timeout: 60_000,
        intervals: [200],
      })
      .toBe(`${LINE}${suffix}\r${rawLine}\r`)
  } finally {
    try {
      if (profileId !== null) {
        const info = await resolveBackend(page)
        await rpc(page, info, 'profiles.delete', { id: profileId })
      }
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})
