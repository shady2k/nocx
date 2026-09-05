/**
 * e2e: the Skills page's switches form one column (nocx-zrh5z).
 *
 * The page draws the same control twice in two different frames. Above, the
 * settings section it owns holds `skills.enabled`, whose control line reserves
 * the reset gutter every settings row reserves — so the switch stops short of
 * the page's content edge. Below, every skill's enable switch hangs off the
 * record row's trailing edge, which IS the page's content edge. Both are right
 * inside their own frame, and the owner saw the result: two identical switches
 * down one screen that do not share a vertical line.
 *
 * THIS CANNOT BE PINNED IN A UNIT TEST. jsdom performs no layout, so
 * getBoundingClientRect answers zero for both elements and every arrangement
 * of them passes. It takes a real engine, which is what this suite is.
 *
 * The assertion compares the two switches WITH EACH OTHER and never against a
 * pixel constant: the rail's width is the settings surface's to change, and
 * this spec has no opinion about where the column stands — only that there is
 * one. Both edges are measured off the same page box, so the fractional
 * offsets that box carries cancel; the tolerance is one pixel of rounding, not
 * of slack for a real disagreement (30px is what the defect measured).
 *
 * The row comes from the skill nocx ships, so this needs no fixture, no model
 * and no install: a fresh profile discovers `skill-authoring` and the list has
 * a row from the moment the page opens. The count assertions come first on
 * purpose — with either element missing the comparison would pass by measuring
 * nothing, which is the one way an alignment test lies.
 */
import { appReadyForInput, test, expect, settingsReady } from './harness'

const ASSISTANT_GROUP = '.ui-grouped-nav__group[data-group="assistant"]'
const SKILLS_NAV = '.ui-grouped-nav__item[data-item="skills"]'
/** The page-level switch, addressed through the declaration key it renders. */
const PAGE_SWITCH = '.ui-settings-row[data-key="skills.enabled"] .ui-checkbox'
/** A row's switch, in the kit's state cell — the whole point of the cell. */
const ROW_SWITCH = '.ui-record-row__state .ui-checkbox'

test('the page switch and a skill row switch stand in one column (nocx-zrh5z)', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 10_000 })
  await appReadyForInput(page)
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`${ASSISTANT_GROUP} ${SKILLS_NAV}`).click()

  const pageSwitch = page.locator(PAGE_SWITCH)
  const rowSwitch = page.locator(ROW_SWITCH).first()
  await expect(pageSwitch).toHaveCount(1)
  await expect(rowSwitch).toBeVisible({ timeout: 10_000 })

  const pageBox = await pageSwitch.boundingBox()
  const rowBox = await rowSwitch.boundingBox()
  if (pageBox === null || rowBox === null) throw new Error('a switch had no box to measure')

  // Right edges, because that is the edge both frames anchor to: the settings
  // control line ends at its rail and the row's state cell hangs from the
  // trailing edge of its actions.
  expect(Math.abs(pageBox.x + pageBox.width - (rowBox.x + rowBox.width))).toBeLessThanOrEqual(1)
})
