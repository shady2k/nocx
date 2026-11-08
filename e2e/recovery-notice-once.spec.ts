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

// nocx-ipuy3. THE SAME HOLE, ANNOUNCED ONCE.
//
// What a person does: works in a tab until the session's own size limit has
// dropped the oldest of what it printed, then closes the window and opens it
// again. The reclaim card names the hole the first time. Closing and opening
// the window again must NOT name it again — those bytes were lost once, long
// before the first close, and announcing them again reads as the close losing
// output. A hole that grew in between is still news, and only its growth is.
//
// The hole is made, not hoped for: the setting is the product's own per-command
// cap (history.outputCapKB, at its declared minimum) and the command prints
// four times that, so the bound must evict. Its existence is asserted off the
// coordinator's own recoveryStatus before any window is reopened, which is the
// same fact the card renders (recovery-account.ts is what both read).
//
// WAITING IS ON STATES: the recording's own cap gap, the pane's session id,
// and the prompt editor owning input. The second reload's silence is asserted
// after the prompt is back — the card is mounted in the same bind that starts
// the session, so a prompt on screen is proof the decision was taken.

const test = base

const NOTICE = '.nocx-recovery-notice'
/** At the declaration's minimum (internal/settings/settings.go): 16 KiB. */
const CAP_KB = 16
/** Four times the cap, in 60-byte chunks that cost stream bytes and not rows:
 *  each one fits the grid line and the carriage return rewrites it. */
const CHUNKS = 1200

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

/** The RECLAIM card, told apart from the live-loss card that shares its host
 *  class: this one is about the session's output that is gone for good. */
const reclaimCard = (page: Page) =>
  page.locator(NOTICE).filter({ hasText: "of this session's output is missing" })

const capLoss = (status: RecoveryStatus): number =>
  status.gaps
    .filter((gap) => gap.reason === 'cap')
    .reduce((total, gap) => total + gap.end - gap.start, 0)

test.describe('the missing-output card is announced once (nocx-ipuy3)', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-recovery-once-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('reopening the window twice over one hole shows the card once', async ({ browser }) => {
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

    // ── the hole, made by the session's own size limit ────────────────────
    await clickIntoEditor(page)
    await page.keyboard.type(
      `f=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; ` +
        `i=0; while [ "$i" -lt ${CHUNKS} ]; do printf '%s\\r' "$f"; i=$((i+1)); done; ` +
        `echo NOCX-CAP-DONE; sleep 600`,
    )
    await page.keyboard.press('Enter')
    // The command's own last line is the anchor that the filler is out: the
    // block stays running on the trailing sleep, so a settled block is not
    // something this shape has.
    await expect(
      page
        .locator('.pane.active [role="grid"][aria-label="Terminal output"]')
        .getByRole('row')
        .filter({ hasText: 'NOCX-CAP-DONE' })
        .first(),
    ).toBeVisible({ timeout: 60_000 })

    const [live] = await liveSessions(ep)
    expect(live).toBeDefined()
    expect(live.paneId).not.toBeNull()
    await expect
      .poll(async () => capLoss(await recoveryStatus(ep, live)), {
        timeout: 60_000,
        message: 'the recording never dropped output, so there is no hole to report',
      })
      .toBeGreaterThan(0)

    // ── the window closes and opens again: the hole is news ──────────────
    await page.reload()
    await expect(page.locator(`.pane[data-session-id="${live.sessionId}"]`)).toBeVisible({
      timeout: 120_000,
    })
    await expect(page.locator('.ui-connection-overlay')).toBeHidden({ timeout: 120_000 })
    // The reclaimed screen is back, which is the reclaim finishing: the card
    // is mounted in the bind that starts the session, so it is decided by now.
    await expect(
      page
        .locator(
          `.pane[data-session-id="${live.sessionId}"] [role="grid"][aria-label="Terminal output"]`,
        )
        .getByRole('row')
        .filter({ hasText: 'NOCX-CAP-DONE' })
        .first(),
    ).toBeVisible({ timeout: 120_000 })
    await expect(reclaimCard(page)).toBeVisible({ timeout: 60_000 })
    await expect(reclaimCard(page).locator('.ui-status-card__title')).toContainText(
      "of this session's output is missing",
    )

    // ── …and again: the same bytes are not news a second time ────────────
    await page.reload()
    await expect(page.locator(`.pane[data-session-id="${live.sessionId}"]`)).toBeVisible({
      timeout: 120_000,
    })
    await expect(page.locator('.ui-connection-overlay')).toBeHidden({ timeout: 120_000 })
    await expect(
      page
        .locator(
          `.pane[data-session-id="${live.sessionId}"] [role="grid"][aria-label="Terminal output"]`,
        )
        .getByRole('row')
        .filter({ hasText: 'NOCX-CAP-DONE' })
        .first(),
    ).toBeVisible({ timeout: 120_000 })
    // The reclaimed screen is up and the prompt's own editor is back; the
    // card decision was taken before either, and it said nothing.
    await expect(reclaimCard(page)).toHaveCount(0)

    // The session is still the same one, still running: the silence is about
    // one hole, not about a window that failed to reclaim anything.
    const [after] = await liveSessions(ep)
    expect(after.sessionId).toBe(live.sessionId)
    expect(after.attached).toBe(true)

    await page.context().close()
  })
})
