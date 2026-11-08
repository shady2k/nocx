import { appReadyForInput, test, expect, type Page } from './harness'

// nocx-ouru5.1.3: a new tab opened third or fifth instead of second.
//
// The strip's order is STORED (tabs.position) and the backend seats a new tab
// immediately after the one that was active. A create in the middle of a strip
// renumbers every tab after it, and the create's answer carries only the tab
// that was minted — so a renderer that folds that answer into its cache and
// sorts by the positions it still holds compares the new tab's fresh position
// against its neighbours' STALE ones, and the tie resolves by array order.
// Measured on the shipped build: with the first of five tabs active, the new
// tab landed third.
//
// These cases read the strip's own order (the DOM the person looks at) and the
// pane ids it carries, so "right after the active tab, every time" is what is
// asserted rather than an index the test predicted.

const TAB = '.nocx-tab'

/** The strip's rows, in drawing order, as their pane ids. */
async function stripIds(page: Page): Promise<string[]> {
  return page.evaluate(() =>
    Array.from(document.querySelectorAll('.nocx-tab')).map(
      (t) => t.getAttribute('data-pane-id') ?? '',
    ),
  )
}

/** Activate the nth row (1-based, as Cmd+1..9 counts them). */
async function activate(page: Page, n: number): Promise<void> {
  await page.keyboard.press(`Meta+${n}`)
  await expect(page.locator(TAB).nth(n - 1)).toHaveAttribute('aria-selected', 'true', {
    timeout: 10_000,
  })
}

/**
 * Create a tab and assert the row it landed on: the id that was not there
 * before is the one that was minted, and its index in the strip is the answer.
 */
async function createTabBeside(page: Page, before: string[], at: number): Promise<void> {
  await page.keyboard.press('Meta+t')
  await expect(page.locator(TAB)).toHaveCount(before.length + 1, { timeout: 15_000 })
  await expect
    .poll(
      async () => {
        const after = await stripIds(page)
        const fresh = after.filter((id) => !before.includes(id))
        return fresh.length === 1 ? after.indexOf(fresh[0]) : -2
      },
      { timeout: 15_000, message: `the new tab must be drawn at row ${at + 1}` },
    )
    .toBe(at)
}

test('a new tab opens immediately right of the active one, five tabs and after a close', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })
  await appReadyForInput(page)

  // Five tabs, the shape the owner measured on.
  for (let i = 0; i < 4; i++) {
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(i + 2, { timeout: 15_000 })
  }
  await expect(page.locator(TAB)).toHaveCount(5)

  // The first tab is active: the new one is SECOND.
  await activate(page, 1)
  await createTabBeside(page, await stripIds(page), 1)

  // Now six rows, and the fourth is active: the new one is FIFTH.
  await activate(page, 4)
  await createTabBeside(page, await stripIds(page), 4)

  // And the same holds after a tab was closed in between, which is where the
  // stored positions carry what a close left behind.
  const ids = await stripIds(page)
  const closing = ids[1]
  await page.locator(TAB).nth(1).hover()
  await page.locator(TAB).nth(1).locator('.ui-icon-button').click()
  await expect(page.locator(TAB)).toHaveCount(ids.length - 1, { timeout: 15_000 })
  const left = await stripIds(page)
  expect(left, 'the closed row left the strip and nothing else moved').toEqual(
    ids.filter((id) => id !== closing),
  )

  await activate(page, 1)
  await createTabBeside(page, await stripIds(page), 1)

  await activate(page, 3)
  await createTabBeside(page, await stripIds(page), 3)
})

/**
 * TWO CREATES IN ONE TURN (the second half of nocx-ouru5.1.3).
 *
 * A tab's row reaches the renderer a round trip after the tab is minted, and
 * the seat of the next create is "immediately after the tab in front". Asked
 * twice before the first answer, that seat used to be resolved through the
 * cache, which had no row yet — so the second tab named no anchor and the
 * backend seated it at the head of the strip.
 *
 * The two keydowns are dispatched in ONE task, which is what makes the window
 * deterministic: a websocket answer is delivered in a later task, so neither
 * create can be answered between them. It is the product's own listener on
 * window (panes.ts's onKeydown), not a synthetic shortcut around it — the same
 * reason the reorder specs dispatch their own DragEvents.
 */
test('two tabs created in one turn are seated one after the other', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })
  await appReadyForInput(page)

  for (let i = 0; i < 4; i++) {
    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(i + 2, { timeout: 15_000 })
  }
  // THE THIRD TAB IS THE ANCHOR, deliberately: seat the pair beside the first
  // tab and a create that named NO anchor still lands next to it, so the
  // assertion could not tell the two builds apart. Beside a tab in the middle
  // of a strip, "no anchor" means the head of the strip and "beside the tab I
  // just asked for" means the row after it.
  await activate(page, 3)
  const before = await stripIds(page)

  await page.evaluate(() => {
    for (let press = 0; press < 2; press++) {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', metaKey: true, bubbles: true }))
    }
  })
  await expect(page.locator(TAB)).toHaveCount(before.length + 2, { timeout: 15_000 })

  await expect
    .poll(
      async () => {
        const after = await stripIds(page)
        const fresh = after.filter((id) => !before.includes(id))
        if (fresh.length !== 2) return `saw ${fresh.length} new rows`
        const seats = fresh.map((id) => after.indexOf(id)).sort((a, b) => a - b)
        return seats.join(',')
      },
      { timeout: 15_000, message: 'the two new tabs must be the rows right after the third' },
    )
    .toBe('3,4')
})
