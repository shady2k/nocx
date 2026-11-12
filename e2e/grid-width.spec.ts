/**
 * The grid is never wider than the box it is drawn in (nocx-vydj).
 *
 * The height half of this invariant was learned the hard way in nocx-6w4z: the
 * grid was fitted to the pane while it was shown in a scroller shorter than the
 * pane, and the bottom rows had nowhere to be drawn. The width half was left
 * unfixed and fails the same way, one axis over — the viewport delivered to
 * content is `pane.getBoundingClientRect()`, a BORDER box that includes
 * `.pane`'s `padding: 0 10px`, so `cols` was computed from 20px the grid does
 * not have and the last columns landed past the right edge of `.terminal-live-inner`,
 * where its `overflow: hidden` cut them mid-glyph.
 *
 * Why this must run in a browser and in BOTH engines: the amount clipped
 * differs by engine, because `scrollbar-gutter: stable` on `.scrollback-area`
 * reserves 10px in Chromium and is ignored by WebKit. Measured at a 1232px
 * pane, the same build overhung by 20px in Chromium and 10px in WKWebView —
 * which is why the defect was reported as "the packaged app clips and the
 * browser does not". A jsdom test cannot see any of it: there is no layout, so
 * `clientWidth` is 0 and `usableViewport` returns the delivered box unchanged.
 *
 * The box is the live row's content box since nocx-9bpeq.8: the pane's gutter
 * moved into the rows, so the scroller's clientWidth is the grid width plus
 * that inset, and the fit — and this spec — must measure the box the inset
 * leaves rather than the scroller itself.
 *
 * SINCE THE CUTOVER (nocx-zg3k3.2.5) the grid a person sees is the cell
 * painter's, so what is measured is the PAINTED width: the first painted
 * row's inline content, through a Range. A block element spans its container,
 * but the range over the row's runs is exactly the columns the painter drew,
 * each contributing its published cell width (ADR-0009 rule 3).
 */

import { test, expect, clickIntoEditor, promptReady } from './harness'

test('the grid is not wider than the scroller it is drawn in', async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('.pane.active .term-grid')

  // BOTH HALVES ARE WAITED FOR TOGETHER, and separating them is what made this
  // spec flaky (nocx-sx4sg). The wait used to be on the overhang alone — "the
  // grid is not wider than the box" — and a grid that has not been fitted yet
  // is much NARROWER than the box, so that predicate is satisfied by a large
  // negative number on the first evaluation. The fill was then measured once,
  // outside the wait, against exactly the un-fitted layout the wait had
  // accepted: webkit reported 0.576 where the assertion wants 0.98. The
  // predicate was not merely passing early, it was passing BECAUSE of the
  // state the next line rejects.
  //
  // So the closing event is now the settled layout itself — the fit path's
  // resize and rAF have both landed — and neither half is read anywhere else.
  // The polled value carries the numbers rather than a boolean, so a timeout
  // in a CI log says which half is unsatisfied and by how much; a bare `false`
  // cannot be debugged from a log nobody can attach a debugger to.
  const geometry = () =>
    page.evaluate(() => {
      const pane = document.querySelector('.pane.active')
      const live = pane?.querySelector('.terminal-live-container') as HTMLElement | null
      const row = pane?.querySelector('.term-grid-row') as HTMLElement | null
      if (!live || !row) return { overhang: 1, fill: 0, settled: false }
      // THE GRID'S BOX IS THE LIVE ROW'S CONTENT BOX (nocx-9bpeq.8): rows
      // carry the pane gutter, so the scroller's clientWidth is the grid width
      // plus that inset on both sides.
      const cs = getComputedStyle(live)
      const box = live.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
      // THE PAINTED WIDTH IS THE ROW'S INLINE CONTENT: every row of the
      // committed grid is a full rectangle (one entry per column), so any
      // painted row measures the whole grid's width.
      const range = document.createRange()
      range.selectNodeContents(row)
      const width = range.getBoundingClientRect().width
      const overhang = Math.round(width - box)
      const fill = width / box
      // At most zero overhang: whole cells rarely tile the box exactly, so the
      // grid is normally a few pixels NARROWER, and any positive number is a
      // column the user cannot see. And it still has to FILL the scroller —
      // "not too wide" is satisfied just as well by a grid of two columns — so
      // the leftover may be a fraction of one column and never a margin.
      return { overhang, fill, settled: overhang <= 0 && fill > 0.98 }
    })

  await expect.poll(geometry, { timeout: 10_000 }).toMatchObject({ settled: true })
  const measured = await geometry()

  // Restated on the settled layout, one matcher each, so a regression names
  // which half of the invariant broke rather than reporting "not settled".
  expect(measured.overhang).toBeLessThanOrEqual(0)
  expect(measured.fill).toBeGreaterThan(0.98)
})

/**
 * THE INVARIANT ALSO HAS TO HOLD WHILE A PROGRAM RUNS, AND THERE THE BOX CAN
 * MOVE UNDER THE GRID (nocx-vydj).
 *
 * The scroller's content width is not a constant: it is the box minus the
 * vertical scrollbar. `scrollbar-gutter: stable` reserves that space for the
 * whole life of the box in Chromium, so the width the grid is fitted to never
 * changes there. WebKit ignores the property, so the bar appears the moment the
 * transcript first overflows — and the grid was fitted a moment earlier, to the
 * wider box.
 *
 * The frontend does not refit on that move while a program holds the pane
 * (nocx-2v80t.3.50: nothing but the pane may resize a running program, so a
 * width change seen then is owed and paid at the next prompt). That is right,
 * and it is why the fix cannot be a refit: the box has to stop moving. Measured
 * in the e2e container before the fix, at a 984px pane with twelve history rows
 * and one command whose output overflows the scroller: `.scrollback-area`
 * clientWidth 984 -> 974 under the running grid, the grid stayed 133 columns
 * wide, and the last column was painted 5px past the clip's right edge — cut
 * mid-glyph for the whole command, and one column wider than the same pane in
 * Chromium, which is the engine difference the owner sees as "the app clips and
 * the browser does not".
 *
 * So this test starts with a transcript that FITS (no bar in WebKit, the gutter
 * already reserved in Chromium) and then runs a command whose own output is what
 * makes the scroller scroll. Nothing here waits on a duration: the closing
 * event is a painted running grid in a scroller that overflows.
 */
test('the grid stays inside its box when the running command makes the scroller scroll', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/')
  await promptReady(page)

  // A transcript that fits, so the bar's arrival is the command's doing.
  await clickIntoEditor(page)
  await page.keyboard.type('for i in $(seq 1 12); do echo "history-$i"; done')
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active', { hasText: 'history-12' })).toHaveCount(1, {
    timeout: 20_000,
  })
  await promptReady(page)

  // Sixty rows in a pane of about thirty-six: the transcript overflows while
  // the command still holds the pane.
  await clickIntoEditor(page)
  await page.keyboard.type('for i in $(seq 1 60); do echo "run-$i"; done; sleep 300')
  await page.keyboard.press('Enter')

  const geometry = () =>
    page.evaluate(() => {
      const pane = document.querySelector('.pane.active')
      const area = pane?.querySelector('.scrollback-area') as HTMLElement | null
      const live = pane?.querySelector('.terminal-live-container') as HTMLElement | null
      const grid = pane?.querySelector('.term-grid') as HTMLElement | null
      const inner = grid?.parentElement as HTMLElement | null
      const rows = [...(grid?.querySelectorAll('.term-grid-row') ?? [])] as HTMLElement[]
      // The LAST painted row: every row of a committed grid is a full
      // rectangle, and the first one is the echo of the command, which the
      // painter may still be filling in on the frame it lands.
      const row = rows[rows.length - 1] ?? null
      if (!area || !live || !grid || !inner || !row) return { settled: false, overhang: 1 }
      const cs = getComputedStyle(live)
      const inset = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight)
      const box = live.clientWidth - inset
      const cellWidth =
        parseFloat(
          getComputedStyle(document.querySelector('.scrollback-inner')!).getPropertyValue(
            '--term-cell-width',
          ),
        ) || 1
      const cols = Number(grid.getAttribute('aria-colcount'))
      // The painted width, measured on the inline content rather than on the
      // block: a row spans its container whatever the painter drew in it.
      const range = document.createRange()
      range.selectNodeContents(row)
      const right = range.getBoundingClientRect().right
      const clipRight = inner.getBoundingClientRect().right
      const overhang = Math.round((right - clipRight) * 100) / 100
      const expectedCols = Math.floor(box / cellWidth)
      const running = live.classList.contains('live-running')
      const scrolling = area.scrollHeight > area.clientHeight
      // NOT WHILE THE SETTLE HOLDS THE BAR DOWN. `_glide` adds `is-settling`
      // while the stack is displaced, which hides the bar (on the one engine
      // that grows one) and so restores the wider box for those ~140ms. That
      // window is not the state a person reads a full screen in, and taking
      // the measurement there is how this test would pass over the defect.
      const settling = area.classList.contains('is-settling')
      return {
        overhang,
        cols,
        expectedCols,
        running,
        scrolling,
        settling,
        settled: running && scrolling && !settling && overhang <= 0 && cols === expectedCols,
      }
    })

  // The polled object carries the numbers, and the last one is printed on the
  // way out: a timeout in a CI log has to say which half is unsatisfied and by
  // how much, because a bare `settled: false` cannot be diagnosed from the log
  // and re-running it to reproduce what it already said is the failure mode
  // this repository names (nocx-sx4sg, nocx-n14oo.11).
  let last: Awaited<ReturnType<typeof geometry>> | null = null
  try {
    await expect
      .poll(
        async () => {
          last = await geometry()
          return last.settled
        },
        { timeout: 20_000 },
      )
      .toBe(true)
  } catch (error) {
    console.error('grid-width last measured geometry:', JSON.stringify(last))
    throw error
  }
  const measured = await geometry()

  // One matcher each, so a regression names which half broke: a column drawn
  // past the clip, or a grid that gave up a column the box still had.
  expect(measured.overhang).toBeLessThanOrEqual(0)
  expect(measured.cols).toBe(measured.expectedCols)
})

/**
 * AN ALTERNATE-SCREEN PROGRAM WEARS THE SAME ONE GUTTER (ADR-92zzg2y2).
 *
 * The owner's rule of 2026-11-10: one gutter for everything the pane shows —
 * ordinary output, blocks, a plain ssh shell and a full-screen program alike.
 * One character cell wide on each side. The old rule gave a full-screen program
 * the whole pane and a normal command a 24 px gutter, which is why a switch had
 * to change the column count; now the program is fitted to the same content box
 * as every other row.
 *
 * The assertion is stated in CELLS, so it holds at any font size and display
 * ratio: the program's own columns, at the published cell width, fill the
 * pane's content box — the pane less one cell on each side — within ONE cell,
 * and never overflow it. One cell of slack is the remainder of the integer
 * division, which no terminal can avoid.
 */
test('an alternate-screen program wears the one-cell gutter', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)

  // The alternate screen, then a program that holds it. `printf` is the shell
  // the stand starts, so the buffer switch is the same escape any TUI sends.
  await clickIntoEditor(page)
  await page.keyboard.type("printf '\\033[?1049h\\033[H'; sleep 300")
  await page.keyboard.press('Enter')

  const geometry = () =>
    page.evaluate(() => {
      const pane = document.querySelector('.pane.active')
      const area = pane?.querySelector('.scrollback-area') as HTMLElement | null
      const live = pane?.querySelector('.terminal-live-container') as HTMLElement | null
      const grid = pane?.querySelector('.term-grid') as HTMLElement | null
      if (!area || !live || !grid) return { settled: false }
      const cellWidth =
        parseFloat(
          getComputedStyle(document.querySelector('.scrollback-inner')!).getPropertyValue(
            '--term-cell-width',
          ),
        ) || 0
      const cols = Number(grid.getAttribute('aria-colcount'))
      const paneEl = area.closest<HTMLElement>('.pane')!
      const gutter =
        parseFloat(getComputedStyle(paneEl).getPropertyValue('--pane-inline-padding')) || 0
      const cs = getComputedStyle(live)
      const box = area.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
      const painted = cols * cellWidth
      // The content box the program may draw in: the scroller less one cell on
      // each side. The painted grid fills it within the integer remainder.
      const panel = area.clientWidth
      const expected = panel - 2 * gutter
      const shortByCells = (expected - painted) / cellWidth
      const fullscreen = live.classList.contains('live-fullscreen')
      return {
        cols,
        cellWidth: Math.round(cellWidth * 1e3) / 1e3,
        gutter: Math.round(gutter * 1e3) / 1e3,
        box: Math.round(box),
        panel: Math.round(panel),
        expected: Math.round(expected),
        painted: Math.round(painted),
        shortByCells: Math.round(shortByCells * 100) / 100,
        fullscreen,
        settled: fullscreen && cellWidth > 0 && gutter > 0 && Math.abs(shortByCells) < 1,
      }
    })

  let last: Awaited<ReturnType<typeof geometry>> | null = null
  try {
    await expect
      .poll(
        async () => {
          last = await geometry()
          return last.settled
        },
        { timeout: 20_000 },
      )
      .toBe(true)
  } catch (error) {
    console.error('alt-screen grid-width last measured geometry:', JSON.stringify(last))
    throw error
  }
  const measured = await geometry()

  // One matcher each: a grid that gave the program fewer columns than the
  // content box could paint, or one that overhangs it.
  expect(measured.shortByCells!).toBeLessThan(1)
  expect(measured.shortByCells!).toBeGreaterThanOrEqual(-1)
  // …and the gutter itself is one cell, not the old flush edge.
  expect(measured.gutter).toBeCloseTo(measured.cellWidth!, 1)
  await page.keyboard.press('Control+C')
})
