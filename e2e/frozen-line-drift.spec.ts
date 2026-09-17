// The frozen-line drift instrument, measured where layout is real (nocx-4n6sj).
//
// Everything else about this instrument is arithmetic and is covered in
// jsdom. THIS is the part jsdom cannot report: whether the thing measures
// anything at all. It already caught one silent constant — `.term-line` is
// `display: block`, so a border-box read would have returned the container's
// width for every row, identically, forever.
//
// The W row lands on the grid BY CONSTRUCTION — cell-metric.ts calibrates
// --term-cell-delta from a probe of 'W', so N of them advance N × cellWidth
// whatever font was picked. That half asserts a real product guarantee and
// needs nothing from the environment.
//
// The symbol row used to lean on the opposite guarantee going the other way:
// type ⬢ ⟳ 🗑 and hope the host's font renders them off the cell. That is not
// a property of the product, it is a property of whichever font the run
// happens to have, and it went unfalsifiable the day scrollback/cell-fit.ts
// (nocx-ec18) shipped: cell-fit measures every non-ASCII cluster and boxes
// the ones that miss, at EXACTLY the grid width the columns call for — so a
// FROZEN LINE lands back on the grid regardless of what the glyph's natural
// advance was. Confirmed on this branch, in the container: the same payload
// as below reports two real per-glyph misses (⬢ 11px, ⟳ 15px against a 7px
// cell) in `report.offenders`, while `report.drifted` is 0 — cell-fit is
// correcting the line, not the font behaving.
//
// So the symbol row no longer asks the DOM to disagree with the grid on its
// own. It drives the disagreement directly, through a style scoped to the
// drift instrument's OWN probe host (`.cell-drift-probe`, cell-drift.ts) —
// never touching the live row or cell-fit's separate probe — adding one
// whole extra cell of tracking to every character THE PROBE measures. That
// is a known, exact miss on every host: the arithmetic below no longer cares
// what a font does, only whether the instrument correctly reports a real DOM
// disagreement when there is one. Which is the whole of what this e2e spec
// exists to check (see above) — the arithmetic itself is jsdom's job.

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
  // Scoped to `.cell-drift-probe .term-line` — the instrument's own
  // measurement host (cell-drift.ts), never the live row and never
  // cell-fit's separate `.cell-fit-probe`. One whole cell of extra
  // tracking per character is a miss no font can accidentally cancel: it
  // does not depend on what ⬢, ⟳ or 🗑 measure natively on this host, only
  // on whether the probe's own getBoundingClientRect read comes back
  // reflecting it.
  await page.addStyleTag({
    content:
      '.cell-drift-probe .term-line { letter-spacing: calc(var(--term-cell-delta, 0px) + var(--term-cell-width, 1px)) !important; }',
  })
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
