// @vitest-environment jsdom
import { describe, expect, it, afterEach, vi } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { PageScroller, type PageScrollerProps, type PageScrollerHandle } from './page-scroller'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageScrollerProps>) {
  const props: PageScrollerProps = {
    children: 'Scroll content',
    ...overrides,
  }
  return render(() => <PageScroller {...props} />)
}

describe('PageScroller', () => {
  it('renders children', () => {
    subject()
    const el = document.querySelector('.ui-page__scroll')
    expect(el?.textContent).toBe('Scroll content')
  })

  it('applies .ui-page__scroll class', () => {
    subject()
    expect(document.querySelector('.ui-page__scroll')).not.toBeNull()
  })

  // The scroller fills the window so its scrollbar stays on the window edge;
  // the measure is what the content is actually poured into. Without the
  // separation a maximised window puts a Field's label and its control a
  // thousand pixels apart and the row stops reading as one row.
  it('pours children into a measure inside the scroller, not into the scroller itself', () => {
    subject()
    const measure = document.querySelector('.ui-page__scroll > .ui-page__measure')
    expect(measure).not.toBeNull()
    expect(measure!.textContent).toBe('Scroll content')
  })

  it('calls functional handle with a PageScrollerHandle', () => {
    const fn = vi.fn()
    subject({ handle: fn })
    expect(fn).toHaveBeenCalledTimes(1)
    const handle = fn.mock.calls[0][0] as PageScrollerHandle
    expect(handle).toHaveProperty('scrollToElement')
    expect(typeof handle.scrollToElement).toBe('function')
  })

  it('copies handle onto object handle', () => {
    const h: PageScrollerHandle = {} as PageScrollerHandle
    subject({ handle: h })
    expect(typeof h.scrollToElement).toBe('function')
  })

  it('scrollToElement calls scrollTo on the scroller element, not scrollIntoView on the target', () => {
    const h: PageScrollerHandle = {} as PageScrollerHandle
    // Using setTimeout to flush Solid's effect so scrollEl is bound
    // before we call scrollToElement
    render(() => (
      <PageScroller handle={h}>
        <div>content</div>
      </PageScroller>
    ))
    const scrollerEl = document.querySelector('.ui-page__scroll')!

    const scrollTo = vi.fn()
    scrollerEl.scrollTo = scrollTo

    const target = document.createElement('div')
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView

    // Append target inside the scroller so getBoundingClientRect works
    scrollerEl.appendChild(target)

    h.scrollToElement(target)

    // Must use scrollTo (scroller-owned position), NOT scrollIntoView
    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(scrollTo).toHaveBeenCalledTimes(1)
    expect(scrollTo.mock.calls[0][0]).toHaveProperty('top')
    expect(scrollTo.mock.calls[0][0]).toHaveProperty('behavior', 'smooth')
  })
})
