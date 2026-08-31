/**
 * e2e: recall search — the panel's one field narrows the rung, the matched
 * text is bolded, and a search's Enter inserts instead of executing
 * (brief search-ui; the search half of nocx-ms7v).
 *
 * The acceptance check, in the owner's words:
 *
 *   Run two commands, open recall, type a substring of one of them, and
 *   only that one remains — driven through the real UI against the real
 *   backend, with the panel showing a search field carrying what you typed
 *   and the coverage line at its right-hand end.
 *
 * Three commands are run (the open-time rung-climb wants a useful page),
 * recall is opened with Up, and typing "alpha" into the panel — keys land
 * on the editor, the overlay's arbiter captures them — leaves exactly the
 * alpha command, with the beta and gamma commands gone, "1 result" in the
 * count, "alpha" bolded inside the surviving row, and the "oldest entry …"
 * coverage line on the same row as the field.
 *
 * Then the Enter rule that is the real fix (brief search-ui §3): with the
 * filter non-empty, Enter INSERTS the alpha command into the line without
 * running it (no new command block, panel closed); the second Enter — now
 * with an empty filter and the command visible — runs it. A blind run from
 * a typed search and a reviewed run of a visible command must not share
 * one keystroke.
 *
 * The backend runs with `history.retentionDays: 30` seeded into the
 * profile's settings.json BEFORE the first launch (the same posture as
 * history-persistence.spec.ts, nocx-rtg0.16): retention is the reason the
 * coverage line exists — with a 30-day horizon a search can only see part
 * of history, and the panel says how far back instead of presenting a
 * partial answer as the whole one.
 *
 * Drives the real frontend against cmd/nocx-server with NO Secret Service
 * for the backend and fresh XDG directories.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { appReadyForInput, VaultBackend, bindEndpoint, type DisposableRoot } from './harness'
import { readStand } from './stand'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

// Outside the ranges used by `wails dev` (34115), the e2e suite default
// (9876), `dev-web.sh` (9880/5180), `npm run dev` (5173), and the other
// history/vault specs (19876-19879), so the suites can run in parallel.

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'

interface XdgDirsResult {
  root: string
  data: string
  config: string
  cache: string
}

function createXdgDirs(): XdgDirsResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-recall-search-'))
  for (const d of ['data', 'config', 'cache'] as const) {
    mkdirSync(join(root, d), { recursive: true })
  }
  return {
    root,
    data: join(root, 'data'),
    config: join(root, 'config'),
    cache: join(root, 'cache'),
  }
}

function asXdgDirs(r: XdgDirsResult): DisposableRoot {
  // VaultBackend isolates the backend's WHOLE home inside a disposable root
  // (harness.ts) — the XDG trio alone never covered ~/.nocx and the rc
  // files, so the root travels with it.
  return { root: r.root }
}

const test = base

test.describe('recall: typing narrows, and the panel states its coverage', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // Seed retention before the backend starts — the coverage line's reason
    // for existing is a store that cannot see all of history.
    const settingsDir = join(xdg.config, 'nocx')
    mkdirSync(settingsDir, { recursive: true })
    writeFileSync(
      join(settingsDir, 'settings.json'),
      JSON.stringify({
        schemaVersion: 1,
        values: { 'history.retentionDays': 30 },
        secretRefs: {},
      }),
    )
    backend = new VaultBackend(serverBin(), asXdgDirs(xdg))
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('run three commands, type a substring, only the match remains, coverage line visible', async ({
    page,
  }) => {
    const marker = Date.now()
    const alpha = `echo alpha-${marker}`
    const beta = `echo beta-${marker}`
    const gamma = `echo gamma-${marker}`

    // ── Phase 1: launch, run three commands ─────────────────────────────
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)

    const input = page.locator(INPUT)
    await expect(input).toBeVisible({ timeout: 10_000 })
    await expect(input).toBeFocused({ timeout: 10_000 })
    for (const cmd of [alpha, beta, gamma]) {
      await input.fill(cmd)
      await page.keyboard.press('Enter')
      // The block's presence is the completed OSC 133 cycle, which is what
      // finalizes the ledger record; then give history.record a moment to
      // cross the socket (fire-and-forget by design, nocx-rtg0.13).
      const block = page.locator('.cmd-block', { hasText: cmd }).first()
      await expect(block).toBeVisible({ timeout: 15_000 })
      await page.waitForTimeout(800)
    }

    // ── Phase 2: open recall — all three commands on the rung ──────────
    await page.keyboard.press('ArrowUp')
    const panel = page.locator('.ui-floating-panel[data-variant="recall"]')
    await expect(panel).toBeVisible({ timeout: 10_000 })
    await expect(panel).toContainText(alpha, { timeout: 10_000 })
    await expect(panel).toContainText(beta)
    await expect(panel).toContainText(gamma)

    // ── Report 5: the recall panel is a FloatingPanel variant — the same
    //    sizing rule as the completion dropdown: content-sized between a
    //    floor and a ceiling, never full width, with the same footer. ──
    const recallBox = await panel.boundingBox()
    expect(recallBox).not.toBeNull()
    const paneBox = await page.locator('.pane.active').first().boundingBox()
    expect(paneBox).not.toBeNull()
    expect(recallBox!.width).toBeLessThan(paneBox!.width * 0.75)
    expect(recallBox!.width).toBeGreaterThanOrEqual(300)
    await page.screenshot({ path: '/tmp/nocx-recall-panel.png' })
    expect(recallBox!.width).toBeLessThanOrEqual(640)
    await expect(panel.locator('.ui-floating-panel__footer')).toBeVisible()
    console.log(`E2E recall panel width: ${recallBox!.width}px (report 5)`)

    // ── Phase 3: type the substring — only the match remains ───────────
    await page.keyboard.type('alpha')
    // The panel's one field — at the bottom edge, with a magnifier and a
    // caret — carries what was typed. It replaced the old "filter: …"
    // status line (brief search-ui).
    const field = panel.locator('.ui-search-field__input')
    await expect(field).toContainText('alpha', { timeout: 10_000 })
    await expect(panel).toContainText(alpha)
    await expect(panel.getByText(beta)).toHaveCount(0)
    await expect(panel.getByText(gamma)).toHaveCount(0)
    await expect(panel).toContainText('1 result')
    // The matched substring is bolded inside the surviving row, so the row
    // says why it matched.
    await expect(panel.locator('mark.ui-floating-panel__match')).toHaveCount(1)
    await expect(panel.locator('mark.ui-floating-panel__match')).toHaveText('alpha')

    // ── Phase 4: the coverage line is on screen, and it is honest ──────
    await expect(panel).toContainText(/oldest entry/, { timeout: 10_000 })
    // The store answered (three rows exist), so the panel must NOT claim
    // "this session only".
    await expect(panel.getByText('this session only')).toHaveCount(0)

    // ── Phase 5: a search's Enter INSERTS without running; the second
    //    Enter — the command now visible in the line — runs it. A blind
    //    run from a typed search and a reviewed run of a visible command
    //    must not share one keystroke (brief search-ui §3). ────────────
    await page.keyboard.press('Enter')
    await expect(panel).toBeHidden({ timeout: 10_000 })
    await expect(input).toContainText(alpha, { timeout: 10_000 })
    const blocksBefore = await page.locator('.cmd-block').count()
    await page.waitForTimeout(300) // give a phantom run a moment to surface
    expect(await page.locator('.cmd-block').count()).toBe(blocksBefore)
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore + 1, {
      timeout: 15_000,
    })
  })
})
