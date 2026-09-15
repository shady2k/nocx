/**
 * A person scanning the ledger sees the failure and not the successes
 * (nocx-9bpeq.6, spec 2026-09-14 §3). Watched through the product: a real shell
 * runs `true` and `false`; every theme is applied the way Settings applies it
 * (the data-theme attribute on the root); contrast is read off computed styles.
 * No wait is on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

const THEMES = [
  'tokyo-night',
  'light',
  'ayu-dark',
  'catppuccin-latte',
  'catppuccin-mocha',
  'dracula',
  'gruvbox-dark',
  'nord',
  'one-dark',
  'rose-pine',
  'solarized-dark',
  'solarized-light',
]

async function run(page: Page, command: string) {
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, { timeout: 30_000 })
  await promptReady(page)
}

test('success is silent, failure is legible, in every theme', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  await run(page, 'false')

  const blocks = page.locator(
    '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]',
  )
  // `nth(-1)` is documented as the last match; a second negative index is not,
  // so the one before it is addressed by position instead.
  const count = await blocks.count()
  const ok = blocks.nth(count - 2)
  const bad = blocks.nth(-1)
  const statusOf = (b: typeof ok) =>
    b.locator(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')

  await expect(ok).toHaveAttribute('data-outcome', 'success')
  await expect(statusOf(ok)).toHaveCount(0)
  await expect(bad).toHaveAttribute('data-outcome', 'failure')
  await expect(statusOf(bad)).toHaveText('Exit 1')

  for (const theme of THEMES) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
    const measured = await bad.evaluate((block) => {
      const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
      const lum = ([r, g, b]: number[]) => {
        const f = (v: number) => {
          const s = v / 255
          return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
        }
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
      }
      const status = block.querySelector(
        ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])',
      )!
      const fg = lum(parse(getComputedStyle(status).color))
      const bg = lum(parse(getComputedStyle(block).backgroundColor))
      const bar = getComputedStyle(block, '::before')
      return {
        ratio: (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05),
        barWidth: bar.width,
        barColor: bar.backgroundColor,
        dangerSurfaceSet:
          getComputedStyle(block).getPropertyValue('--color-danger-surface').trim() !== '',
      }
    })
    expect(measured.dangerSurfaceSet, theme).toBe(true)
    expect(measured.ratio, theme).toBeGreaterThanOrEqual(4.5)
    // Not by colour alone: the bar is drawn.
    expect(measured.barWidth, theme).toBe('3px')
  }
})

test('the quiet ⋮ is still reachable from the keyboard', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  const block = page.locator('.pane.active .scrollback-inner > .cmd-block').last()
  const dots = block.locator(':scope > .cmd-header .cmd-header-right > .ui-icon-button')
  await page.mouse.move(0, 0)
  await expect(dots).toHaveCSS('opacity', '0')
  await dots.focus()
  await expect(dots).toBeFocused()
  await expect(dots).toHaveCSS('opacity', '1')
})
