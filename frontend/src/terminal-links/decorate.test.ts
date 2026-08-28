// @vitest-environment jsdom
//
// The decorator's contract, stated as what a person can then do: the link is
// in the DOM as one element they can click, the row still reads exactly as
// the program printed it, and colour that ran through the link survives.
import { describe, expect, it } from 'vitest'
import { decorateLinks, LINK_CLASS, linkTargetOf } from './decorate'

function line(html: string): HTMLElement {
  const el = document.createElement('div')
  el.innerHTML = `<span class="term-line">${html}</span>`
  return el
}

function anchors(root: Element): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(`.${LINK_CLASS}`)]
}

describe('decorateLinks', () => {
  it('wraps a path reference and carries its target', () => {
    const root = line('see docs/architecture.md:101 for more')
    decorateLinks(root)
    const [a] = anchors(root)
    expect(a.textContent).toBe('docs/architecture.md:101')
    expect(linkTargetOf(a)).toEqual({ kind: 'path', path: 'docs/architecture.md', line: 101 })
  })

  it('leaves the row reading exactly as the program printed it', () => {
    const text = 'see docs/architecture.md:101 for more'
    const root = line(text)
    decorateLinks(root)
    expect(root.textContent).toBe(text)
  })

  it('wraps a link that straddles two colour runs', () => {
    // The serializer emits one span per colour run, so a path printed half
    // bold arrives as two elements. A node-local walk would truncate it at
    // the markup — the same defect word-selection was written to avoid.
    const root = line('<span style="color:#f00">docs/arch</span><span>itecture.md:101</span>')
    decorateLinks(root)
    const [a] = anchors(root)
    expect(a.textContent).toBe('docs/architecture.md:101')
    // The colour run inside the link is preserved, not flattened away.
    expect(a.querySelector('span[style]')?.textContent).toBe('docs/arch')
  })

  it('wraps every link on a row', () => {
    const root = line('a/b.ts:1 and https://example.com/x and c/d.go')
    decorateLinks(root)
    expect(anchors(root).map((a) => a.textContent)).toEqual([
      'a/b.ts:1',
      'https://example.com/x',
      'c/d.go',
    ])
  })

  it('carries a url target', () => {
    const root = line('open https://example.com/x')
    decorateLinks(root)
    expect(linkTargetOf(anchors(root)[0])).toEqual({
      kind: 'url',
      url: 'https://example.com/x',
    })
  })

  it('decorates every row of a block, not just the first', () => {
    const root = document.createElement('div')
    root.innerHTML =
      '<span class="term-line">a/b.ts:1</span><span class="term-line">c/d.go:2</span>'
    decorateLinks(root)
    expect(anchors(root)).toHaveLength(2)
  })

  it('adds nothing when the row holds no link', () => {
    const root = line('total 12/20 checks passed, e.g. v0.3.0')
    decorateLinks(root)
    expect(anchors(root)).toHaveLength(0)
    expect(root.innerHTML).toBe(
      '<span class="term-line">total 12/20 checks passed, e.g. v0.3.0</span>',
    )
  })

  it('is idempotent — a second pass neither double-wraps nor loses text', () => {
    const text = 'a/b.ts:1 and https://example.com/x'
    const root = line(text)
    decorateLinks(root)
    const after = root.innerHTML
    decorateLinks(root)
    expect(root.innerHTML).toBe(after)
    expect(root.textContent).toBe(text)
    expect(anchors(root)).toHaveLength(2)
  })

  it('reads no target from an element that is not a link', () => {
    const root = line('plain text')
    expect(linkTargetOf(root.firstElementChild as HTMLElement)).toBeNull()
  })

  it('survives a row with no text nodes', () => {
    const root = line('')
    expect(() => decorateLinks(root)).not.toThrow()
    expect(anchors(root)).toHaveLength(0)
  })
})
