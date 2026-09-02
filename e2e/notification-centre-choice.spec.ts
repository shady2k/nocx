import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { test, expect, promptReady, settingsReady, showSidebarView } from './harness'
import type { Page } from './harness'

/**
 * e2e: a person can choose which notification kinds the centre shows
 * (nocx-9wsb5 — W6, the refinements epic's gate).
 *
 * This is deliberately a second file beside notification-centre.spec.ts. That
 * existing file is the previous epic's independent gate: a session ending in
 * the background remains in the bell and its live program-notification row
 * focuses the originating tab (nocx-p0xhg.9). This file owns a different
 * sentence — named kind/session presentation, the centre visibility choice and
 * read-state preservation — so keeping the gates separate makes a red run say
 * which promise broke instead of combining two unrelated journeys.
 *
 * The test walks the product's surfaces only. It produces events through real
 * shells, clicks the Settings matrix cell where the Terminal bell row meets
 * the Notification centre column, and reads the rendered centre. It never
 * writes the setting through an RPC or reads the feed store underneath the UI.
 */

const TAB = '.nocx-tab'
const TITLE = '.nocx-tab-title'
const BELL = 'button[data-view="notifications"]'
const BADGE = '.ui-badge'
const INPUT = '.pane.active .nocx-editor-input'
const LIST = '.notifications-panel__list'
const ROW = `${LIST} > .ui-collection-row`
const KIND_BADGE = '.ui-record-row__meta .ui-badge'
const TERMINAL_BELL = 'Terminal bell'
const COMMAND_FINISHED = 'Command finished'
const BELL_SETTING_KEY = 'notifications.centre.bell'
const NOTIFICATIONS_NAV = '.ui-grouped-nav__item[data-item="Notifications"]'

const rowsOfKind = (page: Page, kind: string) =>
  page.locator(ROW).filter({ has: page.getByText(kind, { exact: true }) })

/**
 * The command block a parked command left behind, found by the title it
 * printed — every command here names its own tab, so the text is unique and
 * the lookup needs no pane scoping (a background pane's block is in the DOM;
 * tabs are hidden, not unmounted).
 */
const blockOf = (page: Page, title: string) => page.locator('.cmd-block').filter({ hasText: title })

/**
 * Wait until the ledger has CLOSED this command, before asking the centre
 * about it.
 *
 * The exit chip is written by the authenticated completion, so it is the
 * nearest observable to "the product knows this command ended" — and it sits
 * on the near side of the seam the notification travels: the row is raised
 * from the record the renderer sends afterwards. Waiting here first is what
 * makes a red run diagnostic. A missing row with a frozen block says the
 * notification half failed; a block that never froze says the completion
 * never arrived. Without it the only evidence is a number on the bell, which
 * is what a webkit CI run left behind and why nocx-td6d4.10 took an artefact
 * dig to attribute.
 *
 * Every parked command exits non-zero on purpose (see parkBellAndFinish), so
 * the chip is the failure one.
 */
async function commandFinished(page: Page, title: string): Promise<void> {
  await expect(blockOf(page, title).locator('.cmd-header-exit-fail').first()).toHaveText('exit 1', {
    timeout: 30_000,
  })
}

const filterFor = (page: Page, label: string) =>
  page
    .locator('.notifications-panel__filters .ui-field')
    .filter({ has: page.getByText(label, { exact: true }) })
    .locator('select.ui-select')

const tabNamed = (page: Page, title: string) => page.locator(TAB).filter({ hasText: title })

async function backToTheTerminal(page: Page): Promise<void> {
  await page.locator(INPUT).click()
  await promptReady(page)
}

/**
 * Establishes a quiet, open centre without assuming the shared stand's feed is
 * empty. Mark-all-read changes read state but deliberately does not delete
 * retained occurrences, so unread rows below are this journey's transitions.
 */
async function quietBell(page: Page): Promise<void> {
  await showSidebarView(page, 'notifications')
  const markRead = page.getByTestId('notifications-mark-read')
  if (await markRead.isEnabled()) await markRead.click()
  await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
  await expect(page.locator(`${ROW}[data-selected="true"]`)).toHaveCount(0)
  await backToTheTerminal(page)
}

/** Start a command that names its tab, waits on a test-owned gate, rings BEL,
 * then exits non-zero so its finished-command row has a distinct level from
 * any earlier successful commands in the same session. */
async function parkBellAndFinish(page: Page, title: string, gate: string): Promise<void> {
  await page.keyboard.type(
    `printf '\\033]0;${title}\\007'; while [ ! -e ${gate} ]; do sleep 0.1; done; printf '\\a'; false`,
  )
  await page.keyboard.press('Enter')
  await expect(page.locator(TITLE).filter({ hasText: title })).toBeVisible({ timeout: 15_000 })
}

async function selectSession(page: Page, title: string): Promise<void> {
  await filterFor(page, 'Session').selectOption({ label: title })
  await expect(page.locator(ROW).first()).toBeVisible()
}

function assertReadableIdentifiers(values: readonly string[], what: string): void {
  for (const value of values) {
    expect(value, `${what} must not expose a dotted identifier`).not.toMatch(/\./)
    expect(value, `${what} must not expose a session/kind hex run`).not.toMatch(/[0-9a-f]{8,}/i)
  }
}

test.use({ viewport: { width: 1280, height: 900 } })

test('a person can choose visible notification kinds without losing rows or read state', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)

  const project = testInfo.project.name
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'nocx-e2e-notification-choice-'))
  const gateA = path.join(root, 'bell-a')
  const gateB = path.join(root, 'bell-b')
  const gateC = path.join(root, 'bell-c')
  const titleA = `W6 choice primary ${project}`
  const titleB = `W6 choice secondary ${project}`
  const titleC = `W6 choice hidden ${project}`

  try {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await promptReady(page)
    await quietBell(page)

    const bellRowsBefore = await rowsOfKind(page, TERMINAL_BELL).count()
    const finishedRowsBefore = await rowsOfKind(page, COMMAND_FINISHED).count()

    // Three named sessions let the Session axis prove real tab titles rather
    // than disappearing because a single value cannot narrow anything. Each
    // command names its tab before it parks, so the later events occur while a
    // different tab is selected.
    await parkBellAndFinish(page, titleA, gateA)
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator(TAB)).toHaveCount(2)
    await promptReady(page)
    await parkBellAndFinish(page, titleB, gateB)
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator(TAB)).toHaveCount(3)
    await promptReady(page)
    await parkBellAndFinish(page, titleC, gateC)

    // A is raised while B is frontmost. Its event consists of a real BEL and
    // the command's real completion; both are held behind the observable gate.
    await tabNamed(page, titleB).click()
    await expect(tabNamed(page, titleA)).toHaveAttribute('aria-selected', 'false')
    fs.writeFileSync(gateA, '')
    await commandFinished(page, titleA)
    await showSidebarView(page, 'notifications')
    const badge = page.locator(BELL).locator(BADGE)
    // One wait on all three facts, and it carries the numbers.
    //
    // A raised tab produces TWO events on two different paths: the BEL is a
    // byte the renderer sees, while the command's completion is the ledger's
    // KindBlockFinished, which travels history.record to the backend and comes
    // back through the notification pipeline. So the badge can pass through
    // "1" on its way to "2", and a wait that closes on the badge merely BEING
    // a number closes on that "1" — the state the next assertion rejects. That
    // is the repo's recurring flake shape, and it went red on a loaded runner
    // exactly as recorded: fourteen polls at "1" while webkit passed.
    //
    // Polling the conjunction fixes the shape; returning the counts rather
    // than a boolean is what makes a red run useful, because the timeout then
    // names which half never arrived instead of only that the badge read "1".
    await expect
      .poll(
        async () => ({
          badge: await badge.textContent(),
          bells: await rowsOfKind(page, TERMINAL_BELL).count(),
          finished: await rowsOfKind(page, COMMAND_FINISHED).count(),
        }),
        { timeout: 30_000 },
      )
      .toEqual({
        badge: '2',
        bells: bellRowsBefore + 1,
        finished: finishedRowsBefore + 1,
      })

    // Read A before B is admitted. This gives the restore assertion two
    // states to preserve: A is read, while B and C remain unread.
    await page.getByTestId('notifications-mark-read').click()
    await expect(badge).toHaveCount(0)
    await expect(page.locator(`${ROW}[data-selected="true"]`)).toHaveCount(0)

    // B is raised while C is frontmost, with the centre still enabled. Its
    // two unread rows are the count that the visibility toggle must lower.
    await tabNamed(page, titleC).click()
    await expect(tabNamed(page, titleB)).toHaveAttribute('aria-selected', 'false')
    fs.writeFileSync(gateB, '')
    await commandFinished(page, titleB)
    // The same wait the gateA moment uses, and for the same reason: the badge
    // passes through "1" on its way to "2" because the BEL and the ledger's
    // completion travel different paths, so a wait that closes on the badge
    // alone can close on the state the next assertion rejects — and when it
    // times out it names which half never arrived.
    await expect
      .poll(
        async () => ({
          badge: await badge.textContent(),
          bells: await rowsOfKind(page, TERMINAL_BELL).count(),
          finished: await rowsOfKind(page, COMMAND_FINISHED).count(),
        }),
        { timeout: 30_000 },
      )
      .toEqual({
        badge: '2',
        bells: bellRowsBefore + 2,
        finished: finishedRowsBefore + 2,
      })

    const countBeforeOff = Number(await badge.textContent())
    expect(countBeforeOff).toBe(2)

    // The setting is changed through the matrix cell a person can reach, not
    // by mutating its persisted key underneath Settings.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(NOTIFICATIONS_NAV).click()
    const matrix = page.locator('.ui-toggle-matrix')
    await expect(matrix).toBeVisible()
    const centreCell = matrix.locator(
      '.ui-toggle-matrix__cell[data-row="bell"][data-column="centre"]',
    )
    await expect(centreCell).toBeVisible()
    await expect(centreCell.locator('.ui-settings-matrix-cell')).toHaveAttribute(
      'data-key',
      BELL_SETTING_KEY,
    )
    const centreSwitch = centreCell.locator('input.ui-checkbox__control')
    await expect(centreSwitch).toHaveAttribute(
      'aria-label',
      `${TERMINAL_BELL} → Notification centre`,
    )
    await expect(centreSwitch).toBeChecked()
    await centreSwitch.click()
    await expect(centreSwitch).not.toBeChecked()

    await showSidebarView(page, 'notifications')
    // Both retained bell rows are hidden, while B's finished-command row is
    // still visible and unread. The count therefore drops from 2 to 1.
    await expect(rowsOfKind(page, TERMINAL_BELL)).toHaveCount(0)
    await expect(rowsOfKind(page, COMMAND_FINISHED)).toHaveCount(finishedRowsBefore + 2)
    await expect(badge).toHaveText('1')
    const countAfterOff = Number(await badge.textContent())
    expect(countAfterOff).toBeLessThan(countBeforeOff)

    // C is raised with the centre OFF and while A is frontmost. Its finished
    // command remains visible; its BEL is retained but hidden until re-enabled.
    await tabNamed(page, titleA).click()
    await expect(tabNamed(page, titleC)).toHaveAttribute('aria-selected', 'false')
    fs.writeFileSync(gateC, '')
    await commandFinished(page, titleC)
    await expect(rowsOfKind(page, COMMAND_FINISHED)).toHaveCount(finishedRowsBefore + 3, {
      timeout: 30_000,
    })
    await expect(rowsOfKind(page, TERMINAL_BELL)).toHaveCount(0)
    await expect(badge).toHaveText('2')

    // Turn the same matrix choice back on. The feed still holds A, B and C;
    // visibility changed only which rows and unread facts were projected.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(NOTIFICATIONS_NAV).click()
    const matrixAgain = page.locator('.ui-toggle-matrix')
    const centreCellAgain = matrixAgain.locator(
      '.ui-toggle-matrix__cell[data-row="bell"][data-column="centre"]',
    )
    const centreSwitchAgain = centreCellAgain.locator('input.ui-checkbox__control')
    await expect(centreSwitchAgain).not.toBeChecked()
    await centreSwitchAgain.click()
    await expect(centreSwitchAgain).toBeChecked()

    await showSidebarView(page, 'notifications')
    await expect(rowsOfKind(page, TERMINAL_BELL)).toHaveCount(bellRowsBefore + 3)
    await expect(rowsOfKind(page, COMMAND_FINISHED)).toHaveCount(finishedRowsBefore + 3)
    await expect(badge).toHaveText('4')

    const sessionFilter = filterFor(page, 'Session')
    await expect(sessionFilter).toBeVisible()
    const sessionOptions = await sessionFilter.locator('option').allTextContents()
    expect(sessionOptions).toEqual(expect.arrayContaining([titleA, titleB, titleC]))
    assertReadableIdentifiers(sessionOptions, 'session filter option')

    // Kind badges use catalogue noun phrases, never wire slugs. Keep the
    // assertion to the badge elements, not the whole panel: titles and bodies
    // are free text and may legitimately contain commit hashes.
    const kindTexts = await page.locator(`${ROW} ${KIND_BADGE}`).allTextContents()
    expect(kindTexts).toEqual(expect.arrayContaining([TERMINAL_BELL, COMMAND_FINISHED]))
    assertReadableIdentifiers(kindTexts, 'kind badge')

    // The three sessions retain their own rows and their own read state. A is
    // read because it was marked before B was admitted; B and C are unread.
    await selectSession(page, titleA)
    await expect(page.locator(ROW)).toHaveCount(2)
    const aBell = rowsOfKind(page, TERMINAL_BELL)
    const aFinished = rowsOfKind(page, COMMAND_FINISHED)
    await expect(aBell).toHaveCount(1)
    await expect(aFinished).toHaveCount(1)
    await expect(aBell).not.toHaveAttribute('data-selected', 'true')
    await expect(aFinished).not.toHaveAttribute('data-selected', 'true')

    await selectSession(page, titleB)
    await expect(page.locator(ROW)).toHaveCount(2)
    const bBell = rowsOfKind(page, TERMINAL_BELL)
    const bFinished = rowsOfKind(page, COMMAND_FINISHED)
    await expect(bBell).toHaveCount(1)
    await expect(bFinished).toHaveCount(1)
    await expect(bBell).toHaveAttribute('data-selected', 'true')
    await expect(bFinished).toHaveAttribute('data-selected', 'true')

    await selectSession(page, titleC)
    await expect(page.locator(ROW)).toHaveCount(2)
    const cBell = rowsOfKind(page, TERMINAL_BELL)
    const cFinished = rowsOfKind(page, COMMAND_FINISHED)
    await expect(cBell).toHaveCount(1)
    await expect(cFinished).toHaveCount(1)
    await expect(cBell).toHaveAttribute('data-selected', 'true')
    await expect(cFinished).toHaveAttribute('data-selected', 'true')

    // A row activates the tab that owns its session, not whichever tab happens
    // to be selected when the click occurs.
    await sessionFilter.selectOption({ label: titleA })
    await aBell.locator('.ui-record-row__title').click()
    await expect(tabNamed(page, titleA)).toHaveAttribute('aria-selected', 'true')
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
})
