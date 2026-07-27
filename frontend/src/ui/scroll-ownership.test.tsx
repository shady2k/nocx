/**
 * Scroll-ownership invariant (§6.2):
 *
 * "Only PageScroller and PageRail may scroll. No third scroll container."
 *
 * This test asserts that a rendered Page's DOM subtree contains no element
 * with a computed overflow of `auto` or `scroll` other than the named
 * scroll owners (.ui-page__scroll, .ui-page__rail).
 *
 * ── jsdom limitation ──
 * This assertion runs in jsdom, which does apply CSS from <style> elements
 * (we inject surface.css) but does NOT compute layout — no flex, no
 * min-height chain, no actual scrollbar creation. So the test verifies
 * the *structural* invariant: no class or style rule produces an overflow
 * value we do not own. It cannot prove the chain works under WebKit.
 *
 * ── What a real browser check would look like ──
 * 1. Render Page with a rail that overflows (many items) and content that
 *    overflows (many sections) in a container < 400px tall.
 * 2. Query every node: getComputedStyle(n).overflowY (and X).
 * 3. Assert only .ui-page__scroll and .ui-page__rail are `auto`/`scroll`.
 * 4. Assert that .ui-page__rail's overflow is NOT `auto`/`scroll` when
 *    the viewport is ≤ 640px wide (narrow breakpoint).
 * 5. Run in a packaged WKWebView, where the flex chain actually resolves.
 *
 * WebKit is the release-relevant target (Chromium's implicit flex min-size
 * treatment masks this bug; see §3.1).
 */

// @vitest-environment jsdom
import { describe, expect, it, afterEach, beforeEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { Page, type PageProps } from './page'
import { PageSection } from './page-section'
import { type PageScrollerHandle } from './page-scroller'

// Keep in sync with frontend/src/styles/surface.css — the CSS import is
// mocked by vitest, so we inject the page-relevant rules directly.
const SURFACE_CSS = `
.ui-page {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
}
.ui-page__header {
  flex: 0 0 auto;
}
.ui-page__body {
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow: hidden;
}
.ui-page__rail {
  flex: 0 0 auto;
  min-height: 0;
  overflow-y: auto;
}
.ui-page__scroll {
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  overflow-y: auto;
}
@media (max-width: 640px) {
  .ui-page__body {
    flex-direction: column;
  }
  .ui-page__rail {
    overflow-y: visible;
  }
}
`

// Helpers
type ScrollInfo = { node: Element; overflowY: string; overflowX: string }

function injectSurfaceStyles(): HTMLStyleElement {
  const style = document.createElement('style')
  style.id = 'surface-styles-test'
  style.textContent = SURFACE_CSS
  document.head.appendChild(style)
  return style
}

/** Walk every element in the subtree (excluding the host itself) and
 *  collect those whose computed overflow-y or overflow-x is auto/scroll.
 *  Returns the full list so the test can inspect which nodes it found. */
function findScrollContainers(root: HTMLElement): ScrollInfo[] {
  const results: ScrollInfo[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT, null)
  let node: Node | null
  while ((node = walker.nextNode())) {
    const el = node as HTMLElement
    const cs = getComputedStyle(el)
    const oy = cs.overflowY
    const ox = cs.overflowX
    if (oy === 'auto' || oy === 'scroll' || ox === 'auto' || ox === 'scroll') {
      results.push({
        node: el,
        overflowY: oy,
        overflowX: ox,
      })
    }
  }
  return results
}

/** Page spec helper — avoids repeating default props everywhere. */
function renderPage(
  overrides?: Partial<
    PageProps & { scrollerRef?: PageScrollerHandle | ((h: PageScrollerHandle) => void) }
  >,
) {
  return render(() => (
    <Page
      title={overrides?.title ?? 'Test'}
      description={overrides?.description}
      actions={overrides?.actions}
      leading={overrides?.leading}
      scrollerRef={overrides?.scrollerRef}
    >
      {overrides?.children ?? 'Content'}
    </Page>
  ))
}

describe('scroll ownership', () => {
  let styleEl: HTMLStyleElement

  beforeEach(() => {
    styleEl = injectSurfaceStyles()
  })

  afterEach(() => {
    cleanup()
    styleEl.remove()
  })

  it('the only scroll containers are the scroller and the rail', () => {
    const { container } = renderPage({
      leading: <nav>Rail nav</nav>,
      children: (
        <>
          <PageSection id="s1" title="Section 1">
            <p>Content</p>
          </PageSection>
          <PageSection id="s2" title="Section 2">
            <p>Content</p>
          </PageSection>
        </>
      ),
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    // Allow exactly the scroller and the rail
    const disallowed = scrollContainers.filter(
      (s) =>
        !s.node.classList.contains('ui-page__scroll') &&
        !s.node.classList.contains('ui-page__rail'),
    )

    expect(disallowed).toEqual([])
  })

  it('still passes when there is no rail', () => {
    const { container } = renderPage({
      children: 'Simple content',
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    const disallowed = scrollContainers.filter(
      (s) =>
        !s.node.classList.contains('ui-page__scroll') &&
        !s.node.classList.contains('ui-page__rail'),
    )

    expect(disallowed).toEqual([])
  })

  it('catches a stray overflow:auto in the subtree', () => {
    const { container } = renderPage({
      children: (
        <>
          <PageSection title="Section">
            <div class="stray-scroll" style={{ 'overflow-y': 'auto', height: '100px' }}>
              Should not scroll
            </div>
          </PageSection>
        </>
      ),
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    // The stray element registers as a scroll container — it should be
    // caught by the invariant.
    const disallowed = scrollContainers.filter(
      (s) =>
        !s.node.classList.contains('ui-page__scroll') &&
        !s.node.classList.contains('ui-page__rail'),
    )

    expect(disallowed.length).toBeGreaterThan(0)
    expect(disallowed.some((s) => s.node.classList.contains('stray-scroll'))).toBe(true)
  })

  it('the rail scrolls independently (wide viewport)', () => {
    const { container } = renderPage({
      leading: <nav style={{ height: '500px' }}>Tall rail content</nav>,
      children: 'Content',
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    const rail = scrollContainers.find((s) => s.node.classList.contains('ui-page__rail'))
    expect(rail).toBeDefined()
    expect(rail!.overflowY).toBe('auto')
  })

  it('the scroller scrolls content', () => {
    const { container } = renderPage({
      children: <div style={{ height: '2000px' }}>Tall content</div>,
    })

    const scroller = container.querySelector('.ui-page__scroll')!
    const cs = getComputedStyle(scroller)
    expect(cs.overflowY).toBe('auto')
  })

  it('exposes scrollToElement handle', () => {
    let handle!: PageScrollerHandle
    renderPage({
      scrollerRef: (h) => {
        handle = h
      },
    })
    expect(handle).toBeDefined()
    expect(typeof handle.scrollToElement).toBe('function')
  })

  it('ui-page__body and .ui-page are not scroll containers', () => {
    const { container } = renderPage({
      leading: <nav>Rail</nav>,
      children: 'Content',
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    // The body and root must NOT appear as scroll containers.
    // (jsdom cannot reliably resolve overflow:hidden from <style>
    //  elements, so we check the invariant negatively instead of
    //  asserting getComputedStyle.overflowY === 'hidden'.)
    const bodyNames = scrollContainers
      .filter((s) => s.node.classList.contains('ui-page__body'))
      .map((s) => `${s.node.className} (oy=${s.overflowY})`)
    expect(bodyNames).toEqual([])

    const rootNames = scrollContainers
      .filter((s) => s.node.classList.contains('ui-page'))
      .map((s) => `${s.node.className} (oy=${s.overflowY})`)
    expect(rootNames).toEqual([])
  })

  it('the rail and scroller ARE scroll containers', () => {
    const { container } = renderPage({
      leading: <nav>Rail</nav>,
      children: <div style={{ height: '2000px' }}>Tall</div>,
    })

    const uiPage = container.querySelector<HTMLElement>('.ui-page')!
    const scrollContainers = findScrollContainers(uiPage)

    const railNodes = scrollContainers.filter((s) => s.node.classList.contains('ui-page__rail'))
    expect(railNodes.length).toBeGreaterThan(0)

    const scrollerNodes = scrollContainers.filter((s) =>
      s.node.classList.contains('ui-page__scroll'),
    )
    expect(scrollerNodes.length).toBeGreaterThan(0)
  })
})
