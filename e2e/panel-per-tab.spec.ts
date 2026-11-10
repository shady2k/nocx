/**
 * e2e: each tab remembers its own right-hand panel (ADR-x7cvh8e1,
 * `nocx-ouru5.1.18`).
 *
 * The owner's criterion, watched end to end: open Ports on tab 2, switch to
 * tab 1 — which has no panel open — and the rail is collapsed; back to tab 2
 * and Ports is still what it shows. And because the panel is stored on the
 * tab's own layout row, a restart brings it back with the tab.
 *
 * The cold start is deliberately NOT changed: the window's opening tab is
 * seeded with the app's default panel (Files), which is what the file-manager
 * design §7 and e2e/files.spec.ts have always asserted. A tab the person opens
 * afterwards has no panel and shows the rail collapsed — the seed is once per
 * page load, on the tab the window opens with.
 *
 * The spec starts its OWN backend (like e2e/active-tab-restore.spec.ts) so it
 * can restart it mid-test and watch the panel come back through layout.read,
 * not through a value it kept in memory.
 */
import { expect } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  openControlPlane,
  promptReady,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

/** Lazily: the stand is started by globalSetup, which runs after Playwright
 *  has collected this file. */
const serverBin = () => readStand().server

const TAB = '.nocx-tab'
const NEW_TAB = '[aria-label="New tab"]'
const SIDEBAR = '#sidebar'
const PORTS_BTN = '.activity-bar button[data-view="ports"]'
const PANEL_TITLE = '.ui-sidebar-view__header h2'

const test = base

test.describe('each tab remembers its own right-hand panel (nocx-ouru5.1.18)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-panel-per-tab-')) }
    // `true` = no Secret Service for this backend.
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('switching tabs restores the panel each one left behind, and a restart brings it back', async ({
    page,
  }) => {
    const ep1 = await backend.start()
    await bindEndpoint(page, ep1)
    await page.goto('/')
    await promptReady(page)

    // THE COLD START IS UNCHANGED: the tab the window opens with shows the
    // default panel, and it is Files. (e2e/files.spec.ts owns the whole
    // criterion; this is the half this bead could have broken.)
    await expect(page.locator(SIDEBAR)).not.toHaveClass(/collapsed/)
    await expect(page.locator(PANEL_TITLE)).toHaveText('Files')

    // Tab 1 is then left with NO PANEL OPEN — the state the criterion names.
    await page.keyboard.press('Control+b')
    await expect(page.locator(SIDEBAR)).toHaveClass(/collapsed/)

    // Tab 2 is a tab the person opened, so it starts with no panel of its own.
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(SIDEBAR)).toHaveClass(/collapsed/)

    // Ports on tab 2.
    await page.locator(PORTS_BTN).click()
    await expect(page.locator(SIDEBAR)).not.toHaveClass(/collapsed/)
    await expect(page.locator(PANEL_TITLE)).toHaveText('Ports')
    await expect(page.locator(PORTS_BTN)).toHaveAttribute('aria-selected', 'true')

    // Back to tab 1: its rail is collapsed, NOT Ports.
    await page.locator(TAB).first().click()
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(SIDEBAR)).toHaveClass(/collapsed/)

    // Back to tab 2: Ports is still the panel it left behind.
    await page.locator(TAB).nth(1).click()
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(SIDEBAR)).not.toHaveClass(/collapsed/)
    await expect(page.locator(PANEL_TITLE)).toHaveText('Ports')

    // THE DURABLE SEAM, waited on rather than assumed: the write is
    // fire-and-forget, so a restart immediately after the click would prove
    // nothing. The chain is asked for the tab's own row.
    const wire = await openControlPlane(ep1.port, ep1.token)
    try {
      await expect
        .poll(
          async () => {
            const layout = (await wire.call('layout.read', {})) as {
              tabs: Array<{ id: string; panelView: string; panelCollapsed: boolean }>
            }
            const tab = layout.tabs.find((row) => row.panelView === 'ports')
            return tab ? `${tab.panelView}/${tab.panelCollapsed}` : 'none'
          },
          { timeout: 15_000, message: 'the panel was never recorded on the tab' },
        )
        .toBe('ports/false')
    } finally {
      wire.close()
    }

    // The application restarts. Nothing in the first process survives it.
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()
    await appReadyForInput(page)

    // The same two tabs, the same one in front — and its panel with it.
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 90_000 })
    await expect(page.locator(TAB).nth(1)).toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(SIDEBAR)).not.toHaveClass(/collapsed/)
    await expect(page.locator(PANEL_TITLE)).toHaveText('Ports')

    // And tab 1's choice came back too: collapsed, not Ports.
    await page.locator(TAB).first().click()
    await expect(page.locator(SIDEBAR)).toHaveClass(/collapsed/)
  })
})
