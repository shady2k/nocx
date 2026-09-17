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
    // the markup — the same defect word-selection was written to avoid; the
    // link must still come out whole.
    //
    // WHOLE, NOT SINGLE, AND THAT IS A DELIBERATE CHANGE (nocx-ec18). This
    // used to read the first anchor's textContent and expect the entire
    // link, which asserted one anchor per link. It cannot: one Range over
    // the whole link is extracted and re-parented, and when an end of it
    // falls inside a `.term-cell` — the fixed-width box that holds a frozen
    // row on its columns — the box is split, leaving an empty original and a
    // clone inside the `<a>`. The decorator wraps each text node's own slice
    // now, so a straddling link is several anchors that together read as the
    // link, each carrying the target. The accessibility cost (N links where
    // a person sees one) is named in decorate.ts and filed, not paid here.
    const root = line('<span style="color:#f00">docs/arch</span><span>itecture.md:101</span>')
    decorateLinks(root)
    const parts = anchors(root)
    expect(parts.map((a) => a.textContent).join('')).toBe('docs/architecture.md:101')
    // Every part opens the same thing — surface.ts reads the target off
    // whatever element the click landed on, so a part without one is half a
    // link that opens nothing.
    for (const part of parts) {
      expect(linkTargetOf(part)).toEqual({ kind: 'path', path: 'docs/architecture.md', line: 101 })
    }
    // The colour run around the link is preserved, not flattened away: the
    // anchor went INSIDE the run rather than the run inside the anchor.
    expect(root.querySelector('span[style]')?.textContent).toBe('docs/arch')
    expect(root.querySelector('span[style]')?.firstElementChild?.className).toBe(LINK_CLASS)
  })

  it('leaves a cell box whole when a link ends inside it', () => {
    // THE CASE THE WHOLE CHANGE EXISTS FOR (nocx-ec18). A frozen row holds
    // itself on the grid with `.term-cell` boxes of a known width; a link
    // whose last character is one of them used to have that box extracted
    // out from under it, and a row with an emptied box is a row off its
    // columns. The box must still be a box, still hold its own text, and the
    // anchor must be inside it.
    // A URL, because the path grammar stops at the first character outside
    // its token set and would end BEFORE the box — the very case this test
    // has to reach. A url runs to whitespace, so it ends inside it.
    const root = line(
      'open https://example.com/x<span class="term-cell" data-cols="1">\u{1F5D1}</span>',
    )
    decorateLinks(root)
    const box = root.querySelector('.term-cell')!
    expect(box.textContent).toBe('\u{1F5D1}')
    expect(box.firstElementChild?.className).toBe(LINK_CLASS)
    expect(root.querySelectorAll('.term-cell')).toHaveLength(1)
    expect(
      anchors(root)
        .map((a) => a.textContent)
        .join(''),
    ).toBe('https://example.com/x\u{1F5D1}')
    expect(root.textContent).toBe('open https://example.com/x\u{1F5D1}')
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
