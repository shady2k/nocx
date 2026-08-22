/**
 * The epic's happy path (nocx-hbdw4): a person starts an upload, goes to
 * another sidebar view, collapses the sidebar altogether — and the transfer
 * is still visible and still cancellable.
 *
 * ## Why this spec is the criterion and the unit tests are not
 *
 * The complaint that bought this feature was made by running the product:
 * a 2 GB upload was invisible and uncancellable from anywhere except the
 * Files panel, while it went on running on its own SSH lease. Every layer
 * of that was green at the time. What no unit test could report is the
 * thing the person actually did — switch away, and lose sight of the work —
 * because losing sight of it is a property of the whole shell and not of
 * any component in it.
 *
 * ## The transfer is held open by holding its BODY, never by timing
 *
 * A transfer must still be running when the assertions look at it, and a
 * payload sized to "take long enough" is a test that passes on a slow
 * machine and fails on a fast one. So the POST that carries the bytes is
 * intercepted and held: `files.upload` has already returned, the backend's
 * sink is waiting for a body that will not arrive, and the transfer stays
 * `running` until this test says otherwise. Every wait below is on an
 * observable state — a badge, an attribute, a row — and there is no
 * `waitForTimeout` anywhere.
 *
 * Cancelling is then a real cancel and not a trick: the backend's
 * body-wait select answers `files.uploadCancel` immediately
 * (`internal/transport/ws_upload.go`, `runUpload`), settles the transfer
 * `cancelled`, and the renderer learns it from `files.uploadDone` — which
 * is what puts the row's `data-phase` where this spec reads it.
 *
 * ## Which tab, and why a local one
 *
 * A local tab in a browser, which uploads: the renderer holds a `File`, the
 * tab's shell is on the backend's machine, and D9 says whoever has only the
 * bytes uploads them (nocx-9le.5.24). That costs no sshd fixture, and the
 * indicator does not care which machine the bytes were going to.
 */
import { randomBytes } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import path from 'node:path'

import { dropFileOnActivePane } from './drop-gesture'
import { test, expect, promptReady, showSidebarView } from './harness'
import { readStand } from './stand'

const FILES_PANEL = '[data-testid="files-panel"]'
const TREE_ROW = '.ui-tree-row'
const INDICATOR = '[data-testid="ops-indicator"]'
const BADGE = '[data-testid="ops-badge"]'
const PROGRESS = '[data-testid="ops-progress"]'
const POPOVER = '[data-testid="ops-popover"]'
const OP_ROW = '.ui-operation-row'

/** 256 KiB is the transfer's chunk; the payload is deliberately larger, so
 *  the held body is a body a working loop would have had to stream. */
const PAYLOAD_BYTES = 256 * 1024 + 4096

test('a running upload is visible and cancellable with the sidebar collapsed', async ({ page }) => {
  // A cold renderer and a shell that has to start and report OSC 7 do not
  // reliably fit the suite's 30 s default. Every WAIT inside is still on
  // observable state; this is only the ceiling.
  test.setTimeout(120_000)

  const stand = readStand()
  // INSIDE the stand's disposable home: the drop writes real bytes to real
  // disk, and the backend's own cwd is the CHECKOUT.
  const destDir = mkdtempSync(path.join(stand.home, 'ops-indicator-'))
  const destBase = path.basename(destDir)

  /** Released in the finally: a route left holding a request would outlive
   *  the assertions it exists to make possible. */
  let releaseBody: () => void = () => {}
  const bodyHeld = new Promise<void>((resolve) => {
    releaseBody = resolve
  })

  try {
    await page.goto('/')
    await promptReady(page)

    // ── The premise of the gesture: this tab knows where it is ───────────
    // An upload refuses an UNVERIFIED cwd, so the drop cannot be attempted
    // until the shell's OSC 7 has landed. Asserted separately so a failure
    // here reads as "the shell never reported its directory" rather than as
    // an indicator bug.
    await page.keyboard.type(`cd ${destDir}`)
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active .nocx-editor-cwd')).toContainText(destBase, {
      timeout: 60_000,
    })
    const panel = page.locator(FILES_PANEL)
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(TREE_ROW, { hasText: destBase })).toHaveAttribute(
      'data-selected',
      'true',
      { timeout: 60_000 },
    )

    // ── The icon is there before anything is running ────────────────────
    // Not conditional on anything: a fixed position is one a person learns,
    // and there is always somewhere to click to ask "is anything happening".
    await expect(page.locator(INDICATOR)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveCount(0)
    await expect(page.locator(PROGRESS)).toHaveCount(0)

    // ── Hold the body, then drop ────────────────────────────────────────
    await page.route('**/upload/*', async (route) => {
      await bodyHeld
      await route.abort()
    })
    await dropFileOnActivePane(page, `held-${Date.now()}.bin`, randomBytes(PAYLOAD_BYTES))

    // The transfer exists and is live: the badge counts ACTIVE operations,
    // and the aggregate appears only while something runs.
    await expect(page.locator(BADGE)).toHaveText('1', { timeout: 30_000 })
    await expect(page.locator(PROGRESS)).toHaveCount(1)

    // ── 1: another sidebar view ─────────────────────────────────────────
    await showSidebarView(page, 'ports')
    await expect(panel).toHaveCount(0)
    await expect(page.locator(INDICATOR)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveText('1')

    // ── 2: the sidebar collapsed altogether ─────────────────────────────
    // The case a view in the top zone could never have answered: views are
    // mutually exclusive and vanish with the panel.
    await page.keyboard.press('Control+b')
    await expect(page.locator('#sidebar')).toHaveClass(/collapsed/)
    await expect(page.locator(INDICATOR)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveText('1')

    // ── 3: and it is still operable from there ──────────────────────────
    await page.locator(INDICATOR).click()
    await expect(page.locator(POPOVER)).toBeVisible()
    const row = page.locator(`${POPOVER} ${OP_ROW}`)
    await expect(row).toHaveCount(1)
    await expect(row).toHaveAttribute('data-phase', 'running')

    await page.locator(POPOVER).getByRole('button', { name: 'Cancel' }).click()

    // The cancel reached the backend, which settled the transfer: the
    // renderer decides no phase locally, so this attribute changing IS
    // files.uploadDone having arrived.
    await expect(row).toHaveAttribute('data-phase', 'cancelled', { timeout: 30_000 })

    // ── 4: a finished operation leaves the badge and stays in the list ───
    // Success does not shout, and does not vanish without trace either.
    await expect(page.locator(BADGE)).toHaveCount(0)
    await expect(page.locator(PROGRESS)).toHaveCount(0)
    await expect(row).toHaveCount(1)
  } finally {
    releaseBody()
    // The stand's home is shared by every spec in the run AND by both
    // browser projects, so what this spec made is this spec's to remove.
    rmSync(destDir, { recursive: true, force: true })
  }
})
