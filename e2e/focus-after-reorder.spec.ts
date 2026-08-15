import { test, expect, promptReady, type Page } from './harness'

// ── Placement helpers (shared with tabs.spec.ts conventions) ─────────────

const PLACEMENT_ROW = '.ui-settings-row[data-key="tab.placement"]'
const PLACEMENT_SELECT = `${PLACEMENT_ROW} select`

async function switchPlacement(page: Page, value: 'horizontal' | 'vertical'): Promise<void> {
  await page.keyboard.press('Meta+,')
  // Settings opens on its FIRST section, so a row in any other section is in the
  // DOM and hidden. Navigate before waiting, or this times out on a control that
  // is right there and reads like a broken selector.
  await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
  await expect(page.locator(PLACEMENT_SELECT)).toBeVisible({ timeout: 5000 })
  await page.selectOption(PLACEMENT_SELECT, value)
  await page.keyboard.press('Meta+w')
  if (value === 'vertical') {
    await expect(page.locator('#vertical-tabstrip')).toHaveClass(/tabstrip-vertical/, {
      timeout: 5000,
    })
  } else {
    await expect(page.locator('#tabbar')).toHaveClass(/tabbar/, { timeout: 5000 })
  }
}

/**
 * Wait until a freshly opened tab has finished taking focus for itself.
 *
 * A new tab's pane focuses its own prompt when the shell's first marker
 * arrives: the input-state transition shows the editor and CommandEditor.show()
 * focuses its CodeMirror view (terminal-content.ts:1568, editor.ts:711). That
 * is correct product behaviour and it is asynchronous — it waits on the shell,
 * not on the click.
 *
 * A test that puts focus somewhere deliberately before that has landed is
 * racing the app for the same resource and will sometimes lose. It did, on CI:
 * the poll below saw the tab button focused, and by the next evaluate the
 * editor had taken focus, so document.activeElement carried no data-tab-id at
 * all and the drag was never attempted (nocx-z9s9.11). Letting the app finish
 * first makes the test's own focus the last word.
 */
async function settleNewTab(page: Page): Promise<void> {
  await promptReady(page)
}

/**
 * Drag the second tab onto the first tab to trigger a real reorder
 * (reorderTab with the dragged tab after the target index moves it forward).
 * Assert the same DOM node is still document.activeElement after the reorder.
 *
 * Also confirms the tab order actually changed, so a silently-dropped drag
 * does not produce a false-positive pass.
 *
 * Caller must have exactly two tabs present before calling.
 */
async function assertFocusSurvivesReorder(page: Page): Promise<void> {
  const secondTab = page.locator('.nocx-tab').nth(1)
  const firstTab = page.locator('.nocx-tab').first()
  const tabIdSecond = await secondTab.getAttribute('data-tab-id')
  const tabIdFirst = await firstTab.getAttribute('data-tab-id')
  expect(tabIdFirst).not.toBeNull()
  expect(tabIdSecond).not.toBeNull()

  // Focus the SECOND tab button (we'll drag it onto the first).
  //
  // Poll rather than focus-then-assert. A placement switch remounts the strip
  // into the other host, so a locator resolved a moment earlier can point at a
  // node that is about to be replaced — focus then lands on a detached element
  // and the assertion fails before any drag has happened. Retrying the focus
  // until it sticks makes the setup wait for the strip to settle, which is the
  // real precondition.
  await expect
    .poll(
      async () => {
        await secondTab.focus()
        return secondTab.evaluate((el) => document.activeElement === el)
      },
      { timeout: 5000, message: 'second tab never took focus' },
    )
    .toBe(true)

  // Capture a JSHandle to the focused element so we can compare identity
  // after the reorder (proves the DOM node itself survived, not merely
  // that a node with the same data-tab-id is focused).
  const focusedHandle = await page.evaluateHandle(() => document.activeElement)

  // Snapshot the active element's data-tab-id before the drag
  const preId = await page.evaluate(() => document.activeElement?.getAttribute('data-tab-id'))
  expect(preId).toBe(tabIdSecond)

  // Snapshot pre-reorder tab order for post-reorder comparison
  const preOrder = await page.evaluate(() => {
    const tabs = document.querySelectorAll('.nocx-tab')
    return Array.from(tabs).map((t) => t.getAttribute('data-tab-id'))
  })
  expect(preOrder).toEqual([tabIdFirst, tabIdSecond])

  // Dispatch native HTML5 DragEvent sequence: dragstart → dragover → drop → dragend
  // Dragging the SECOND tab onto the FIRST tab triggers a real reorder
  // (reorderTab moves the dragged tab before the target tab).
  await page.evaluate(
    ({ draggedId }: { draggedId: string }) => {
      const src = document.querySelector(`[data-tab-id="${draggedId}"]`) as HTMLElement | null
      const targets = document.querySelectorAll('.nocx-tab')
      const tgt = targets[0] as HTMLElement | null // drop on FIRST tab
      if (!src || !tgt) throw new Error('Source or target tab not found')

      const dt = new DataTransfer()
      dt.setData('text/plain', draggedId)

      src.dispatchEvent(new DragEvent('dragstart', { dataTransfer: dt, bubbles: true }))
      src.classList.add('dragging')

      tgt.dispatchEvent(
        new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }),
      )
      tgt.dispatchEvent(
        new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }),
      )

      src.dispatchEvent(new DragEvent('dragend', { dataTransfer: dt, bubbles: true }))
      src.classList.remove('dragging')
    },
    { draggedId: tabIdSecond! },
  )

  // Wait for the reorder to be observable rather than sleeping for it. A fixed
  // 200ms timeout here failed about one run in three on chromium: under load the
  // assertions ran before Solid had re-sorted the nodes, so the test reported a
  // focus bug that was really a timing bug. Poll the outcome instead.
  await expect
    .poll(
      () =>
        page.evaluate(() =>
          Array.from(document.querySelectorAll('.nocx-tab')).map((t) =>
            t.getAttribute('data-tab-id'),
          ),
        ),
      { timeout: 5000, message: 'tab order did not settle after the drag' },
    )
    .toEqual([tabIdSecond, tabIdFirst])

  // Assert 1: the exact same DOM node (JSHandle identity) is still activeElement
  const sameNode = await page.evaluate((handle) => document.activeElement === handle, focusedHandle)
  expect(sameNode).toBe(true)

  // Assert 2: document.activeElement still has the original data-tab-id
  const postId = await page.evaluate(() => document.activeElement?.getAttribute('data-tab-id'))
  expect(postId).toBe(tabIdSecond)

  // Assert 3: the locator chain agrees
  await expect(page.locator(`[data-tab-id="${tabIdSecond}"]`)).toBeFocused()
}

// ── Specs ───────────────────────────────────────────────────────────────

test.describe('focus survives tab reorder', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', {
      timeout: 10_000,
    })
    // Reset to horizontal so persisted state does not contaminate.
    await switchPlacement(page, 'horizontal')
  })

  test('horizontal orientation: focus survives drag reorder', async ({ page }) => {
    // Add a second tab so there is something to reorder.
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(2)
    await settleNewTab(page)

    await assertFocusSurvivesReorder(page)
  })

  test('vertical orientation: focus survives drag reorder', async ({ page }) => {
    // Switch to vertical placement (the strip remounts into #vertical-tabstrip).
    await switchPlacement(page, 'vertical')

    // Add a second tab in the vertical strip.
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(2)
    await settleNewTab(page)

    await assertFocusSurvivesReorder(page)
  })

  test.afterEach(async ({ page }) => {
    // Reset so persisted state does not affect other test files.
    await switchPlacement(page, 'horizontal')
  })
})
