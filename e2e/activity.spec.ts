import { test, expect } from '@playwright/test'

// Drives the real app at :34115 (wails dev serves the UI and the bound Go
// methods together), so this exercises the real transport, PTY and renderer.
// The activity indicator is invisible to jsdom — no layout, no GPU, no focus.

const TAB = '.tab'
const ACTIVITY = '.tab-indicator.tab-activity'

test('a background tab lights the activity indicator on normal-buffer output', async ({
  page,
}) => {
  const logs: string[] = []
  page.on('console', (m) => logs.push(m.text()))

  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)

  // The first tab is activated and focused on load, so typing goes straight in.
  await page.keyboard.type('sleep 3; echo PROBE-OUTPUT\n')

  // Open a second tab; the first drops to the background.
  await page.locator('.tab-add').click()
  await expect(page.locator(TAB)).toHaveCount(2)
  await expect(page.locator(TAB).first()).not.toHaveClass(/active/)

  // Wait past the sleep so the output lands while tab 1 is in the background.
  await page.waitForTimeout(6000)

  console.log('--- console from the page ---')
  for (const l of logs.filter((l) => l.includes('NOCXDBG'))) console.log(l)

  const state = await page.evaluate(() => {
    const tabs = [...document.querySelectorAll('.tab')]
    return tabs.map((t) => ({
      cls: t.className,
      indicator: t.querySelector('.tab-indicator')?.className,
    }))
  })
  console.log('--- tab state ---', JSON.stringify(state, null, 1))

  await expect(page.locator(TAB).first().locator(ACTIVITY)).toBeAttached()
})
