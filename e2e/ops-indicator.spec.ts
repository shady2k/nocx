/**
 * The epic's happy path (nocx-hbdw4): a person starts an upload, goes to
 * another sidebar view, collapses the sidebar altogether — and the transfer
 * is still visible and still cancellable. Plus the defect that shipped inside
 * it (nocx-hbdw4.1): pressing Cancel did nothing at all.
 *
 * ## Why this spec is the criterion and the unit tests are not
 *
 * The complaint that bought this feature was made by running the product: a
 * 2 GB upload was invisible and uncancellable from anywhere except the Files
 * panel, while it went on running on its own SSH lease. Every layer of that
 * was green at the time. What no unit test could report is the thing the
 * person actually did — switch away, and lose sight of the work — because
 * losing sight of it is a property of the whole shell and not of any
 * component in it.
 *
 * The same is true, harder, of the cancel defect. The list rendered its rows
 * with an unkeyed `For` over a projection that mints fresh objects on every
 * read, so every store change disposed and rebuilt every row. A person's
 * press then straddled a rebuild: the button they went down on no longer
 * existed when they came up, and the browser fires `click` on the nearest
 * common ancestor of the two, which is the list. NO unit test can see that —
 * jsdom's `click()` is synthesised on the element and cannot straddle
 * anything — and the first version of this spec could not either, because it
 * pressed Cancel while the list was perfectly still. So the press below is a
 * real mouse down, a real store change, and a real mouse up.
 *
 * ## The transfer is held open by holding its BODY, never by timing
 *
 * A transfer must still be running when the assertions look at it, and a
 * payload sized to "take long enough" is a test that passes on a slow machine
 * and fails on a fast one. So the POST that carries the bytes is intercepted
 * and held: `files.upload` has already returned, the backend's sink is
 * waiting for a body that will not arrive, and the transfer stays `running`
 * until this test says otherwise. Every wait below is on an observable state
 * — a badge, an attribute, a row — and there is no `waitForTimeout` anywhere.
 *
 * The store change the press straddles is made the same way: a SECOND file is
 * dropped while the button is held down. Its row arriving is a change to the
 * list the first row is in, observable as a row count, and it involves no
 * duration either. In the product the same change arrives several times a
 * second as progress frames.
 *
 * Cancelling is then a real cancel and not a trick: the backend's body-wait
 * select answers `files.uploadCancel` immediately
 * (`internal/transport/ws_upload.go`, `runUpload`), settles the transfer
 * `cancelled`, and the renderer learns it from `files.uploadDone` — which is
 * what puts the row's `data-phase` where this spec reads it.
 *
 * ## Where the list is, and where the count is
 *
 * They are in two places on purpose (nocx-hbdw4.1). The LIST is an ordinary
 * top-zone view and opens the panel, like Files, Git and Ports; it vanishes
 * with the panel like every other view. The COUNT and the aggregate bar are
 * drawn on the view's activity-bar icon, which is on screen whatever the
 * panel is doing — and that is the half this spec's first two acts are about.
 *
 * ## Which tab, and why a local one
 *
 * A local tab in a browser, which uploads: the renderer holds a `File`, the
 * tab's shell is on the backend's machine, and D9 says whoever has only the
 * bytes uploads them (nocx-9le.5.24). That costs no sshd fixture, and the
 * list does not care which machine the bytes were going to.
 */
import { randomBytes } from 'node:crypto'
import { mkdtempSync, rmSync } from 'node:fs'
import path from 'node:path'

import { dropFileOnActivePane } from './drop-gesture'
import type { Locator } from '@playwright/test'

import { test, expect, promptReady, showSidebarView } from './harness'
import { readStand } from './stand'

const FILES_PANEL = '[data-testid="files-panel"]'
const TREE_ROW = '.ui-tree-row'
const OPS_ICON = 'button[data-view="operations"]'
const BADGE = '[data-view-badge="operations"]'
const PROGRESS = '[data-view-progress="operations"]'
const OPS_PANEL = '[data-testid="operations-panel"]'
const OP_ROW = '.ui-operation-row'

/** 256 KiB is the transfer's chunk; the payload is deliberately larger, so
 *  the held body is a body a working loop would have had to stream. */
const PAYLOAD_BYTES = 256 * 1024 + 4096

/** Is this element's text clipped to an ellipsis? That is what
 *  `text-overflow: ellipsis` does and there is no other way to ask: the
 *  computed style says the rule is on, never that it fired. */
async function ellipsised(locator: Locator): Promise<boolean> {
  return locator.evaluate((el) => el.scrollWidth > el.clientWidth)
}

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
    // an operations bug.
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
    await expect(page.locator(OPS_ICON)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveCount(0)
    await expect(page.locator(PROGRESS)).toHaveCount(0)

    // ── Hold the body, then drop ────────────────────────────────────────
    await page.route('**/upload/*', async (route) => {
      await bodyHeld
      await route.abort()
    })
    const firstName = 'held.bin'
    await dropFileOnActivePane(page, firstName, randomBytes(PAYLOAD_BYTES))

    // The transfer exists and is live: the badge counts ACTIVE operations,
    // and the aggregate appears only while something runs.
    await expect(page.locator(BADGE)).toHaveText('1', { timeout: 30_000 })
    await expect(page.locator(PROGRESS)).toHaveCount(1)

    // ── 1: another sidebar view ─────────────────────────────────────────
    await showSidebarView(page, 'ports')
    await expect(panel).toHaveCount(0)
    await expect(page.locator(OPS_ICON)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveText('1')

    // ── 2: the sidebar collapsed altogether ─────────────────────────────
    // The half a view could never have answered on its own — which is why
    // the count lives on the icon and not in the panel.
    await page.keyboard.press('Control+b')
    await expect(page.locator('#sidebar')).toHaveClass(/collapsed/)
    await expect(page.locator(OPS_ICON)).toBeVisible()
    await expect(page.locator(BADGE)).toHaveText('1')
    await expect(page.locator(PROGRESS)).toHaveCount(1)

    // ── 3: the list opens the panel from there ──────────────────────────
    await page.locator(OPS_ICON).click()
    await expect(page.locator('#sidebar')).not.toHaveClass(/collapsed/)
    await expect(page.locator(OPS_PANEL)).toBeVisible()
    const row = page.locator(`${OPS_PANEL} ${OP_ROW}`)
    await expect(row).toHaveCount(1)
    await expect(row).toHaveAttribute('data-phase', 'running')

    // The row names the FILE, never the 32-hex transfer id it is addressed
    // by (nocx-hbdw4.1, defect 2), and the panel's width is enough for the
    // whole name rather than an ellipsis of it (defect 3).
    // The row names the FILE, never the 32-hex transfer id it is addressed
    // by (nocx-hbdw4.1, defect 2) — and it names it WHOLE. `scrollWidth`
    // over `clientWidth` is what an ellipsis actually is, so this is the
    // paint and not the markup, which is the half no jsdom test can reach
    // (defect 3).
    const title = row.locator('.ui-operation-row__title')
    await expect(title).toHaveText(firstName)
    expect(await ellipsised(title), 'the file name is not ellipsised').toBe(false)
    // And the path, which cannot fit any panel, is what gives way instead —
    // on its own line, so it takes no room from the name. It reads WITH the
    // machine, as one fact: the operations list is global, one list for
    // every tab, so a row naming a path and no machine named nowhere the
    // moment a second connection was open (amendment to nocx-hbdw4.4). This
    // is a local tab, and the local machine has a name too — it is not a
    // blank.
    const dest = row.locator('.ui-operation-row__destination')
    await expect(dest).toHaveAttribute('title', `This machine · ${destDir}`)

    // ── 4: cancel, pressed ACROSS a change to the list ──────────────────
    // The defect that shipped: an unkeyed row was rebuilt under the finger
    // between mousedown and mouseup, and the click never reached the button.
    const cancel = page.locator(OPS_PANEL).getByRole('button', { name: 'Cancel' }).first()
    const box = await cancel.boundingBox()
    expect(box, 'the cancel button is on screen').not.toBeNull()
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
    await page.mouse.down()

    // A second transfer starts while the button is held down: the list
    // moves, exactly as a progress frame moves it in the product. Waited on
    // as a row COUNT, so nothing here depends on a duration.
    await dropFileOnActivePane(page, 'second.bin', randomBytes(PAYLOAD_BYTES))
    await expect(page.locator(`${OPS_PANEL} ${OP_ROW}`)).toHaveCount(2, { timeout: 30_000 })

    await page.mouse.up()

    // The cancel reached the backend, which settled the transfer: the
    // renderer decides no phase locally, so this attribute changing IS
    // files.uploadDone having arrived — for the row that was pressed, and
    // not for the one that arrived under it.
    const first = page.locator(`${OPS_PANEL} ${OP_ROW}`).filter({ hasText: firstName })
    await expect(first).toHaveAttribute('data-phase', 'cancelled', { timeout: 30_000 })

    // ── 5: a finished operation leaves the badge and stays in the list ───
    // Success does not shout, and does not vanish without trace either. The
    // second transfer is still running, so the badge counts it and only it.
    await expect(page.locator(BADGE)).toHaveText('1')
    await expect(first).toHaveCount(1)
    await expect(first.locator('.ui-operation-row__title')).toHaveText(firstName)

    // And the outcome badge says its whole word. It read "D…" in the popover
    // this list replaced, which is the state defect 3 was reported from: a
    // badge clips its own overflow, so as a flex sibling it was free to
    // shrink to nothing.
    const outcome = first.locator('.ui-badge')
    await expect(outcome).toHaveText('Cancelled')
    expect(await ellipsised(outcome), 'the outcome badge is not ellipsised').toBe(false)
  } finally {
    releaseBody()
    // The stand's home is shared by every spec in the run AND by both
    // browser projects, so what this spec made is this spec's to remove.
    rmSync(destDir, { recursive: true, force: true })
  }
})
