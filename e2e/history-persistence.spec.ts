/**
 * e2e: history that actually records a command (nocx-rtg0.13, nocx-rtg0.14).
 *
 * The epic's acceptance, in the owner's words:
 *
 *   Run a command. Restart. Press Up. The command is there, and the panel
 *   says source: store.
 *
 * The backend runs with `history.retentionDays: 30` written into the
 * profile's settings.json BEFORE the first launch, the way a user's profile
 * carries it. This is deliberate (nocx-rtg0.16): the defect this spec
 * guards was hidden by the default of 0 — with retention off the age sweep
 * never runs, so the acceptance e2e stayed green while the frontend stamped
 * startedAt/endedAt with performance.now() and every user who set retention
 * lost every command microseconds after it was recorded (1970 timestamps,
 * swept in the same writer turn as the INSERT). Do not "simplify" this back
 * to the default: the setting is the point.
 *
 * Drives the real frontend against cmd/nocx-server with NO Secret Service for
 * the backend (the `true` argument) and fresh XDG directories, so the
 * content key is DERIVED at startup from the machine salt — the vault and
 * its seal are irrelevant to it, which is the point of nocx-rtg0.14.
 *
 * The command is typed and Enter pressed; the OSC 133 D finalizes the
 * ledger record, which crosses the control plane as history.record (AD-1 as
 * amended). The backend is restarted — a fresh launch, fresh token, fresh
 * session — and the page reloaded. Up on the empty prompt opens recall, and
 * the recorded command must be there with source=store: the panel shows the
 * command and does NOT carry the "this session only" badge, because the
 * session ledger is empty after the reload and only the store could have
 * answered.
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

// Two distinct ports so restart never conflicts with the first instance's
// TIME_WAIT. Outside the ranges used by `wails dev` (34115), the e2e suite
// default (9876), `dev-web.sh` (9880/5180), and `npm run dev` (5173).
// Distinct from vault.spec's 19876/19877 so the suites can run in parallel.

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'

interface XdgDirsResult {
  root: string
  data: string
  config: string
  cache: string
}

function createXdgDirs(): XdgDirsResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-history-'))
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

// The backend is given the disposable ROOT, and derives the rest from it.
//
// This used to hand over { data, config, cache } as an `XdgDirs`, a type
// harness.ts no longer has: the boundary moved from three XDG directories to
// one disposable home, because XDG_CONFIG_HOME outranks $HOME and redirecting
// only the former let a backend walk straight back out (nocx-ti8w). The spec
// was never updated, `this.disposable.root` was undefined, and the run died in
// path.resolve — invisible until CI built the devharness these specs then needed
// (nocx-azxe.2), and invisible to the type checker because nothing type-checks
// e2e/.
function asDisposableRoot(r: XdgDirsResult): DisposableRoot {
  return { root: r.root }
}

const test = base

test.describe('history: a command survives a restart and recall answers from the store', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: XdgDirsResult

  test.beforeAll(() => {
    xdg = createXdgDirs()
    // Seed the profile with the owner's retention value before the backend
    // starts (nocx-rtg0.16 — see the header comment): the age sweep must be
    // live for this spec to prove anything, and it must run against real
    // wall-clock timestamps.
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
    // `true` = no Secret Service for this backend, regardless of the
    // session the suite runs in — the derived-key branch is the point.
    backend = new VaultBackend(serverBin(), asDisposableRoot(xdg))
  })

  test.afterAll(() => {
    backend?.stop()
  })

  test('run a command, restart, press Up: the command is there, source=store', async ({ page }) => {
    const marker = `echo history-e2e-${Date.now()}`

    // ── Phase 1: first launch, run a command ─────────────────────────────
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)

    const input = page.locator(INPUT)
    await expect(input).toBeVisible({ timeout: 10_000 })
    await expect(input).toBeFocused({ timeout: 10_000 })
    await input.fill(marker)
    await page.keyboard.press('Enter')

    // The command ran: post-scrollback, its output lives in a DOM scrollback
    // block (the OSC 133 D clears the live viewport into the block), so the
    // block's presence IS the completed OSC 133 cycle — which is also what
    // finalizes the ledger record. Then give the history.record round trip a
    // moment to land — this is an integration test of a real local socket,
    // and the record is fire-and-forget by design.
    const block = page.locator('.cmd-block', { hasText: marker }).first()
    await expect(block).toBeVisible({ timeout: 15_000 })

    // THE RECORD REACHED THE STORE — established, not waited out.
    //
    // This was `waitForTimeout(800)`, on the honest reasoning that
    // history.record is fire-and-forget by design and the round trip needs a
    // moment. 800 ms is a claim about how fast the machine is, written on a
    // machine where it held: this spec passes alone in 6.9s and fails inside
    // the full suite on both engines, at the runner's four vCPU, with the
    // panel reporting "0 results" after the restart. By then the record can
    // never arrive — the backend it was going to has been replaced — so the
    // failure lands three phases away from its cause and reads as a lost
    // feature (nocx-cbtc's method note, and the fourth instance of this shape
    // on this branch).
    //
    // The wait is now on the product's own answer to the same question. The
    // recall panel says where its rows came from, and "this session only" is
    // what it says when nothing but the in-memory ledger replied. Its absence
    // beside the marker IS the store having answered — which is exactly what
    // phase 3 asserts, so the premise is established with the very statement
    // the test is about, and a store that never records still fails.
    const panel1 = page.locator('.ui-floating-panel[data-variant="recall"]')
    await expect
      .poll(
        async () => {
          await page.keyboard.press('ArrowUp')
          const answered = await panel1
            .filter({ hasText: marker })
            .filter({ hasNotText: 'this session only' })
            .waitFor({ state: 'visible', timeout: 2_000 })
            .then(() => true)
            .catch(() => false)
          // Esc closes exactly the panel and leaves the line as it was; the
          // next Up reopens it against fresh results.
          await page.keyboard.press('Escape')
          return answered
        },
        { timeout: 30_000, intervals: [250] },
      )
      .toBe(true)
    await expect(input).toBeFocused({ timeout: 10_000 })

    // ── Phase 2: restart the backend (fresh launch, fresh token) ────────
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)
    await expect(input).toBeVisible({ timeout: 10_000 })
    await expect(input).toBeFocused({ timeout: 10_000 })

    // ── Phase 3: press Up — the recall panel, served by the store ───────
    await page.keyboard.press('ArrowUp')
    const panel = page.locator('.ui-floating-panel[data-variant="recall"]')
    await expect(panel).toBeVisible({ timeout: 10_000 })
    // The command is there — and the session ledger is empty after the
    // reload, so only the store could have answered: the "this session
    // only" badge must be absent.
    await expect(panel).toContainText(marker, { timeout: 10_000 })
    await expect(panel.getByText('this session only')).toHaveCount(0)
  })
})
