// nocx-ipuy3, the owner's decision of 2026-11-08: the missing-output CARD is
// gone, and what replaced it is one quiet line in a command's own history, at
// the place its output was cut.
//
// What a person does: runs a command whose output passes the session's size
// limit, comes back to the tab later, and scrolls up through what the block
// kept. The card used to sit above the pane with a session-level byte count —
// a number that named no place, that a full-screen program's redraws kept
// growing, and that came back every time the window did. Now the block's
// stored rows carry absolute row numbers, so the hole between two kept rows
// IS the place, and the line at it says why. A person sees it only by
// scrolling to it.
//
// WHY THE SECOND CASE IS THE POINT. A full-screen program redraws the
// alternate screen, and the alternate screen is not history: its rows never
// leave the screen, so the store keeps no readable output to cut, and there is
// no marker at all. That is the defect the owner was reading: a herdr tab
// announced 80.2 kB missing over and over for redraws nobody could read back.
//
// THE NUMBERS ARE THE LEDGER'S OWN. The count on the line comes from the rows
// themselves, and this spec compares it with the block-rows artifact's
// droppedRows — the store's count of the rows its limit took — so a line that
// drifts from the ledger fails here rather than reading plausibly. The old
// spec compared the CARD with the recording's account; the account and the
// card are both gone (recovery-account.ts had no reader left), and this is the
// check that replaced it.
//
// WAITING IS ON STATES: the command's own last line, the sealed artifact's
// verdict, the marker's visibility after a scroll. No duration is waited out,
// and nothing is retried with a wider timeout.

import { expect, type Browser, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const test = base

/** At the declaration's minimum (internal/settings/settings.go): 16 KiB. */
const CAP_KB = 16
/** Ordinary lines, each a row that leaves the screen: the store keeps the
 *  head and the tail and drops the middle, which is the cut under test. */
const LINES = 1500
/** One screenful a full-screen program repaints, and how many times it
 *  repaints: 600 × 40 rows of redraws is far past the limit and leaves no
 *  readable row behind, because the alternate screen has no history. */
const REDRAW_ROWS = 40
const REDRAWS = 600

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
  attached: boolean
}

interface RecoveryStatus {
  produced: number
  gaps: { start: number; end: number; reason: string }[]
}

interface Artifact {
  id: string
  mediaType: string
  state: string
  truncated: string | null
  payload: { droppedRows?: number }
}

interface LedgerEntry {
  entry: { id: string; intent: string }
  artifacts: Artifact[]
}

async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

async function liveSessions(ep: BackendEndpoint): Promise<LiveSession[]> {
  return (await ask<{ sessions: LiveSession[] }>(ep, 'sessions.live', {})).sessions
}

function recoveryStatus(ep: BackendEndpoint, live: LiveSession): Promise<RecoveryStatus> {
  return ask<RecoveryStatus>(ep, 'session.recoveryStatus', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
  })
}

async function openWindow(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

/** The missing-output presentations this build no longer has. Both host
 *  classes are named so a card that comes back fails here by name. */
const CARD = '.nocx-recovery-notice, [data-notice-kind="live-output-gap"]'

/** The one line the block's own history carries at a cut. */
const MARKER = '.cmd-output-gap'

const capLoss = (status: RecoveryStatus): number =>
  status.gaps
    .filter((gap) => gap.reason === 'cap')
    .reduce((total, gap) => total + gap.end - gap.start, 0)

/** The command block's own ledger entry: the artifact holds the rows the
 *  store kept, and its dropped count is what the marker must agree with. */
async function blockRows(ep: BackendEndpoint, entryId: string) {
  const entry = await ask<LedgerEntry>(ep, 'ledger.get', { id: entryId })
  const rows = entry.artifacts.find((a) => a.mediaType === 'application/x-nocx-rows')
  return rows ?? null
}

test.describe('the cut is in the history, and the card is not (nocx-ipuy3)', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-output-cut-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('an ordinary command past the size limit shows the cut when scrolled to', async ({
    browser,
  }) => {
    test.setTimeout(300_000)
    const ep = await backend.start()

    // The bound is the product's own setting, read live by the recorder; set
    // before the first append fixes the reservation, so before any command.
    const setup = await openControlPlane(ep.port, ep.token)
    try {
      await setup.call('settings.set', { key: 'history.outputCapKB', value: CAP_KB })
    } finally {
      setup.close()
    }

    const page = await openWindow(browser, ep)
    await promptReady(page)

    await clickIntoEditor(page)
    await page.keyboard.type(
      `i=0; while [ "$i" -lt ${LINES} ]; do echo CUTLINE-$i; i=$((i+1)); done; echo NOCX-CUT-DONE`,
    )
    await page.keyboard.press('Enter')
    await expect(
      page
        .locator('.pane.active [role="grid"][aria-label="Terminal output"]')
        .getByRole('row')
        .filter({ hasText: 'NOCX-CUT-DONE' })
        .first(),
    ).toBeVisible({ timeout: 120_000 })

    const [live] = await liveSessions(ep)
    expect(live).toBeDefined()
    expect(live.paneId).not.toBeNull()
    const pane = page.locator(`.pane[data-session-id="${live.sessionId}"]`)

    // The command's own block, and the store's verdict on what it kept. The
    // row cap is what made the cut, so the seal carries the cap verdict.
    let entryId = ''
    await expect
      .poll(
        async () => {
          entryId =
            (await pane
              .locator('.cmd-block[data-entry-id]')
              .first()
              .getAttribute('data-entry-id')) ?? ''
          return entryId
        },
        { timeout: 60_000, message: 'the command block never joined its ledger entry' },
      )
      .not.toBe('')
    let droppedRows = 0
    await expect
      .poll(
        async () => {
          const rows = await blockRows(ep, entryId)
          if (rows === null) return -1
          droppedRows = rows.payload.droppedRows ?? 0
          return rows.state === 'sealed' && rows.truncated === 'cap' ? droppedRows : -1
        },
        { timeout: 60_000, message: 'the command’s stored rows never reported a cut' },
      )
      .toBeGreaterThan(0)

    // ── the window comes back: the history is what a person reads ────────
    await page.reload()
    await expect(page.locator(`.pane[data-session-id="${live.sessionId}"]`)).toBeVisible({
      timeout: 120_000,
    })
    await expect(page.locator('.ui-connection-overlay')).toBeHidden({ timeout: 120_000 })
    const restored = pane.locator('.cmd-block[data-restored="true"]')
    await expect(restored).toHaveCount(1, { timeout: 120_000 })

    // NO CARD, in any case — this is the owner's first sentence.
    await expect(page.locator(CARD)).toHaveCount(0)

    // ── the line at the cut ──────────────────────────────────────────────
    const marker = restored.locator(MARKER)
    await expect(marker).toHaveCount(1, { timeout: 60_000 })
    await expect(marker).not.toBeInViewport()

    // It sits BETWEEN the rows it cuts, and the number it states is the gap
    // the block's own row numbers leave — never a number from somewhere else.
    const cut = await marker.evaluate((el) => {
      const before = el.previousElementSibling?.textContent ?? ''
      const after = el.nextElementSibling?.textContent ?? ''
      const number = (text: string) => {
        const found = text.match(/CUTLINE-(\d+)/)
        return found === null ? null : Number(found[1])
      }
      return { before: number(before), after: number(after), text: el.textContent ?? '' }
    })
    expect(cut.before).not.toBeNull()
    expect(cut.after).not.toBeNull()
    expect(cut.after! - cut.before!).toBe(droppedRows + 1)
    expect(cut.text).toContain("Earlier output was not kept: this session's size limit dropped")
    expect(cut.text).toContain(`${droppedRows} rows here.`)

    // The person scrolls up to it: it is not on screen at the tail, and the
    // wheel finds it. "Found" means INSIDE the scroller's box — `isVisible`
    // answers whether the element has a box at all, which a row 200 rows
    // above the tail has whether or not anybody scrolled to it.
    const scroller = pane.locator('.scrollback-area')
    await scroller.hover()
    const inTheScrollbox = (): Promise<boolean> =>
      marker.evaluate((el) => {
        const row = el.getBoundingClientRect()
        const box = el.closest('.scrollback-area')?.getBoundingClientRect()
        if (!box) return false
        return row.bottom > box.top && row.top < box.bottom
      })
    await expect
      .poll(
        async () => {
          if (await inTheScrollbox()) return true
          // One screen's worth per gesture, so the row cannot be stepped
          // over: a wheel notch larger than the reader's box can jump past
          // the one line it was aimed at.
          await page.mouse.wheel(0, -400)
          return false
        },
        { timeout: 60_000, intervals: [150], message: 'the cut was never reached by scrolling' },
      )
      .toBe(true)
    await expect(marker).toBeInViewport()
    await page.context().close()
  })

  test('a full-screen program’s redraws past the size limit show no marker', async ({
    browser,
  }) => {
    test.setTimeout(300_000)
    const ep = await backend.start()

    const setup = await openControlPlane(ep.port, ep.token)
    try {
      await setup.call('settings.set', { key: 'history.outputCapKB', value: CAP_KB })
    } finally {
      setup.close()
    }

    const page = await openWindow(browser, ep)
    await promptReady(page)

    // A full-screen program: enter the alternate screen, repaint a screenful
    // from the home corner REDRAWS times, leave. Every byte is a redraw, and
    // 600 screens of them are far past the limit.
    const fill = 'x'.repeat(56)
    await clickIntoEditor(page)
    await page.keyboard.type(
      `printf '\\033[?1049h'; i=0; while [ "$i" -lt ${REDRAWS} ]; do printf '\\033[H'; ` +
        `j=0; while [ "$j" -lt ${REDRAW_ROWS} ]; do printf 'REDRAW-%s-%s ${fill}\\n' "$i" "$j"; j=$((j+1)); done; ` +
        `i=$((i+1)); done; printf '\\033[?1049l'; echo NOCX-FULLSCREEN-DONE`,
    )
    await page.keyboard.press('Enter')
    await expect(
      page
        .locator('.pane.active [role="grid"][aria-label="Terminal output"]')
        .getByRole('row')
        .filter({ hasText: 'NOCX-FULLSCREEN-DONE' })
        .first(),
    ).toBeVisible({ timeout: 180_000 })

    // The limit really was crossed: the recording dropped bytes of redraws.
    const [live] = await liveSessions(ep)
    expect(live).toBeDefined()
    await expect
      .poll(async () => capLoss(await recoveryStatus(ep, live)), {
        timeout: 60_000,
        message: 'the redraws never passed the size limit, so this case proves nothing',
      })
      .toBeGreaterThan(0)

    // NO CARD in any case — the owner's complaint was this one returning.
    await expect(page.locator(CARD)).toHaveCount(0)

    // …and the window comes back, which is where the card used to reappear.
    await page.reload()
    const pane = page.locator(`.pane[data-session-id="${live.sessionId}"]`)
    await expect(pane).toBeVisible({ timeout: 120_000 })
    await expect(page.locator('.ui-connection-overlay')).toBeHidden({ timeout: 120_000 })
    await expect(page.locator(CARD)).toHaveCount(0)

    // Nothing readable was lost, so there is no line at all: the alternate
    // screen's rows never left the screen, and the store kept none of them.
    let entryId = ''
    await expect
      .poll(
        async () => {
          entryId =
            (await pane
              .locator('.cmd-block[data-entry-id]')
              .first()
              .getAttribute('data-entry-id')) ?? ''
          return entryId
        },
        { timeout: 60_000, message: 'the full-screen command never joined its ledger entry' },
      )
      .not.toBe('')
    await expect
      .poll(
        async () => {
          const rows = await blockRows(ep, entryId)
          if (rows === null) return null
          return { state: rows.state, truncated: rows.truncated }
        },
        { timeout: 60_000, message: 'the full-screen command’s rows never sealed' },
      )
      .toEqual({ state: 'sealed', truncated: null })
    await expect(pane.locator(MARKER)).toHaveCount(0)
    await page.context().close()
  })
})
