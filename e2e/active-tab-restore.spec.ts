/**
 * e2e: the window reopens on the tab you left (nocx-mqie.5).
 *
 * The epic's own criterion, watched end to end. nocx-mqie.4 wired
 * uistate.activeTab and proved the round trip at the RENDERER seam — two
 * PaneManagers over one chain with a fake socket between them
 * (panes-layout.test.ts) — which is a real test of the renderer and says
 * nothing about the trip through the backend: uistate.set, uistate.json on
 * disk, uistate.get on the next start.
 *
 * So this spec starts its OWN nocx-server backend, the way
 * e2e/sidebar-resize.spec.ts does, precisely so it can restart it mid-test.
 * The assertion afterwards is on the STRIP — which row carries
 * aria-selected — rather than on a value read back, because "the tab you
 * left is the tab that opens" is a claim about what the person sees.
 *
 * It fails on a build whose boot activates panes[0]: the tab left in front
 * is the second one, and the first is what a naive boot would choose.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync, readFileSync } from 'node:fs'
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

/** Lazily: the stand is started by globalSetup, which runs after Playwright
 *  has collected this file. */
const serverBin = () => readStand().server

const TAB = '.nocx-tab'
const NEW_TAB = '[aria-label="New tab"]'

const test = base

/** The persisted active tab in the backend's uistate.json — the durable
 *  seam. It is a UI-state document and not a setting (ADR-0048): which tab
 *  was in front is not a decision anybody made. */
function persistedActiveTab(backend: VaultBackend): unknown {
  try {
    const doc = JSON.parse(
      readFileSync(join(documentDir(backend.isolatedHome), 'uistate.json'), 'utf8'),
    ) as { activeTab?: unknown }
    return doc.activeTab
  } catch {
    return undefined
  }
}

test.describe('the window reopens on the tab you left (nocx-mqie.5)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-active-tab-')) }
    // `true` = no Secret Service for this backend.
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('the tab that was in front is the tab that is selected after a restart', async ({
    page,
  }) => {
    const ep1 = await backend.start()
    await bindEndpoint(page, ep1)
    await page.goto('/')
    await promptReady(page)

    // THE BOOT'S OWN RECORD FIRST, and it is what this test used to mistake
    // for the one it wanted. Bringing a tab forward writes the document
    // (PaneManager.activate), and boot brings the first tab forward — so the
    // file holds an id before the second tab exists. A poll for "truthy"
    // accepts that one, and then `remembered` is the FIRST tab while the
    // assertions above are about the second. The restart then restored the
    // second tab correctly and the comparison called it a failure: the
    // product was right and the spec was reading the wrong record. It failed
    // on webkit and passed on chromium, which is the signature of a race
    // rather than of a defect (uuid7 is time-ordered, and the id it captured
    // was the older of the two).
    await expect.poll(() => persistedActiveTab(backend)).toBeTruthy()
    const atBoot = persistedActiveTab(backend)

    // Two tabs, the SECOND in front — the state a boot that activates
    // panes[0] would get wrong, and the reason this is not asserted on one
    // tab.
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')

    // The write must land in the durable store BEFORE the restart, or the
    // restart proves nothing about persistence. Waiting for it to CHANGE is
    // what "the tab in front is recorded" looks like from out here: a second
    // tab came forward, so the record cannot still be the boot's — and this
    // waits on that state change rather than on a duration or on a weaker
    // predicate than the one meant.
    await expect
      .poll(() => persistedActiveTab(backend), {
        timeout: 15_000,
        message: 'the second tab coming forward was never recorded',
      })
      .not.toBe(atBoot)
    const remembered = persistedActiveTab(backend)

    // The application restarts. Nothing in the first process survives it.
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 90_000 })
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')
    // And it is the SAME tab, not merely a tab in the same slot: the chain
    // comes back in its stored order, so an id that changed would mean the
    // strip was rebuilt rather than restored.
    expect(persistedActiveTab(backend)).toBe(remembered)
  })
})
