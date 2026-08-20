import { test, expect, promptReady } from './harness'

/**
 * e2e: a paragraph typed into a snippet body stays inside the box (nocx-dn33v).
 *
 * The unit test in frontend/src/cm-host.test.ts asserts the host mounts with
 * wrapping on, which is the mechanism. This one asserts the consequence a
 * person sees, and it has to be a browser: jsdom lays nothing out, so "the text
 * does not run off the right edge" is not a question it can answer.
 */

const DIALOG = '.nocx-dialog[open]'
const EDITOR = '.sn-body-editor'
const CONTENT = '.sn-body-editor .cm-content'

const LONG =
  'This is one paragraph with no newline in it at all, long enough that it ' +
  'cannot possibly fit across the width of the snippet dialog, which is the ' +
  'whole point of the check.'

test('a long snippet body wraps instead of running off the box', async ({ page }) => {
  await page.goto('/')
  // The app has to be up before the shortcut is pressed. On WebKit this
  // keystroke landed before Meta+, was wired: Settings never opened, the click
  // on its rail waited on an element that was never going to appear, and the
  // test died on the 30s budget. snippets.spec.ts records the same failure in
  // the same words — waiting on the observable state is the fix, and a bigger
  // budget only moves it.
  await promptReady(page)
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="snippets"]').click()
  await expect(page.locator('.sn-root')).toBeVisible({ timeout: 20_000 })

  await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New snippet' }).click()
  const dialog = page.locator(DIALOG).filter({ hasText: 'New snippet' })
  await expect(dialog).toBeVisible()

  await dialog.locator(CONTENT).click()
  await page.keyboard.type(LONG)

  // WRAPPED, so the document is drawn on more than one line…
  // WRAPPED, so the one document line is drawn on more than one visual row.
  // Counting `.cm-line` elements would not say this: CM6 keeps one of those per
  // document line and wraps INSIDE it, so a wrapped paragraph and an unwrapped
  // one both render exactly one. Height against the row pitch is what tells
  // them apart.
  const rows = await page.evaluate(
    ({ ct }) => {
      const content = document.querySelector(ct) as HTMLElement
      const line = content.querySelector('.cm-line') as HTMLElement
      const pitch = parseFloat(getComputedStyle(line).lineHeight)
      return Math.round(line.getBoundingClientRect().height / pitch)
    },
    { ct: CONTENT },
  )
  expect(rows).toBeGreaterThan(1)

  // …and therefore nothing of it is off to the side: the content is no wider
  // than the frame, and the frame has nothing to scroll horizontally.
  const fits = await page.evaluate(
    ({ ed, ct }) => {
      const box = document.querySelector(ed) as HTMLElement
      const content = document.querySelector(ct) as HTMLElement
      const scroller = box.querySelector('.cm-scroller') as HTMLElement
      return {
        contentWidth: Math.round(content.getBoundingClientRect().width),
        boxWidth: Math.round(box.getBoundingClientRect().width),
        overflowX: scroller.scrollWidth - scroller.clientWidth,
      }
    },
    { ed: EDITOR, ct: CONTENT },
  )
  expect(fits.contentWidth).toBeLessThanOrEqual(fits.boxWidth)
  expect(fits.overflowX).toBe(0)

  // And the first line is drawn INSIDE the frame, not under its top border —
  // the other half of what the owner saw was a line cut in two by the edge.
  const clipped = await page.evaluate(
    ({ ed, ct }) => {
      const box = (document.querySelector(ed) as HTMLElement).getBoundingClientRect()
      const first = (
        document.querySelector(`${ct} .cm-line`) as HTMLElement
      ).getBoundingClientRect()
      return { boxTop: box.top, lineTop: first.top }
    },
    { ed: EDITOR, ct: CONTENT },
  )
  expect(clipped.lineTop).toBeGreaterThanOrEqual(clipped.boxTop)

  await dialog.getByRole('button', { name: 'Cancel' }).click()
})
