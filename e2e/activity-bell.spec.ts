import { test, expect } from '@playwright/test'

const TAB = '.tab'
const ACTIVITY = '.tab-indicator.tab-activity'

// A full-screen TUI repaints constantly in the alternate buffer, and those
// repaints deliberately do not light the indicator (nocx-5mf). A bell is the
// program explicitly asking for attention, so it must light it even there —
// that is the whole escape hatch, and it is what tells you Claude Code wants
// you back. If this fails, a background agent is silent and the feature is
// useless in the case it was built for.
test('a bell lights the indicator from inside the alternate buffer', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)

  // Enter the alternate screen, wait, ring, and stay there — this is what a
  // TUI looks like from the outside.
  await page.keyboard.type("printf '\\033[?1049h'; sleep 5; printf '\\a'; sleep 30\n")

  // Background the tab only AFTER the switch to the alternate buffer has
  // happened. Otherwise the switch's own bytes arrive while _bufferType is
  // still 'normal' and light the indicator through the ordinary path, and the
  // test passes without ever exercising the bell.
  await page.waitForTimeout(2000)

  await page.locator('.tab-add').click()
  await expect(page.locator(TAB)).toHaveCount(2)
  await expect(page.locator(TAB).first()).not.toHaveClass(/active/)

  await page.waitForTimeout(6000)

  const state = await page.evaluate(() =>
    [...document.querySelectorAll('.tab')].map((t) => ({
      cls: t.className,
      indicator: t.querySelector('.tab-indicator')?.className,
    })),
  )
  console.log('--- tab state ---', JSON.stringify(state))

  await expect(page.locator(TAB).first().locator(ACTIVITY)).toBeAttached()
})
