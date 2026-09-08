import { test, expect, promptReady, showSidebarView } from './harness'
import type { Page } from './harness'

/**
 * e2e: a command you ran and walked away from is waiting in the notification
 * centre, and one that FAILED says so (nocx-n3nfg — the block.finished gate).
 *
 * The sentence the epic exists for, walked through the product's own
 * surfaces: a person types a command, it finishes, and there is a row for it
 * in the bell. Nothing here reads the store, calls a JSON-RPC method or holds
 * a seam with a double — it types into a real shell, watches the block stop
 * running, opens the panel and reads the rendered rows.
 *
 * WHY THIS SPEC EXISTS AT ALL. `block.finished` was declared routable in
 * internal/notify/catalogue.go — a label, a description and a channel toggle
 * a user could set — and raised by no production code, so Settings offered
 * "A command finished" and nothing ever produced one. Every unit and
 * integration suite was green throughout, and so was the whole e2e suite,
 * because every notification spec drives either OSC 777 or a session ending:
 * the two paths that WERE wired. The raise has since been written against the
 * `NotifyRaiser` seam, which a test double holds — so everything past ingress
 * has been read from the code and never watched. This is the check that
 * cannot be fooled that way (AGENTS.md rule 2: `deadcode` and coverage are
 * floors, and neither can report a feature that is missing).
 *
 * NO TOAST, NO BANNER, DELIBERATELY. `blockFinished` ships with no default
 * channels: it fires once per command, hundreds of times an hour, and a
 * banner default would let `ls` interrupt the user. It reaches the FEED
 * because ingress records the occurrence before the router decides anything
 * (internal/notify/ingress.go — "stamps, records, then submits"). A spec
 * asserting a toast here would be asserting a decision that was deliberately
 * not taken.
 *
 * THE FAILING CASE IS THE ONE THAT MATTERS. The report that opened this was a
 * `du -Hs` that exited 130 and a centre that said "Nothing to catch up on" —
 * so "did anything appear" is not the assertion. A command that exited
 * non-zero must be distinguishable from one that succeeded, on the row, in
 * three independent ways: the title's verb, the badge's tone, and the detail
 * line carrying the exit status the title had no room for.
 *
 * TWO TRAPS, both already paid for by somebody:
 *
 *  - The feed is IN-MEMORY in a shared stand and `resetStand` does not clear
 *    it. Rows from earlier spec files are legitimately still there, so this
 *    test ESTABLISHES its starting state rather than assuming it: everything
 *    is marked read first (`quietBell`), which makes UNREAD mean "raised
 *    since this test began". Nothing here passes only when this file runs
 *    first.
 *  - Both browser projects run this file against ONE nocx-server and one $HOME
 *    (playwright.config.ts declares chromium and webkit; workers is 1, so they
 *    run in sequence), and marking read does not empty the feed. An unread
 *    scope survives that, but a `hasText` filter cannot be read, it has to
 *    MATCH — an untagged 'echo block-ok succeeded' would match the other
 *    project's row too. So the COMMANDS carry the project name, which puts it
 *    in the title, because the title is the masked intent verbatim
 *    (ws_ledger_notify.go, blockSubject). This is the same trap
 *    notification-centre-grouping.spec.ts documents at `runTitles`, and CI
 *    hides it: `ci-e2e` runs one job per browser in parallel, so each browser
 *    gets its own backend there and the collision cannot happen.
 */

const TAB = '.nocx-tab'
/** The bell's rail button. Addressed by `data-view`, which is the sidebar's
 *  OWN vocabulary for a view button and what every other view is found by. */
const BELL = 'button[data-view="notifications"]'
const BADGE = '.ui-badge'
const PANE = '.pane.active'
const INPUT = `${PANE} .nocx-editor-input`
const LIST = '.notifications-panel__list'
/** The panel's OWN rows. A run member is a RecordRow too and sits INSIDE its
 *  row's disclosed region, so an unqualified descendant selector starts
 *  counting members as rows of the list the moment anything is expanded. The
 *  child combinator keeps "how many rows are there" a question about the
 *  list. */
const ROW = `${LIST} > .ui-collection-row`
/** The rows this test raised. `data-selected` is the kit's own account of the
 *  panel's `selected={!o.read}` (ui/collection-view.tsx), so unread is read
 *  off the surface rather than out of the store. */
const UNREAD_ROW = `${ROW}[data-selected="true"]`

/**
 * The two commands, tagged per project — see the second trap above.
 *
 * Neither carries a quote or a bracket, and that is deliberate: the composer
 * is CodeMirror, and a spec that types `sh -c 'exit 7'` is also testing
 * whatever the editor does with an opening quote. `false` is a shell builtin
 * that ignores its arguments and returns 1, so the tag rides along without
 * changing the outcome — a failing command whose failure is the shell's own,
 * not a construction of this test's.
 */
const succeeds = (project: string) => `echo block-ok-${project}`
const fails = (project: string) => `false block-bad-${project}`

/** The keyboard belongs to the editor again after a click on the chrome.
 *  promptReady only WAITS for focus; something has to give it back. */
async function backToTheTerminal(page: Page): Promise<void> {
  await page.locator(INPUT).click()
  await promptReady(page)
}

/**
 * Leave the bell quiet, the panel OPEN and the keyboard back in the terminal.
 *
 * Marking read is the product's own way to say "I have seen these", so every
 * count below is about a TRANSITION this test caused rather than about which
 * spec files ran first. Nothing is weakened: "nothing is waiting yet" is
 * still asserted, it is simply established instead of assumed.
 *
 * The panel is left open, as notification-centre-grouping.spec.ts leaves it:
 * the list being on screen while the rows arrive is the observable this test
 * waits on, and toggling a panel shut in order to toggle it back is two more
 * clicks that can go wrong for no assertion gained.
 */
async function quietBell(page: Page): Promise<void> {
  await showSidebarView(page, 'notifications')
  const markRead = page.getByTestId('notifications-mark-read')
  if (await markRead.isEnabled()) await markRead.click()
  await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
  await expect(page.locator(UNREAD_ROW)).toHaveCount(0)
  await backToTheTerminal(page)
}

/**
 * Write one shell line, press Enter, and WAIT FOR THE PROMPT TO COME BACK.
 *
 * The wait is the fix rather than politeness (notification-osc.spec.ts):
 * submitting hands the composer's box to the command, and the next line typed
 * into a surface that is still busy reaches the shell as whatever survived.
 * The failure then reports as a missing notification rather than at the line
 * that was dropped.
 */
async function run(page: Page, line: string): Promise<void> {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await promptReady(page)
}

/**
 * The block this command left behind, once it has stopped running.
 *
 * This is claim 1 — "a command runs in a pane and FINISHES" — asserted on the
 * product's own account of the fact rather than on a duration: the scrollback
 * drops `cmd-block-running` when the terminal sets the block's status, and
 * paints the exit chip from the authenticated execution attempt (ADR-0024),
 * never from the byte stream. `exit` is what the chip reads for a non-zero
 * code and `ok` for zero (scrollback/blocks.ts).
 */
function finishedBlock(page: Page, line: string) {
  return page.locator(`${PANE} .cmd-block:not(.cmd-block-running)`).filter({ hasText: line })
}

/** One panel row's parts, by the kit's identity classes scoped to the panel —
 *  the established pattern (notes.spec.ts, snippets.spec.ts). RecordRow takes
 *  no data-testid and adding one to the kit for a test is the tail wagging the
 *  dog. */
const titleOf = (row: ReturnType<Page['locator']>) => row.locator('.ui-record-row__title')
const badgeOf = (row: ReturnType<Page['locator']>) =>
  row.locator('.ui-record-row__heading .ui-badge')
const detailOf = (row: ReturnType<Page['locator']>) => row.locator('.ui-record-row__detail')

test.use({ viewport: { width: 1280, height: 900 } })

test('a command that finished is waiting in the notification centre, and a failure says so', async ({
  page,
}, testInfo) => {
  const OK = succeeds(testInfo.project.name)
  const BAD = fails(testInfo.project.name)
  // Past the suite's 30 s ceiling because this spec drives two real ptys end
  // to end and waits for each one's outcome to travel the ledger, ingress and
  // the feed back to the renderer. Every WAIT inside it is still on an
  // observable — a class, a chip, a row, a count — and never on a duration.
  test.setTimeout(90_000)

  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)
  // The precondition that is not "a tab exists": the editor must own the
  // keyboard, or the lines below are typed into nothing and nothing ever runs
  // (nocx-z9s9.15).
  await promptReady(page)
  await quietBell(page)

  // ── 1. a command runs, and finishes ─────────────────────────────────────
  await run(page, OK)
  const okBlock = finishedBlock(page, OK)
  await expect(okBlock).toHaveCount(1, { timeout: 30_000 })
  await expect(okBlock.locator('.cmd-header-exit')).toHaveText('ok')

  // ── 2. the centre holds a row naming it ─────────────────────────────────
  // The row is the observable, and it is the whole claim. An explicit budget
  // because this predicate spans a real pty's exit, the ledger's close, the
  // notify ingress and a change notification back to the renderer — a hang
  // detector, not a claim about how fast a shared runner is.
  const okRow = page.locator(UNREAD_ROW).filter({ hasText: `${OK} succeeded` })
  await expect(okRow).toHaveCount(1, { timeout: 30_000 })
  // Exactly one, so the row is this test's own doing and not a leftover:
  // quietBell zeroed the unread set a moment ago and only this command has
  // run since.
  await expect(page.locator(UNREAD_ROW)).toHaveCount(1)
  await expect(page.locator(BELL).locator(BADGE)).toHaveText('1')

  // It names the command VERBATIM and says how it went, in that order —
  // subject first, because that is what a person scans a row for. Read as raw
  // text, because Playwright's text matchers normalise whitespace, which is
  // precisely the defect they would hide.
  expect(await titleOf(okRow).textContent()).toBe(`${OK} succeeded`)
  // The badge is the kind, and its TONE is the level: an ordinary completion
  // is a success, not an advisory (notify/notifications-panel.tsx, toneOf).
  await expect(badgeOf(okRow)).toHaveText('Command finished')
  await expect(badgeOf(okRow)).toHaveAttribute('data-tone', 'success')
  // And it carries no exit line: a zero says nothing the title has not said
  // already, and a row that spends its detail on emptiness reads as broken
  // (ws_ledger_notify.go, blockFinishedBody).
  await expect(detailOf(okRow)).toHaveCount(0)

  // ── 3. and a command that FAILED is a different row ─────────────────────
  // The case the report was about: a `du -Hs` that exited 130 while the centre
  // said "Nothing to catch up on". A "did anything appear" assertion would
  // pass with failure and success rendered identically; these three do not.
  await backToTheTerminal(page)
  await run(page, BAD)
  const badBlock = finishedBlock(page, BAD)
  await expect(badBlock).toHaveCount(1, { timeout: 30_000 })
  await expect(badBlock.locator('.cmd-header-exit-fail')).toHaveText('exit 1')

  const badRow = page.locator(UNREAD_ROW).filter({ hasText: `${BAD} failed` })
  await expect(badRow).toHaveCount(1, { timeout: 30_000 })
  expect(await titleOf(badRow).textContent()).toBe(`${BAD} failed`)
  // Warning, not danger: a command exiting non-zero is a normal event in a
  // terminal, and a `danger` for `grep` finding nothing would spend the
  // loudest level the pipeline has on the least alarming thing it sees
  // (ws_ledger_notify.go, blockFinishedLevel).
  await expect(badgeOf(badRow)).toHaveAttribute('data-tone', 'warning')
  // The shell's own answer, and the number the user will search for.
  await expect(detailOf(badRow)).toHaveText('exit status 1')

  // TWO rows, not one that changed its mind. The feed collapses a run by
  // (backend, session, kind, LEVEL) and reads no title (internal/notify/
  // feed.go, collapseKeyOf), so a success and a failure from one session are
  // two rows by construction — which is the property that makes the failure
  // survivable next to the ninety-nine commands that worked.
  await expect(page.locator(UNREAD_ROW)).toHaveCount(2)
  await expect(okRow).toHaveCount(1)
  await expect(page.locator(BELL).locator(BADGE)).toHaveText('2')

  // Marking read is the ONLY thing that clears the count, and the rows STAY:
  // this is an inbox over a journal, not a queue that empties.
  const rowsBefore = await page.locator(ROW).count()
  await page.getByTestId('notifications-mark-read').click()
  await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
  await expect(page.locator(ROW)).toHaveCount(rowsBefore)
})
