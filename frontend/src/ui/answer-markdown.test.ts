// @vitest-environment jsdom
//
// AnswerMarkdown (ui/answer-markdown.ts) — the kit contract for ONE line of
// a model's answer, pinned: the structure a model actually emits is painted,
// the model's bytes are DATA and never markup, and a line with no structure
// is left exactly as it was before any of this existed.
import { describe, it, expect } from 'vitest'
import { paintAnswerLine } from './answer-markdown'
import { readFileSync } from 'node:fs'

function paint(text: string): HTMLElement {
  const row = document.createElement('span')
  row.className = 'term-line'
  paintAnswerLine(row, text)
  return row
}

describe('AnswerMarkdown — structure', () => {
  it('paints a heading as its level, with the marker gone', () => {
    const h1 = paint('# What went wrong')
    expect(h1.dataset.md).toBe('h1')
    expect(h1.textContent).toBe('What went wrong')
    expect(paint('### Three').dataset.md).toBe('h3')
    expect(paint('###### Six').dataset.md).toBe('h6')
    // Seven is not a heading in any markdown, and a hash with no space is a
    // comment or a colour, not a title.
    expect(paint('####### Seven').dataset.md).toBeUndefined()
    expect(paint('#nocx').dataset.md).toBeUndefined()
  })

  it('paints a list item with a bullet and its nesting depth', () => {
    const flat = paint('- first')
    expect(flat.dataset.md).toBe('li')
    expect(flat.dataset.mdDepth).toBe('0')
    expect(flat.querySelector('.ui-md-marker')?.textContent).toBe('•')
    expect(flat.textContent).toContain('first')
    expect(paint('    - nested').dataset.mdDepth).toBe('2')
    // An ordered list keeps the number the model chose — renumbering an
    // answer would be inventing a fact.
    expect(paint('3. third').querySelector('.ui-md-marker')?.textContent).toBe('3.')
  })

  it('paints a quote, and leaves a bare dash alone', () => {
    expect(paint('> so it said').dataset.md).toBe('quote')
    // A dash with no text after it is a rule or a stray, not a list item.
    expect(paint('-').dataset.md).toBeUndefined()
    expect(paint('---').dataset.md).toBeUndefined()
  })

  it('leaves an ordinary line exactly as it was — same text, no markup, no attribute', () => {
    const row = paint('the command exited with 1')
    expect(row.dataset.md).toBeUndefined()
    expect(row.innerHTML).toBe('the command exited with 1')
    expect(row.children.length).toBe(0)
  })
})

describe('AnswerMarkdown — inline', () => {
  it('paints inline code, bold and emphasis with real elements', () => {
    const row = paint('run `ls -la` in **the repo** or *here*')
    expect(row.querySelector('code.ui-md-code')?.textContent).toBe('ls -la')
    expect(row.querySelector('strong.ui-md-strong')?.textContent).toBe('the repo')
    expect(row.querySelector('em.ui-md-em')?.textContent).toBe('here')
    // The markers themselves are gone from the text.
    expect(row.textContent).toBe('run ls -la in the repo or here')
  })

  it('does not emphasise an underscore — a shell is full of them', () => {
    const row = paint('set _MY_VAR_ and read some_file_name')
    expect(row.querySelector('em')).toBeNull()
    expect(row.querySelector('strong')).toBeNull()
    expect(row.textContent).toBe('set _MY_VAR_ and read some_file_name')
  })

  it('does not parse markup inside inline code', () => {
    const row = paint('`**not bold**`')
    expect(row.querySelector('strong')).toBeNull()
    expect(row.querySelector('code.ui-md-code')?.textContent).toBe('**not bold**')
  })

  it('renders inline code inside a bold span — and code still wins over markup, in the same test', () => {
    const row = paint('**`repos`** (8.7G)')
    const code = row.querySelector('code.ui-md-code')
    expect(code).not.toBeNull()
    expect(code!.textContent).toBe('repos')
    // The chip sits INSIDE the strong: a bold span's contents are markup
    // and go through the inline pass again.
    expect(code!.parentElement?.className).toBe('ui-md-strong')
    expect(row.textContent).toBe('repos (8.7G)')
    expect(row.textContent).not.toContain('`')

    // The reverse, held by the same test: a model explaining markdown
    // writes `**not bold**` and means the asterisks — code wins the
    // alternation and its contents stay text.
    const reverse = paint('`**not bold**`')
    expect(reverse.querySelector('strong')).toBeNull()
    expect(reverse.querySelector('code.ui-md-code')?.textContent).toBe('**not bold**')
  })

  it('keeps nesting bounded — adversarial marker runs terminate and stay data', () => {
    // 500 asterisks: the inline pattern needs a non-marker character
    // between markers, so nothing matches and the line is plain text.
    const stars = paint('*'.repeat(500))
    expect(stars.querySelector('strong')).toBeNull()
    expect(stars.querySelector('em')).toBeNull()
    expect(stars.textContent).toBe('*'.repeat(500))
    // Deeply alternating markers: every nested span strips its own
    // markers, so each level shrinks the input and the recursion ends.
    const row = paint('**`a`** *`b`* `**not bold**`')
    const chips = row.querySelectorAll('code.ui-md-code')
    expect(chips.length).toBe(3)
    expect(chips[0].textContent).toBe('a')
    expect(chips[1].textContent).toBe('b')
    expect(chips[2].textContent).toBe('**not bold**')
  })
})

describe('AnswerMarkdown — the marker sits inside the block (nocx-gxr9w.1)', () => {
  it('builds the hanging indent from the marker’s own box, never from text-indent', () => {
    const css = readFileSync('src/styles/components/answer-markdown.css', 'utf8')
    // text-indent is inherited, and .ui-md-marker is inline-block — a
    // block container — so a text-indent on the row applies a SECOND time
    // to the marker's first line and drags the glyph out through
    // .cmd-children's border-left into the gutter. Neither the row nor
    // the marker may declare it: the row's would inherit into the marker,
    // and the marker's would re-shift the glyph it positions by margin.
    const liBlock = css.match(/\.term-line\[data-md='li'\]\s*\{([^}]*)\}/)?.[1]
    expect(liBlock).toBeDefined()
    expect(liBlock).not.toMatch(/text-indent/)
    const markerBlock = css.match(/\.ui-md-marker\s*\{([^}]*)\}/)?.[1]
    expect(markerBlock).toBeDefined()
    expect(markerBlock).not.toMatch(/text-indent/)
    // The row keeps the full indent (marker column + nesting), so wrapped
    // continuation lines still start under the item's text.
    expect(liBlock).toMatch(/padding-left:\s*calc\(1\.2em \+ var\(--md-depth, 0\) \* 1\.2em\)/)
    // The marker pulls itself back into that padding with its own negative
    // margin: the glyph lands inside the block, the text starts at the
    // content edge, and the gap is the marker column, not the padding.
    expect(markerBlock).toMatch(/margin-left:\s*-1\.2em/)
    expect(markerBlock).toMatch(/width:\s*1\.2em/)
  })

  it('puts the marker first and the text after it, at depths 0–3, bullet and ordered', () => {
    for (const depth of [0, 1, 2, 3]) {
      const row = paint(`${'  '.repeat(depth)}- item ${depth}`)
      expect(row.dataset.md).toBe('li')
      expect(row.dataset.mdDepth).toBe(String(depth))
      // The marker is the row's first child, so its negative margin pulls
      // it into the padding and the item text starts at the content edge.
      expect(row.firstElementChild?.className).toBe('ui-md-marker')
      expect(row.firstElementChild?.textContent).toBe('•')
      expect(row.textContent).toContain(`item ${depth}`)

      const ordered = paint(`${'  '.repeat(depth)}7. seven ${depth}`)
      expect(ordered.dataset.md).toBe('li')
      expect(ordered.dataset.mdDepth).toBe(String(depth))
      expect(ordered.firstElementChild?.className).toBe('ui-md-marker')
      expect(ordered.firstElementChild?.textContent).toBe('7.')
      expect(ordered.textContent).toContain(`seven ${depth}`)
    }
  })
})

describe('AnswerMarkdown — the model’s text is DATA, never markup', () => {
  it('escapes a tag the model wrote instead of building one', () => {
    const row = paint('use <script>alert(1)</script> or <img src=x onerror=y>')
    expect(row.querySelector('script')).toBeNull()
    expect(row.querySelector('img')).toBeNull()
    expect(row.textContent).toBe('use <script>alert(1)</script> or <img src=x onerror=y>')
  })

  it('never builds a link, so a javascript: href stays text', () => {
    const row = paint('read [the docs](javascript:alert(1)) now')
    expect(row.querySelector('a')).toBeNull()
    expect(row.innerHTML).not.toContain('href')
    expect(row.innerHTML).not.toContain('<a')
    // Verbatim: the URL is shown, it is simply not navigable.
    expect(row.textContent).toBe('read [the docs](javascript:alert(1)) now')
  })

  it('escapes inside every painted piece — a heading, a bullet and a code span', () => {
    expect(paint('# <b>hi</b>').textContent).toBe('<b>hi</b>')
    expect(paint('# <b>hi</b>').querySelector('b')).toBeNull()
    const li = paint('- <i>x</i>')
    expect(li.querySelector('i')).toBeNull()
    const code = paint('`<img src=x onerror=alert(1)>`')
    expect(code.querySelector('img')).toBeNull()
    expect(code.querySelector('code')?.textContent).toBe('<img src=x onerror=alert(1)>')
  })

  it('escapes an ampersand so it is not read as an entity', () => {
    const row = paint('**a & b**')
    expect(row.textContent).toBe('a & b')
    expect(row.innerHTML).toContain('&amp;')
  })
})
