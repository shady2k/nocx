/**
 * e2e: an ssh pane on a non-integrated sh writes none of its held submits
 * before its prompt (nocx-evj4b.1.6, as amended by the owner 2026-11-10;
 * nocx-evj4b.1.8).
 *
 * # The report
 *
 * Found by the nocx-evj4b.1.2 worker on this stand: an ssh pane whose far shell
 * is a non-integrated sh accepted the first composer submit and then stopped
 * taking composer submits. Plain ssh sessions without integration are a
 * supported case (owner), so the commands after the first must reach the shell.
 *
 * # What this asserts, and where it reads it
 *
 * The pane opens on a host whose account shell is `sh` (dash) with
 * `desiredMode: 'script'`: the launcher runs, stage-1 finds no bash/zsh to
 * integrate, and the session settles `conventional` — there is no prompt mark
 * and there never will be one. During that startup interval the composer is the
 * person's input surface; FIVE submits are typed into it. Each Enter still
 * takes its own line (the draft empties; nocx-evj4b.1.6), but the lines are
 * held against a prompt that is never coming — so, per the owner's decision of
 * 2026-11-10, they are NOT written into the pty: the far side is still cooked
 * and its startup chain can read stdin. The fixture's own record of what the
 * pty accepted (`-input-log`) is read after the bootstrap frames: it must hold
 * NOTHING, and the composer must hold the five lines.
 *
 * Only then does the person submit at the prompt they can see — the
 * conventional pane's input surface is the grid — and every one of those five
 * lines must reach the far program as its own line.
 *
 * # The startup interval is HELD at the shell, and its bound is the stand's
 *
 * The host's account shell is a wrapper that waits for a file and then becomes
 * `/bin/sh`. The loader, stage-1, the capability frame and the launcher all run
 * at full speed inside the bootstrap's own deadlines (review round 2: holding
 * the command start, or the client's bytes, put a three-second bootstrap
 * timer in front of the test). Only the prompt waits — the far side reaches no
 * verdict until the spec releases it — so the interval lasts exactly as long as
 * the spec needs, and nothing in it turns on how long the browser took.
 *
 * The bound that ends that interval is the stand's too (review round 3): the
 * backend is built with its dev tag, and a session opened under the account
 * name this spec uses gets 120 s instead of the product's 10 s
 * (internal/transport/dev_bind_dev.go). Without it, five Playwright submits on
 * a loaded runner could outlast the default and the test would be racing a
 * clock; with it, the interval is long enough that the submits are the only
 * thing that ends it. The 10 s default is untouched for every other spec.
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
 * Where the launcher's own bootstrap frames end, or null while they are still
 * arriving.
 *
 * The frame protocol (internal/shellintegration/carrier.go) is a fixed 17-byte
 * header — `NOCX1`, space, sequence digit, space, eight columns of right-
 * aligned length, newline — followed by exactly that many bytes. The open
 * sends frame 1 (stage-1) and then frame 2 (the capability or its refusal),
 * and the far side reads both off this same pty before the plain shell runs.
 * Anything after them is what a person's submits put there.
 *
 * Null is "not yet", never "wrong": the frames reach the fixture only after
 * the spec releases its input hold, so a reader that treated an incomplete log
 * as a failure would race the release it just made.
 */
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

/** The bytes the far program read after the open's own bootstrap frames, or
 *  null while the frames are still arriving (and the reason it is strict about
 *  what follows: a submit is one typed line and its own Enter). */
function submitsIn(logPath: string): string[] | null {
  const all = readFileSync(logPath)
  const framesEnd = bootstrapFramesEnd(all)
  if (framesEnd === null) return null
  return all.subarray(framesEnd).toString('utf8').split('\r')
}

test('an ssh pane on a non-integrated sh writes none of its held submits before its prompt', async ({
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
  // THE HOLD, AT THE SHELL ITSELF (review, round 2). The account shell the
  // launcher execs is a wrapper that waits for a file and then becomes
  // /bin/sh. Everything before it — the loader's LOADER_READY, stage-1's
  // STAGE_READY, the capability frame, the launcher's own dispatch — runs at
  // full speed inside the bootstrap's own deadlines, so no far-side timer is
  // racing the spec. What waits is the prompt: the session reaches no verdict
  // until the spec releases it, and the only clock left is the product's
  // startup bound, which is what this feature IS.
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
    // basename `sh`, so the launcher dispatches exactly as it does for the
    // account shell of a host whose shell is /bin/sh.
    args: ['-input-log', logRoot, '-shell', holdBin],
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
        // THE STARTUP BOUND'S MARKER, IN THE ONE PLACE THE OPEN CARRIES: the
        // stand gives a session opened under this account a long integration
        // startup bound (internal/transport/dev_bind_dev.go). The fixture
        // authenticates the key and does not care about the name.
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

    // Release the shell: it becomes /bin/sh and the session settles
    // conventional. THE QUEUE'S PROMISE CANNOT BE KEPT (owner, 2026-11-10;
    // nocx-evj4b.1.8): no prompt mark is coming, so the lines are NOT written
    // into the pty — they come back to the composer, where the person left
    // them, and nothing reaches the far side until they submit at the prompt
    // they can see.
    writeFileSync(holdPath, '')
    // The far program's own record of what arrived, announced by the fixture
    // as soon as the channel it records starts.
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
    // The composer holds the lines the queue could not deliver — the whole
    // draft, in the order the person made it.
    await expect
      .poll(() => editor.locator('.cm-line').allTextContents(), {
        timeout: 10_000,
        message: 'the composer does not hold the lines the queue could not deliver',
      })
      .toEqual([...lines])
    // And the far shell read NOTHING after its own bootstrap frames.
    expect(
      submitsIn(logPath),
      'the plain sh received bytes before its prompt — the cooked pty the ' +
        'queue must not write into',
    ).toEqual([''])

    // THE PERSON SUBMITS AT THE PROMPT THEY CAN SEE — and the composer holds
    // their draft again, so their Enter is the submit: their five lines run,
    // each as its own line.
    await editor.click()
    await editor.press('Enter')
    await expect
      .poll(() => submitsIn(logPath), {
        timeout: 60_000,
        message:
          `the plain sh has not received the five lines after the bootstrap frames: ` +
          `${JSON.stringify(submitsIn(logPath))} for ${JSON.stringify(lines)}, ` +
          'want each line and its own Enter',
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
