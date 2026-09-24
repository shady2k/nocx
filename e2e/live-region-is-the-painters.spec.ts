/**
 * The live region is the painter's picture, end to end (nocx-zg3k3.2.5).
 *
 * After the cutover, what a person sees in the live region is the cell
 * painter drawing the backend's frames, and xterm — still in the live
 * region as the INVISIBLE input layer — draws nothing: no visual renderer
 * is mounted, and its root is occluded. What a person DOES still reaches
 * the program through xterm, and that path is proven here at its hardest:
 * a program that enabled mouse reporting must receive a click at the cell
 * the person clicked on the PAINTER's surface, which means the painter's
 * cell grid and xterm's coincide — asserted at two zoom levels, where a
 * zoom is a resize the runtime commits and the next frame re-binds the
 * mapping.
 *
 * The mouse proof runs the full circle over the real backend: the printed
 * escape turns mouse reporting on in BOTH parsers that still see bytes
 * (the runtime's, for the screen; xterm's, for the input encoding), and
 * `cat -v` echoes back the SGR sequence a click encodes — so the bytes the
 * PROGRAM received are read off the painted screen. Every wait is on
 * painted or model state, never on a duration.
 */

import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

test('a program output appears in the painted grid, and xterm draws nothing', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)

  // The marker is split across two shell strings, so only the command's
  // output — never the prompt echo — can carry the contiguous text.
  const nonce = Date.now().toString(36)
  const marker = `NOCX-PAINTED-${nonce}`
  await page.keyboard.type(`printf '%s\\n' 'NOCX-PAI''NTED-${nonce}'`)
  await page.keyboard.press('Enter')

  // THE OUTPUT IS PAINTED: the live region's grid carries it in its rows —
  // the same surface a screenshot would show, read as DOM. The marker is
  // an evaluate ARGUMENT: a serialised closure carries nothing else.
  await expect
    .poll(
      async () =>
        page.evaluate((marker) => {
          const live = document.querySelector('.pane.active .xterm-live-container')
          return live?.textContent?.includes(marker) ?? false
        }, marker),
      { timeout: 15_000, message: `the painted grid never carried the output marker` },
    )
    .toBe(true)
  // And the exact output row, not just somewhere in the region's text.
  await expect(
    page.locator('.pane.active .xterm-live-container .term-grid-row', { hasText: marker }),
  ).toHaveCount(1)

  // XTERM DRAWS NOTHING HERE: its root in the live region is the occluded
  // input layer, and no visual renderer was mounted — a canvas is what the
  // WebGL or canvas renderer would have brought, and neither is mounted.
  const occluded = await page.evaluate(() => {
    const live = document.querySelector('.pane.active .xterm-live-container')
    const root = live?.querySelector('.xterm')
    return {
      occluded: root?.classList.contains('xterm-occluded') ?? false,
      canvases: live?.querySelectorAll('canvas').length ?? -1,
    }
  })
  expect(occluded.occluded).toBe(true)
  expect(occluded.canvases).toBe(0)
})

/** Everything the click math needs: the page pixel at the centre of one
 *  cell of the PAINTED grid, and that cell's (row, col) on the painter's
 *  grid. The numbers come from the painted surface itself — the rows' own
 *  rects — so the expected cell is what the person's view says, never
 *  xterm's. */
interface ClickTarget {
  x: number
  y: number
  row: number
  col: number
}

/** A painted grid row the live box can actually hit: the box clips the
 *  surface, and a click outside it lands on nothing. */
async function clickTarget(page: Page, colWithin: number): Promise<ClickTarget | null> {
  return page.evaluate((colWithin) => {
    const live = document.querySelector<HTMLElement>('.pane.active .xterm-live-container')
    const clip = document.querySelector<HTMLElement>('.pane.active .xterm-live-viewport')
    const rows = live?.querySelectorAll<HTMLElement>('.term-grid-row')
    if (!live || !clip || !rows || rows.length === 0) return null
    // THE REAL CLIP IS THE VIEWPORT, not the live flow box: the box's
    // inline height and the echo shift decide which painted rows a person
    // — and a click — can actually reach.
    const box = clip.getBoundingClientRect()
    const grid = rows[0].parentElement
    if (!grid) return null
    const gridTop = grid.getBoundingClientRect().top
    const pitch = rows[0].getBoundingClientRect().height
    const rowRange = document.createRange()
    rowRange.selectNodeContents(rows[0])
    const firstRow = rowRange.getBoundingClientRect()
    const cols = rows[0].textContent?.length ?? 0
    if (cols === 0 || firstRow.width === 0 || pitch === 0) return null
    const cellWidth = firstRow.width / cols
    const col = Math.min(colWithin, cols - 1)
    for (let r = 0; r < rows.length; r++) {
      const rowRect = rows[r].getBoundingClientRect()
      if (rowRect.top < box.top || rowRect.bottom > box.bottom) continue
      const x = firstRow.left + (col + 0.5) * cellWidth
      const y = gridTop + (r + 0.5) * pitch
      if (x < box.left || x > box.right) continue
      return { x, y, row: r, col }
    }
    return null
  }, colWithin)
}

/** One zoom level of the mouse proof: print a round marker, turn mouse
 *  reporting on, park `cat -v` on the pty, click the cell the painter's
 *  grid says, and read back the SGR sequence the program received — off
 *  the painted screen. */
async function clickRound(page: Page, colWithin: number, roundMarker: string): Promise<void> {
  await page.keyboard.type(
    `printf '%s\\n' '${roundMarker}'; printf '\\033[?1000h\\033[?1006h'; cat -v`,
  )
  await page.keyboard.press('Enter')

  // The round started (its marker is painted) before any click.
  await expect
    .poll(
      async () =>
        page.evaluate((roundMarker) => {
          const live = document.querySelector('.pane.active .xterm-live-container')
          return live?.textContent?.includes(roundMarker) ?? false
        }, roundMarker),
      { timeout: 15_000, message: `the round marker ${roundMarker} never painted` },
    )
    .toBe(true)

  // The click target is read only after the grid is painted and visible.
  let target: ClickTarget | null = null
  await expect
    .poll(
      async () => {
        target = await clickTarget(page, colWithin)
        return target !== null
      },
      { timeout: 15_000, message: 'no clickable painted row ever appeared' },
    )
    .toBe(true)

  await page.mouse.click(target!.x, target!.y)

  // THE PROGRAM'S OWN ECHO IS THE PROOF: `cat -v` prints the SGR mouse
  // sequence it received — ESC spelled ^[ — and the painted grid carries
  // it. The cell it names must be the cell the person clicked.
  const wanted = `^[[<0;${target!.col + 1};${target!.row + 1}M`
  await expect
    .poll(
      async () =>
        page.evaluate(
          ([wanted, roundMarker]) => {
            const live = document.querySelector('.pane.active .xterm-live-container')
            const text = live?.textContent ?? ''
            return text.includes(wanted) && text.includes(roundMarker)
          },
          [wanted, roundMarker],
        ),
      {
        timeout: 15_000,
        message: `the program never received a click for ${wanted} (${roundMarker})`,
      },
    )
    .toBe(true)
}

test('a mouse-reporting program receives the clicked painter cell, at two zoom levels', async ({
  page,
  browserName,
}) => {
  // The zoom is simulated with a CDP device-metrics override (below), and
  // CDP is a Chromium-only protocol — the same reason api-import.spec.ts's
  // drag test is chromium-only. This is not the retired serializer path
  // this file's own suite was written against (nocx-zg3k3.2.5); it is an
  // unrelated, pre-existing browser-capability limit the test never
  // guarded, newly exposed because this spec is new on this branch.
  test.skip(browserName !== 'chromium', 'zoom emulation needs CDP, which only Chromium exposes')
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)

  // Zoom level 1: the display as it comes up.
  await clickRound(page, 12, 'Z1-OK')

  // Leave `cat -v`, then move the display: a zoom is a resize the runtime
  // commits, so the next frame re-binds the committed geometry.
  await page.keyboard.press('Control+C')
  await promptReady(page)
  const committedCellBefore = await page.evaluate(
    () =>
      (
        window as unknown as {
          __nocxPaneScreen?: () => { geometry: { cellWidthPx: number } | null } | null
        }
      ).__nocxPaneScreen?.()?.geometry?.cellWidthPx ?? 0,
  )
  const cdp = await page.context().newCDPSession(page)
  // The override must carry the NEW SIZE ITSELF: it pins the view, so a
  // later setViewportSize lands on nothing. Size + ratio in one override
  // is the reflow that makes the pane re-fit and re-report at the new
  // ratio (the override does not deliver the resolution media-change
  // event the renderer's watcher listens for — a real display move does).
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 1282,
    height: 802,
    deviceScaleFactor: 2,
    mobile: false,
  })
  // The zoom is in force only when the COMMITTED metric reflects it: the
  // device cell doubles with the ratio, so the committed px doubles too.
  await expect
    .poll(
      async () =>
        page.evaluate(
          () =>
            (
              window as unknown as {
                __nocxPaneScreen?: () => { geometry: { cellWidthPx: number } | null } | null
              }
            ).__nocxPaneScreen?.()?.geometry?.cellWidthPx ?? 0,
        ),
      { timeout: 15_000, message: 'the committed cell metric never moved with the zoom' },
    )
    .toBeGreaterThan(committedCellBefore * 1.5)

  // Zoom level 2: a different cell, same circle, on the re-committed grid.
  await clickRound(page, 30, 'Z2-OK')
  await cdp.send('Emulation.clearDeviceMetricsOverride')
})
