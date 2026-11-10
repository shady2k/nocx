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
 *     owner met. The same frames count distinct clock readings in the
 *     program's own header — the wait for the third one is bounded — so "at
 *     least three refreshes" is the program's fact and not a duration this
 *     spec chose.
 *  2. After `q` (typed into the grid, as a person quits top) exactly one
 *     block remains: not running, outcome `success` (top exits 0 on quit),
 *     and its stored rows carry the program's screen — the summary lines and
 *     the process list — rather than an empty card or a second frame.
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

/** The program's interval, and the bound on waiting for its third repaint. A
 *  clock reading in its header changes once per interval, so three distinct
 *  readings are three refreshes — the program's own fact, not a duration this
 *  spec chose. */
const REFRESH_MS = 1000
const THREE_REFRESHES_MS = 20_000

interface Frame {
  /** Blocks on the pane, and how many of them claim to be running. */
  total: number
  running: number
  /** The clock reading in the program's own header, when it has one. */
  clock: string | null
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
      host.__topSplitSamples!.push({
        total: blocks.length,
        running: blocks.filter((b) => b.classList.contains('cmd-block-running')).length,
        clock: /\b\d{2}:\d{2}:\d{2}\b/.exec(grid)?.[0] ?? null,
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
  return frames.filter((f) => f.running !== 1 || f.total !== 1)
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

  const frames = await stopSampling(page)
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

test('a full-screen program repainting the primary screen keeps one block over ssh (nocx-evj4b.1.9)', async ({
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
        // The piped integration carrier: the far shell's prompt is
        // authenticated, so the block model — not a conventional grid — owns
        // the pane, which is the mode the owner reported.
        desiredMode: 'script',
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
    await promptReady(page)
    await expectOneBlockAcrossRepaints(page)
    // The pane really is the far host's: the block's where-line names the
    // fixture's destination, so a local pane could not pass this spec.
    await expect(page.locator('.pane.active .cmd-block .cmd-header-meta')).toContainText(
      `${fixture.user}@${fixture.host}`,
    )
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
