/**
 * A block's cache follows the retention the store applied (nocx-4y82c.1.24).
 *
 * The store bounds one command's stored rows by `history.outputCapKB`: it keeps
 * a head run and a tail run and drops the middle. The renderer caches the rows
 * it reads, and the two only agree if a new read's retained ranges are allowed
 * to take rows AWAY. They were not: the merge appended every page to the cache,
 * so a command that crossed the cap kept growing the renderer's copy — rows the
 * store had already dropped stayed cached, counted and selectable — while its
 * geometry said the block held only what survived.
 *
 * WHAT A PERSON DOES. Runs a command whose output crosses the cap, goes on
 * running it past the cap a second time, and reads the block. What this spec
 * watches: the block reserves exactly the retained height, caches and paints no
 * more rows than that, shows the surviving tail and the line stating what the
 * cap took, and still reaches the surviving head by scrolling to it.
 *
 * THE TWO PHASES ARE THE POINT. A single burst can be served by one read and
 * would say nothing about retention moving under a cache; the silent read in
 * the middle makes the first phase's retained tail settled and painted before
 * the second phase evicts it. Waiting is on states — the last printed row, the
 * ledger's sealed verdict, the row a scroll causes to arrive — never a duration.
 */

import { expect, type Browser, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const test = base

/** The declaration's minimum (internal/settings/settings.go): 16 KiB, small
 *  enough that one command's ordinary lines fill it many times over. */
const CAP_KB = 16
/** Rows in each phase. Two phases of this many cross the 16 KiB cap in both,
 *  so the second one evicts rows the first one had already settled. */
const PHASE_ROWS = 2000
const TOTAL_ROWS = PHASE_ROWS * 2

/** One row's text: the marker a person reads, padded so the artifact's bytes
 *  per row stay ordinary rather than pathologically small. */
const marker = (row: number): string => `KEEP-${String(row).padStart(5, '0')}-xxxxx`

const command = [
  `i=1; while [ "$i" -le ${PHASE_ROWS} ]; do printf 'KEEP-%05d-xxxxx\\n' "$i"; i=$((i+1)); done`,
  // Silent: the keystroke that ends the pause must not become output of its
  // own (the same reason e2e/backend-block-rows.spec.ts reads silently).
  'IFS= read -rs _',
  `while [ "$i" -le ${TOTAL_ROWS} ]; do printf 'KEEP-%05d-xxxxx\\n' "$i"; i=$((i+1)); done`,
].join('; ')

interface RowsArtifact {
  id: string
  mediaType: string
  state: string
  truncated: string | null
  payload: { droppedRows?: number }
}

interface LedgerEntry {
  entry: { id: string; intent: string }
  artifacts: RowsArtifact[]
}

/** One page read's answer for a rows artifact. An OPEN artifact's payload
 *  carries no geometry — it is written at close — so the runs a block holds
 *  while it runs come with every read (ADR-ywzcpbqe decision 1). */
interface ArtifactRead {
  id: string
  rowCount: number
  endRow: number | null
  retainedRanges: Array<{ from: number; to: number }>
}

async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

/** The store's account of one block's rows: how many runs it keeps, how many
 *  rows those are, how many the cap took, and whether it is finished. The
 *  block's DOM is compared with this, never with a number of the spec's own. */
async function storedRows(ep: BackendEndpoint, entryId: string) {
  const entry = await ask<LedgerEntry>(ep, 'ledger.get', { id: entryId })
  const artifact = entry.artifacts.find((a) => a.mediaType === 'application/x-nocx-rows')
  if (artifact === undefined) return null
  const read = await ask<ArtifactRead>(ep, 'ledger.artifact', {
    id: artifact.id,
    rowLimit: 1,
  })
  const ranges = read.retainedRanges ?? []
  return {
    id: artifact.id,
    sealed: artifact.state === 'sealed',
    truncated: artifact.truncated,
    retained: read.rowCount,
    retainedRanges: ranges,
    // A cap eviction leaves a head run and a tail run; the hole between the
    // first two runs is the rows it took. At close the store states the same
    // count in its payload, and the two must agree.
    gapRows: ranges.length > 1 ? ranges[1].from - ranges[0].to : 0,
    droppedRows: artifact.payload.droppedRows ?? 0,
  }
}

async function openWindow(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

test.describe('sustained output across the retention cap (nocx-4y82c.1.24)', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-paged-retention-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('cached and painted rows stay inside the store’s retention, and the rows it kept stay readable', async ({
    browser,
  }) => {
    test.setTimeout(300_000)
    const ep = await backend.start()

    // The bound is the product's own setting, read live by the recorder; set
    // before the first command, which is when the reservation is fixed.
    const setup = await openControlPlane(ep.port, ep.token)
    try {
      await setup.call('settings.set', { key: 'history.outputCapKB', value: CAP_KB })
    } finally {
      setup.close()
    }

    const page = await openWindow(browser, ep)
    await promptReady(page)

    const input = page.locator('.pane.active .nocx-editor-input')
    await input.fill(command)
    await page.keyboard.press('Enter')

    const running = page.locator('.pane.active .cmd-block.cmd-block-running')
    await expect(running).toHaveCount(1, { timeout: 30_000 })
    const entryId = await running.getAttribute('data-entry-id')
    expect(entryId).toBeTruthy()

    const liveGrid = page.locator('.pane.active .terminal-live-container')
    const body = running.locator('.cmd-output')

    // ── the first crossing, and the pause that settles it ────────────────
    await expect(liveGrid).toContainText(marker(PHASE_ROWS), { timeout: 120_000 })
    // The rows the cap kept are already read and painted: the head at the top
    // of the body, the tail at the bottom, one line between them saying what
    // was dropped. That is the state the second phase evicts from.
    await expect(body).toContainText(marker(1), { timeout: 60_000 })

    let kept = null as Awaited<ReturnType<typeof storedRows>> | null
    await expect
      .poll(
        async () => {
          kept = await storedRows(ep, entryId as string)
          return kept?.gapRows ?? 0
        },
        { timeout: 60_000, message: 'the cap never split the runs, so this case proves nothing' },
      )
      .toBeGreaterThan(0)
    // The block holds no more rows than the store kept, and the store kept far
    // fewer than the command printed. Polled because the last growth read may
    // still be landing: the cache is clipped when that read is accepted.
    await expect
      .poll(
        async () =>
          Number(await body.getAttribute('data-rows-loaded')) <=
          ((await storedRows(ep, entryId as string))?.retained ?? 0),
        { timeout: 60_000, message: 'the block kept rows the store no longer holds' },
      )
      .toBe(true)
    expect(kept!.retained).toBeLessThanOrEqual(PHASE_ROWS)
    // One retained head run, one retained tail run: exactly one line says what
    // the cap took. A body showing more is a body holding rows the store has
    // already dropped, each stale fragment opening a gap of its own.
    await expect(body.locator('.cmd-output-gap')).toHaveCount(1, { timeout: 60_000 })

    // ── the second crossing ─────────────────────────────────────────────
    await expect(page.locator('.pane.active .ui-terminal-input')).toBeFocused()
    await page.keyboard.press('Enter')
    // The block's freeze is the command's own end: its rows artifact is sealed
    // and its body is the reserved one, not the growing hold.
    await expect(running).toHaveCount(0, { timeout: 120_000 })

    const frozen = page.locator(`.pane.active .cmd-block[data-entry-id="${entryId}"]`)
    await expect(frozen).toBeVisible({ timeout: 30_000 })
    await expect(frozen).toHaveAttribute('data-rows-sealed', 'true', { timeout: 60_000 })

    let sealed = null as Awaited<ReturnType<typeof storedRows>> | null
    await expect
      .poll(
        async () => {
          sealed = await storedRows(ep, entryId as string)
          return sealed !== null && sealed.sealed && sealed.truncated === 'cap'
        },
        { timeout: 60_000, message: 'the closed rows artifact never reported the cap' },
      )
      .toBe(true)

    const frozenBody = frozen.locator('.cmd-output')
    // The height is the retained geometry, the cache is no larger than it, and
    // the painted rows are no more than the cache.
    await expect(frozenBody).toHaveAttribute('data-rows-count', String(sealed!.retained), {
      timeout: 60_000,
    })
    const loaded = Number(await frozenBody.getAttribute('data-rows-loaded'))
    expect(loaded).toBeLessThanOrEqual(sealed!.retained)
    expect(await frozenBody.locator('.term-grid-row').count()).toBeLessThanOrEqual(sealed!.retained)
    // The surviving tail is readable, and the one line at the cut states the
    // count the store itself reports.
    await expect(frozenBody).toContainText(marker(TOTAL_ROWS))
    // The runs say how many rows the cap took, the closed payload says it too,
    // and the one line at the cut states that same count.
    expect(sealed!.droppedRows).toBe(sealed!.gapRows)
    await expect(frozen.locator('.cmd-output-gap')).toHaveCount(1)
    await expect(frozen.locator('.cmd-output-gap')).toContainText(
      `${sealed!.droppedRows} rows here`,
    )

    // ── the surviving head is still reachable ───────────────────────────
    const scroller = page.locator('.pane.active .scrollback-area')
    await scroller.evaluate((el) => el.scrollTo({ top: 0, behavior: 'instant' }))
    const firstRow = frozenBody.locator('.term-grid-row').first()
    await expect(firstRow).toContainText(marker(1), { timeout: 60_000 })
    await expect(firstRow).toBeInViewport()
    // Reading the head back does not grow the cache past the retention either.
    expect(Number(await frozenBody.getAttribute('data-rows-loaded'))).toBeLessThanOrEqual(
      sealed!.retained,
    )
    await page.context().close()
  })
})
