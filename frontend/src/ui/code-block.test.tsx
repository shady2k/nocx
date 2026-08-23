// @vitest-environment jsdom
//
// CodeBlock — the identity it renders, and its one piece of variance.
//
// The variance is asserted BOTH as the attribute the component renders and as
// the declaration `code-block.css` keys off it, because either half alone is
// green while the pair is broken: an attribute nothing styles changes nothing
// on screen, and a rule nothing sets the attribute for is unreachable CSS.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { CodeBlock } from './code-block'

afterEach(() => cleanup())

function block(wrap?: boolean): HTMLElement {
  const { container } = render(() => (
    <CodeBlock ariaLabel="Machine output" wrap={wrap}>
      {'a very long line of machine output'}
    </CodeBlock>
  ))
  const el = container.querySelector<HTMLElement>('.ui-code-block')
  if (!el) throw new Error('CodeBlock rendered no ui-code-block')
  return el
}

const CSS = readFileSync(resolve(process.cwd(), 'src/styles/components/code-block.css'), 'utf8')

describe('CodeBlock', () => {
  it('wraps by default, which is what a list of short lines wants', () => {
    expect(block().dataset.wrap).toBeUndefined()
    expect(block(true).dataset.wrap).toBeUndefined()
  })

  // A BLOCK HOLDING BYTES SAYS SO, and gets the sideways scroll the editors
  // give the same octets (nocx-kdawd).
  it('a block that must not wrap says so on the element that carries the look', () => {
    expect(block(false).dataset.wrap).toBe('false')
  })

  it('the stylesheet answers that attribute, so the variant is not decoration', () => {
    expect(CSS).toContain("[data-wrap='false']")
    // `pre`, not `pre-wrap`: the line stays one line and the block's own
    // scroll box — declared on `.ui-code-block` above it — is what moves.
    expect(CSS).toMatch(/\[data-wrap='false'\][^}]*white-space:\s*pre;/)
  })

  it('keeps its identity and its accessible name in both forms', () => {
    for (const el of [block(), block(false)]) {
      expect(el.tagName).toBe('PRE')
      expect(el.getAttribute('aria-label')).toBe('Machine output')
      // A scrollable region a mouse wheel alone can move is unreachable by
      // keyboard, and the variant that scrolls sideways needs it most.
      expect(el.tabIndex).toBe(0)
    }
  })
})
