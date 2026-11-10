/**
 * e2e: a markerless ssh pane keeps every character a person types
 * (nocx-evj4b.1.12).
 *
 * # The report
 *
 * Found on this stand: an ssh pane with no shell integration takes every
 * keystroke as its own intent, and a long line containing quotes, spaces and
 * punctuation typed at full speed arrives at the far program with characters
 * missing. The worker who found it typed short, quote-free commands at a 20 ms
 * pace to get anything done.
 *
 * # What this asserts, and where it reads it
 *
 * A markerless session (`desiredMode: 'raw'`, no OSC 133, so the grid is the
 * input surface and the composer never takes the keys), a shell prompt on
 * screen, and then ONE line typed at full speed — `page.keyboard.type` with no
 * delay — followed by Enter. The fixture's own record of what the pty accepted
 * (`-input-log`, cmd/e2e-sshd) is the far side's account: after the launcher's
 * bootstrap frames it must be exactly the typed line and its Enter, byte for
 * byte.
 *
 * The line has the shape that fails: quotes of both kinds, spaces, punctuation
 * and length. It is also a real `echo`, so a run that loses nothing prints it —
 * the person sees what they asked for, not a mangled command.
 *
 * Nothing is waited out: every wait is on product state or on the fixture's own
 * file growing.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import type { Page } from '@playwright/test'
import { test, expect, appReadyForInput, resolveBackend } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const PANE = '.pane.active'
const LIVE = `${PANE} .terminal-live-container`

/** The line: 84 characters of the shapes a person actually types. */
const LINE =
  'echo "Q1 alpha beta" \'Q2 gamma-delta\' Q3.eps:four/five=5,6 Q7_x+y "Q8 %+@" \'Q9[a]b\' END'

/** Where the launcher's bootstrap frames end (internal/shellintegration/carrier.go:
 *  `NOCX1 <seq> <len>` and its body), or null while they are still arriving. */
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

/** Open a markerless ssh pane and leave the keyboard in its grid. */
async function openMarkerlessPane(
  page: Page,
): Promise<{ fixture: SshdFixture; createdId: string }> {
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'drop-keys-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'drop-keys-cwd-'))
  const logRoot = mkdtempSync(path.join(tmpdir(), 'drop-keys-log-'))
  const fixture = await startSshd({
    home: remoteHome,
    cwd: remoteCwd,
    args: ['-input-log', logRoot],
  })
  let createdId = ''
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
  await page.goto('/')
  await appReadyForInput(page)
  const info = await resolveBackend(page)
  const name = `drop-keys-${Date.now()}`
  const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
    type: 'ssh',
    name,
    options: {
      host: fixture.host,
      port: fixture.port,
      user: fixture.user,
      keyPath: fixture.userKey,
      // No integration: the shell emits no prompt mark, the grid is the input
      // surface, and every key is its own intent — the mode the report is about.
      desiredMode: 'raw',
    },
  })
  createdId = created.id
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible({ timeout: 30_000 })
  await search.fill(name)
  await expect(page.locator('.quick-connect__item', { hasText: name })).toBeVisible({
    timeout: 30_000,
  })
  await page.keyboard.press('Enter')
  // The shell must have painted before the person speaks — the only readiness a
  // markerless pane has.
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
  // THE SHELL'S OWN PROMPT, not the first row that happens to be non-empty:
  // the launcher's carrier prints before it hands the pty to the shell, and a
  // key typed in that window is addressed to a program that is not the shell.
  // A person's cue is the prompt, so the prompt is this spec's cue too.
  await expect
    .poll(
      async () =>
        page.evaluate(() =>
          Array.from(document.querySelectorAll('.pane.active .term-grid-row')).some((row) =>
            /#\s*$/.test((row.textContent ?? '').trimEnd()),
          ),
        ),
      { timeout: 60_000 },
    )
    .toBe(true)
  await page.locator(LIVE).click()
  return { fixture, createdId }
}

/** The bytes the far pty received, or null while the fixture has not announced
 *  its log yet.
 *
 *  A markerless pane runs the shell directly, so there are no launcher
 *  bootstrap frames to skip — the log IS the program's input. The skip exists
 *  for the integrated carriers that do print them, so it is applied only when
 *  the log actually starts with a frame header. */
function acceptedBytes(fixture: SshdFixture): string | null {
  const line = fixture.lines().find((l) => l.startsWith('INPUTLOG='))
  if (line === undefined) return null
  const logPath = line.slice('INPUTLOG='.length).split(' ')[0]
  const all = readFileSync(logPath)
  if (!all.subarray(0, 6).toString('latin1').startsWith('NOCX1 ')) return all.toString('utf8')
  const end = bootstrapFramesEnd(all)
  return end === null ? null : all.subarray(end).toString('utf8')
}

test('a long quoted line typed at full speed reaches a markerless ssh pane byte for byte (nocx-evj4b.1.12)', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const { fixture, createdId } = await openMarkerlessPane(page)
  try {
    // FULL SPEED: no delay between keys, which is what a person typing fast and
    // what Playwright's own type are; the pane turns each into one intent.
    await page.keyboard.type(LINE)
    await page.keyboard.press('Enter')

    // THE WHOLE LINE, waited for — not "the log exists": the person is still
    // typing when the first byte lands, and a read at that moment says the
    // first character arrived, which is not what this spec is about.
    await expect
      .poll(() => acceptedBytes(fixture), { timeout: 60_000, intervals: [100] })
      .toBe(`${LINE}\r`)
    // The far side's own record: the line, and its Enter. A missing character
    // is the defect this bead is about.
    expect(acceptedBytes(fixture)!).toBe(`${LINE}\r`)
    // And the run the person asked for printed the line back.
    await expect(page.locator(PANE)).toContainText('END', { timeout: 30_000 })
  } finally {
    // THE PROFILE GOES WITH THE PANE (the stand is shared by the repeats of this
    // spec and by the specs beside it): a saved profile left behind is a row
    // another run's palette answers with, and the session it names outlives
    // this fixture.
    try {
      const info = await resolveBackend(page)
      await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})
