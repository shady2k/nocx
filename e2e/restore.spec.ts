/**
 * e2e: the application opens on what you left (nocx-zpq9j, epic nocx-l21ib).
 *
 * The epic's happy path, watched end to end and through the real backend:
 * tabs are opened, a command is run in each, the backend is restarted, and
 * the page is reloaded. What must come back is the tabs, their panes, and the
 * blocks those panes printed — with the text the commands actually produced,
 * read out of the encrypted store rather than out of a process that still
 * remembers writing it.
 *
 * Its own devharness backend, like sidebar-resize and active-tab-restore,
 * precisely so it can be restarted mid-test. Everything asserted is on
 * screen: a person's answer to "did my work come back" is what the pane
 * shows, not what a query returns.
 *
 * The setting is asserted in both directions, because "off gives a clean
 * start" is half of what nocx-l21ib promises and the half nobody would
 * notice breaking.
 */
import { test as base, expect } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  VaultBackend,
  bindEndpoint,
  promptReady,
  clickIntoEditor,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const devharnessBin = () => readStand().devharness

// Distinct ports, outside `wails dev` (34115), the suite default (9876) and
// the other restart specs (19876-19885).
const FIRST_PORT = 19886
const SECOND_PORT = 19887

const TAB = '.nocx-tab'
const NEW_TAB = '[aria-label="New tab"]'
const BLOCK = '.cmd-block'
const RESTORED = '[data-restored="true"]'
const BOUNDARY = '[data-restore-boundary="true"]'
const RESTORE_ROW = '.ui-settings-row[data-key="restore.onStartup"]'

const test = base

type Endpoint = { port: number; token: string }

/** One JSON-RPC call on a socket of the test's own, against the backend the
 *  page is bound to. The page's client is the product's and is not reachable
 *  from here; this asks the same server the same question. */
async function rpc(
  page: import('@playwright/test').Page,
  ep: Endpoint,
  method: string,
  params: unknown,
): Promise<Record<string, unknown>> {
  return page.evaluate(
    (a: { p: number; t: string; m: string; par: unknown }) =>
      new Promise<Record<string, unknown>>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${a.p}/session`, `nocx.token.${a.t}`)
        const timer = setTimeout(() => reject(new Error('rpc timeout')), 15_000)
        ws.onopen = () =>
          ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: a.m, params: a.par }))
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error('rpc socket error'))
        }
        ws.onmessage = (ev) => {
          if (typeof ev.data !== 'string') return
          const msg = JSON.parse(ev.data) as Record<string, unknown>
          if (msg.id !== 1) return
          clearTimeout(timer)
          ws.close()
          resolve((msg.result ?? { error: msg.error }) as Record<string, unknown>)
        }
      }),
    { p: ep.port, t: ep.token, m: method, par: params },
  )
}

/**
 * Wait until the STORE holds this command with the body a restore draws.
 *
 * A block on screen is not a row in the store, and the gap is deliberate: the
 * record goes through the outbox and the body is sent fire-and-forget once the
 * ack names an entry (history-client.ts, capture-client.ts), because the
 * terminal is never blocked on a write. A restart that does not wait for the
 * write is measuring that race, not the restore — and it lost it every time:
 * the block appears at SUBMIT, so the backend was being killed before `echo`
 * had even finished, and the first session stored nothing at all.
 *
 * Waiting on the record rather than on a duration is AGENTS.md's rule: a spec
 * that needs a slow machine to pass is broken on a fast one too. Nothing here
 * is asserted — this is the precondition the restore is about ("what was
 * stored comes back"), and everything the test CLAIMS is still read off the
 * screen.
 */
async function stored(
  page: import('@playwright/test').Page,
  ep: Endpoint,
  command: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const page1 = (await rpc(page, ep, 'ledger.query', {
          scope: 'everywhere',
          limit: 100,
        })) as { entries?: { id: string; intent: string }[] }
        const row = (page1.entries ?? []).find((e) => e.intent === command)
        if (!row) return false
        const detail = (await rpc(page, ep, 'ledger.get', { id: row.id })) as {
          artifacts?: { mediaType: string }[]
        }
        return (detail.artifacts ?? []).some((a) => a.mediaType === 'application/vt')
      },
      { timeout: 60_000, message: `the store never took "${command}"` },
    )
    .toBe(true)
}

/** Run one command in the active pane and wait for its block to be there. */
async function run(page: import('@playwright/test').Page, command: string): Promise<void> {
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator(BLOCK, { hasText: command }).first()).toBeVisible({ timeout: 30_000 })
}

test.describe('the application opens on what you left (nocx-l21ib)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-restore-')) }
    // `true` = no Secret Service for this backend.
    backend = new VaultBackend(devharnessBin(), home, true)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  // This spec found three defects, and the third is the one it was marked
  // fixme for (nocx-8won8). Neither end was empty: the first session wrote
  // both blocks, each anchored to its own pane, each with its vt and
  // text/plain artifacts, and in the second session ledger.query answered
  // with them for exactly those pane ids. The read was never issued. A
  // restored pane is activated BETWEEN Pane.start() and the renderer being
  // built, so the one show it ever gets finds no scrollback — and "not yet"
  // had no "later", because the tab stays active and a page load is not a
  // reconnect. mount() is the later.
  test('the tabs, and the output of what ran in them, come back', async ({ page }) => {
    const ep1 = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep1)
    await page.goto('/')

    // Two tabs, each having printed something only it printed. Two rather
    // than one because a restore that put every block in every pane would
    // pass a one-tab test.
    await run(page, 'echo FIRST-TAB-OUTPUT')
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })
    await run(page, 'echo SECOND-TAB-OUTPUT')

    // Both commands are in the store, with their bodies, before anything is
    // killed. See `stored` — without this the restart raced the write.
    await stored(page, ep1, 'echo FIRST-TAB-OUTPUT')
    await stored(page, ep1, 'echo SECOND-TAB-OUTPUT')

    // The application restarts. Nothing in the first process survives it —
    // including the shells, which is the point: what comes back is the tab.
    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 90_000 })

    // The pane in front is the second one, and it shows what IT printed,
    // marked as restored and above the boundary that says the shell below is
    // a new one (ADR-0019 §3).
    await expect(page.locator(`.pane.active ${RESTORED}`).first()).toBeVisible({ timeout: 60_000 })
    await expect(page.locator(`.pane.active ${BOUNDARY}`)).toBeVisible()
    await expect(page.locator('.pane.active').getByText('SECOND-TAB-OUTPUT').first()).toBeVisible()
    // And not the other tab's: a pane's blocks are its own, anchored by its
    // pane id.
    await expect(page.locator('.pane.active').getByText('FIRST-TAB-OUTPUT')).toHaveCount(0)

    // The first tab has its own, once it is looked at — the past is drawn on
    // first show, not at boot.
    await page.locator(TAB).first().click()
    await expect(page.locator('.pane.active').getByText('FIRST-TAB-OUTPUT').first()).toBeVisible({
      timeout: 60_000,
    })
  })

  // THE PANE BELONGS TO THE PROGRAM, and the restore boundary is part of the
  // chrome that has to step aside for it (nocx-nkwm2). An alternate-buffer
  // program takes the pane; the blocks and the separator already went, and the
  // words PREVIOUS SESSION with their rule stayed painted over the program's
  // screen, because the rule that empties the stack named the two child kinds
  // that existed when it was written and this one arrived later.
  //
  // Asserted through visibility rather than through the class, and after a real
  // restore rather than by planting an element: the defect is in the stylesheet,
  // so a test that reads the DOM cannot see it, and a test that reads the class
  // would have been green throughout.
  test('a program taking the pane hides the previous-session boundary', async ({ page }) => {
    const ep1 = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep1)
    await page.goto('/')

    await run(page, 'echo BOUNDARY-ALT-SCREEN')
    await stored(page, ep1, 'echo BOUNDARY-ALT-SCREEN')

    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()

    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 90_000 })
    await expect(page.locator(`.pane.active ${BOUNDARY}`)).toBeVisible({ timeout: 60_000 })

    // Into the alternate buffer, and block there so nothing races a deadline.
    await promptReady(page)
    await clickIntoEditor(page)
    await page.keyboard.type("printf '\\033[?1049h'; cat")
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active .xterm-live-container')).toHaveClass(
      /live-fullscreen/,
      { timeout: 30_000 },
    )

    // The whole assertion: nothing of the block chrome is left on screen.
    await expect(page.locator(`.pane.active ${BOUNDARY}`)).toBeHidden()
    await expect(page.locator(`.pane.active ${BLOCK}`).first()).toBeHidden()

    // And it comes back when the program gives the pane up, because the
    // boundary is still true — the shell below it is still a new one.
    await page.keyboard.press('Control+c')
    await page.keyboard.type("printf '\\033[?1049l'")
    await page.keyboard.press('Enter')
    await expect(page.locator(`.pane.active ${BOUNDARY}`)).toBeVisible({ timeout: 30_000 })
  })

  test('with the setting off, the same restart gives one fresh tab', async ({ page }) => {
    const ep1 = await backend.start(FIRST_PORT)
    await bindEndpoint(page, ep1)
    await page.goto('/')

    await run(page, 'echo BEFORE-THE-CLEAN-START')
    await page.locator(NEW_TAB).click()
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 30_000 })

    // Turn restoring off through the control a person actually reaches.
    // `restore.onStartup` is in the Interface section, and Settings opens on
    // its first section, so the rail has to navigate there before the row is
    // visible (the shape theme-switch.spec.ts uses).
    await page.keyboard.press('Meta+,')
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
    const toggle = page.locator(`${RESTORE_ROW} input[type="checkbox"]`)
    await expect(toggle).toBeVisible({ timeout: 10_000 })
    await toggle.uncheck()
    await expect(toggle).not.toBeChecked()
    await page.keyboard.press('Meta+w')

    const ep2 = await backend.restart(SECOND_PORT)
    await bindEndpoint(page, ep2)
    await page.reload()

    // One tab, nothing restored, and no boundary — a clean start says
    // nothing about a previous session because it is not showing one.
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 90_000 })
    await promptReady(page)
    await expect(page.locator(RESTORED)).toHaveCount(0)
    await expect(page.locator(BOUNDARY)).toHaveCount(0)
  })
})
