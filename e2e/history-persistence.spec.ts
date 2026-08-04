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
 * Drives the real frontend against cmd/devharness with NO Secret Service for
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
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, type BackendEndpoint, type DisposableRoot } from './harness'

const DEVHARNESS_BIN = process.env.NOCX_VAULT_BIN ?? '/tmp/nocx-devharness'

// Two distinct ports so restart never conflicts with the first instance's
// TIME_WAIT. Outside the ranges used by `wails dev` (34115), the e2e suite
// default (9876), `dev-web.sh` (9880/5180), and `npm run dev` (5173).
// Distinct from vault.spec's 19876/19877 so the suites can run in parallel.
const FIRST_PORT = 19878
const SECOND_PORT = 19879

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'

interface DisposableRootResult {
  root: string
  config: string
}

function createDisposableRoot(): DisposableRootResult {
  const root = mkdtempSync(join(tmpdir(), 'nocx-history-'))
  const home = join(root, 'home')
  const config =
    process.platform === 'darwin'
      ? join(home, 'Library', 'Application Support', 'nocx-dev')
      : join(home, '.config', 'nocx-dev')
  mkdirSync(config, { recursive: true })
  return { root, config }
}

function asDisposableRoot(r: DisposableRootResult): DisposableRoot {
  return { root: r.root }
}

/** Inject Wails stubs pointing at the given backend endpoint (the same
 *  bindEndpoint vault.spec uses — the token is minted fresh per launch, so
 *  a restart must re-bind before the page reloads). */
async function bindEndpoint(page: Page, endpoint: BackendEndpoint): Promise<void> {
  await page.context().addInitScript(
    (opts: { p: number; t: string }) => {
      ;(window as unknown as { go: unknown }).go = {
        main: {
          WailsApp: {
            GetWSPort: () => Promise.resolve(opts.p),
            GetWSToken: () => Promise.resolve(opts.t),
            CheckForUpdate: () => Promise.resolve(null),
            ReportHealthy: () => Promise.resolve(),
            ApplyUpdate: () => Promise.resolve(),
          },
        },
      }
    },
    { p: endpoint.port, t: endpoint.token },
  )
}

const test = base

test.describe('history: a command survives a restart and recall answers from the store', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  let backend: VaultBackend
  let xdg: DisposableRootResult

  test.beforeAll(() => {
    xdg = createDisposableRoot()
    // Seed the profile with the owner's retention value before the backend
    // starts (nocx-rtg0.16 — see the header comment): the age sweep must be
    // live for this spec to prove anything, and it must run against real
    // wall-clock timestamps.
    const settingsDir = xdg.config
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
    backend = new VaultBackend(DEVHARNESS_BIN, asDisposableRoot(xdg), true)
  })

  test.afterAll(async () => {
    await backend?.stop()
  })

  test('run a command, restart, press Up: the command is there, source=store', async ({ page }) => {
    const marker = `echo history-e2e-${Date.now()}`

    // ── Phase 1: first launch, run a command ─────────────────────────────
    const ep = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })

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
    await page.waitForTimeout(800)

    // ── Phase 2: restart the backend (fresh launch, fresh token) ────────
    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
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
