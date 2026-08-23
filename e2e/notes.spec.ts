import { test, expect, promptReady, showSidebarView, type Page } from './harness'

/**
 * e2e: write it down without leaving the terminal (nocx-z56hq.7 — the notes
 * epic's gate).
 *
 * The whole sentence, walked through the product's own surfaces: press one
 * chord, type, close the tab, and find the note later by a word that
 * appears only INSIDE it — never in its title, so nothing but the search
 * index can have found it.
 *
 * The check restarts nothing by itself: a Playwright reload drops the
 * renderer and its store, so what survives it is what the backend wrote.
 * That is the survival this feature promises — the words are on disk, not
 * in a tab somebody is careful not to close.
 */

const PANEL = '.notes-panel'
const ROW = `${PANEL} .ui-record-row__title`

/** The filter, BY ITS ACCESSIBLE NAME rather than by where it sits.
 *  It used to be `.notes-panel input[type="search"]`, and that stopped
 *  matching the day the shell took the filter row over (nocx-708q.3): the
 *  field is now in `.ui-sidebar-view__filter`, a SIBLING of the body the
 *  panel root lives in. Naming it the way a person finds it survives the
 *  shell deciding where to put it, which is the point of the slot
 *  (nocx-708q.6). */
const filterField = (page: Page) => page.getByRole('searchbox', { name: 'Search notes' })

/** The chord is matched on the physical key (notes/chord.ts). */
async function pressChord(page: Page): Promise<void> {
  await page.keyboard.press('Alt+Meta+n')
}

/** The note tab's editor — a real CM6 document, typed into like one. */
async function typeIntoTheNote(page: Page, text: string): Promise<void> {
  const content = page.locator('.note-tab__editor .cm-content')
  await expect(content).toBeVisible({ timeout: 10_000 })
  await content.click()
  await page.keyboard.type(text)
}

/** Open the notes panel from the activity bar — or leave it open if a reload
 *  has already brought it back (see showSidebarView). */
async function openPanel(page: Page): Promise<void> {
  await showSidebarView(page, 'notes')
  await expect(page.locator(PANEL)).toBeVisible({ timeout: 10_000 })
}

test.describe('notes', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('a note written by the chord survives, and is found by a word from inside it', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await promptReady(page)

    // One keystroke from the impulse to the first character.
    await pressChord(page)
    await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 10_000 })

    // Two lines: the first names the note, the second is what the search
    // has to find. `zephyrine` appears nowhere else in the product.
    await typeIntoTheNote(page, 'Standup notes\nask about zephyrine rollout')

    // The tab is named by the first line, without anybody typing a name.
    await expect(page.locator('.nocx-tab[aria-selected="true"] .nocx-tab-title')).toHaveText(
      'Standup notes',
      { timeout: 10_000 },
    )

    // Close the tab. Whatever was unsaved is written on the way out.
    await page.locator('.nocx-tab[aria-selected="true"] [aria-label="Close tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 10_000 })

    // The renderer goes away entirely: what comes back can only be what the
    // backend kept.
    await page.reload()
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })

    await openPanel(page)
    await filterField(page).fill('zephyrine')

    // Found by a word that is only in the body — the title says nothing
    // about zephyrine, so a filter over titles could not have found it.
    const row = page.locator(ROW, { hasText: 'Standup notes' })
    await expect(row).toBeVisible({ timeout: 10_000 })

    await row.click()
    await expect(page.locator('.note-tab__editor .cm-content')).toContainText(
      'ask about zephyrine rollout',
      { timeout: 10_000 },
    )
    // Exactly what was typed, both lines, in order.
    await expect(page.locator('.note-tab__editor .cm-content')).toContainText('Standup notes')
  })

  test('the panel offers the first note when the library is empty, and finds nothing honestly', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await openPanel(page)

    // A query that matches nothing says so — it does not look like a
    // library that failed to load.
    await filterField(page).fill('quixotictermite')
    await expect(page.locator(`${PANEL} .ui-empty-state`)).toContainText('Nothing matches', {
      timeout: 10_000,
    })
  })

  test('a note nobody typed into is not left in the library', async ({ page }) => {
    // The chord is cheap to press by accident, and the record exists before
    // the first character does — so an empty one must not survive the tab
    // that made it, or the panel fills up with notes called by their date
    // and containing nothing.
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await promptReady(page)
    await openPanel(page)
    const rowsBefore = await page.locator(ROW).count()

    await pressChord(page)
    await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 10_000 })
    await page.locator('.nocx-tab[aria-selected="true"] [aria-label="Close tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 10_000 })

    await page.reload()
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })
    await openPanel(page)
    await expect(page.locator(ROW)).toHaveCount(rowsBefore, { timeout: 10_000 })
  })

  test('the same note opens once: asking twice focuses the tab already open', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await promptReady(page)

    await pressChord(page)
    await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 10_000 })
    await typeIntoTheNote(page, 'Only one editor')
    await expect(page.locator('.nocx-tab[aria-selected="true"] .nocx-tab-title')).toHaveText(
      'Only one editor',
      { timeout: 10_000 },
    )

    await openPanel(page)
    await page.locator(ROW, { hasText: 'Only one editor' }).click()

    // Still two tabs: the terminal and the one note. Two editors over one
    // document would be two drafts of it, and the second to save would win
    // silently.
    await expect(page.locator('.nocx-tab')).toHaveCount(2)
  })
})
