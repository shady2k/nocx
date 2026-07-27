/**
 * Scroll-ownership invariant (§6.2) — verified where layout resolves.
 *
 * The jsdom test (scroll-ownership.test.tsx) asserts that no CSS rule
 * declares an overflow we do not own. That is a fast structural check and
 * catches a different class of mistake (e.g. a stray `overflow:auto` in a
 * class that should not scroll). It cannot prove the height chain works:
 * jsdom does not compute flex resolution, min-height propagation, or
 * actual scrollbar creation.
 *
 * This spec proves the chain resolves. It renders the full Page DOM
 * structure (#panes → .pane → .surface-host → .ui-page → …) from
 * base.css (plus the tokens and theme that define its custom properties) in a
 * real browser, constrains the container to under 400px, and:
 *   1. Walks every node via getComputedStyle and asserts the only nodes
 *      with `auto`/`scroll` overflow are .ui-page__scroll and .ui-page__rail.
 *   2. Asserts the rail is NOT scrollable at ≤640px (narrow breakpoint),
 *      where it stacks above the content.
 *   3. Asserts the content actually scrolls — that the last section can be
 *      brought into view — because "no stray scroll containers" and "the
 *      intended one works" are different claims and the Settings scroll bug
 *      (nocx-82l9.2) was the second one failing.
 *
 * Design choice: synthetic Page, not the real Settings surface.
 * Settings now renders through Page (nocx-imkb.2), so it would be a valid
 * subject — but it is one page among several and carries its own content. The
 * synthetic Page keeps the subject the contract itself, and e2e/settings-scroll
 * covers the real surface separately.
 *
 * The synthetic page is constructed via page.setContent() — no app server,
 * no fixture route needed. The CSS is READ FROM base.css (the successor
 * of surface.css after ADR-0013 folded it into styles/), so the spec
 * cannot drift away from the contract it is asserting.
 */

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { test, expect } from './harness'

/**
 * The height-chain rules come from the real stylesheet, not a copy.
 *
 * An earlier draft reproduced surface.css verbatim here. That drifts silently:
 * change the source and this spec keeps passing against a stale duplicate, so
 * the one test meant to prove the contract stops testing it. Reading the file
 * makes divergence impossible.
 *
 * ADR-0013 migrated surface.css into base.css, which also absorbed the .pane
 * rules that were previously in style.css — so this now covers the full
 * height chain in one read. The tokens and theme files are also loaded so
 * that var(--color-*) references in base.css resolve (the app normally loads
 * these as @imports from style.css).
 *
 * The testbed's own framing (#testbed, #panes height) remains inline because
 * it provides the container size the app normally supplies through the shell
 * layout — it is scaffolding for the test, not part of the contract.
 */
const dir = '../frontend/src/styles/'
const TOKENS_CSS = readFileSync(resolve(__dirname, dir + 'tokens.css'), 'utf8')
const THEME_CSS = readFileSync(resolve(__dirname, dir + 'themes/tokyo-night.css'), 'utf8')
const BASE_CSS = readFileSync(resolve(__dirname, dir + 'base.css'), 'utf8')
const TESTBED_CSS = `
#testbed { position: relative; overflow: hidden; }
#panes { height: 100%; }
`
const PAGE_CSS = `${TOKENS_CSS}\n${THEME_CSS}\n${BASE_CSS}\n${TESTBED_CSS}`

type ScrollInfo = { tag: string; cls: string; overflowY: string; overflowX: string }

/** Rail items — enough to overflow a 300px tall container. */
function railItems(count: number): string {
  const items: string[] = []
  for (let i = 0; i < count; i++) {
    items.push(
      `<div style="padding:12px 16px;border-bottom:1px solid #2a2b3d">Navigation item ${i + 1}</div>`,
    )
  }
  return items.join('\n')
}

/** Content sections — enough to overflow a 300px container. */
function contentSections(count: number): string {
  const sections: string[] = []
  for (let i = 0; i < count; i++) {
    sections.push(
      `<section id="s${i + 1}" style="padding:16px 24px;border-bottom:1px solid #2a2b3d">
        <h2 style="margin:0 0 8px">Section ${i + 1}</h2>
        <p style="margin:0;color:#a9b1d6">Content for section ${i + 1} — enough text to occupy vertical space in the scroller.</p>
      </section>`,
    )
  }
  return sections.join('\n')
}

function pageHtml(height: string, railCount: number, sectionCount: number): string {
  return `
    <html><head>
      <style>${PAGE_CSS}</style>
    </head><body>
      <div id="testbed" style="height:${height};">
        <div id="panes" style="height:100%;">
          <div class="pane">
            <div class="surface-host">
              <div class="ui-page">
                <div class="ui-page__header">
                  <h1>Test Page</h1>
                </div>
                <div class="ui-page__body">
                  <div class="ui-page__rail">
                    ${railItems(railCount)}
                  </div>
                  <div class="ui-page__scroll">
                    ${contentSections(sectionCount)}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </body></html>
  `
}

test.describe('scroll ownership — layout resolved', () => {
  test('only .ui-page__scroll and .ui-page__rail have auto/scroll overflow — wide layout', async ({
    page,
  }) => {
    await page.setContent(pageHtml('300px', 25, 12))

    const scrollContainers: ScrollInfo[] = await page.evaluate(() => {
      function findScrollContainers(root: HTMLElement) {
        const results: Array<{ tag: string; cls: string; overflowY: string; overflowX: string }> =
          []
        const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT)
        let n: Node | null
        while ((n = walker.nextNode())) {
          const el = n as HTMLElement
          const cs = getComputedStyle(el)
          const oy = cs.overflowY
          const ox = cs.overflowX
          if (oy === 'auto' || oy === 'scroll' || ox === 'auto' || ox === 'scroll') {
            results.push({
              tag: el.tagName.toLowerCase(),
              cls: el.className,
              overflowY: oy,
              overflowX: ox,
            })
          }
        }
        return results
      }
      const root = document.querySelector('#testbed') as HTMLElement
      return findScrollContainers(root)
    })

    // Allow only .ui-page__scroll and .ui-page__rail
    const disallowed = scrollContainers.filter(
      (s) => !s.cls.includes('ui-page__scroll') && !s.cls.includes('ui-page__rail'),
    )
    expect(disallowed).toEqual([])

    // Sanity: the scroller and rail ARE in the list
    const scrollers = scrollContainers.filter((s) => s.cls.includes('ui-page__scroll'))
    expect(scrollers.length).toBeGreaterThanOrEqual(1)
    const rails = scrollContainers.filter((s) => s.cls.includes('ui-page__rail'))
    expect(rails.length).toBeGreaterThanOrEqual(1)
  })

  test('rail is NOT scrollable at narrow breakpoint (≤640px)', async ({ page }) => {
    // The media query in surface.css targets viewport width, not container width
    await page.setViewportSize({ width: 600, height: 720 })
    await page.setContent(pageHtml('300px', 25, 12))

    const railScroll: { overflowY: string } = await page.evaluate(() => {
      const rail = document.querySelector('.ui-page__rail') as HTMLElement
      const cs = getComputedStyle(rail)
      return { overflowY: cs.overflowY }
    })

    // At narrow breakpoint, rail should have overflow-y: visible (not auto/scroll)
    expect(railScroll.overflowY).toBe('visible')

    // The scroller should still be scrollable
    const scrollerScroll: { overflowY: string; scrollHeight: number; clientHeight: number } =
      await page.evaluate(() => {
        const scroller = document.querySelector('.ui-page__scroll') as HTMLElement
        const cs = getComputedStyle(scroller)
        return {
          overflowY: cs.overflowY,
          scrollHeight: scroller.scrollHeight,
          clientHeight: scroller.clientHeight,
        }
      })

    expect(scrollerScroll.overflowY).toBe('auto')
    expect(scrollerScroll.scrollHeight).toBeGreaterThan(scrollerScroll.clientHeight)
  })

  test('content scrolls — last section can be scrolled into view', async ({ page }) => {
    await page.setContent(pageHtml('280px', 25, 15))

    // First verify the scroller overflows (scrollHeight > clientHeight)
    const overflow: { scrollHeight: number; clientHeight: number } = await page.evaluate(() => {
      const scroller = document.querySelector('.ui-page__scroll') as HTMLElement
      return { scrollHeight: scroller.scrollHeight, clientHeight: scroller.clientHeight }
    })
    expect(overflow.scrollHeight).toBeGreaterThan(overflow.clientHeight)

    // Scroll to the bottom
    await page.evaluate(() => {
      const scroller = document.querySelector('.ui-page__scroll') as HTMLElement
      scroller.scrollTop = scroller.scrollHeight
    })

    // Check the last section (s15) is visible within the scroller's viewport
    const lastSectionVisible: boolean = await page.evaluate(() => {
      const scroller = document.querySelector('.ui-page__scroll') as HTMLElement
      const lastSection = document.getElementById('s15') as HTMLElement
      if (!lastSection) return false
      const scrollerRect = scroller.getBoundingClientRect()
      const sectionRect = lastSection.getBoundingClientRect()
      return sectionRect.bottom <= scrollerRect.bottom + 1 // allow 1px rounding
    })

    expect(lastSectionVisible).toBe(true)

    // Verify scrollTop moved from 0
    const scrollTop: number = await page.evaluate(() => {
      const scroller = document.querySelector('.ui-page__scroll') as HTMLElement
      return scroller.scrollTop
    })
    expect(scrollTop).toBeGreaterThan(0)
  })
})
