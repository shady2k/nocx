/**
 * A running block shows only what its command printed (nocx-zg3k3.32).
 *
 * Nothing clears the terminal between commands (nocx-2v80t.3.3), so while a
 * command runs the emulator's screen still holds the rows the previous
 * command left there. The running block is that command's body: the rows
 * above its own output start belong to the frozen block above it, and a
 * person must not see them twice. The owner saw seq's last rows inside a
 * starting claude's block; any command that waits before it repaints the
 * screen shows the same.
 */

import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor } from './harness'

/** The painted rows a person can see: those that intersect every clipping
 *  ancestor of the live grid. Read inside an animation frame, after the
 *  painter's own callback for it: a frame's rows are painted there while its
 *  layout is applied when it is accepted, and only the state after both is
 *  ever rendered — a read between them sees a screen nobody does. */
async function visibleLiveRows(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    const g = document.querySelector<HTMLElement>('.pane.active .terminal-live-grid')
    if (!g) return []
    let top = -Infinity
    let bottom = Infinity
    for (let n: HTMLElement | null = g.parentElement; n; n = n.parentElement) {
      if (getComputedStyle(n).overflowY !== 'visible') {
        const r = n.getBoundingClientRect()
        top = Math.max(top, r.top)
        bottom = Math.min(bottom, r.bottom)
      }
    }
    return [...g.querySelectorAll<HTMLElement>('.term-grid-row')]
      .filter((row) => {
        const r = row.getBoundingClientRect()
        return r.height > 0 && r.bottom > top + 0.5 && r.top < bottom - 0.5
      })
      .map((row) => (row.textContent ?? '').trimEnd())
  })
}

async function run(page: Page, command: string): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test.describe('a running block', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await run(page, 'seq 1 100')
    await expect(page.locator('.pane.active .cmd-block').last()).toContainText('100', {
      timeout: 15_000,
    })
    await promptReady(page)
  })

  test('that printed a line and waits shows that line and none of the previous output', async ({
    page,
  }) => {
    await run(page, "printf 'OWN-LINE\\n'; read x")
    await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('OWN-LINE', {
      timeout: 15_000,
    })
    await expect
      .poll(async () => visibleLiveRows(page), { timeout: 5_000 })
      .toEqual(expect.arrayContaining(['OWN-LINE']))
    const rows = await visibleLiveRows(page)
    expect(
      rows.filter((r) => /^\d+$/.test(r.trim())),
      `previous command's rows painted in the running block: ${JSON.stringify(rows)}`,
    ).toEqual([])
    await page.keyboard.press('Enter')
  })

  test('that has printed nothing yet shows none of the previous output', async ({ page }) => {
    await run(page, 'sleep 30')
    // Read at the first frame that painted the command's own echo: that is
    // the moment the block opens, before any output anchor can have landed.
    await expect(page.locator('.pane.active .terminal-live-grid')).toContainText('sleep 30', {
      timeout: 15_000,
    })
    const rows = await visibleLiveRows(page)
    expect(
      rows.filter((r) => /^\d+$/.test(r.trim())),
      `previous command's rows painted in the running block: ${JSON.stringify(rows)}`,
    ).toEqual([])
    await page.keyboard.press('Control+C')
  })

  test('keeps its own line without misattributing earlier rows after the pane gets shorter', async ({
    page,
  }) => {
    await run(page, "printf 'OWN-LINE\\n'; read x")
    await expect
      .poll(async () => visibleLiveRows(page), { timeout: 15_000 })
      .toEqual(expect.arrayContaining(['OWN-LINE']))
    const gridRows = page.locator('.pane.active .terminal-live-grid .term-grid-row')
    const before = await gridRows.count()
    const size = page.viewportSize()!
    // A known height-only commit preserves the command window. If accepted
    // geometry was coalesced, ADR-0104 instead exposes whole-terminal history
    // outside the card; neither path may attribute old rows to this command.
    await page.setViewportSize({ width: size.width, height: size.height - 51 })
    await expect.poll(async () => gridRows.count(), { timeout: 15_000 }).toBeLessThan(before)
    await expect
      .poll(async () => visibleLiveRows(page), { timeout: 5_000 })
      .toEqual(expect.arrayContaining(['OWN-LINE']))
    const ownership = await page.evaluate(() => {
      const pane = document.querySelector<HTMLElement>('.pane.active')!
      const command = pane.querySelector<HTMLElement>('.cmd-block-running')!
      const grid = pane.querySelector<HTMLElement>('.terminal-live-grid')!
      const visibleRows = [...pane.querySelectorAll<HTMLElement>('.term-grid-row')].filter((row) =>
        row.checkVisibility(),
      )
      return {
        qualified: command.contains(grid),
        wholeTerminal:
          grid.closest('.inner-terminal-history') !== null &&
          grid.closest('.cmd-block') === null &&
          grid
            .closest('.terminal-live-container')
            ?.parentElement?.classList.contains('scrollback-inner') === true,
        commandRows: visibleRows
          .filter((row) => command.contains(row))
          .map((row) => (row.textContent ?? '').trim()),
        ownCopies: visibleRows.filter((row) => row.textContent?.trim() === 'OWN-LINE').length,
      }
    })
    expect(ownership.commandRows.filter((row) => /^\d+$/.test(row))).toEqual([])
    expect(ownership.ownCopies).toBe(1)
    if (ownership.qualified) {
      expect((await visibleLiveRows(page)).join('|')).toMatch(/^OWN-LINE(\|)?$/)
    } else {
      expect(ownership.wholeTerminal).toBe(true)
    }
    await page.keyboard.press('Enter')
  })
})
