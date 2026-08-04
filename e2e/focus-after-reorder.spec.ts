import { test, expect, type Page } from './harness'

// ── Placement helpers (shared with tabs.spec.ts conventions) ─────────────

const PLACEMENT_ROW = '.ui-settings-row[data-key="tab.placement"]'
const PLACEMENT_SELECT = `${PLACEMENT_ROW} select`

async function switchPlacement(page: Page, value: 'horizontal' | 'vertical'): Promise<void> {
  await page.keyboard.press('Meta+,')
  // Settings opens on its FIRST section, so a row in any other section is in the
  // DOM and hidden. Navigate before waiting, or this times out on a control that
  // is right there and reads like a broken selector.
  await page.locator('.ui-settings-section-nav-item[data-section="Interface"] button').click()
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
  const secondTab = page.getByRole('tab').nth(1)
  const firstTab = page.getByRole('tab').first()
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
  const focusedHandle = await secondTab.elementHandle()
  expect(focusedHandle).not.toBeNull()

  // Snapshot pre-reorder tab order for post-reorder comparison.
  const preOrder = await page
    .getByRole('tab')
    .evaluateAll((tabs) => tabs.map((tab) => tab.getAttribute('data-tab-id')))
  expect(preOrder).toEqual([tabIdFirst, tabIdSecond])

  // Dispatch events on locator-derived handles. They resolve through the tab
  // strip shadow root, while preserving focus exactly as a real tab drop does.
  const firstHandle = await firstTab.elementHandle()
  expect(firstHandle).not.toBeNull()
  await page.evaluate(
    ({ source, target, draggedId }) => {
      const src = source as HTMLElement
      const tgt = target as HTMLElement
      const dataTransfer = new DataTransfer()
      dataTransfer.setData('text/plain', draggedId)

      src.dispatchEvent(new DragEvent('dragstart', { dataTransfer, bubbles: true }))
      src.classList.add('dragging')
      tgt.dispatchEvent(
        new DragEvent('dragover', { dataTransfer, bubbles: true, cancelable: true }),
      )
      tgt.dispatchEvent(new DragEvent('drop', { dataTransfer, bubbles: true, cancelable: true }))
      src.dispatchEvent(new DragEvent('dragend', { dataTransfer, bubbles: true }))
      src.classList.remove('dragging')
    },
    { source: focusedHandle, target: firstHandle, draggedId: tabIdSecond! },
  )

  // Wait for the reorder to be observable rather than sleeping for it. A fixed
  // 200ms timeout here failed about one run in three on chromium: under load the
  // assertions ran before Solid had re-sorted the nodes, so the test reported a
  // focus bug that was really a timing bug. Poll the outcome instead.
  await expect
    .poll(
      () =>
        page
          .getByRole('tab')
          .evaluateAll((tabs) => tabs.map((tab) => tab.getAttribute('data-tab-id'))),
      { timeout: 5000, message: 'tab order did not settle after the drag' },
    )
    .toEqual([tabIdSecond, tabIdFirst])

  // Assert 1: the original focused node moved to the first position and
  // remains focused. Role locators pierce the strip's component shadow root.
  const reorderedFirst = page.getByRole('tab').first()
  await expect(reorderedFirst).toBeFocused()
  const reorderedHandle = await reorderedFirst.elementHandle()
  expect(reorderedHandle).not.toBeNull()
  const sameNode = await focusedHandle.evaluate(
    (el, reordered) => el === reordered,
    reorderedHandle,
  )
  expect(sameNode).toBe(true)

  // Assert 2: document.activeElement still has the original data-tab-id
  const postId = await page.evaluate((handle) => handle.getAttribute('data-tab-id'), focusedHandle)
  expect(postId).toBe(tabIdSecond)

  // The reordered first tab is the original focused second tab.
  await expect(page.getByRole('tab').first()).toBeFocused()
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
    await expect(page.getByRole('tab')).toHaveCount(2)

    await assertFocusSurvivesReorder(page)
  })

  test('vertical orientation: focus survives drag reorder', async ({ page }) => {
    // Switch to vertical placement (the strip remounts into #vertical-tabstrip).
    await switchPlacement(page, 'vertical')

    // Add a second tab in the vertical strip.
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.getByRole('tab')).toHaveCount(2)

    await assertFocusSurvivesReorder(page)
  })

  test.afterEach(async ({ page }) => {
    // Reset so persisted state does not affect other test files.
    await switchPlacement(page, 'horizontal')
  })
})
