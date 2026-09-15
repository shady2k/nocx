/**
 * Rows are full width, and moving the gutter into them moved nothing a person
 * reads (nocx-9bpeq.8, spec 2026-09-14 §4).
 *
 * The pane used to inset everything; the ledger's rows carry that inset now so
 * a row can paint to the edge. The risk is geometry, not colour: xterm's cols
 * come from a width, the frozen block reproduces the grid at a measured cell
 * width (nocx-dvf6k), and the scrollbar must stay the pane's edge (nocx-mvbne).
 * Every wait is on a settled layout, never on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

const GUTTER_TOLERANCE = 0.5

async function measure(page: Page) {
  return page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    const area = pane.querySelector<HTMLElement>('.scrollback-area')!
    const inner = pane.querySelector<HTMLElement>('.scrollback-inner')!
    const live = pane.querySelector<HTMLElement>('.xterm-live-container')!
    const screen = pane.querySelector<HTMLElement>('.xterm-screen')!
    const blocks = pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block')
    const block = blocks[blocks.length - 1] ?? null
    const line = block?.querySelector<HTMLElement>('.cmd-output .term-line') ?? null
    const cs = getComputedStyle(live)
    const liveContent = live.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
    const cellWidth = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
    const areaRect = area.getBoundingClientRect()
    return {
      frozenLeft: line ? line.getBoundingClientRect().left : null,
      liveLeft: screen.getBoundingClientRect().left,
      cols: Math.round(screen.getBoundingClientRect().width / cellWidth),
      expectedCols: Math.floor(liveContent / cellWidth),
      cellWidth,
      blockWidth: block ? block.getBoundingClientRect().width : null,
      areaContentWidth: area.clientWidth,
      areaLeft: areaRect.left,
      blockLeft: block ? block.getBoundingClientRect().left : null,
      areaRight: areaRect.right,
      paneRight: pane.getBoundingClientRect().right,
    }
  })
}

async function settled(page: Page, previousCols: number | null) {
  // Settled = a cell width is published and the grid fills its box exactly
  // as the fit computes it; after a resize, also that the cols moved.
  await expect
    .poll(
      async () => {
        const m = await measure(page)
        return (
          m.cellWidth > 0 &&
          m.frozenLeft !== null &&
          m.cols === m.expectedCols &&
          (previousCols === null || m.cols !== previousCols)
        )
      },
      { timeout: 15_000 },
    )
    .toBe(true)
  return measure(page)
}

function assertAligned(m: Awaited<ReturnType<typeof measure>>) {
  expect(Math.abs((m.frozenLeft ?? NaN) - m.liveLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(m.cols).toBe(m.expectedCols)
  // The row spans the scroller's content box: its tint can reach the edge.
  expect(Math.abs((m.blockLeft ?? NaN) - m.areaLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(Math.abs((m.blockWidth ?? NaN) - m.areaContentWidth)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  // The scrollbar is the pane's trailing edge (nocx-mvbne).
  expect(Math.abs(m.areaRight - m.paneRight)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
}

test('the frozen column and the live column share one edge, before and after a resize', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await page.keyboard.type('printf "gutter-probe\\n"')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .scrollback-inner > .cmd-block .term-line').first(),
  ).toContainText('gutter-probe', { timeout: 30_000 })
  await promptReady(page)

  const before = await settled(page, null)
  assertAligned(before)

  await page.setViewportSize({ width: 960, height: 800 })
  const after = await settled(page, before.cols)
  assertAligned(after)
})
