/**
 * A running command's grid fits inside what the pane shows (nocx-g6hnk).
 *
 * The rows the pty is sized to were fitted to the running cap — the whole
 * box — while the box shows them inside the running body's padding. About a
 * row and a half sat below the clip, so an inline TUI that draws below its
 * input line (omp's hint row) lost its bottom. It shows only at some window
 * heights, which is why a single 800px run passed: here the last row the
 * program writes, at `tput lines`, must end inside the clip.
 */

import { test, expect, promptReady, clickIntoEditor } from './harness'

for (const ratio of [1, 1.8, 2]) {
  test.describe(`at a display ratio of ${String(ratio)}`, () => {
    test.use({ deviceScaleFactor: ratio })

    for (const height of [800, 1027]) {
      test(`the last row of a full-height program is visible in a ${String(height)}px window`, async ({
        page,
      }) => {
        await page.setViewportSize({ width: 1600, height })
        await page.goto('/')
        await promptReady(page)
        await clickIntoEditor(page)
        await page.keyboard.type(
          `n=$(tput lines); for i in $(seq 1 $n); do if [ $i -lt $n ]; then echo "ROW-$i"; else printf 'LAST''-ROW'; fi; done; sleep 60`,
        )
        await page.keyboard.press('Enter')
        const grid = page.locator('.pane.active .terminal-live-grid')
        await expect(grid.locator('.term-grid-row').last()).toContainText('LAST-ROW', {
          timeout: 15_000,
        })
        // THE PANE'S OWN SETTLE IS PART OF THE MEASUREMENT. A block opening or
        // a freeze plays a short glide (scrollback/controller: SETTLE_MS), and
        // it is a TRANSFORM on the stack, so getBoundingClientRect reports the
        // displaced box while it runs. Measuring then reads a fraction of the
        // displacement, not the layout the pane lands on: at a device ratio of
        // 2 in a 1027px window the last row sat 0.577px below the clip during
        // that window and inside it a frame later. So wait on the animation —
        // the same wait running-block-body-seam.spec.ts's `geometry()` makes,
        // and never on a duration — then measure the settled box.
        await page.evaluate(async () => {
          const stack = document.querySelector<HTMLElement>('.pane.active .scrollback-inner')
          if (!stack) return
          for (;;) {
            const settling = stack.getAnimations().filter((a) => a.playState !== 'finished')
            await Promise.all(settling.map((a) => a.finished.catch(() => undefined)))
            await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
            if (stack.getAnimations().every((a) => a.playState === 'finished')) break
          }
        })
        const measurement = await page.evaluate(() => {
          const g = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
          const rows = g?.querySelectorAll<HTMLElement>('.term-grid-row')
          const last = rows?.[rows.length - 1]
          if (!g || !last?.textContent?.includes('LAST-ROW')) return null
          let clip = Infinity
          for (let n: HTMLElement | null = g.parentElement; n; n = n.parentElement) {
            if (getComputedStyle(n).overflowY !== 'visible') {
              clip = Math.min(clip, n.getBoundingClientRect().bottom)
            }
          }
          return { rowBottom: last.getBoundingClientRect().bottom, clipBottom: clip }
        })
        expect(
          measurement,
          `the running-grid row carrying LAST-ROW disappeared between the paint and the measurement (viewport height ${String(height)}px, device pixel ratio ${String(ratio)})`,
        ).not.toBeNull()
        if (!measurement) return
        expect(
          measurement.rowBottom,
          'the program’s last row ends inside the clip',
        ).toBeLessThanOrEqual(measurement.clipBottom + 0.5)
        await page.keyboard.press('Control+C')
      })
    }
  })
}
