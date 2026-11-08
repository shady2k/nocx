/**
 * Rows are full width, and a normal command's rows all sit in the transcript's
 * column (nocx-9bpeq.8, spec 2026-09-14 §4, ADR-2yzsq8mk).
 *
 * The pane used to inset everything; the ledger's rows carry that inset now so
 * a row can paint to the edge. ADR-2yzsq8mk decides which rows wear it: a
 * normal command — running or finished — sits in the transcript's 24px gutter,
 * so nothing moves when its card freezes, and only a full-screen program owns
 * the whole width (e2e/grid-width.spec.ts). The risk is geometry, not colour:
 * cols come from a width the fit computes, the frozen block reproduces the grid
 * at a measured cell width (nocx-dvf6k), and the scrollbar must stay the pane's
 * edge (nocx-mvbne). Every wait is on a settled layout, never on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

const GUTTER_TOLERANCE = 0.5

async function measure(page: Page) {
  return page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    const area = pane.querySelector<HTMLElement>('.scrollback-area')!
    const inner = pane.querySelector<HTMLElement>('.scrollback-inner')!
    const live = pane.querySelector<HTMLElement>('.terminal-live-container')!
    // The live painter publishes the accepted model width as aria-colcount.
    // Do not infer it from text length: a wide grapheme occupies two model
    // columns but one UTF-16 code unit. Use Range only for painted geometry.
    // Scope to the live painter: frozen rows share the same row class.
    const grid = pane.querySelector<HTMLElement>('.term-grid')!
    const row = grid.querySelector<HTMLElement>('.term-grid-row')!
    const rowRange = document.createRange()
    rowRange.selectNodeContents(row)
    const blocks = pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block')
    const block = blocks[blocks.length - 1] ?? null
    const line = block?.querySelector<HTMLElement>('.cmd-output .term-grid-row') ?? null
    const cs = getComputedStyle(live)
    const inlineInset = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)
    const liveContent = live.clientWidth - inlineInset
    // The transcript gutter a normal row wears, read from the box that pays it
    // rather than hard-coded: the frozen block and the active grid are one
    // column (ADR-2yzsq8mk).
    const blockStyle = block ? getComputedStyle(block) : null
    const blockPadLeft = blockStyle ? parseFloat(blockStyle.paddingLeft) : 0
    // Match terminal-content.ts `usableViewport`: fitting starts at the
    // scroller's clientWidth and subtracts the live row's inset. The live
    // container can have a different box width while its layout is changing.
    const fitContent = area.clientWidth - inlineInset
    const cellWidth = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
    const areaRect = area.getBoundingClientRect()
    const rangeRect = rowRange.getBoundingClientRect()
    return {
      frozenLeft: line ? line.getBoundingClientRect().left : null,
      frozenInset: line ? line.getBoundingClientRect().left - areaRect.left : null,
      blockPadLeft,
      liveLeft: rangeRect.left,
      // The grid's own box, not the text's first glyph: the inset this spec
      // is about is the box's.
      liveInset: row.getBoundingClientRect().left - areaRect.left,
      cols: Number(grid.getAttribute('aria-colcount')),
      expectedCols: Math.floor(fitContent / cellWidth),
      rangeCols: Math.round(rangeRect.width / cellWidth),
      // Keep the independent model and owner facts beside the rendered Range
      // width: when the session owns its accepted grid, it may be narrower than
      // the local fit, and the Range value alone cannot distinguish the two.
      gridOwner: area.dataset.gridOwner ?? 'local',
      rowTextLength: row.textContent?.length ?? 0,
      rangeWidth: rangeRect.width,
      liveContentWidth: liveContent,
      fitContentWidth: fitContent,
      overflowX: getComputedStyle(area).overflowX,
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
  // Settled = a cell width is published and either the local fit fills its
  // box, or the session-owned narrower grid remains scrollable. After a resize,
  // also require that the painted grid's column count moved.
  let lastMeasurement: Awaited<ReturnType<typeof measure>> | null = null
  try {
    await expect
      .poll(
        async () => {
          const m = await measure(page)
          lastMeasurement = m
          const localFit = m.gridOwner === 'local' && m.cols === m.expectedCols
          const narrowerSessionGrid =
            m.gridOwner === 'session' && m.cols < m.expectedCols && m.overflowX === 'auto'
          return (
            m.cellWidth > 0 &&
            m.frozenLeft !== null &&
            (localFit || narrowerSessionGrid) &&
            (previousCols === null || m.cols !== previousCols)
          )
        },
        { timeout: 15_000 },
      )
      .toBe(true)
  } catch (error) {
    console.error('row-gutter last measured geometry:', JSON.stringify(lastMeasurement))
    throw error
  }
  return measure(page)
}

function assertAligned(m: Awaited<ReturnType<typeof measure>>) {
  // ONE COLUMN (ADR-2yzsq8mk): a normal command, running or finished, sits at
  // the transcript's gutter — so nothing moves when the card freezes. What is
  // NOT allowed is either one drifting from that rule: a frozen row off the
  // block's padding, or an active row off the same gutter.
  expect(Math.abs((m.frozenInset ?? NaN) - m.blockPadLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(
    Math.abs(m.liveInset - m.blockPadLeft),
    'the active row shares the frozen column',
  ).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  if (m.gridOwner === 'session' && m.cols < m.expectedCols) {
    // An accepted session grid is not resized to fit this window. A narrower
    // grid is valid only while the scroller preserves it as a whole, and its
    // rendered Range must still span the model's full columns.
    expect(m.overflowX).toBe('auto')
    expect(m.cols).toBeLessThan(m.expectedCols)
  } else {
    expect(m.cols).toBe(m.expectedCols)
  }
  expect(m.rangeCols).toBe(m.cols)
  // The row spans the scroller's content box: its tint can reach the edge.
  expect(Math.abs((m.blockLeft ?? NaN) - m.areaLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(Math.abs((m.blockWidth ?? NaN) - m.areaContentWidth)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  // The scrollbar is the pane's trailing edge (nocx-mvbne).
  expect(Math.abs(m.areaRight - m.paneRight)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
}

test('the frozen column and the live grid share the transcript gutter, before and after a resize', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await page.keyboard.type('printf "gutter-probe\\n"')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .scrollback-inner > .cmd-block .term-grid-row').first(),
  ).toContainText('gutter-probe', { timeout: 30_000 })
  await promptReady(page)

  const before = await settled(page, null)
  assertAligned(before)

  await page.setViewportSize({ width: 960, height: 800 })
  const after = await settled(page, before.cols)
  assertAligned(after)
})
