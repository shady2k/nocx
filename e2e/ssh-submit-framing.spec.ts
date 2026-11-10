import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, appReadyForInput, promptReady, resolveBackend, test, type Page } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

/**
 * e2e: an ssh pane's composer submit is NEVER bracketed when shell integration
 * is live (owner, 2026-11-10; nocx-evj4b.1.2).
 *
 * # The report
 *
 * Over ssh, one session's submits alternated between framed and bare: the
 * framing was decided from a mode belief the ssh path cannot prove (the far
 * side's own `ESC[?2004h` can be consumed before the parser sees it). Harmless
 * on readline 8.2, which accepts a bare paste; on dash, sh or `bash --noediting`
 * the far shell ran `\x1b[200~echo …` as text.
 *
 * # What this asserts, and why
 *
 * A composer submit is a whole line the editor held. With integration live the
 * editor is the protection, so the markers protect nothing and the product does
 * not add them: for every submit, the bytes the far shell actually read
 * (`-input-log`) must be exactly the typed line and its Enter — no
 * `ESC[200~`/`ESC[201~` — whether or not that shell announces bracketed paste
 * in its prompt. The two cases differ only in what the shell says; the product
 * ignores the announcement, which is the rule this spec exists to pin.
 *
 * (A program running inside a block is the other half of the rule and is not
 * this path: its own announcement decides, which is the runtime's existing
 * mode-driven framing, covered by the clipboard-paste specs and the pane's
 * unit test.)
 *
 * # Nothing is waited out
 *
 * The waits are on product state (the composer holding the keyboard, a
 * command's block settling) and on the fixture's own announcement of its input
 * log.
 */
const PANE = '.pane.active'
const EDITOR = `${PANE} .nocx-editor-input`
const BLOCKS = `${PANE} .scrollback-inner > .cmd-block[data-block-kind="command"]`
/** A command block's settled form, written by the one owner that settles one. */
const SETTLE_MS = 120_000
/** How many submits in a row: the report's alternation was within one session,
 *  and every one after the first arrives with a command between it and its
 *  predecessor — the sequence the per-prompt fence exists for. */
const SUBMITS = 5

/** One fixture and one open ssh pane, with the far side's input log found. */
interface Stand {
  readonly fixture: SshdFixture
  readonly profileId: string
  readonly logPath: string
  /** Byte offset into the input log after the open's own bootstrap frames. */
  readonly openOffset: number
}

function receivedSince(logPath: string, offset: number): { bytes: Buffer; next: number } {
  const all = readFileSync(logPath)
  return { bytes: all.subarray(offset), next: all.length }
}

function hex(b: Buffer): string {
  return b.toString('hex').replace(/(..)/g, '$1 ').trim()
}

async function openPane(page: Page, shell: string, inputrc: string | null): Promise<Stand> {
  const logRoot = mkdtempSync(path.join(tmpdir(), 'nocx-framing-log-'))
  const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-framing-host-'))
  const remoteHome = path.join(hostRoot, 'home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  // A readline that asks for no bracketed paste: the shell is fully integrated
  // (it marks its prompts, so the composer and blocks work) but announces no
  // mode 2004, which is the program this spec's second case is about.
  if (inputrc !== null) writeFileSync(path.join(remoteHome, '.inputrc'), inputrc + '\n')
  const fixture = await startSshd({
    home: remoteHome,
    cwd: remoteHome,
    args: ['-input-log', logRoot, '-shell', shell],
  })
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

  const info = await resolveBackend(page)
  const profileName = `e2e-framing-${shell}-${Date.now()}`
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
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible({ timeout: 30_000 })
  await search.fill(profileName)
  await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
    timeout: 30_000,
  })
  await page.keyboard.press('Enter')
  await expect(page.locator(EDITOR)).toBeVisible({ timeout: 180_000 })

  await expect
    .poll(() => fixture.lines().find((line) => line.startsWith('INPUTLOG=')) ?? '', {
      timeout: 60_000,
      message: 'the fixture never announced an input log for the ssh pane',
    })
    .not.toBe('')
  const announced = fixture.lines().filter((line) => line.startsWith('INPUTLOG='))
  expect(
    announced,
    `the fixture recorded ${announced.length} pty channels for this open; this spec reads the one ` +
      'interactive shell, so it needs exactly one',
  ).toHaveLength(1)
  const logPath = announced[0].slice('INPUTLOG='.length).split(' ')[0]
  // THE BASELINE IS TAKEN AT THE PROMPT, NOT AT THE ANNOUNCEMENT (nocx-evj4b.1.2,
  // review round 3). The fixture announces its input log as soon as the channel
  // starts, and the shell's own bootstrap input keeps arriving after that: a
  // baseline sampled there left those bytes to be read as the first submit's
  // input. The prompt is the last thing the open writes before a person can
  // type, so it is where the person's bytes begin.
  await promptReady(page)
  return { fixture, profileId: created.id, logPath, openOffset: readFileSync(logPath).length }
}

/** One submit through the composer: type the line and press Enter. */
async function submitWithComposer(page: Page, line: string): Promise<void> {
  await promptReady(page)
  const editor = page.locator(EDITOR)
  await editor.click()
  await editor.pressSequentially(line)
  await expect(editor).toHaveText(line)
  const index = await page.locator(BLOCKS).count()
  await editor.press('Enter')
  await expect
    .poll(() => page.locator(BLOCKS).count(), {
      timeout: SETTLE_MS,
      message: `the submit of ${JSON.stringify(line)} produced no command block`,
    })
    .toBeGreaterThan(index)
  await expect(page.locator(BLOCKS).nth(index)).toHaveAttribute(
    'data-outcome',
    /^(success|failure)$/,
    { timeout: SETTLE_MS },
  )
}

/** The bytes one submit put on the far side's input, and nothing else. */
function expectFraming(received: Buffer, typed: string, framed: boolean, where: string): void {
  const text = Buffer.from(typed, 'utf8')
  const want = framed
    ? Buffer.concat([Buffer.from('\x1b[200~'), text, Buffer.from('\x1b[201~\r')])
    : Buffer.concat([text, Buffer.from('\r')])
  expect(
    received.toString('hex'),
    `${where}: the far program read ${hex(received)} (${JSON.stringify(received.toString('utf8'))}), ` +
      `want ${framed ? "the shell's bracketed paste" : 'the line bare'} ${hex(want)}`,
  ).toBe(want.toString('hex'))
}

/** The two far programs this spec puts on the stand, both readline-enabled so
 *  the composer and the block machinery work, differing only in whether the
/** The two far programs this spec puts on the stand, both fully integrated so
 *  the composer and the block machinery work, differing only in whether the
 *  prompt announces bracketed paste — and the answer the product owes each. */
const CASES = [
  { name: 'bash asking for bracketed paste', inputrc: null },
  {
    name: 'bash asking for none',
    inputrc: 'set enable-bracketed-paste off',
  },
] as const

for (const tc of CASES) {
  test(`an ssh pane's submits are never bracketed at an integrated prompt: ${tc.name}`, async ({
    page,
  }) => {
    test.setTimeout(600_000)
    await page.goto('/')
    await appReadyForInput(page)
    await promptReady(page)

    const stand = await openPane(page, 'bash', tc.inputrc)
    let offset = stand.openOffset
    try {
      for (let i = 0; i < SUBMITS; i++) {
        const line = `echo probe-${i}`
        await submitWithComposer(page, line)
        await expect
          .poll(() => receivedSince(stand.logPath, offset).bytes.length, {
            timeout: 60_000,
            message: `${tc.name}: the shell read nothing for ${JSON.stringify(line)}`,
          })
          .toBeGreaterThan(0)
        const { bytes, next } = receivedSince(stand.logPath, offset)
        offset = next
        // THE LINE AND ITS ENTER, AND NOTHING ELSE — no frame, whether the
        // shell asked for one or not.
        expectFraming(bytes, line, false, `submit ${i + 1} of ${SUBMITS}, ${tc.name}`)
      }
    } finally {
      try {
        const info = await resolveBackend(page)
        await rpc(page, info, 'profiles.delete', { id: stand.profileId })
      } catch {
        // A cleanup that throws would replace the real failure with its own.
      }
      stand.fixture.proc.kill('SIGKILL')
    }
  })
}
