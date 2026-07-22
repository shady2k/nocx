import { test, expect } from '@playwright/test'

// nocx-83a: a new tab must never flash 'Terminal' before the directory name
// arrives. The bug was that the constructor wrote FALLBACK_TITLE into the DOM
// synchronously, and the real directory only landed after the async openSession
// WebSocket round-trip. The fix removes the constructor assignment and leaves
// the title span empty until start() resolves — the tab bar has a fixed width
// so an empty span does not cause a layout jump.
//
// A plain textContent() read is racy: the WebSocket to localhost resolves in
// microseconds. A MutationObserver fires synchronously when the tab's DOM node
// is inserted, so it captures the initial textContent before any async handler
// can touch it.

const TITLE = '.tab-title'

test('a new tab never displays "Terminal" in its title', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('.tab')).toHaveCount(1)

  // Wait for the first tab's title to be populated — it must be the directory,
  // not 'Terminal'.
  await expect(page.locator(TITLE).first()).not.toHaveText('')
  await expect(page.locator(TITLE).first()).not.toHaveText('Terminal')

  // Inject a MutationObserver that snapshots the title textContent at the
  // instant each new tab button is inserted into the DOM. The observer
  // callback runs synchronously — before any async openSession response.
  await page.evaluate(() => {
    ;(window as any).__nocxTitleSnapshots = [] as string[]
    const observer = new MutationObserver((mutations) => {
      for (const m of mutations) {
        for (const node of m.addedNodes) {
          if (node instanceof HTMLElement && node.classList.contains('tab')) {
            const title = node.querySelector('.tab-title')
            ;(window as any).__nocxTitleSnapshots.push(title?.textContent ?? '')
          }
        }
      }
    })
    observer.observe(document.querySelector('.tabs-container')!, { childList: true })
  })

  // Open a second tab. The MutationObserver fires as the tab button is
  // appended to the DOM, recording the title's textContent while the
  // constructor has just finished and openSession is still pending.
  await page.keyboard.press('Meta+t')
  await expect(page.locator('.tab')).toHaveCount(2)

  // Read the snapshots collected by the observer.
  const snapshots: string[] = await page.evaluate(
    () => (window as any).__nocxTitleSnapshots,
  )
  expect(snapshots.length).toBeGreaterThanOrEqual(1)
  for (const s of snapshots) {
    expect(s, 'tab title must never be "Terminal"').not.toBe('Terminal')
  }

  // Confirm the real title eventually lands and is not 'Terminal'.
  await expect(page.locator(TITLE).nth(1)).not.toHaveText('')
  await expect(page.locator(TITLE).nth(1)).not.toHaveText('Terminal')
})
