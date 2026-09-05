// The frozen-line drift instrument, measured where layout is real (nocx-4n6sj).
//
// Everything else about this instrument is arithmetic and is covered in
// jsdom. THIS is the part jsdom cannot report: whether the thing measures
// anything at all. It already caught one silent constant — `.term-line` is
// `display: block`, so a border-box read would have returned the container's
// width for every row, identically, forever.
//
// The two rows are chosen so the assertion holds on any platform, including
// a container whose font stack resolves to something proportional:
//
//   the W row lands on the grid BY CONSTRUCTION — cell-metric.ts calibrates
//   --term-cell-delta from a probe of 'W', so N of them advance N × cellWidth
//   whatever font was picked;
//
//   the symbol row differs from it only in three glyphs, so any drift it
//   reports is those glyphs and nothing else.
//
// If the symbol row ever comes back clean, that is not a flake to retry: it
// means this environment renders ⬢ ⟳ 🗑 at the cell, and the instrument is
// telling the truth about it.

import { test, expect, promptReady } from './harness'

interface DriftReport {
  lines: number
  drifted: number
  worstCols: number
  offenders: Array<{ cluster: string; codepoints: string; fitsNeither: boolean }>
}

interface DriftApi {
  enable(): string
  reset(): string
  report(): DriftReport
}

declare global {
  interface Window {
    nocxCellDrift?: DriftApi
  }
}

const WIDTH = 60

test('drift is zero on the row the grid was calibrated on', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  const marker = `DW-${Date.now().toString(36)}`
  await page.evaluate(() => {
    window.nocxCellDrift!.enable()
    window.nocxCellDrift!.reset()
  })
  await page.keyboard.type(`printf '%s\\n' '${'W'.repeat(WIDTH)}' # ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  const report = await expect
    .poll(async () => (await page.evaluate(() => window.nocxCellDrift!.report())).lines, {
      timeout: 10_000,
    })
    .toBeGreaterThan(0)
    .then(() => page.evaluate(() => window.nocxCellDrift!.report()))

  expect(report.worstCols).toBeLessThan(0.05)
  expect(report.drifted).toBe(0)
})

test('drift and the offending glyphs are reported on a row with symbols', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  const marker = `DS-${Date.now().toString(36)}`
  await page.evaluate(() => {
    window.nocxCellDrift!.enable()
    window.nocxCellDrift!.reset()
  })
  const payload = `${'W'.repeat(WIDTH - 3)}⬢⟳🗑`
  await page.keyboard.type(`printf '%s\\n' '${payload}' # ${marker}`)
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
    timeout: 15_000,
  })

  const report = await expect
    .poll(async () => (await page.evaluate(() => window.nocxCellDrift!.report())).lines, {
      timeout: 10_000,
    })
    .toBeGreaterThan(0)
    .then(() => page.evaluate(() => window.nocxCellDrift!.report()))

  // The row is off its columns, and the report names which glyphs did it —
  // the two halves of what a week of dogfooding has to come back with.
  expect(report.drifted).toBeGreaterThan(0)
  expect(report.worstCols).toBeGreaterThan(0.05)
  expect(report.offenders.length).toBeGreaterThan(0)
  expect(report.offenders.every((o) => o.fitsNeither)).toBe(true)
})
