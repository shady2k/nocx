/**
 * The grid is never wider than the box it is drawn in (nocx-vydj).
 *
 * The height half of this invariant was learned the hard way in nocx-6w4z: the
 * grid was fitted to the pane while it was shown in a scroller shorter than the
 * pane, and the bottom rows had nowhere to be drawn. The width half was left
 * unfixed and fails the same way, one axis over — the viewport delivered to
 * content is `pane.getBoundingClientRect()`, a BORDER box that includes
 * `.pane`'s `padding: 0 10px`, so `cols` was computed from 20px the grid does
 * not have and the last columns landed past the right edge of `.xterm-inner`,
 * where its `overflow: hidden` cut them mid-glyph.
 *
 * Why this must run in a browser and in BOTH engines: the amount clipped
 * differs by engine, because `scrollbar-gutter: stable` on `.scrollback-area`
 * reserves 10px in Chromium and is ignored by WebKit. Measured at a 1232px
 * pane, the same build overhung by 20px in Chromium and 10px in WKWebView —
 * which is why the defect was reported as "the packaged app clips and the
 * browser does not". A jsdom test cannot see any of it: there is no layout, so
 * `clientWidth` is 0 and `usableViewport` returns the delivered box unchanged.
 */

import { test, expect } from './harness'

test('the grid is not wider than the scroller it is drawn in', async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('.pane.active .xterm-screen')

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
      const area = pane?.querySelector('.scrollback-area') as HTMLElement | null
      const screen = pane?.querySelector('.xterm-screen') as HTMLElement | null
      if (!area || !screen) return { overhang: 1, fill: 0, settled: false }
      const width = screen.getBoundingClientRect().width
      const overhang = Math.round(width - area.clientWidth)
      const fill = width / area.clientWidth
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
