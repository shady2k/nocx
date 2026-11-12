/**
 * A full-screen program that repaints the PRIMARY screen keeps ONE block
 * (nocx-evj4b.1.9).
 *
 * The owner, nightly 202611100224, over an integrated ssh connection to a
 * Linux host: procps `top` started in one running block and painted fully;
 * about one refresh later that block showed a check and a duration, frozen,
 * and a NEW "top Running" block opened below it with one row of top's output.
 * Locally on macOS the same build kept one block, because BSD `top` borrows
 * the alternate screen there — and a program on the alternate screen is a
 * separate grid by design (ADR-3kqrem7v). procps top never borrows it: it
 * clears and repaints in the primary buffer, inside the block model, and that
 * is the mode this spec drives on both sides of the connection.
 *
 * WHAT IS ASSERTED, and why each half matters:
 *
 *  1. ONE running block, checked on every animation frame the app paints
 *     while the program repaints. A repaint is a screen the program drew,
 *     never a command boundary: a second block at any point is the split the
 *     owner met.
 *  2. EVERY EARLIER CARD'S BODY IS BYTE-IDENTICAL, before, during and after
 *     the program: a recorded command keeps exactly the rows it printed. The
 *     frozen card the owner saw above the second "top Running" was an earlier
 *     command's card wearing the new program's rows, and this assertion names
 *     the card that changed.
 *  3. The live region shows the program's WHOLE screen, first line included,
 *     on each of at least three repaints, counted by the screen number the
 *     program paints. A live region clipped at the block's own start keeps
 *     the last row and loses the rest, so the count of whole screens is what
 *     that cannot pass. The same frames count the program's own repaint
 *     numbers — the wait for the third is bounded — so "at least three
 *     refreshes" is the program's fact and not a duration this spec chose.
 *
 *  After `q` (typed into the grid, as a person quits top) the running block is
 *  the only card that changed: it is not running, outcome `success` (the
 *  program exits 0 on quit), and its stored rows carry the program's last
 *  whole screen rather than an empty card or a second frame.
 *
 * Both sides run the same test: the local control says whether the
 * connection is part of the cause (the owner's report says it is not, it is
 * TERM/terminfo that makes the local program take a different screen), and
 * the ssh case is the reported one.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor, resolveBackend } from './harness'
import { startSshd, rpc, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

/** The program's interval, and the bound on waiting for its last repaint. A
 *  clock reading in its header changes once per interval, so three distinct
 *  readings are three refreshes — the program's own fact, not a duration this
 *  spec chose. */
const REFRESH_MS = 1000
const THREE_REFRESHES_MS = 20_000
/** Screens the repainting program draws. One more than the three the spec
 *  asserts, because its first is on the screen before a sampler installed by
 *  the driver exists: the program's own count is what the wait is bounded by
 *  and what the frames are compared against, never a duration. */
const REPAINT_SCREENS = 4

interface Frame {
  /** Blocks on the pane, and how many of them claim to be running. */
  total: number
  running: number
  /** The clock reading in the program's own header, when it has one. */
  clock: string | null
  /** DOM text is not enough: the live viewport may clip the summary away. */
  summaryVisible: boolean
  liveHeight: number
  transform: string
  attemptIds: string[]
  reportedRows: number | null
  committedRows: number | null
}

/**
 * Sample the pane in the app's own animation frames, from the page.
 *
 * Recorded here rather than by polling from the driver because every sample
 * the waiting assertion reads must also be one the one-block invariant is
 * checked on: a driver-side poll sees whichever frames it happens to catch,
 * and the split this spec is about can exist between two of them.
 */
async function startSampling(page: Page): Promise<void> {
  await page.evaluate(() => {
    const host = window as unknown as {
      __topSplitSamples?: Frame[]
      __topSplitStop?: () => void
    }
    host.__topSplitSamples = []
    let stopped = false
    host.__topSplitStop = () => {
      stopped = true
    }
    const pane = document.querySelector<HTMLElement>('.pane.active')
    if (pane === null) return
    const tick = (): void => {
      if (stopped) return
      const blocks = [...pane.querySelectorAll<HTMLElement>('.cmd-block')]
      const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')?.textContent ?? ''
      const live = pane.querySelector<HTMLElement>('.terminal-live-viewport')
      const scroller = pane.querySelector<HTMLElement>('.scrollback-area')
      const summary = [
        ...pane.querySelectorAll<HTMLElement>('.terminal-live-grid .term-grid-row'),
      ].find((row) => row.textContent?.includes('load average'))
      const rowBox = summary?.getBoundingClientRect()
      const liveBox = live?.getBoundingClientRect()
      const scrollBox = scroller?.getBoundingClientRect()
      const summaryVisible = !!(
        rowBox &&
        liveBox &&
        scrollBox &&
        rowBox.height > 0 &&
        rowBox.top >= Math.max(liveBox.top, scrollBox.top) - 1 &&
        rowBox.bottom <= Math.min(liveBox.bottom, scrollBox.bottom) + 1
      )
      host.__topSplitSamples!.push({
        total: blocks.length,
        running: blocks.filter((b) => b.classList.contains('cmd-block-running')).length,
        clock: /\b\d{2}:\d{2}:\d{2}\b/.exec(grid)?.[0] ?? null,
        summaryVisible,
        liveHeight: liveBox?.height ?? 0,
        transform: pane.querySelector<HTMLElement>('.terminal-live-inner')?.style.transform ?? '',
        attemptIds: blocks.map((block) => block.dataset.entryId ?? '(unbound)'),
        reportedRows: window.__nocxPaneScreen?.()?.reported?.rows ?? null,
        committedRows: window.__nocxPaneScreen?.()?.geometry?.rows ?? null,
      })
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
}

async function readSamples(page: Page): Promise<Frame[]> {
  return page.evaluate(
    () => (window as unknown as { __topSplitSamples?: Frame[] }).__topSplitSamples?.slice() ?? [],
  )
}

async function stopSampling(page: Page): Promise<Frame[]> {
  const frames = await page.evaluate(() => {
    const host = window as unknown as { __topSplitSamples?: Frame[]; __topSplitStop?: () => void }
    host.__topSplitStop?.()
    return host.__topSplitSamples?.slice() ?? []
  })
  return frames
}

/** One running block cannot become two, so every recorded frame is checked;
 *  a split makes the assertion that is waiting on the clocks fail with the
 *  frames that show it. */
function framesSplitting(frames: Frame[]): Frame[] {
  return frames.filter(
    (f) => f.running !== 1 || f.total !== 1 || (f.clock !== null && !f.summaryVisible),
  )
}

/** Run the repainting program, wait (bounded) for its third repaint while
 *  checking the one-block invariant on every sample, then quit it with a key
 *  and assert the one frozen block. */
async function expectOneBlockAcrossRepaints(page: Page): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(`top -d ${REFRESH_MS / 1000}`)
  await page.keyboard.press('Enter')

  const running = page.locator('.pane.active .cmd-block.cmd-block-running')
  await expect(running).toHaveCount(1, { timeout: 20_000 })
  await expect(page.locator('.pane.active .cmd-block')).toHaveCount(1, { timeout: 20_000 })
  // ITS FIRST SCREEN, before anything is sampled: a frame drawn before the
  // program painted is not evidence about a repaint.
  await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('load average', {
    timeout: 20_000,
  })

  await startSampling(page)
  try {
    await expect
      .poll(
        async () => {
          const frames = await readSamples(page)
          const split = framesSplitting(frames)
          // The invariant is checked on EVERY sample, not only the last one: a
          // second block that appeared and went again between two driver polls
          // is still a split.
          if (split.length > 0) {
            throw new Error(`the block split: ${JSON.stringify(split.slice(0, 3))}`)
          }
          const clocks = new Set(frames.map((f) => f.clock).filter((c) => c !== null))
          return clocks.size
        },
        { timeout: THREE_REFRESHES_MS, intervals: [100] },
      )
      .toBeGreaterThanOrEqual(3)
  } finally {
    const samples = await stopSampling(page)
    await test.info().attach('top-repaint-samples', {
      body: JSON.stringify(samples),
      contentType: 'application/json',
    })
  }

  const frames = await readSamples(page)
  const said = `split frames: ${JSON.stringify(framesSplitting(frames).slice(0, 3))}`
  expect(frames.length, said).toBeGreaterThan(0)
  expect(framesSplitting(frames), said).toEqual([])

  // QUIT IT THE WAY A PERSON DOES: the program owns the keys while it runs.
  await page.locator('.pane.active .terminal-live-container').click()
  await page.keyboard.press('q')
  await expect(page.locator('.pane.active .cmd-block.cmd-block-running')).toHaveCount(0, {
    timeout: 20_000,
  })

  const frozen = page.locator('.pane.active .cmd-block')
  await expect(frozen).toHaveCount(1, { timeout: 20_000 })
  await expect(frozen).toHaveAttribute('data-outcome', 'success')
  await expect(frozen.locator('.cmd-header-text')).toHaveText(`top -d ${REFRESH_MS / 1000}`)
  const rows = frozen.locator('.cmd-output .term-grid-row')
  await expect(rows.first()).toBeVisible({ timeout: 20_000 })
  expect(await rows.count()).toBeGreaterThan(20)
  await expect(frozen.locator('.cmd-output')).toContainText('load average')
  await expect(frozen.locator('.cmd-output')).toContainText('Tasks:')
}

test('a full-screen program repainting the primary screen keeps one block locally (nocx-evj4b.1.9)', async ({
  page,
}) => {
  test.setTimeout(180_000)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await expectOneBlockAcrossRepaints(page)
})

for (const desiredMode of ['script', 'helper'] as const) {
  for (const scenario of ['top', 'history', 'clear'] as const) {
    if (desiredMode === 'script' && scenario !== 'top') continue
    test(`${scenario === 'top' ? 'a full-screen program repainting the primary screen keeps one block' : `history-first primary CSI H and EL repaint${scenario === 'clear' ? ' after clear' : ''}`} over ssh ${desiredMode} (nocx-peg8e.1.1)`, async ({
      page,
    }) => {
      test.setTimeout(300_000)
      const remoteHome = mkdtempSync(path.join(tmpdir(), 'top-split-remote-'))
      const remoteCwd = mkdtempSync(path.join(tmpdir(), 'top-split-cwd-'))
      const fixture: SshdFixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
      let createdId: string | null = null
      try {
        const sshDir = path.join(readStand().home, '.ssh')
        mkdirSync(sshDir, { recursive: true, mode: 0o700 })
        writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
        await page.setViewportSize({ width: 1280, height: 800 })
        observeBackendAttempts(page)
        await page.goto('/')
        await promptReady(page)
        const info = await resolveBackend(page)
        const profileName = `top-split-${Date.now()}`
        const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
          type: 'ssh',
          name: profileName,
          options: {
            host: fixture.host,
            port: fixture.port,
            user: fixture.user,
            keyPath: fixture.userKey,
            // Keep the original piped-carrier control and exercise the remote
            // helper too: the script-only control did not cover that PTY owner.
            desiredMode,
          },
        })
        createdId = created.id
        await page.keyboard.press('Control+Shift+P')
        const search = page.locator('.quick-connect__search input')
        await expect(search).toBeVisible({ timeout: 30_000 })
        await search.fill(profileName)
        await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
          timeout: 30_000,
        })
        await page.keyboard.press('Enter')
        if (desiredMode === 'helper') {
          const helperAsk = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
          await expect(helperAsk).toBeVisible({ timeout: 30_000 })
          await helperAsk.getByRole('radio', { name: 'Helper' }).click()
          await helperAsk.getByRole('button', { name: 'Continue' }).click()
          await expect(helperAsk).not.toBeVisible()
        }
        await promptReady(page)
        if (scenario === 'top') await expectOneBlockAcrossRepaints(page)
        else await expectHistoryFirstRepaint(page, scenario === 'clear')
        // The pane really is the far host's: the block's where-line names the
        // fixture's destination, so a local pane could not pass this spec.
        await expect(
          page.locator('.pane.active .cmd-block').last().locator('.cmd-header-meta'),
        ).toContainText(`${fixture.user}@${fixture.host}`)
      } finally {
        try {
          const info = await resolveBackend(page)
          if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
        } catch {
          // A cleanup that throws would replace the real failure with its own.
        }
        fixture.proc.kill('SIGKILL')
      }
    })
  }
}

const lifecycleAttempts = new WeakMap<Page, Set<string>>()
function observeBackendAttempts(page: Page): void {
  const attempts = new Set<string>()
  lifecycleAttempts.set(page, attempts)
  page.on('websocket', (socket) => {
    socket.on('framereceived', ({ payload }) => {
      if (typeof payload !== 'string') return
      const message = JSON.parse(payload) as {
        method?: string
        params?: { lifecycle?: string; attempt?: { id: string; state: string } }
      }
      if (
        message.method === 'lifecycle.changed' &&
        message.params?.lifecycle === 'running' &&
        message.params.attempt?.state === 'open'
      )
        attempts.add(message.params.attempt.id)
    })
  })
}

/** The repainting program, labelled so two runs of it are two different
 *  screens of rows. It homes the cursor on the PRIMARY screen and clears each
 *  line it paints, three screens one second apart, then waits for `q`. No
 *  alternate buffer and no terminfo decision is involved: this is the shape
 *  procps `top` has on a host whose TERM gives it the primary buffer. */
function repaintSource(label: string): string {
  return `bash -c 'stty -echo -icanon; trap "stty sane" EXIT; n=1; while :; do printf "\\033[H"; for ((i=1;i<=20;i++)); do printf "\\033[K${label}-%s-LINE-%02d" "$n" "$i"; if ((i<20)); then printf "\\r\\n"; fi; done; if ((n<${REPAINT_SCREENS})); then IFS= read -r -t 1 -n1 key; else IFS= read -r -n1 key; fi; [[ "$key" == q ]] && break; ((n++)); done'`
}

/** Type a command into the pane's editor and press Enter. */
async function runCommand(page: Page, command: string): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

/** Quit the foreground program the way a person does: while it runs the
 *  program owns the keys, so the key goes into the grid, not the editor. */
async function quitProgram(page: Page): Promise<void> {
  const live = page.locator('.pane.active .terminal-live-container')
  // THE PREMISE, asserted where it is consumed. Quitting this program means
  // typing into the grid, so the grid has to have a box to receive the key —
  // and with history above its start a live region clipped at the block's own
  // anchor has none. Named here, the failure says the region is gone instead
  // of timing out inside a click at the end of the test.
  await expect(live).toBeVisible({ timeout: 20_000 })
  await live.click()
  await page.keyboard.press('q')
  await expect(page.locator('.pane.active .cmd-block.cmd-block-running')).toHaveCount(0, {
    timeout: 20_000,
  })
}

/** Wait for the program's Nth screen, by the label that screen carries. */
async function awaitRepaint(page: Page, label: string, screen: number): Promise<void> {
  for (const line of ['01', '20']) {
    await expect(page.locator('.pane.active .terminal-live-grid')).toContainText(
      `${label}-${screen}-LINE-${line}`,
      { timeout: THREE_REFRESHES_MS },
    )
  }
}

interface HistoryFrame {
  total: number
  running: number
  ids: string[]
  /** Which screen of the program the live grid held, or null before its first. */
  repaint: number | null
  /** BOTH ENDS of the program's screen inside the visible live region: its
   *  first line is the one a live region clipped at the block's own start
   *  loses, and it is the line the owner never saw. */
  whole: boolean
  /** The body of every card that existed before this run, by entry id. A card
   *  is a record of one command: another program's rows appearing in it is
   *  the block split. */
  bodies: Record<string, string>
}

/** Primary-screen repaint with scrollback already present. The screen above
 *  is filled twice over: by a command whose output scrolled, and by a
 *  previous run of the SAME program, quit with `q` — the frozen card the
 *  owner saw wearing the next run's rows. Then the program runs again and
 *  repaints the primary screen from row 0. */
async function expectHistoryFirstRepaint(page: Page, afterClear: boolean): Promise<void> {
  await runCommand(page, "printf 'HISTORY-BEFORE-REPAINT-%s\\n' {1..100}")
  await promptReady(page)
  await expect(page.locator('.pane.active .cmd-block .cmd-output')).toContainText(
    'HISTORY-BEFORE-REPAINT-100',
  )
  // THE PREVIOUS CARD THE OWNER MET: one earlier run of the same program,
  // frozen with its own outcome, still showing its own last screen.
  await runCommand(page, repaintSource('PRIOR'))
  const runningPrior = page.locator('.pane.active .cmd-block.cmd-block-running')
  await expect(runningPrior).toHaveCount(1)
  await awaitRepaint(page, 'PRIOR', REPAINT_SCREENS)
  await quitProgram(page)
  await promptReady(page)
  const priorCard = page.locator('.pane.active .cmd-block').last()
  await expect(priorCard).toHaveAttribute('data-outcome', 'success')
  await expect(priorCard.locator('.cmd-output')).toContainText(`PRIOR-${REPAINT_SCREENS}-LINE-01`)
  await expect(priorCard.locator('.cmd-output')).toContainText(`PRIOR-${REPAINT_SCREENS}-LINE-20`)
  if (afterClear) {
    // THE CONTROL. `clear` erases the display, and the cards the erase took go
    // with it — their records remain (backend-clear-boundary.spec.ts), their
    // DOM does not — so this program's block starts at screen row 0, the case
    // the owner reported as working. `clear`'s own card survives its own
    // erase, so the pane holds that one card and nothing above it.
    const erasedIds = await page.evaluate(() =>
      [...document.querySelectorAll<HTMLElement>('.pane.active .cmd-block')]
        .map((block) => block.dataset.entryId ?? '')
        .filter((id) => id !== ''),
    )
    await runCommand(page, 'clear')
    await promptReady(page)
    for (const id of erasedIds) {
      await expect(page.locator(`.pane.active .cmd-block[data-entry-id="${id}"]`)).toHaveCount(0)
    }
    // `clear`'s own card is the one the boundary may keep, and a boundary that
    // arrived before it bound keeps nothing; either way nothing older than the
    // erase is left above this program, which is the control's premise.
    expect(await page.locator('.pane.active .cmd-block').count()).toBeLessThanOrEqual(1)
  }
  // WHAT EVERY EARLIER CARD SAID, before the new program touches the screen.
  const priorBodies = await page.evaluate(() =>
    Object.fromEntries(
      [...document.querySelectorAll<HTMLElement>('.pane.active .cmd-block')].map((block) => [
        block.dataset.entryId ?? '(unbound)',
        block.querySelector<HTMLElement>('.cmd-output')?.textContent ?? '(no body)',
      ]),
    ),
  )
  const priorIds = Object.keys(priorBodies)
  const priorBlocks = priorIds.length
  const attempts = lifecycleAttempts.get(page)!
  attempts.clear()

  await runCommand(page, repaintSource('LIVE'))
  const running = page.locator('.pane.active .cmd-block-running')
  await expect(running).toHaveCount(1)
  await page.evaluate(
    ({ label, priorIds }) => {
      const host = window as unknown as {
        __historyRepaintFrames: HistoryFrame[]
        __historyRepaintStop: () => void
      }
      host.__historyRepaintFrames = []
      let stopped = false
      host.__historyRepaintStop = () => {
        stopped = true
      }
      const screenOf = new RegExp(`${label}-(\\d+)-LINE-01`)
      const tick = (): void => {
        if (stopped) return
        const pane = document.querySelector<HTMLElement>('.pane.active')!
        const blocks = [...pane.querySelectorAll<HTMLElement>('.cmd-block')]
        const rows = [...pane.querySelectorAll<HTMLElement>('.terminal-live-grid .term-grid-row')]
        const text = rows.map((row) => row.textContent ?? '')
        const repaint = text
          .map((value) => screenOf.exec(value))
          .find((match) => match !== null)?.[1]
        const screen = repaint === undefined ? null : Number(repaint)
        const first = rows.find((row) => screenOf.test(row.textContent ?? ''))
        const last = rows.find((row) => row.textContent?.includes(`${label}-${screen}-LINE-20`))
        const viewport = pane.querySelector('.terminal-live-viewport')!.getBoundingClientRect()
        const scroll = pane.querySelector('.scrollback-area')!.getBoundingClientRect()
        const whole = [first, last].every((row) => {
          const box = row?.getBoundingClientRect()
          return (
            box !== undefined &&
            box.height > 0 &&
            box.top >= Math.max(viewport.top, scroll.top) - 1 &&
            box.bottom <= Math.min(viewport.bottom, scroll.bottom) + 1
          )
        })
        host.__historyRepaintFrames.push({
          repaint: screen,
          whole,
          total: blocks.length,
          running: blocks.filter((block) => block.classList.contains('cmd-block-running')).length,
          ids: blocks.map((block) => block.dataset.entryId ?? '(unbound)'),
          bodies: Object.fromEntries(
            priorIds.map((id) => [
              id,
              pane
                .querySelector<HTMLElement>(`.cmd-block[data-entry-id="${id}"]`)
                ?.querySelector<HTMLElement>('.cmd-output')?.textContent ?? '(no body)',
            ]),
          ),
        })
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    },
    { label: 'LIVE', priorIds },
  )
  let frames: HistoryFrame[] = []
  try {
    await expect
      .poll(
        async () =>
          new Set(
            (
              await page.evaluate(
                () =>
                  (window as unknown as { __historyRepaintFrames: HistoryFrame[] })
                    .__historyRepaintFrames,
              )
            )
              // The whole screen, not a repaint that happened to show its last
              // row: "three repaints" is only evidence when each one is the
              // screen the program drew.
              .filter((frame) => frame.whole && frame.repaint !== null)
              .map((frame) => frame.repaint),
          ).size,
        { timeout: THREE_REFRESHES_MS, intervals: [100] },
      )
      .toBeGreaterThanOrEqual(3)
  } finally {
    frames = await page.evaluate(() => {
      const host = window as unknown as {
        __historyRepaintFrames: HistoryFrame[]
        __historyRepaintStop: () => void
      }
      host.__historyRepaintStop()
      return host.__historyRepaintFrames
    })
    await test.info().attach('history-first-repaint-samples', {
      body: JSON.stringify({ priorBodies, frames, backendAttempts: [...attempts] }),
      contentType: 'application/json',
    })
  }
  // (1) NO EARLIER CARD CHANGED WHILE THE PROGRAM RAN. Sampled on every frame,
  // because a body that took the new rows and gave them back between two
  // driver polls is still the split.
  expect(
    frames
      .filter((frame) => JSON.stringify(frame.bodies) !== JSON.stringify(priorBodies))
      .slice(0, 3),
  ).toEqual([])
  // (2) ONE running block, and the same cards in the same order, on every frame.
  expect(frames.filter((frame) => frame.total !== priorBlocks + 1 || frame.running !== 1)).toEqual(
    [],
  )
  expect(new Set(frames.map((frame) => frame.ids.join(','))).size).toBe(1)
  // The backend opened exactly one attempt for this program: the block is the
  // projection of a lifecycle fact, not of what the screen did.
  expect(attempts.size).toBe(1)
  // Its LAST screen is on the grid before q: the wait above stops at the
  // third, and a q typed during the program's own pause would quit it on its
  // third screen and leave this assertion asking for a screen nobody drew.
  await awaitRepaint(page, 'LIVE', REPAINT_SCREENS)
  await quitProgram(page)
  await promptReady(page)
  expect(attempts.size).toBe(1)
  await expect(page.locator('.pane.active .cmd-block')).toHaveCount(priorBlocks + 1)
  const frozen = page.locator('.pane.active .cmd-block').last()
  await expect(frozen).toHaveAttribute('data-outcome', 'success')
  for (let line = 1; line <= 20; line++) {
    await expect(frozen.locator('.cmd-output')).toContainText(
      `LIVE-${REPAINT_SCREENS}-LINE-${String(line).padStart(2, '0')}`,
    )
  }
  await expect(frozen.locator('.cmd-output')).not.toContainText(`LIVE-${REPAINT_SCREENS - 1}-LINE-`)
  // (1), after the program ended: the earlier cards still say what they said.
  const afterBodies = await page.evaluate((ids) => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    return Object.fromEntries(
      ids.map((id) => [
        id,
        pane
          .querySelector<HTMLElement>(`.cmd-block[data-entry-id="${id}"]`)
          ?.querySelector<HTMLElement>('.cmd-output')?.textContent ?? '(no body)',
      ]),
    )
  }, priorIds)
  expect(afterBodies).toEqual(priorBodies)
}

for (const afterClear of [false, true]) {
  test(`history-first primary CSI H and EL repaint locally${afterClear ? ' after clear' : ''} (nocx-peg8e.1.1)`, async ({
    page,
  }) => {
    test.setTimeout(180_000)
    await page.setViewportSize({ width: 1280, height: 800 })
    observeBackendAttempts(page)
    await page.goto('/')
    await promptReady(page)
    await expectHistoryFirstRepaint(page, afterClear)
  })
}
