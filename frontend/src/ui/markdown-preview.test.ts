// @vitest-environment jsdom
//
// The markdown preview, read the way an ATTACKER writes it (nocx-qfdy7).
//
// A skill can be installed from a URL, so the bytes this renders are somebody
// else's, and this file is where that is proved rather than assumed. The
// posture is two layers and both are tested: the renderer is INERT BY
// CONSTRUCTION — links and images never become elements that can navigate or
// fetch — and DOMPurify is the second gate behind it. A test that only
// checked for `<script>` would pass on a renderer that still emitted
// `<img src="https://attacker/beacon">`, which is the leak that costs a
// person their privacy without any script at all.
import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown-preview'

/** What a browser SHOWS for an element: its text with runs of whitespace
 *  collapsed, the way HTML lays it out. jsdom does no layout, so a newline
 *  the author wrote survives in `textContent` while being invisible on
 *  screen — and asserting on the raw string would fail a paragraph that is
 *  correct. */
const flowed = (el: Element): string => (el.textContent ?? '').replace(/\s+/g, ' ').trim()

function draw(text: string): HTMLElement {
  const host = document.createElement('div')
  renderMarkdown(host, text)
  return host
}

describe('markdown preview — the document', () => {
  it('joins a paragraph the author hard-wrapped, which is the whole reason this exists', () => {
    // A SKILL.md is prose wrapped at 80 columns. A renderer that kept the
    // author's line breaks broke every paragraph at their editor's column,
    // at any pane width — the defect the owner reported twice.
    const host = draw(
      'A skill is a procedure this machine follows,\nwritten once so nobody\nhas to say it again.\n',
    )
    const paragraphs = host.querySelectorAll('p')
    // ONE paragraph is the assertion. The author's newlines survive INSIDE
    // it as whitespace, which is what makes it reflow: HTML collapses them,
    // so the text finds the pane's width rather than the author's column.
    // `flowed` is that collapsing, done here because jsdom does no layout.
    expect(paragraphs).toHaveLength(1)
    expect(flowed(paragraphs[0])).toBe(
      'A skill is a procedure this machine follows, written once so nobody has to say it again.',
    )
  })

  it('separates paragraphs at a blank line, and only there', () => {
    const host = draw('First one.\nstill first.\n\nSecond one.\n')
    expect(Array.from(host.querySelectorAll('p')).map(flowed)).toEqual([
      'First one. still first.',
      'Second one.',
    ])
  })

  it('draws headings, lists and fences as themselves', () => {
    const host = draw('# Title\n\n## Section\n\n- one\n- two\n\n```sh\necho hi\n```\n')
    expect(host.querySelector('h1')?.textContent).toBe('Title')
    expect(host.querySelector('h2')?.textContent).toBe('Section')
    expect(host.querySelectorAll('ul > li')).toHaveLength(2)
    expect(host.querySelector('pre')?.textContent).toContain('echo hi')
    // No markers left on screen — that is what "rendered" means.
    expect(host.textContent).not.toContain('##')
    expect(host.textContent).not.toContain('```')
  })

  it('shows a file’s frontmatter as its header rather than as its first paragraph', () => {
    const host = draw('---\nname: deploy\ndescription: How we ship.\n---\n\n# Deploy\n')
    const front = host.querySelector('.ui-md-front')
    expect(front).not.toBeNull()
    expect(front?.textContent).toContain('name')
    expect(front?.textContent).toContain('deploy')
    // The document's own title is still the first heading, and the metadata
    // is not prose above it.
    expect(host.querySelector('h1')?.textContent).toBe('Deploy')
    expect(front?.contains(host.querySelector('h1') as Node)).toBe(false)
  })
})

describe('markdown preview — somebody else’s bytes', () => {
  const hostile: readonly [string, string][] = [
    ['a script element', '<script>alert(1)</script>\n'],
    ['an event handler', '<img src=x onerror="alert(1)">\n'],
    ['an iframe', '<iframe src="https://attacker.invalid"></iframe>\n'],
    ['an object', '<object data="x"></object>\n'],
    ['a style block', '<style>body{display:none}</style>\n'],
    ['a javascript: link', '[click](javascript:alert(1))\n'],
    ['an encoded javascript: link', '[click](java&#115;cript:alert(1))\n'],
    ['a data: link', '[click](data:text/html;base64,PHNjcmlwdD4=)\n'],
    ['a file: link', '[home](file:///etc/passwd)\n'],
    ['a remote image', '![beacon](https://attacker.invalid/b.png)\n'],
    ['a relative image', '![beacon](./secrets.png)\n'],
    ['an autolink', '<https://attacker.invalid/beacon>\n'],
    ['a bare url', 'Visit https://attacker.invalid/beacon now.\n'],
    ['a hostile fence info string', '```sh" onload="alert(1)\necho hi\n```\n'],
    ['a form', '<form action="https://attacker.invalid"><input name="p"></form>\n'],
    ['malformed nesting', '<div><span><script>alert(1)\n'],
  ]

  for (const [what, text] of hostile) {
    it(`renders ${what} without anything active or addressable`, () => {
      const host = draw(text)
      // Nothing that runs.
      expect(host.querySelector('script, iframe, object, embed, form, style, link')).toBeNull()
      // Nothing that navigates or fetches. An anchor is not "sanitised" here,
      // it is never built: the renderer emits the text and the URL as inert
      // characters, so there is no href to police.
      expect(host.querySelector('a, img')).toBeNull()
      // And no attribute anywhere carries a URI or an event.
      for (const el of host.querySelectorAll('*')) {
        for (const attr of el.attributes) {
          expect(attr.name.toLowerCase().startsWith('on')).toBe(false)
          expect(['href', 'src', 'srcset', 'action', 'formaction', 'data', 'style']).not.toContain(
            attr.name.toLowerCase(),
          )
        }
      }
    })
  }

  it('still shows a link’s words and its target, inert, so a reader can judge it', () => {
    // Dropping the URL would hide what the skill is pointing at from the very
    // person deciding whether to trust the skill.
    const host = draw('See [the runbook](https://example.invalid/runbook).\n')
    expect(host.querySelector('a')).toBeNull()
    expect(host.textContent).toContain('the runbook')
    expect(host.textContent).toContain('https://example.invalid/runbook')
  })

  it('escapes text rather than trusting it, in a fence as much as in prose', () => {
    const host = draw('```\n<script>alert(1)</script>\n```\n')
    expect(host.querySelector('script')).toBeNull()
    expect(host.querySelector('pre')?.textContent).toContain('<script>alert(1)</script>')
  })

  it('names a fence’s language only from a closed set, never from the author’s string', () => {
    const known = draw('```sh\necho hi\n```\n').querySelector('pre code')
    const unknown = draw('```wat-is-this\nx\n```\n').querySelector('pre code')
    expect(known?.className).toBe('ui-md-code-block language-shell')
    // An unrecognised info string resolves to plain — it never reaches a
    // class, so there is nothing to break out of.
    expect(unknown?.className).toBe('ui-md-code-block')
  })

  it('replaces the whole host, so a second render leaves nothing of the first', () => {
    const host = document.createElement('div')
    renderMarkdown(host, '# First\n')
    renderMarkdown(host, '# Second\n')
    expect(host.querySelectorAll('h1')).toHaveLength(1)
    expect(host.querySelector('h1')?.textContent).toBe('Second')
  })
})
