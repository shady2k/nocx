/**
 * e2e: the sidebar is resizable and the width survives a restart (nocx-qmcu).
 *
 * The acceptance, in the owner's words: the user drags the edge between the
 * sidebar and the panes, the sidebar gets wider, and the Git panel's file
 * names stop clipping because there is room for them; the width they chose
 * is still there after a restart.
 *
 * This spec starts its own nocx-server backend (VaultBackend) so it can
 * RESTART it mid-test — the width's persistence is the subject, and the
 * seam that persists it is the UI-state document (ADR-0048): drag →
 * uistate.set → uistate.json → restart → uistate.get → applied to #sidebar.
 * The assertion after the restart is the panel's computed width, not a
 * variable read back.
 *
 * The drag is driven with real mouse events against the kit ResizeHandle
 * (role=separator) inside #sidebar; the clipping assertions measure the
 * Git row's name element (scrollWidth vs clientWidth), because the defect
 * this exists for is clipped file names, not a width number moving.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync, appendFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  VaultBackend,
  bindEndpoint,
  documentDir,
  promptReady,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'
import { createRepo, cleanupRepo, type GitRepo } from './git-fixture'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. The path was hard-coded to
 *  /tmp/nocx-devharness, which was true of the runner that built three copies
 *  by hand and stopped being true when one stand took ownership of the
 *  lifecycle — the spec then failed on its first line with 'nocx-server binary
 *  not found'. The manifest is the one place that knows. */
const serverBin = () => readStand().server

const SIDEBAR = '#sidebar'
const HANDLE = '[role="separator"][aria-label="Resize sidebar"]'
const VIEW_GIT = 'button[data-view="git"]'
const BRANCH = '[data-testid="git-branch"]'
const UNSTAGED = '[data-testid="git-unstaged-list"]'
const ROW = '.ui-collection-row'
const NAME = '.ui-file-status-row__name'
const TAB_TITLE = '.nocx-tab-title'

// A file name long enough to clip in a 240px sidebar (≈ 42 characters at
// the row's font renders wider than the name's budget at the default width)
// and comfortably inside it once the sidebar passes ~500px.
const LONG_NAME = 'a-very-long-file-name-that-will-clip-at-240px.txt'

const test = base

async function sidebarWidth(page: import('@playwright/test').Page): Promise<number> {
  return page.locator(SIDEBAR).evaluate((el) => el.getBoundingClientRect().width)
}

/** The Git row's name box: {client, scroll} — clipped when scroll > client. */
async function nameBox(
  page: import('@playwright/test').Page,
): Promise<{ client: number; scroll: number }> {
  const el = page.locator(UNSTAGED).locator(ROW).locator(NAME).first()
  await expect(el).toBeVisible({ timeout: 20_000 })
  return el.evaluate((n) => ({ client: n.clientWidth, scroll: n.scrollWidth }))
}

/** Drag the resize handle so the sidebar GROWS by `dx` pixels (a negative
 *  `dx` shrinks it). The handle is on the panel's LEADING edge — the panel is
 *  at the window's trailing edge — so growing it means moving the pointer
 *  LEFT (nocx-crjft). Every call site says what it wants of the sidebar; only
 *  the direction that means is in here. */
async function dragHandle(page: import('@playwright/test').Page, dx: number): Promise<void> {
  const handle = page.locator(HANDLE)
  const box = await handle.boundingBox()
  if (!box) throw new Error('resize handle has no box')
  const startX = box.x + box.width / 2
  const y = box.y + box.height / 2
  await page.mouse.move(startX, y)
  await page.mouse.down()
  await page.mouse.move(startX - dx, y, { steps: 10 })
  await page.mouse.up()
}

/** Park the shell in the repo and open the Git view. */
async function openGitPanel(page: import('@playwright/test').Page, repo: GitRepo): Promise<void> {
  await promptReady(page)
  await page.keyboard.type(`cd ${repo.root}`)
  await page.keyboard.press('Enter')
  await expect(page.locator(TAB_TITLE).first()).toContainText(repo.basename, { timeout: 20_000 })
  await page.locator(VIEW_GIT).click()
  await expect(page.locator(BRANCH)).toBeVisible({ timeout: 20_000 })
}

/** The persisted width in the backend's uistate.json — the durable seam.
 *  It moved out of settings.json in nocx-mqie.3: a width produced by
 *  dragging a panel edge is not a decision, so it is not a setting
 *  (ADR-0048). The backend's HOME is the isolated home under the disposable
 *  root (root/home, per home-isolation.ts), so the doc lives under THAT
 *  home, never under the root itself. */
function persistedWidth(backend: VaultBackend): unknown {
  try {
    const doc = JSON.parse(
      readFileSync(join(documentDir(backend.isolatedHome), 'uistate.json'), 'utf8'),
    ) as {
      sidebar?: Record<string, unknown>
    }
    return doc.sidebar?.width
  } catch {
    return undefined
  }
}

test.describe('sidebar resize (nocx-qmcu)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  let home: DisposableRoot
  let backend: VaultBackend

  // Each test gets a FRESH home and backend: the previous test's persisted
  // width is exactly what the next test must not inherit (the bounds test
  // leaves 640 behind), and VaultBackend.start() refuses to run twice.
  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-sidebar-resize-')) }
    // `true` = no Secret Service for this backend.
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test.afterAll(() => {
    backend?.stop()
  })
  test('drag widens the sidebar and a clipped Git file name is no longer clipped', async ({
    page,
  }) => {
    const repo = createRepo({ file: LONG_NAME })
    try {
      const ep = await backend.start()
      await bindEndpoint(page, ep)
      await page.goto('/')
      await openGitPanel(page, repo)

      // Make the row appear with counts (+N −0), the fixed parts that eat
      // the name's budget at the default width.
      appendFileSync(join(repo.root, LONG_NAME), 'line2\nline3\nline4\n')
      await expect(page.locator(UNSTAGED).locator(ROW)).toHaveCount(1, { timeout: 20_000 })

      // At the default 240px the name is clipped — the defect this exists for.
      await expect.poll(() => sidebarWidth(page)).toBe(240)
      const before = await nameBox(page)
      expect(before.scroll).toBeGreaterThan(before.client)

      // Drag the handle 280px right: 240 → 520.
      await dragHandle(page, 280)
      await expect.poll(() => sidebarWidth(page)).toBeGreaterThan(400)

      // The name that was clipped now fits.
      await expect.poll(() => nameBox(page).then((b) => b.scroll <= b.client)).toBe(true)
    } finally {
      cleanupRepo(repo)
    }
  })

  test('the bounds hold: dragging past either end stops at the limit', async ({ page }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await promptReady(page)

    await dragHandle(page, -400)
    await expect.poll(() => sidebarWidth(page)).toBe(200)

    await dragHandle(page, 1200)
    await expect.poll(() => sidebarWidth(page)).toBe(640)
  })

  test('the separator is keyboard-operable and every step persists', async ({ page }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await promptReady(page)

    await page.locator(HANDLE).focus()
    // ArrowRight moves the separator right; the panel is to its right, so the
    // panel narrows. The key's physical direction is unchanged — what changed
    // is which side of the separator the panel is on (nocx-crjft).
    await page.keyboard.press('ArrowRight')
    await expect.poll(() => sidebarWidth(page)).toBe(232)
    await expect.poll(() => persistedWidth(backend)).toBe(232)

    await page.keyboard.press('ArrowRight')
    await expect.poll(() => sidebarWidth(page)).toBe(224)

    await page.keyboard.press('Home')
    await expect.poll(() => sidebarWidth(page)).toBe(200)
  })

  test('the chosen width survives a backend restart', async ({ page }) => {
    const repo = createRepo({ file: LONG_NAME })
    try {
      const ep1 = await backend.start()
      await bindEndpoint(page, ep1)
      await page.goto('/')
      await openGitPanel(page, repo)

      // Resize by a single deterministic drag: 240 → 480. (Discrete arrow
      // steps accumulate into a flake — the keyboard test above already
      // proves per-step commits; this test's subject is persistence.)
      await dragHandle(page, 240)
      await expect.poll(() => sidebarWidth(page)).toBe(480)

      // The write must land in the durable store before the restart, or
      // the restart proves nothing about persistence.
      await expect.poll(() => persistedWidth(backend)).toBe(480)

      // Restart the backend and reload the page: the width must come back
      // through the settings seam (snapshot → applied to #sidebar).
      const ep2 = await backend.restart()
      await bindEndpoint(page, ep2)
      await page.reload()
      await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 90_000 })
      await expect.poll(() => sidebarWidth(page)).toBe(480)
    } finally {
      cleanupRepo(repo)
    }
  })
})
