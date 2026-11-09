import { test, expect, clickIntoEditor, promptReady } from './harness'

// nocx-v408b, and the race nocx-3an1s.1.10 describes.
//
// WHAT A PERSON DOES: close every tab, and use the tab that is left. The
// window opens a replacement tab rather than standing empty, so the only
// thing a person can do next is type — and that is what these tests do.
//
// WHY THERE IS A TAB COUNT AND NOT A SLEEP ANYWHERE. The product says when a
// close has happened (the strip loses its row) and when the replacement is
// usable (its prompt editor owns input). Both are states, so both are waited
// on; a duration would pass on a fast machine and be wrong on a slow one.
//
// THE SECOND TEST IS THE RAPID SHAPE. The first waits for the count between
// clicks, the way the owner's script and the .1.10 reproduction did; the
// second fires every click without waiting, which is the ordering an ordinary
// person produces by clicking down the strip. Both must leave one usable tab
// and no pane that failed to start.

const TAB = '.nocx-tab'
const TITLE = '.nocx-tab-title'
const CLOSE = '[aria-label="Close tab"]'
const COMMAND_BLOCK = '.pane.active .cmd-block[data-block-kind="command"]'
const RUNNING_BLOCK = '.pane.active .cmd-block.cmd-block-running'
const PANE_ERROR = '.pane-error'

/** Run one command in the tab in front and wait for its block to SETTLE.
 *  Completed output is the premise of both tests: the close is racing output
 *  the pty has already finished printing, not an empty tab. */
async function runCommandToCompletion(page: import('@playwright/test').Page, marker: string) {
  await clickIntoEditor(page)
  await page.keyboard.type(`echo ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator(COMMAND_BLOCK).filter({ hasText: marker })).toHaveCount(1, {
    timeout: 15_000,
  })
  await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 15_000 })
}

/** Open the tabs this test starts with, each with completed output of its
 *  own, and leave the last one in front with a ready prompt. */
async function tabSeededWindow(page: import('@playwright/test').Page, tabs: number) {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)
  await promptReady(page)
  for (let index = 1; index < tabs; index++) {
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(index + 1)
    await promptReady(page)
    await runCommandToCompletion(page, `OUTPUT-${index}-${Date.now().toString(36)}`)
  }
}

async function closeFirstTab(page: import('@playwright/test').Page) {
  const first = page.locator(TAB).first()
  await first.hover()
  await first.locator(CLOSE).click()
}

test.describe('closing every tab leaves a usable tab (nocx-v408b)', () => {
  test('the replacement tab accepts a command after every tab is closed', async ({ page }) => {
    await tabSeededWindow(page, 4)

    // One at a time, waiting on the strip's own count after each close: the
    // shape in which the count is the product's answer that the close landed.
    for (let remaining = 4; remaining > 1; remaining--) {
      await closeFirstTab(page)
      await expect(page.locator(TAB)).toHaveCount(remaining - 1)
    }
    // …and the last tab, whose close is the one that mints the replacement.
    await closeFirstTab(page)
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })

    // THE SENTENCE A PERSON WOULD SAY: type into it and see the answer. The
    // prompt editor is the product's own readiness signal, and the echo's
    // own output row is that the command ran rather than that the card is up.
    await promptReady(page)
    const marker = `REPLACED-${Date.now().toString(36)}`
    await clickIntoEditor(page)
    // THE KEYS REACHED A SHELL, NOT MERELY AN EDITOR, and two rows say so: the
    // command's own block, and the marker as OUTPUT in the grid. The tab's
    // title used to be the third witness here (`printf '\033]0;…'`), and it
    // stopped being one when nocx-ouru5.1.1 made a title a PROGRAM sets end
    // with that program's run — a title that is gone by the time this reads it.
    await page.keyboard.type(`echo ${marker}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(COMMAND_BLOCK).filter({ hasText: marker })).toHaveCount(1, {
      timeout: 15_000,
    })
    await expect(
      page
        .locator('.pane.active [role="grid"][aria-label="Terminal output"]')
        .getByRole('row')
        .filter({ hasText: marker })
        .first(),
    ).toBeVisible({ timeout: 15_000 })

    // A pane that never started says so in the pane, and its own words are
    // the defect this test was written for.
    await expect(page.locator(PANE_ERROR)).toHaveCount(0)
  })

  test('rapid sequential closes leave one usable tab and start no removed pane (nocx-3an1s.1.10)', async ({
    page,
  }) => {
    await tabSeededWindow(page, 7)

    // NO WAIT BETWEEN CLICKS. Seven tabs are closed as fast as the clicks
    // land, which is the ordering that produced "content: no such pane" at
    // five closures in about 65 ms.
    for (let index = 0; index < 7; index++) {
      await closeFirstTab(page)
    }

    // The window is never empty: a replacement tab is minted by the close
    // that took the last one.
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 20_000 })
    await promptReady(page)

    const marker = `SURVIVOR-${Date.now().toString(36)}`
    await clickIntoEditor(page)
    await page.keyboard.type(`echo ${marker}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(COMMAND_BLOCK).filter({ hasText: marker })).toHaveCount(1, {
      timeout: 15_000,
    })
    await expect(
      page
        .locator('.pane.active [role="grid"][aria-label="Terminal output"]')
        .getByRole('row')
        .filter({ hasText: marker })
        .first(),
    ).toBeVisible({ timeout: 15_000 })

    // "No removed pane is started": a start that named a pane the backend had
    // already taken is the visible `.pane-error`, and there is none.
    await expect(page.locator(PANE_ERROR)).toHaveCount(0)
    // And the tab that is left is the one on screen, not an empty strip.
    await expect(page.locator(TITLE).first()).not.toHaveText('')
  })
})
