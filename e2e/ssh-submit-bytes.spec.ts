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
 * submit's own block reaching its settled form (`data-outcome`, written by the
 * one owner that settles a block), the fixture's own announcement. `apt-get
 * update` runs five times in a row because the report's alternation is the
 * shape of the defect — one corrupted submit in five would still be a failure.
 */
import type { Locator } from '@playwright/test'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, appReadyForInput, promptReady, resolveBackend, test, type Page } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const PANE = '.pane.active'
const EDITOR = `${PANE} .nocx-editor-input`
/** A command block, in DOM order — the RUNNING one included: it is a block of
 *  the command kind from the moment the shell's start mark reaches the pane,
 *  and it carries no outcome until it settles. */
const BLOCKS = `${PANE} .scrollback-inner > .cmd-block[data-block-kind="command"]`
/** The settled status word: the header's own meta that is not the duration. */
const STATUS = ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'
/** How long a submit's own block may take to settle. A hang detector and not a
 *  claim about speed: two long loops and five runs are real work on a real pty. */
const SETTLE_MS = 120_000

/** The three lines the owner reported, exactly as they were typed. */
const OWNER_LINES = [
  'for i in $(seq 1 60); do seq $((i*10)) $((i*10+9)); sleep 0.3; done',
  "for p in $(seq 0 100); do printf '\\rprogress %3d%%' $p; sleep 0.05; done; echo",
]
/** The command whose every other submit failed with `bash: fg: %apt-get`. */
const REPEATED = 'apt-get update'
/** How many times in a row the report's alternation is watched. */
const REPEATS = 5

/**
 * APT IS PINNED TO A SOURCE THAT FAILS INSTANTLY, on BOTH sides of this spec.
 *
 * `apt-get update` is the owner's own line and it stays exactly that, but its
 * runtime is the network's: with a runner whose mirrors are slow or blackholed,
 * apt waits out its own 30-second timeouts and retries — CI measured 37 s for
 * one run and more than 120 s for the next — and this spec's settle window is a
 * hang detector, not a claim about how long apt may take (nocx-4y82c.1.5).
 * Measured, to be sure it was not the product: a 300-second command over ssh
 * settles at 300.7 s, so blocks do end with their commands; only apt's runtime
 * was ever the variable.
 *
 * So both panes are given one apt config, by the variable apt reads, with a
 * source that refuses the connection: the same exit code on both sides, in
 * about a tenth of a second, on any machine. `Dir::Etc::sourceparts` is emptied
 * as well, because the machine's own sources are what the run would otherwise
 * reach for.
 */
function hermeticAptConfig(dir: string): string {
  mkdirSync(path.join(dir, 'empty'), { recursive: true })
  writeFileSync(
    path.join(dir, 'sources.list'),
    'deb [trusted=yes] http://127.0.0.1:1/nocx-e2e-apt/ ./\n',
  )
  const conf = path.join(dir, 'apt.conf')
  writeFileSync(
    conf,
    [
      `Dir::Etc::sourcelist "${path.join(dir, 'sources.list')}";`,
      `Dir::Etc::sourceparts "${path.join(dir, 'empty')}";`,
      'Acquire::Retries "0";',
      'Acquire::http::Timeout "2";',
      'Acquire::https::Timeout "2";',
      '',
    ].join('\n'),
  )
  return conf
}

interface RunResult {
  /** What the far side received for this submit. */
  readonly received: string
  /** The block's own exit code: 0 when the header settles with no status
   *  word, which is how a zero exit is shown. */
  readonly exitCode: number
}

/**
 * The block this submit produced, waited for on the product's own settlement.
 *
 * `data-outcome` is written by the ONE owner that settles a block
 * (`settleBlockOutcome`, scrollback/blocks.ts) and by nothing else, so the
 * attribute is the product's statement that this block HAS an exit code. It is
 * waited for, never read once: a settled block and a running one differ by
 * exactly this, and the running one is a block of the command kind too.
 *
 * "No block is running" cannot stand in for it. The running element is created
 * on the shell's own start mark (OSC 133 C), which arrives over the wire after
 * the submit, so a wait begun on the Enter itself is TRUE on a page where the
 * submit has produced nothing yet — and then any later-appearing block, the
 * running one included, satisfies it. That is the window this spec used to read
 * `null` through: ten runs in ten, and widest on an ssh pane, where the start
 * mark crosses a helper and a socket.
 */
async function settledBlock(page: Page, index: number): Promise<Locator> {
  const block = page.locator(BLOCKS).nth(index)
  await expect(
    block,
    'the submitted line produced no settled command block, so no exit code exists for it',
  ).toHaveAttribute('data-outcome', /^(success|failure)$/, { timeout: SETTLE_MS })
  return block
}

/** The exit code a settled block states: `Exit <n>`, and a zero exit is shown
 *  as a check with no word at all. */
async function exitCodeOf(block: Locator): Promise<number> {
  const status = block.locator(STATUS)
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
  // One line makes one block and blocks are appended, so the count before
  // Enter is this submit's own index — whether its block appears still running
  // or arrives already settled.
  const index = await page.locator(BLOCKS).count()
  await editor.press('Enter')
  await expect
    .poll(() => page.locator(BLOCKS).count(), {
      timeout: SETTLE_MS,
      message: 'the submitted line produced no command block of its own',
    })
    .toBeGreaterThan(index)
  return exitCodeOf(await settledBlock(page, index))
}

/**
 * The bytes the far side received, checked against the typed line.
 *
 * NOTHING may be added (owner, 2026-11-10; nocx-evj4b.1.2): at an integrated
 * prompt the composer's submit is never bracketed — the editor held the whole
 * line, and the shell's own mode-2004 announcement is not the answer there.
 * Anything in the interval — the frame pair, a leading byte, a changed
 * character, a second line — is the defect.
 */
function expectExactlyTyped(received: Buffer, typed: string, where: string): void {
  const text = Buffer.from(typed, 'utf8')
  const bare = Buffer.concat([text, Buffer.from('\r')])
  const hex = (b: Buffer) => b.toString('hex').replace(/(..)/g, '$1 ').trim()
  expect(
    received.toString('hex'),
    `${where}: the far shell received ${hex(received)} (${JSON.stringify(received.toString('utf8'))}), ` +
      `want the typed line ${JSON.stringify(typed)} and its Enter, nothing around it (${hex(bare)})`,
  ).toBe(bare.toString('hex'))
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
  // Both panes run on THIS machine (the far side of the fixture is this
  // container), so one file is the config for both; the setup line below is
  // the only thing that points them at it.
  const aptConfig = hermeticAptConfig(mkdtempSync(path.join(tmpdir(), 'nocx-ssh-submit-apt-')))
  const aptSetup = `export APT_CONFIG=${aptConfig}`

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
    // Typed rather than written into the stand's home: that home is shared by
    // every spec of the run, and this is one pane's environment for one test.
    await submitOne(page, aptSetup)
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

    // ── the far side's apt config, typed before anything is measured ─────
    //
    // The bytes still check: this is a line a person could type, it is the
    // first thing this shell receives after the open, and its own submit is
    // read off the far side exactly like the measured ones. It is not one of
    // the REPORTED lines, so it is not counted in the loop below.
    await submitOne(page, aptSetup)
    const setup = receivedSince(logPath, offset)
    offset = setup.next
    expectExactlyTyped(setup.bytes, aptSetup, 'the apt setup line')

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
