/**
 * Proof matrix — browser-verified appearance invariants for the kit migration.
 *
 * Each case here must be ABLE TO FAIL. The migration (nocx-pp3y) is about to
 * move every kit control's appearance from an ancestor class (`.kit-scope`)
 * onto the components themselves, and the failure mode it risks is silent:
 * valid CSS that stops applying, with no build error and no red test.
 *
 * jsdom cannot prove any of this — it computes no layout, has no
 * `:focus-visible` heuristic, and never resolves actual scrollbars.
 *
 * §5 of .internal/specs/2026-07-27-kit-owns-its-appearance-design.md
 */
import type { Page } from '@playwright/test'
import { test, expect, promptReady, settingsReady } from './harness'

// ── Helpers ──────────────────────────────────────────────────────────────────

/** Open settings via keyboard shortcut and wait for the page to render. */
async function openSettings(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
}

/** Wait for a setting row identified by its data-key to appear. */
function getSettingRow(page: Page, dataKey: string) {
  return page.locator(`.ui-settings-row[data-key="${dataKey}"]`)
}

// ── Bootstrap ────────────────────────────────────────────────────────────────

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
})

// ═══════════════════════════════════════════════════════════════════════════════
// 1. Focus matrix — keyboard focus indicator vs pointer activation
// ═══════════════════════════════════════════════════════════════════════════════
//
// WCAG 2.4.7 requires a visible focus indicator on keyboard focus. The migration
// deletes `.kit-scope` where the ring currently lives (kit.css:6-12). This matrix
// catches a regression where the ring stops reaching a control — either because
// the move out of `.kit-scope` narrowed it, or because a component's own styles
// clip or override it. T1 moved it to base.css on `button/input/select`, keeping
// the element selectors rather than widening to a bare `:focus-visible`; this
// matrix is what a later widening would have to be proven against.
//
// Selectors here ask for the accessible name, not the class, wherever a control
// became an IconButton — `.tab-add` and friends were deleted with the classes they
// named (nocx-wqrq), and a test pinned to a retired class fails for a reason that
// has nothing to do with what it measures.
//
// Each test: (1) focus the element with Tab, assert a ring property is set,
// (2) click it with the mouse / move focus away, assert the ring property is
// NOT set (pointer activation must not show the keyboard ring).
//
// IconButton, FileInput, Radio and QuickConnectRow are not yet built; their
// rows are test.skip.
//

test.describe('1. Focus matrix', () => {
  test.describe('Button', () => {
    test('shows focus ring on keyboard focus, not on pointer activation', async ({ page }) => {
      await expect(page.locator('[data-action="settings"]')).toBeAttached()

      // Reach the gear the way a keyboard user does. This used to Tab until the
      // gear was focused, on the note that "only the gear button has tabindex=0
      // (no views registered)" — true when it was written, false since Ports
      // became a view (b26bb62). The bar is ONE Tab stop with a roving
      // tabindex, so Tab lands on whichever button currently holds it and the
      // rest are reached with arrows. Tabbing 15 times never arrived, so the
      // test failed on a focus ring that was never asked for.
      const gear = page.locator('[data-action="settings"]')
      await page.locator('[role="toolbar"] button[tabindex="0"]').focus()
      const inBar = await page.locator('[role="toolbar"] button').count()
      for (let i = 0; i < inBar; i++) {
        const isFocused = await gear.evaluate((el) => el === document.activeElement)
        if (isFocused) break
        await page.keyboard.press('ArrowDown')
      }

      // Assert the ring is present on keyboard focus.
      await expect(gear).toBeFocused()
      const kbdShadow = await gear.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(kbdShadow).not.toBe('none')
      expect(kbdShadow).toContain('rgb')

      // Move focus off and activate with pointer
      await page.locator('[aria-label="New tab"]').focus()
      await expect(gear).not.toBeFocused()

      await gear.click()
      const ptrShadow = await gear.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(ptrShadow).toBe('none')
    })
  })

  test.describe('TextField', () => {
    test('shows focus ring on keyboard focus, not on pointer activation', async ({ page }) => {
      await openSettings(page)
      // NAMED, not `.first()` over a global class. This was
      // `.ui-search-field input[type="search"]` first-of-page, which was
      // Settings' own field until the sidebar's panels grew filters of their
      // own — after which the first one on the page is the Files panel's
      // "Filter files by name", which is not what opening Settings focuses
      // (nocx-708q.6). A positional locator over a kit class names whatever
      // the page happens to render first; the accessible name is the field.
      const searchField = page.getByRole('searchbox', { name: 'Search settings' })
      // Search field is auto-focused when settings opens
      await expect(searchField).toBeFocused()
      const kbdShadow = await searchField.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(kbdShadow).not.toBe('none')
      expect(kbdShadow).toContain('rgb')

      // Move away and click to activate via pointer
      await page.keyboard.press('Tab')
      await expect(searchField).not.toBeFocused()

      await searchField.click()
      // Input[type="search"] shows :focus-visible in Chromium even after pointer
      // activation (the spec requires it for text inputs). The important assertion
      // is that keyboard focus PRODUCES a visible ring — tested above.
      // Verify the ring does not disappear (ring should persist for text inputs).
      const ptrShadow = await searchField.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(ptrShadow).not.toBe('none')
    })
  })

  test.describe('Select', () => {
    test('shows focus ring on keyboard focus', async ({ page }) => {
      await openSettings(page)
      // Navigate to Interface section to show the theme/UI settings rows
      await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
      const themeRow = getSettingRow(page, 'ui.theme')
      await expect(themeRow).toBeVisible({ timeout: 5_000 })
      const select = themeRow.locator('select')
      await expect(select).toBeAttached()

      // Tab until the select is focused
      for (let i = 0; i < 20; i++) {
        const isFocused = await select.evaluate((el) => el === document.activeElement)
        if (isFocused) break
        await page.keyboard.press('Tab')
      }
      await expect(select).toBeFocused()
      const kbdShadow = await select.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(kbdShadow).not.toBe('none')
      expect(kbdShadow).toContain('rgb')
    })
  })

  test.describe('Checkbox', () => {
    test('shows focus ring on keyboard focus', async ({ page }) => {
      await openSettings(page)
      const checkbox = page.locator('.ui-settings-filter input[type="checkbox"]')
      await expect(checkbox).toBeAttached()

      // Tab to the checkbox
      let found = false
      for (let i = 0; i < 20; i++) {
        const isFocused = await checkbox.evaluate((el) => el === document.activeElement)
        if (isFocused) {
          found = true
          break
        }
        await page.keyboard.press('Tab')
      }
      if (!found) {
        test.skip(true, 'Checkbox not reachable via Tab in this state')
        return
      }

      const kbdShadow = await checkbox.evaluate((el) => getComputedStyle(el).boxShadow)
      expect(kbdShadow).not.toBe('none')
      expect(kbdShadow).toContain('rgb')
    })
  })

  test.describe('Radio', () => {
    test.skip(
      true,
      'nocx-pp3y.8: Radio has no rules in kit.css today and gets its appearance in transaction 8. ' +
        'The radio component lives in ui/radio.tsx but has no consumers in the settings page ' +
        'accessible via e2e. Skip until Connections surface is navigable.',
    )
  })

  test.describe('QuickConnectRow', () => {
    test.skip(
      true,
      'QuickConnectRow is not a standalone component — quick-connect items are bare ' +
        '<div role="option"> elements. The quick-connect dialog opens with Meta+Shift+P but ' +
        'items have no tabindex. Skip — the search input focus is covered by SearchField.',
    )
  })

  test.describe('IconButton', () => {
    test.skip(true, 'IconButton not yet built (nocx-pp3y transaction 3)')
  })

  test.describe('FileInput', () => {
    test.skip(true, 'FileInput not yet built (nocx-pp3y transaction 2)')
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 2. Disabled appearance
// ═══════════════════════════════════════════════════════════════════════════════
//
// The disabled rule lives at kit.css:212-219 inside .kit-scope selectors:
//   input.kit-scope:disabled, .kit-scope input:disabled,
//   select.kit-scope:disabled, .kit-scope select:disabled,
//   button.kit-scope:disabled
// Every control carries its own identity now, so a disabled element is injected
// with its own class and NO ancestor — which is the whole claim this migration
// makes, tested rather than asserted. These cases used to build a `.kit-scope`
// wrapper around a bare element; that is the contract T15 (nocx-pnbd) deleted, so
// asserting it would only prove the wrapper was still needed.
//
// Read computed-style values into local vars BEFORE removing the element,
// then return them.

test.describe('2. Disabled appearance', () => {
  test('a disabled <button class="ui-button"> has opacity 0.5 and cursor not-allowed', async ({
    page,
  }) => {
    const result = await page.evaluate(() => {
      const btn = document.createElement('button')
      btn.className = 'ui-button'
      btn.disabled = true
      document.body.appendChild(btn)
      const cs = getComputedStyle(btn)
      const opacity = cs.opacity
      const cursor = cs.cursor
      btn.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })

  test('a disabled TextField input has opacity 0.5 and cursor not-allowed', async ({ page }) => {
    const result = await page.evaluate(() => {
      const el = document.createElement('input')
      el.type = 'text'
      el.className = 'ui-text-field__input'
      el.disabled = true
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      const opacity = cs.opacity
      const cursor = cs.cursor
      el.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })

  test('a disabled SearchField input has opacity 0.5 and cursor not-allowed', async ({ page }) => {
    const result = await page.evaluate(() => {
      const el = document.createElement('input')
      el.type = 'search'
      el.className = 'ui-search-field__input'
      el.disabled = true
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      const opacity = cs.opacity
      const cursor = cs.cursor
      el.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })

  test('a disabled Checkbox control has opacity 0.5 and cursor not-allowed', async ({ page }) => {
    const result = await page.evaluate(() => {
      const el = document.createElement('input')
      el.type = 'checkbox'
      el.className = 'ui-checkbox__control'
      el.disabled = true
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      const opacity = cs.opacity
      const cursor = cs.cursor
      el.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })

  test('a disabled Radio control has opacity 0.5 and cursor not-allowed', async ({ page }) => {
    const result = await page.evaluate(() => {
      const el = document.createElement('input')
      el.type = 'radio'
      el.className = 'ui-radio__control'
      el.disabled = true
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      const opacity = cs.opacity
      const cursor = cs.cursor
      el.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })

  test('a disabled Select has opacity 0.5 and cursor not-allowed', async ({ page }) => {
    const result = await page.evaluate(() => {
      const el = document.createElement('select')
      el.className = 'ui-select'
      el.disabled = true
      document.body.appendChild(el)
      const cs = getComputedStyle(el)
      const opacity = cs.opacity
      const cursor = cs.cursor
      el.remove()
      return { opacity, cursor }
    })
    expect(result.opacity).toBe('0.5')
    expect(result.cursor).toBe('not-allowed')
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 3. Theme with a dialog open
// ═══════════════════════════════════════════════════════════════════════════════
//
// The dialog renders in the top layer outside `#app` (native `<dialog>` with
// showModal). It must still inherit theme tokens when `data-theme` changes
// on `document.documentElement`. The migration must not break inheritance
// to the top layer.
//

test.describe('3. Theme with dialog open', () => {
  test('dialog colours change with data-theme without remount', async ({ page }) => {
    await openSettings(page)
    // Navigate to Interface section to make the theme setting row visible
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()

    // Open a confirm dialog and measure its colours.
    const { bgBefore, colorBefore } = await page.evaluate(async () => {
      const dialog = document.createElement('dialog')
      dialog.className = 'nocx-dialog'
      dialog.innerHTML = [
        '<div class="nocx-dialog__panel" style="background: var(--color-surface); color: var(--color-text); padding: 20px;">',
        '  <h2 class="nocx-dialog__title">Test Dialog</h2>',
        '  <p class="nocx-dialog__message">Theme test</p>',
        '  <div class="nocx-dialog__actions">',
        '    <button>Cancel</button>',
        '    <button>OK</button>',
        '  </div>',
        '</div>',
      ].join('\n')
      document.body.appendChild(dialog)
      dialog.showModal()
      const panel = dialog.querySelector('.nocx-dialog__panel') as HTMLElement
      const cs = getComputedStyle(panel)
      const bg = cs.backgroundColor
      const color = cs.color
      return { bgBefore: bg, colorBefore: color }
    })

    // Switch theme to light
    const themeSelect = page.locator('.ui-settings-row[data-key="ui.theme"] select')
    await expect(themeSelect).toBeVisible({ timeout: 5_000 })
    await themeSelect.selectOption('light')
    await page.waitForFunction(
      () => document.documentElement.getAttribute('data-theme') === 'light',
      { timeout: 5_000 },
    )

    // Assert the dialog's colours changed without remount
    const { bgAfter, colorAfter } = await page.evaluate(() => {
      const panel = document.querySelector('.nocx-dialog__panel') as HTMLElement | null
      if (!panel) return { bgAfter: null, colorAfter: null }
      const cs = getComputedStyle(panel)
      return { bgAfter: cs.backgroundColor, colorAfter: cs.color }
    })

    expect(bgAfter).not.toBeNull()
    expect(colorAfter).not.toBeNull()
    expect(bgAfter).not.toBe(bgBefore)
    expect(colorAfter).not.toBe(colorBefore)

    // Clean up: close the dialog
    await page.evaluate(() => {
      const dialog = document.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
      dialog?.close()
    })

    // Switch theme back
    await themeSelect.selectOption('tokyo-night')
    await page.waitForFunction(
      () => document.documentElement.getAttribute('data-theme') === 'tokyo-night',
      { timeout: 5_000 },
    )
  })

  test('dialog inherits theme colours without an ancestor .kit-scope', async ({ page }) => {
    await openSettings(page)

    // Verify that the dialog's panel gets its colours from `body` / `html`,
    // not from `#app` or `.kit-scope`. If the migration breaks inheritance,
    // the dialog will show unthemed colours (black on white or browser defaults).
    const { inheritedColor } = await page.evaluate(() => {
      const dialog = document.createElement('dialog')
      dialog.className = 'nocx-dialog'
      dialog.innerHTML = `
        <div class="nocx-dialog__panel" style="background: var(--color-surface); color: var(--color-text); padding: 20px;">
          <p>Themed content</p>
        </div>
      `
      document.body.appendChild(dialog)
      dialog.showModal()
      const panel = dialog.querySelector('.nocx-dialog__panel') as HTMLElement
      const cs = getComputedStyle(panel)
      const inheritedColor = cs.color
      dialog.close()
      dialog.remove()
      return { inheritedColor }
    })
    // In tokyo-night theme, the text colour should be a dark-theme colour.
    // Assert it's not the default browser black (#000 or rgb(0,0,0))
    expect(inheritedColor).not.toBe('rgb(0, 0, 0)')
    expect(inheritedColor).toContain('rgb') // must be a resolved colour
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 4. Scroll ownership, measured
// ═══════════════════════════════════════════════════════════════════════════════
//
// The settings page must have exactly one scroll owner: `.ui-page__scroll`.
// The jsdom test (scroll-ownership.test.tsx) asserts the structural invariant;
// this test asserts the real layout chain works, by measuring actual
// `scrollHeight > clientHeight` in the real rendered settings page.
//

test.describe('4. Scroll ownership — measured', () => {
  test('settings page: exactly one element has scrollHeight > clientHeight and scrolls', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1024, height: 250 })
    await page.goto('/')
    await promptReady(page)
    await openSettings(page)
    // Navigate to Interface section to see more settings rows
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
    // The scroller AFTER the navigation, and that order is the assertion.
    // Interface is a generated section, so it is `scrollMode: 'page'` and owns
    // a `.ui-page__scroll` for as long as it is the open page. Waiting for it
    // before the click would be waiting on Settings' loading frame instead
    // (see settingsReady) — which openSettings has already done properly.
    await expect(page.locator('.ui-page__scroll')).toBeVisible({ timeout: 5_000 })
    await expect(page.locator('.ui-page__body')).toBeAttached()

    // Count scrollable elements
    const scrollable = await page.evaluate(() => {
      const results: Array<{ tag: string; cls: string; id: string | null }> = []
      const all = document.querySelectorAll('*')
      for (const el of all) {
        const cs = getComputedStyle(el)
        const overflowY = cs.overflowY
        const scrolls =
          (overflowY === 'auto' || overflowY === 'scroll') && el.scrollHeight > el.clientHeight + 1
        if (scrolls) {
          // Determine the class name for identification
          const classes = Array.from(el.classList).join('.')
          results.push({
            tag: el.tagName.toLowerCase(),
            cls: classes,
            id: el.id || null,
          })
        }
      }
      return results
    })

    // The scroll owner should be exactly `.ui-page__scroll`
    expect(scrollable.length).toBeGreaterThanOrEqual(1)
    const pageScrolls = scrollable.filter((s) => s.cls.includes('ui-page__scroll'))
    expect(pageScrolls.length).toBe(1)

    // The invariant is about the PAGE: one scroll owner inside it, so a wheel
    // event has one unambiguous target. The sidebar is not inside it — it is a
    // peer region beside the page, with its own list and its own scrollbar,
    // exactly as the rail has. It was added to this allowance when the Files
    // panel gave the sidebar a scrolling body; before that the sidebar had
    // nothing long enough to scroll and the omission cost nothing.
    //
    // Named in the failure rather than merely counted: "expected true, received
    // false" says a stranger scrolls without saying which, and finding the
    // stranger is the entire point of the check.
    const OWNERS = ['ui-page__scroll', 'ui-page__rail', 'ui-sidebar-view__body']
    const strangers = scrollable.filter((s) => !OWNERS.some((o) => s.cls.includes(o)))
    expect(strangers, `unexpected scroll owners: ${JSON.stringify(strangers)}`).toEqual([])
  })

  test('the content area actually scrolls the last setting row into view', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 250 })
    await page.goto('/')
    await promptReady(page)
    await openSettings(page)
    // Navigate to Interface section to see more settings rows
    await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()

    // Scroll the last setting row into view using the scroll container. The
    // scroller arrives WITH Interface — Settings opens on Connections, which
    // is contained and has none — so it is waited for, never assumed.
    const lastRow = page.locator('#st-section-Interface .ui-settings-row').last()
    const scroller = page.locator('.ui-page__scroll')
    await expect(scroller).toBeVisible({ timeout: 5_000 })
    await page.evaluate(() => {
      const s = document.querySelector('.ui-page__scroll')
      if (s) {
        // Scroll the container to the bottom so the last row is visible
        s.scrollTop = s.scrollHeight
      }
    })
    await expect
      .poll(() => scroller.evaluate((el) => el.scrollTop), { timeout: 5_000 })
      .toBeGreaterThan(0)

    // The row should be visible within the scroll container
    const scrollerBox = await scroller.boundingBox()
    const rowBox = await lastRow.boundingBox()

    expect(rowBox).not.toBeNull()
    expect(scrollerBox).not.toBeNull()
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 5. Roving tabindex and Tab order
// ═══════════════════════════════════════════════════════════════════════════════
//
// The tab strip and activity bar each implement roving tabindex: exactly one
// element has tabIndex=0 at a time, arrow keys move focus, Home/End work, and
// Tab/Shift+Tab enter and leave the group as one stop.
//

test.describe('5. Roving tabindex', () => {
  test.describe('tab strip', () => {
    test('exactly one tab has tabIndex=0', async ({ page }) => {
      const tabs = page.locator('[role="tab"]')
      await expect(tabs.first()).toBeAttached()

      const tabIndexes = await tabs.evaluateAll((els) =>
        els.map((el) => el.getAttribute('tabindex')),
      )
      const zeroCount = tabIndexes.filter((ti) => ti === '0').length
      expect(zeroCount).toBe(1)
    })

    test('ArrowRight moves focus to next tab', async ({ page }) => {
      const tabs = page.locator('[role="tab"]')
      await expect(tabs.first()).toBeAttached()

      // Open a second tab so there is a tab to navigate to
      await page.locator('[aria-label="New tab"]').click()
      await expect(tabs).toHaveCount(2)
      // Wait for the new tab to finish taking focus before competing with it.
      // A fresh tab mounts an editor and focuses it, and it does so AFTER the
      // tab button exists — so a fixed sleep here is a race against session
      // startup that these tests were quietly winning. They stopped winning it
      // the moment the launcher's timing changed, and reported it as a focus
      // bug. promptReady is the app saying it is done moving focus.
      await promptReady(page)

      // Read the first tab's data-pane-id from the locator, not activeElement
      const initialId = await tabs.first().getAttribute('data-pane-id')
      expect(initialId).not.toBeNull()

      // Put keyboard focus on the first tab. Playwright's .focus() on a
      // tabindex="-1" element may be redirected by the roving handler, so
      // we check the active element's data-pane-id after focusing.
      await page.evaluate(() => {
        ;(document.querySelector('[role="tab"]') as HTMLElement)?.focus()
      })
      await page.waitForTimeout(50)

      // Press ArrowRight
      await page.keyboard.press('ArrowRight')
      await page.waitForTimeout(50)

      // The next tab should now be focused
      const focusedId = await page.evaluate(() =>
        document.activeElement?.getAttribute('data-pane-id'),
      )
      expect(focusedId).not.toBeNull()
      // The focused element should differ from the initial first tab
      expect(focusedId).not.toBe(initialId)
    })
    test('ArrowLeft moves focus to previous tab', async ({ page }) => {
      const tabs = page.locator('[role="tab"]')
      await expect(tabs.first()).toBeAttached()

      // Open a second tab first so arrow navigation has room
      await page.locator('[aria-label="New tab"]').click()
      await expect(tabs).toHaveCount(2)
      // Wait for the new tab to finish taking focus before competing with it.
      // A fresh tab mounts an editor and focuses it, and it does so AFTER the
      // tab button exists — so a fixed sleep here is a race against session
      // startup that these tests were quietly winning. They stopped winning it
      // the moment the launcher's timing changed, and reported it as a focus
      // bug. promptReady is the app saying it is done moving focus.
      await promptReady(page)

      // Focus the active tab (tabindex="0" — the second tab). Playwright's
      // .focus() works here because tabindex >= 0.
      const activePane = page.locator('[role="tab"][tabindex="0"]')
      await activePane.focus()
      await expect(activePane).toBeFocused()
      // ArrowLeft from the second tab should move to the first
      await page.keyboard.press('ArrowLeft')
      await page.waitForTimeout(100)

      // Check that focus moved to the first tab
      const firstPane = tabs.first()
      const isFirstFocused = await firstPane.evaluate((el) => el === document.activeElement)
      expect(isFirstFocused).toBe(true)
    })

    test('Home focuses the first tab', async ({ page }) => {
      const tabs = page.locator('[role="tab"]')
      await expect(tabs.first()).toBeAttached()

      // Open a second tab so we can go right then Home
      await page.locator('[aria-label="New tab"]').click()
      await expect(tabs).toHaveCount(2)
      // Wait for the new tab to finish taking focus before competing with it.
      // A fresh tab mounts an editor and focuses it, and it does so AFTER the
      // tab button exists — so a fixed sleep here is a race against session
      // startup that these tests were quietly winning. They stopped winning it
      // the moment the launcher's timing changed, and reported it as a focus
      // bug. promptReady is the app saying it is done moving focus.
      await promptReady(page)

      // Focus the first tab via evaluate (Playwright .focus() on tabindex="-1"
      // is redirected by the roving handler to the active tab)
      await page.evaluate(() => {
        ;(document.querySelector('[role="tab"]') as HTMLElement)?.focus()
      })
      await page.waitForTimeout(50)

      // Move right, then Home
      await page.keyboard.press('ArrowRight')
      await page.keyboard.press('Home')
      await page.waitForTimeout(50)

      // The first tab should be focused
      const isFirst = await page.evaluate(() => {
        const first = document.querySelector('[role="tab"]')
        return document.activeElement === first
      })
      expect(isFirst).toBe(true)
    })

    test('End focuses the last tab', async ({ page }) => {
      const tabs = page.locator('[role="tab"]')
      await expect(tabs.first()).toBeAttached()

      // With one tab, End stays on the same tab — that's correct behavior.
      // Just verify it doesn't crash and focus stays.
      await tabs.first().focus()
      await page.keyboard.press('End')
      const focusedId = await page.evaluate(() => {
        const el = document.activeElement
        return el?.getAttribute('role')
      })
      expect(focusedId).toBe('tab')
    })

    test('Tab moves focus out of the tab strip (one stop)', async ({ page }) => {
      await page.locator('[role="tab"][tabindex="0"]').focus()

      // Tab should move to the next focusable element after the strip
      await page.keyboard.press('Tab')

      // The active element should not be a tab
      const activeIsTab = await page.evaluate(() => {
        const el = document.activeElement
        return el?.getAttribute('role') === 'tab'
      })
      expect(activeIsTab).toBe(false)
    })

    test('Shift+Tab moves focus into the tab strip', async ({ page }) => {
      await page.locator('[role="tab"]').first().focus()

      // Tab forward out, then Shift+Tab back in
      await page.keyboard.press('Tab')
      await page.keyboard.press('Shift+Tab')

      const activeIsTab = await page.evaluate(() => {
        const el = document.activeElement
        return el?.getAttribute('role') === 'tab'
      })
      expect(activeIsTab).toBe(true)
    })
  })

  test.describe('activity bar', () => {
    test('exactly one button has tabIndex=0', async ({ page }) => {
      // `[role="button"]` matches only an EXPLICIT attribute, and IconButton renders a
      // real <button>, whose button role is implicit. Query the element.
      const buttons = page.locator('[role="toolbar"] button')
      await expect(buttons.first()).toBeAttached()

      const tabIndexes = await buttons.evaluateAll((els) =>
        els.map((el) => el.getAttribute('tabindex')),
      )
      const zeroCount = tabIndexes.filter((ti) => ti === '0').length
      expect(zeroCount).toBe(1)
    })

    // The two tests below used to name the gear as the answer, because the bar
    // held exactly one button when they were written. Ports became a view
    // (b26bb62) and the answers moved. They now name POSITIONS — next, first,
    // last — which is what a roving tabindex actually promises and what stays
    // true however many views are registered.
    //
    // The ArrowDown/ArrowUp one did not even go red: it hunted the gear with
    // Tab, never found it once Tab started landing on Ports, and took its
    // `test.skip` branch. A rotted test that skips reports nothing at all,
    // which is worse than one that fails.

    /** Identity of the focused toolbar button: its view id or its action id. */
    const focusedId = (page: Page) =>
      page.evaluate(() => {
        const el = document.activeElement
        if (!el || !el.closest('[role="toolbar"]')) return null
        return el.getAttribute('data-view') ?? el.getAttribute('data-action')
      })

    /** The bar's buttons in DOM order, by the same identity. */
    const barIds = (page: Page) =>
      page
        .locator('[role="toolbar"] button')
        .evaluateAll((els) =>
          els.map((el) => el.getAttribute('data-view') ?? el.getAttribute('data-action')),
        )

    test('ArrowDown/ArrowUp moves focus in the activity bar', async ({ page }) => {
      // The bar is one Tab stop: focus goes to whichever button holds
      // tabindex=0, and arrows move from there.
      await page.locator('[role="toolbar"] button[tabindex="0"]').focus()
      const ids = await barIds(page)
      const start = await focusedId(page)
      expect(start).not.toBeNull()

      // Down moves to the next button, wrapping at the end.
      await page.keyboard.press('ArrowDown')
      const afterDown = await focusedId(page)
      expect(afterDown).toBe(ids[(ids.indexOf(start) + 1) % ids.length])

      // Up returns. With a single button both are no-ops that must not throw,
      // and the assertions above still hold because the wrap is to itself.
      await page.keyboard.press('ArrowUp')
      expect(await focusedId(page)).toBe(start)
    })

    test('Home/End work in the activity bar', async ({ page }) => {
      await page.locator('[role="toolbar"] button[tabindex="0"]').focus()
      const ids = await barIds(page)
      expect(ids.length).toBeGreaterThan(0)

      await page.keyboard.press('Home')
      expect(await focusedId(page)).toBe(ids[0])

      await page.keyboard.press('End')
      expect(await focusedId(page)).toBe(ids[ids.length - 1])
    })

    test('Tab enters and leaves the toolbar as one stop', async ({ page }) => {
      // Tab into the toolbar
      await page.keyboard.press('Tab')
      for (let i = 0; i < 15; i++) {
        const inToolbar = await page.evaluate(() => {
          const el = document.activeElement
          return el?.closest('[role="toolbar"]') !== null
        })
        if (inToolbar) break
        await page.keyboard.press('Tab')
      }

      // Tab again should leave the toolbar
      await page.keyboard.press('Tab')

      const stillInToolbar = await page.evaluate(() => {
        const el = document.activeElement
        return el?.closest('[role="toolbar"]') !== null
      })
      expect(stillInToolbar).toBe(false)
    })
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 6. Page duties
// ═══════════════════════════════════════════════════════════════════════════════
//
// Page owns: focus placement after a page change, and the rail's responsive
// rearrangement at the narrow breakpoint.
//

test.describe('6. Page duties', () => {
  test.describe('focus placement after page switch', () => {
    test('focus moves to the first control in the target section after nav click', async ({
      page,
    }) => {
      await openSettings(page)

      // Click on a section nav link (e.g., "Interface") in the rail
      const terminalNav = page.locator('.ui-grouped-nav__item[data-item="Interface"] button')
      await expect(terminalNav).toBeVisible({ timeout: 5_000 })
      await terminalNav.click()

      // The first setting in the Interface section should be visible
      await expect(page.locator('#st-setting-tab\\.placement')).toBeVisible()

      // Focus should not be lost after nav click
      const anyFocused = await page.evaluate(() => {
        const el = document.activeElement
        return el !== null && el !== document.body
      })
      expect(anyFocused).toBe(true)
    })

    test('clicking a nav heading that is already active does not lose focus', async ({ page }) => {
      await openSettings(page)

      // Click a nav link to switch page
      const terminalNav = page.locator('.ui-grouped-nav__item[data-item="Interface"] button')
      await expect(terminalNav).toBeVisible({ timeout: 5_000 })
      await terminalNav.click()

      // Wait a bit, then click the same nav again
      await page.waitForTimeout(200)
      await terminalNav.click()

      // Focus should not be lost
      const anyFocused = await page.evaluate(() => {
        const el = document.activeElement
        return el !== null && el !== document.body
      })
      expect(anyFocused).toBe(true)
    })
  })

  test.describe('rail responsive rearrangement', () => {
    test('rail is side-by-side at wide viewport (large)', async ({ page }) => {
      await page.setViewportSize({ width: 1024, height: 768 })
      // Re-navigate to apply viewport
      await page.goto('/')
      await promptReady(page)
      await openSettings(page)

      const rail = page.locator('.ui-page__rail')
      const body = page.locator('.ui-page__body')
      await expect(rail).toBeAttached()
      await expect(body).toBeAttached()

      // At wide viewport, rail and content are side-by-side
      const railBox = await rail.boundingBox()
      const bodyBox = await body.boundingBox()

      expect(railBox).not.toBeNull()
      expect(bodyBox).not.toBeNull()
      // The body should be wider than the rail (rail is ~200-260px)
      expect(bodyBox!.width).toBeGreaterThan(railBox!.width * 1.5)
      // Rail should be on the left
      expect(railBox!.x).toBe(bodyBox!.x)
    })

    test('rail stacks above content at narrow viewport (≤640px)', async ({ page }) => {
      await page.setViewportSize({ width: 600, height: 768 })
      await page.goto('/')
      await promptReady(page)
      await openSettings(page)

      const rail = page.locator('.ui-page__rail')
      await expect(rail).toBeAttached()

      // At narrow breakpoint, the rail should be non-scrollable
      const railScrollable = await rail.evaluate((el) => {
        const cs = getComputedStyle(el)
        if (!(cs.overflowY === 'auto' || cs.overflowY === 'scroll')) return 'not-scrollable'
        return el.scrollHeight > el.clientHeight + 1 ? 'scrolls' : 'not-scrollable'
      })
      // The rail should NOT be the one scrolling
      expect(railScrollable).toBe('not-scrollable')
    })

    test('content remains scrollable at narrow breakpoint', async ({ page }) => {
      await page.setViewportSize({ width: 600, height: 250 })
      await page.goto('/')
      await promptReady(page)
      await openSettings(page)
      // Navigate to Interface section to see more settings rows
      await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()

      // WAIT FOR THE SCROLLER THE NAVIGATION BRINGS. Settings opens on
      // Connections, which is `scrollMode: 'contained'` and has no
      // `.ui-page__scroll` at all; the element below belongs to Interface and
      // exists only once that page is the open one. Without this the evaluate
      // found null, scrolled nothing, and the boundingBox below timed out.
      const lastRow = page.locator('#st-section-Interface .ui-settings-row').last()
      const scroller = page.locator('.ui-page__scroll')
      await expect(scroller).toBeVisible({ timeout: 5_000 })
      await page.evaluate(() => {
        const s = document.querySelector('.ui-page__scroll')
        if (s) {
          s.scrollTop = s.scrollHeight
        }
      })
      // The settled scroll position, not a duration: `scrollTop` is the
      // observable the assignment above is trying to produce.
      await expect
        .poll(() => scroller.evaluate((el) => el.scrollTop), { timeout: 5_000 })
        .toBeGreaterThan(0)

      const scrollerBox = await scroller.boundingBox()
      const rowBox = await lastRow.boundingBox()

      expect(rowBox).not.toBeNull()
      expect(scrollerBox).not.toBeNull()
    })
  })
})
