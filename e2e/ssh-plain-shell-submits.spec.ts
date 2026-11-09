/**
 * e2e: a plain sh with no shell integration takes every composer submit, not
 * just the first (nocx-evj4b.1.6).
 *
 * # The report
 *
 * Found by the nocx-evj4b.1.2 worker on this stand: an ssh pane whose far
 * shell is a non-integrated sh accepted the first composer submit and then
 * stopped taking composer submits. Plain ssh sessions without integration are
 * a supported case (owner), so the commands after the first must reach the
 * shell.
 *
 * # What this asserts, and where it reads it
 *
 * The pane opens on a host whose account shell is `sh` (dash) with
 * `desiredMode: 'script'`: the launcher runs, stage-1 finds no bash/zsh to
 * integrate, and the session settles `conventional` — there is no prompt mark
 * and there never will be one. During that startup interval the composer is
 * the person's input surface; FIVE submits are typed into it. Every one of
 * them must reach the far program as its own line: the fixture's own record
 * of what the pty accepted (`-input-log`) is read after the bootstrap frames,
 * and it must be exactly the five typed lines, each with its own Enter.
 *
 * The defect this guards: the composer held the draft across the whole
 * startup interval, swallowed every Enter after the first as a duplicate of
 * the submission in flight, and then released the concatenation as ONE line.
 * So a reading of the log with fewer than five Enters — or with two lines
 * joined — is the bug, not a slow machine.
 *
 * # The startup interval is HELD, not raced
 *
 * The fixture is started with `-shell-hold-until`: it does not run the pane's
 * shell until the spec creates that file. While it is held the far side says
 * nothing, so no refusal can settle the session's integration axis early and
 * the composer cannot disappear under the five submits — the interval is the
 * fixture's to end. The spec submits, asserts the interval is still open, and
 * only then releases the shell; the refusal that follows is what settles the
 * session conventional and releases the held lines.
 *
 * # Nothing else is waited out
 *
 * The waits are on product state — the composer the startup interval shows,
 * the empty draft an accepted Enter leaves behind, the settled-conventional
 * notice — and on the fixture's own announcement of its input log.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, appReadyForInput, resolveBackend, test } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const PANE = '.pane.active'
const EDITOR = `${PANE} .nocx-editor-input`
/** The settled-conventional card: the integration check is over and the held
 *  lines are released. */
const NOTICE = `${PANE} .nocx-integration-notice`
/** How many submits the report names. */
const SUBMITS = 5

/**
 * Where the launcher's own bootstrap frames end.
 *
 * The frame protocol (internal/shellintegration/carrier.go) is a fixed 17-byte
 * header — `NOCX1`, space, sequence digit, space, eight columns of right-
 * aligned length, newline — followed by exactly that many bytes. The open
 * sends frame 1 (stage-1) and then frame 2 (the capability or its refusal),
 * and the far side reads both off this same pty before the plain shell runs.
 * Anything after them is what a person's submits put there.
 */
function bootstrapFramesEnd(bytes: Buffer): number {
  let offset = 0
  for (const seq of [1, 2]) {
    const header = bytes.subarray(offset, offset + 17).toString('latin1')
    const m = /^NOCX1 (\d) (.{8})\n$/.exec(header)
    expect(
      m,
      `the open's bootstrap frame ${seq} header read ${JSON.stringify(header)} at byte ${offset}, ` +
        "which is not the frame protocol's 17-byte header",
    ).not.toBeNull()
    const length = Number(m![2].trim())
    expect(length, `bootstrap frame ${seq} declares a length outside 1..32768`).toBeGreaterThan(0)
    expect(length, `bootstrap frame ${seq} declares a length outside 1..32768`).toBeLessThanOrEqual(
      32768,
    )
    offset += 17 + length
  }
  expect(offset, 'the open wrote fewer bytes than its two bootstrap frames').toBeLessThanOrEqual(
    bytes.length,
  )
  return offset
}

/** The bytes the far program read after the open's own bootstrap frames. */
function submitsIn(logPath: string): string[] {
  const all = readFileSync(logPath)
  return all.subarray(bootstrapFramesEnd(all)).toString('utf8').split('\r')
}

test('an ssh pane on a non-integrated sh runs five composer submits, each its own line', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const logRoot = mkdtempSync(path.join(tmpdir(), 'nocx-plain-sh-log-'))
  const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-plain-sh-host-'))
  const remoteHome = path.join(hostRoot, 'home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  // The remote home is NOT the backend's: an sshd sharing it would spawn a
  // shell that sourced the local rc hooks and come up integrated, and this
  // spec's whole premise is a shell that never integrates (shell-mode.spec.ts
  // records the same hazard).
  // THE HOLD: the pane's shell does not start until this file exists, so the
  // startup interval below lasts exactly as long as this spec needs and never
  // races a far-side refusal (review of nocx-evj4b.1.6). The file's absence is
  // the hold; creating it is the release.
  const holdPath = path.join(hostRoot, 'release-shell')
  const fixture: SshdFixture = await startSshd({
    home: remoteHome,
    cwd: remoteHome,
    args: ['-input-log', logRoot, '-shell', 'sh', '-shell-hold-until', holdPath],
  })
  let profileId: string | null = null
  try {
    // The backend's ssh client must accept the fixture's freshly minted host
    // key; the file is REPLACED, never appended.
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

    await page.goto('/')
    await appReadyForInput(page)
    const info = await resolveBackend(page)
    // `script` names the launcher explicitly: the open runs it, stage-1 finds
    // no shell it can integrate and execs the account shell plainly. This is
    // the supported case the report is about, and it is never the
    // connect-time helper ask (ADR-0069).
    const profileName = `e2e-plain-sh-${Date.now()}`
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

    // The startup composer coming up is the product's statement that the
    // shell has not proved itself yet — precisely the interval this spec is
    // about (the pane hides it when the session settles). The shell is held,
    // so this interval does not end until the release below.
    const editor = page.locator(EDITOR)
    // The waiting card the startup interval shows: while it is up the shell has
    // not proved itself, which is the state these submits are made in.
    const checking = page.getByRole('status', { name: 'Checking this shell…' })
    await expect(editor).toBeVisible({ timeout: 180_000 })
    await expect(checking).toBeVisible({ timeout: 30_000 })

    const lines = Array.from({ length: SUBMITS }, (_, i) => `echo plain-${i}`)
    for (const line of lines) {
      await editor.click()
      await editor.pressSequentially(line)
      await expect(editor).toHaveText(line)
      await editor.press('Enter')
      // An accepted Enter leaves an empty draft: the line is the host's now,
      // and the next Enter must be a new command rather than more text
      // appended to this one.
      await expect(editor).toHaveText('')
      // The interval was open for this submit, and the check we are about to
      // end says so: if the product had settled it mid-way, this submit would
      // have gone nowhere and the empty-draft assertion above would have said
      // which one.
      await expect(checking).toBeVisible({ timeout: 0 })
    }

    // Release the shell: the far side refuses integration now, the session
    // settles conventional, and every held line is released in order.
    writeFileSync(holdPath, '')
    // The far program's own record of what arrived, announced by the fixture
    // as soon as the shell it was holding starts.
    await expect
      .poll(() => fixture.lines().find((line) => line.startsWith('INPUTLOG=')) ?? '', {
        timeout: 60_000,
        message: 'the fixture never announced an input log for the ssh pane',
      })
      .not.toBe('')
    const logPath = fixture
      .lines()
      .find((line) => line.startsWith('INPUTLOG='))!
      .slice('INPUTLOG='.length)
      .split(' ')[0]
    await expect(page.locator(NOTICE)).toBeVisible({ timeout: 60_000 })
    await expect
      .poll(() => submitsIn(logPath), {
        timeout: 60_000,
        message:
          `the plain sh received ${JSON.stringify(submitsIn(logPath).join('|'))} for ` +
          `${JSON.stringify(lines)}, want each line and its own Enter`,
      })
      .toEqual([...lines, ''])
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
