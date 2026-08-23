import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { test, expect, promptReady, showSidebarView } from './harness'
import type { Page } from './harness'

/**
 * e2e: a session that ends while you are elsewhere waits for you in the bell,
 * and the row you come back to takes you where it came from
 * (nocx-p0xhg.9 — the notification centre's gate).
 *
 * The sentence the epic exists for, walked through the product's own
 * surfaces: two tabs, the one you are NOT looking at ends badly, and the
 * fact is still there when you come back to it. Nothing here reads the
 * store — it clicks the bell, reads the rendered rows and clicks Mark all
 * read, because a feed that is only true in a signal is a feature nobody
 * has.
 *
 * The session must end THE WAY A SESSION ENDS ON ITS OWN. A tab the user
 * closes deliberately raises nothing, and that is deliberate: the
 * `session.close` handler marks the end as requested and monitorExit files
 * no event (internal/transport/ws.go, `takeCloseRequested`). Filing "was
 * interrupted" on the most ordinary action there is was the defect that
 * rule fixed. So the shell exits — non-zero, so the row is a warning
 * carrying its status — and the gate file is what makes the exit happen
 * only once the tab is demonstrably in the background, rather than betting
 * on how long opening a second tab takes (the same discipline as
 * activity-bell.spec.ts, nocx-z9s9.15).
 *
 * TWO SOURCES, because the centre has two and they answer different halves.
 * `session.ended` proves the fact survives your absence — but a CLEAN exit
 * closes its own tab (terminal-content.ts: "A clean exit closes the tab
 * exactly as it always did"), so by the time anybody reads that row the tab
 * it names is gone and the row is correctly inert. "Clicking the row focuses
 * the tab" therefore cannot be proven on it at all: asserted there, the
 * assertion passes on whichever tab happened to survive, and would go on
 * passing with resolution removed entirely. The program notification below is
 * the row whose tab is still open, and it is where that promise is checked.
 */

const TAB = '.nocx-tab'
/** The bell's rail button. Addressed by `data-view`, which is the sidebar's
 *  OWN vocabulary for a view button and what every other view is found by —
 *  this spec used to carry a `data-testid` of its own until the merge with
 *  main, where `SidebarViewDescriptor` grew `status` and dropped the parallel
 *  test hook. Two ways to name one button is the defect, whichever one a
 *  reader happens to find first. */
const BELL = 'button[data-view="notifications"]'
const BADGE = '.ui-badge'
const INPUT = '.pane.active .nocx-editor-input'
const ROW_TITLE = '.notifications-panel__list .ui-record-row__title'
/** The rows this test raised. The feed is IN-MEMORY in the shared stand and
 *  `resetStand` does not clear it — it resets panes, ui state, notes and
 *  snippets — so rows from earlier spec files are legitimately still there.
 *  Everything is marked read at the start, which makes UNREAD mean "raised
 *  since this test began" and keeps every count below about this test's own
 *  work rather than about which files ran first. `data-selected` is the kit's
 *  own account of the panel's `selected={!o.read}` (ui/collection-view.tsx). */
const UNREAD_ROW = '.notifications-panel__list .ui-collection-row[data-selected="true"]'
const UNREAD_TITLE = `${UNREAD_ROW} .ui-record-row__title`

/** The unread rows one SOURCE raised. The panel puts the event's kind on every
 *  row as a badge, in the same words its own kind filter offers
 *  (notify/notifications-panel.tsx) — so this is the panel's own vocabulary for
 *  "what raised this", not a hook invented for a test.
 *
 *  Necessary rather than tidy. A `block.finished` row's title is THE COMMAND
 *  TEXT (internal/transport/ws_ledger_notify.go, blockSubject), so the row that
 *  `printf …deploy done…` raises BY ENDING carries the words of the message the
 *  printf asked us to show, and a match on the announcement's own words finds
 *  both rows. */
const unreadRowsOfKind = (page: Page, kind: string) =>
  page.locator(UNREAD_ROW).filter({ has: page.getByText(kind, { exact: true }) })

/** Leave the bell quiet and the keyboard back in the terminal.
 *
 * Marking read is the product's own way to say "I have seen these", so the
 * assertions that follow are about a TRANSITION the test caused — quiet, then
 * exactly one — rather than about the absolute state some earlier file left
 * behind. Nothing is weakened: "nothing has happened yet" is still asserted,
 * it is simply established first instead of assumed. */
async function quietBell(page: Page): Promise<void> {
  await showSidebarView(page, 'notifications')
  const markRead = page.getByTestId('notifications-mark-read')
  if (await markRead.isEnabled()) await markRead.click()
  await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
  // Collapse it again: the bell is a toggle, and the rest of this test opens
  // the panel the way a person does.
  await page.locator(BELL).click()
  // Collapsed, the button drops the attribute rather than setting it false
  // (ui/icon-button.tsx), so this asks the question the button answers.
  await expect(page.locator(BELL)).not.toHaveAttribute('aria-selected', 'true')
  await backToTheTerminal(page)
}

/** The keyboard belongs to the editor again after a click on the chrome.
 *  promptReady only WAITS for focus; something has to give it back. */
async function backToTheTerminal(page: Page): Promise<void> {
  await page.locator(INPUT).click()
  await promptReady(page)
}

test('a session that ends while you are elsewhere waits for you in the bell', async ({ page }) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'nocx-e2e-notify-'))
  const gate = path.join(dir, 'go')

  try {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    // The precondition that is not "a tab exists": the editor must own the
    // keyboard, or the line below is typed into nothing and the shell never
    // parks on the gate (nocx-z9s9.15).
    await promptReady(page)
    await quietBell(page)

    // The watched tab parks until the test opens the gate, then exits 1.
    await page.keyboard.type(`while [ ! -e ${gate} ]; do sleep 0.1; done; exit 1`)
    await page.keyboard.press('Enter')

    // A second tab, so the one that ends is not the one in front. Asserted,
    // not assumed: this is the whole premise of the feature.
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')
    await promptReady(page)

    // Nothing has happened: the bell is quiet, not a grey zero (nocx-708q.1).
    await expect(page.locator(BELL)).toBeVisible()
    await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)

    // Backgrounded, and only now may the shell exit. Nothing before this
    // line can have raised anything, which is the property under test.
    fs.writeFileSync(gate, '')

    // Wait on the badge, never on a duration. The budget is a hang detector,
    // not a claim about how fast a shared runner is: this predicate spans a
    // real pty's exit, the session layer's classification, the feed and a
    // change notification back to the renderer.
    await expect(page.locator(BELL).locator(BADGE)).toHaveText('1', { timeout: 30_000 })

    // The tab that ended closed itself, which is what a clean exit has always
    // done. Asserted because it is the reason the row below is inert, and
    // because it is what makes "the fact outlived the tab" a real claim.
    await expect(page.locator(TAB)).toHaveCount(1)

    // The bell is the activity-bar's view button, so clicking it IS opening
    // the panel.
    await page.locator(BELL).click()

    // Rows are targeted by the kit's identity class scoped to the panel —
    // the established pattern (notes.spec.ts:19, snippets.spec.ts). RecordRow
    // takes no data-testid and adding one to the kit for a test is the tail
    // wagging the dog.
    await expect(page.locator(UNREAD_TITLE)).toHaveCount(1)
    await expect(page.locator(UNREAD_TITLE)).toContainText('ended')
    // A local session has no host, and the row says so in words rather than
    // in a gap: "Session on  ended" was the centre's first source saying
    // nothing (nocx-lmmi5). Read as raw text, because Playwright's text
    // matchers normalise whitespace — which is precisely the defect they
    // would hide.
    expect(await page.locator(UNREAD_TITLE).textContent()).toBe('Local session ended')

    // Its tab is gone, and the row says that too rather than doing nothing
    // when clicked. This is what `canActivate` means now: the row is inert
    // exactly when the TAB is gone (nocx-2gfh6).
    const endedMeta = page.locator(`${UNREAD_ROW} .ui-record-row__meta-text`)
    await expect(endedMeta).toHaveText('session closed')

    const rowsBefore = await page.locator(ROW_TITLE).count()

    // Mark all read is a deliberate act on the panel header, and it is the
    // ONLY thing that clears the count: looking at a tab is not the same
    // fact as "you saw what we told you".
    await page.getByTestId('notifications-mark-read').click()
    await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
    // The row stays: this is an inbox over a journal, not a queue that
    // empties.
    await expect(page.locator(ROW_TITLE)).toHaveCount(rowsBefore)

    // ── and the row you can go back to ────────────────────────────────────
    // A program in a LIVE tab asks nocx to present a message (ADR-0029, the
    // centre's other source). Its tab is still open, so this is the row the
    // epic's "clicking the row focuses the tab" is about.
    await backToTheTerminal(page)

    // One ordinary command first, and wait until the feed carries its ENDING.
    //
    // A command that finished is itself something to catch up on
    // (`block.finished`, nocx-n3nfg), so every line this test types puts a row
    // in the feed — and the feed collapses a run per session, kind and level
    // (internal/notify/feed.go, collapseKeyOf), so once THIS row exists the
    // printf's own ending joins it instead of adding a second one. That is what
    // makes the number below a fact about the program's message rather than
    // arithmetic over however many sources a command happens to have. The
    // arithmetic is what broke: it read 1 while the centre's only sources were
    // OSC and a session ending, and 2 the day a command's ending was wired,
    // with nothing about this test's own subject changed.
    const settling = 'true settling the feed'
    await page.keyboard.type(settling)
    await page.keyboard.press('Enter')
    await promptReady(page)
    await expect(page.locator(UNREAD_ROW).filter({ hasText: settling })).toHaveCount(1, {
      timeout: 30_000,
    })
    const badge = page.locator(BELL).locator(BADGE)
    await expect(badge).toHaveText(/^\d+$/)
    const waitingBefore = Number(await badge.textContent())

    await page.keyboard.type(`printf '\\033]777;notify;deploy done;staging\\007'`)
    await page.keyboard.press('Enter')

    // Backgrounded again, by a tab opened after the request: the row must
    // take us back to the tab that raised it, not to whatever is in front.
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')

    // Exactly ONE more thing is waiting than was waiting a moment ago. The bell
    // counts ROWS and the printf's ending joined the settling row, so the row
    // the bell gained is the message the program asked us to present.
    await expect(badge).toHaveText(String(waitingBefore + 1), { timeout: 30_000 })
    await showSidebarView(page, 'notifications')
    const deployRow = unreadRowsOfKind(page, 'program.notify')
    await expect(deployRow).toHaveCount(1)
    const deployTitle = deployRow.locator('.ui-record-row__title')
    await expect(deployTitle).toHaveText('deploy done')

    // Clicking the row takes you to the tab it came from — the reason the
    // row is worth reading rather than merely counting.
    await deployTitle.click()
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'true')
  } finally {
    fs.rmSync(dir, { recursive: true, force: true })
  }
})
