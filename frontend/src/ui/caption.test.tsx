// @vitest-environment jsdom
/**
 * Caption — the kit's group-caption register (nocx-dgsp).
 *
 * Identity is asserted the kit's way: the stable base class on the element
 * that carries the appearance, plus the typed data-* variance. The register
 * itself (uppercase, letter-spaced, semibold, small, muted) lives in
 * caption.css; this test pins the identity and the size contract — the
 * default stays the caption's own size, `size="context"` tracks the
 * surrounding column at every column size (the wide md and the narrow sm of
 * the rail's column).
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { Caption } from './caption'

afterEach(() => cleanup())

// The CSS import is mocked by vitest, so the shipped caption rules are
// injected directly (the fixture pattern scroll-ownership.test.tsx uses for
// the Page rules) — read from the real file, so the test cannot go green
// against a selector that drifted from what ships.

const CAPTION_CSS = readFileSync('src/styles/components/caption.css', 'utf8')

function injectCaptionStyles(): HTMLStyleElement {
  const style = document.createElement('style')
  style.id = 'caption-styles-test'
  style.textContent = CAPTION_CSS
  document.head.appendChild(style)
  return style
}

describe('Caption', () => {
  it('renders the kit caption identity on the text element', () => {
    render(() => <Caption>Group</Caption>)
    const el = document.querySelector('.ui-caption') as HTMLElement
    expect(el).not.toBeNull()
    expect(el.tagName).toBe('SPAN')
    expect(el.textContent).toBe('Group')
  })

  it('default register is unchanged for existing callers — no size variance', () => {
    render(() => <Caption>Group</Caption>)
    const el = document.querySelector('.ui-caption') as HTMLElement
    // The default emits no data-size, so the register's own size
    // (--font-size-2xs) applies exactly as before the variance existed.
    expect(el.hasAttribute('data-size')).toBe(false)
  })

  it('size="context" emits the typed variance', () => {
    render(() => <Caption size="context">Group</Caption>)
    const el = document.querySelector('.ui-caption') as HTMLElement
    expect(el.getAttribute('data-size')).toBe('context')
  })

  it('size="context" tracks its column rather than a fixed rem, at any column size', () => {
    const styleEl = injectCaptionStyles()
    try {
      // The rail's column is md (wide) and sm (narrow) — a surface editorial
      // the caption must follow, not undercut. A pinned rem would be right
      // for at most one of the two.
      for (const columnSize of ['16px', '14px']) {
        const { container } = render(() => (
          <div style={{ 'font-size': columnSize }}>
            <Caption size="context">Group</Caption>
          </div>
        ))
        const caption = container.querySelector('.ui-caption') as HTMLElement
        expect(getComputedStyle(caption).fontSize).toBe(columnSize)
        cleanup()
      }
    } finally {
      styleEl.remove()
    }
  })
})
