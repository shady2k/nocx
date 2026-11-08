/**
 * e2e: the bytes an ssh pane's shell receives are the bytes the person typed
 * (nocx-3j4ua, outcome O2 of v0-8-2).
 *
 * # The report
 *
 * The owner, working over ssh to a Debian host (remote bash), found commands
 * arriving changed — and only over ssh; the same lines run in a local tab.
 * `apt-get update` submitted repeatedly alternated between running and
 * `bash: fg: %apt-get: no such job` (a line beginning with `%` reached bash),
 * and lines carrying `$(( ))` or `%` answered arithmetic and printf errors that
 * the same text cannot produce.
 *
 * # What this asserts, and why it is the far side that says it
 *
 * For EVERY submit the spec reads the bytes the far program actually received
 * on its stdin (`-input-log`: the fixture appends what the pty master accepted,
 * and announces the file as `INPUTLOG=`) and requires them to be the typed line
 * and its Enter — with at most the shell's own bracketed-paste framing around
 * the text, which is the shell asking for it, never nocx adding it.
 *
 * Then it requires every run's exit code: each line's block settles with the
 * code the SAME line produced in a local tab on the same machine, so a
 * corrupted submission cannot pass by failing the way a local one did.
 *
 * # Nothing is waited out
 *
 * The waits are on product state: the composer holding the keyboard, the
 * running block disappearing, the fixture's own announcement. `apt-get update`
 * runs five times in a row because the report's alternation is the shape of the
 * defect — one corrupted submit in five would still be a failure.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, appReadyForInput, promptReady, resolveBackend, test, type Page } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const PANE = '.pane.active'
const EDITOR = `${PANE} .nocx-editor-input`
const RUNNING = `${PANE} .cmd-block-running`
/** A settled command block, in DOM order. */
const BLOCKS = `${PANE} .scrollback-inner > .cmd-block[data-block-kind="command"]`
/** The settled status word: the header's own meta that is not the duration. */
const STATUS = ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'

/** The three lines the owner reported, exactly as they were typed. */
const OWNER_LINES = [
  'for i in $(seq 1 60); do seq $((i*10)) $((i*10+9)); sleep 0.3; done',
  "for p in $(seq 0 100); do printf '\\rprogress %3d%%' $p; sleep 0.05; done; echo",
]
/** The command whose every other submit failed with `bash: fg: %apt-get`. */
const REPEATED = 'apt-get update'
/** How many times in a row the report's alternation is watched. */
const REPEATS = 5

interface RunResult {
  /** What the far side received for this submit. */
  readonly received: string
  /** The block's own exit code: -1 when the header settles with no status
   *  word, which is how a zero exit is shown. */
  readonly exitCode: number
}

/** The status the last settled block reports, and this line's exit code. */
async function lastExitCode(page: Page): Promise<number> {
  const blocks = page.locator(BLOCKS)
  await expect(blocks.last()).toBeVisible()
  const outcome = await blocks.last().getAttribute('data-outcome')
  expect(
    outcome,
    'the submitted line produced no settled command block, so no exit code exists for it',
  ).not.toBeNull()
  const status = blocks.last().locator(STATUS)
  if ((await status.count()) === 0) return 0
  const word = (await status.textContent()) ?? ''
  const m = /Exit\s+(\d+)/.exec(word)
  expect(
    m,
    `the block's status word read ${JSON.stringify(word)}, which names no exit code`,
  ).not.toBeNull()
  return Number(m![1])
}

/** Write one line into the composer and wait for its block to settle. */
async function submitOne(page: Page, line: string): Promise<number> {
  // The composer holding the keyboard is the product's statement that the
  // shell is at its prompt: a line typed in that interval is one paste.
  await promptReady(page)
  const editor = page.locator(EDITOR)
  await editor.click()
  await editor.pressSequentially(line)
  await expect(editor).toHaveText(line)
  const before = await page.locator(BLOCKS).count()
  await editor.press('Enter')
  await expect(page.locator(RUNNING)).toHaveCount(0, { timeout: 120_000 })
  await expect
    .poll(() => page.locator(BLOCKS).count(), {
      timeout: 120_000,
      message: 'the submitted line produced no command block of its own',
    })
    .toBeGreaterThan(before)
  return lastExitCode(page)
}

/**
 * The bytes the far side received, checked against the typed line.
 *
 * The only addition allowed is the shell's OWN bracketed-paste framing: mode
 * 2004 is the program's (ADR-0004 §2), and a shell that asked for it gets
 * `ESC[200~` … `ESC[201~` around the text. Anything else in the interval —
 * a leading byte, a changed character, a second line — is the defect.
 */
function expectExactlyTyped(received: Buffer, typed: string, where: string): void {
  const text = Buffer.from(typed, 'utf8')
  const bare = Buffer.concat([text, Buffer.from('\r')])
  const framed = Buffer.concat([Buffer.from('\x1b[200~'), text, Buffer.from('\x1b[201~\r')])
  const hex = (b: Buffer) => b.toString('hex').replace(/(..)/g, '$1 ').trim()
  expect(
    [bare.toString('hex'), framed.toString('hex')],
    `${where}: the far shell received ${hex(received)} (${JSON.stringify(received.toString('utf8'))}), ` +
      `which is neither the typed line ${JSON.stringify(typed)} and its Enter (${hex(bare)}) ` +
      `nor that line in the shell's own bracketed paste (${hex(framed)})`,
  ).toContain(received.toString('hex'))
}

/**
 * Where the launcher's own bootstrap frames end.
 *
 * The frame protocol (internal/shellintegration/carrier.go) is a fixed 17-byte
 * header — `NOCX1`, space, sequence digit, space, eight columns of
 * right-aligned length, newline — followed by exactly that many bytes. The open
 * sends frame 1 (stage-1) and then frame 2 (the capability or its refusal), and
 * the far side reads both off this same pty before the integrated shell runs.
 * Anything after them is what a person will be typing into.
 */
function bootstrapFramesEnd(bytes: Buffer): number {
  let offset = 0
  for (const seq of [1, 2]) {
    const header = bytes.subarray(offset, offset + 17).toString('latin1')
    // The length is RIGHT-ALIGNED in eight columns, so it arrives padded with
    // spaces rather than as eight digits.
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

/** The bytes appended to the fixture's input log since a byte offset. */
function receivedSince(logPath: string, offset: number): { bytes: Buffer; next: number } {
  const all = readFileSync(logPath)
  return { bytes: all.subarray(offset), next: all.length }
}

test('an ssh pane submits the bytes it was given, five times in a row', async ({ page }) => {
  // Two long loops, five apt-get runs and a local comparison on the same
  // machine; every wait inside is on product state.
  test.setTimeout(600_000)

  const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-ssh-submit-host-'))
  const remoteHome = path.join(hostRoot, 'home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const logRoot = mkdtempSync(path.join(tmpdir(), 'nocx-ssh-submit-log-'))

  let fixture: SshdFixture | null = null
  let profileId: string | null = null
  try {
    fixture = await startSshd({
      home: remoteHome,
      cwd: remoteHome,
      // Key auth: this spec is about the bytes, not the credential.
      args: ['-input-log', logRoot],
    })
    await page.goto('/')
    await appReadyForInput(page)

    // The far host's key, trusted in the home the BACKEND runs under — the
    // helper asks the coordinator, and the coordinator reads its own
    // known_hosts. REPLACED, never appended: every fixture spawn mints fresh
    // keys.
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

    // ── the LOCAL baseline: what each line's own exit code is ───────────
    const local: RunResult[] = []
    await promptReady(page)
    for (const line of [REPEATED, ...OWNER_LINES]) {
      local.push({ received: '', exitCode: await submitOne(page, line) })
    }

    // ── the ssh pane ────────────────────────────────────────────────────
    const info = await resolveBackend(page)
    const profileName = `e2e-ssh-submit-${Date.now()}`
    const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'e2e',
        keyPath: fixture.userKey,
        // script is what the default (auto) resolves to for integration — it
        // wraps and installs the scripts — and it names that explicitly so the
        // open is never the connect-time helper ask (ADR-0069) instead of the
        // thing this spec is about.
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

    // The composer coming up is the product's statement that the REMOTE shell
    // reached an authenticated prompt (ADR-0006's A → B).
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 180_000 })

    // The far program's own record of what arrived, announced by the fixture
    // before the channel that fills it exists.
    await expect
      .poll(() => fixture!.lines().find((line) => line.startsWith('INPUTLOG=')) ?? '', {
        timeout: 60_000,
        message: 'the fixture never announced an input log for the ssh pane',
      })
      .not.toBe('')
    const announcement = fixture.lines().filter((line) => line.startsWith('INPUTLOG='))
    expect(
      announcement,
      `the fixture recorded ${announcement.length} pty channels for this open; this spec reasons ` +
        'about the one interactive shell, so it needs exactly one',
    ).toHaveLength(1)
    const logPath = announcement[0].slice('INPUTLOG='.length).split(' ')[0]

    // ── what the OPEN wrote: the bootstrap's own two frames, and nothing else
    //
    // The launcher reads a framed payload off this same pty before the
    // integrated bash exists — header then body, twice (stage-1, then the
    // capability or its refusal). Those bytes are part of the shell's input
    // stream and the far side consumed them; what matters here is that the
    // stream ENDS there. A byte of either frame left unread would be the first
    // thing the interactive shell reads, which is exactly the defect class this
    // spec exists for.
    const openAt = receivedSince(logPath, 0)
    let offset = bootstrapFramesEnd(openAt.bytes)
    expect(
      openAt.bytes.subarray(offset).toString('utf8'),
      "opening the ssh pane left bytes in the remote shell's input after its bootstrap frames; " +
        'the shell will read them as the first thing a person types',
    ).toBe('')

    for (const line of [...OWNER_LINES, ...Array<string>(REPEATS).fill(REPEATED)]) {
      const code = await submitOne(page, line)
      const { bytes, next } = receivedSince(logPath, offset)
      offset = next
      expectExactlyTyped(bytes, line, `the submit of ${JSON.stringify(line)}`)
      // The write is the far program's own read: this is the exit code the
      // line produced THERE, and it must be the one the same line produced
      // locally.
      const baseline =
        line === REPEATED ? local[0].exitCode : local[1 + OWNER_LINES.indexOf(line)].exitCode
      expect(
        code,
        `the ssh run of ${JSON.stringify(line)} exited ${code}; locally it exits ${baseline}`,
      ).toBe(baseline)
    }
    // And nothing after the last submit: a byte the product wrote but the shell
    // never read would be the next thing a person's typing joins.
    const trailing = receivedSince(logPath, offset)
    expect(
      trailing.bytes.toString('utf8'),
      "the ssh pane wrote bytes into the remote shell's input that no submit asked for",
    ).toBe('')
  } finally {
    try {
      if (profileId !== null) {
        const info = await resolveBackend(page)
        await rpc(page, info, 'profiles.delete', { id: profileId })
      }
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture?.proc.kill('SIGKILL')
  }
})
