// ═══════════════════════════════════════════════════════════════════════════
// MarkdownPreview — a markdown DOCUMENT, rendered (nocx-qfdy7).
//
// WHY A LIBRARY, AFTER TWO HAND-ROLLED ATTEMPTS FAILED. The first showed the
// bytes; the second reused `scrollback/answer-body.ts`, which paints ONE
// COMPLETED LINE at a time and says so in its own header — right for a
// streamed answer, which arrives unwrapped, and wrong for a file, which is
// prose the author hard-wrapped at 80 columns. Every paragraph broke at
// their editor's column, at any pane width. Extending that into real
// markdown means implementing paragraphs, nested lists, escapes, fences,
// tables, an HTML policy and a URI policy — badly, and one defect at a time.
//
// The survey settled it: VS Code renders its markdown preview with
// markdown-it in a separate webview (Monaco itself only highlights markdown
// SOURCE); termic — the closest sibling, a terminal on CM6 — uses markdown-it
// plus DOMPurify; orca uses remark/rehype. `react-markdown` and `streamdown`
// are React-only and this is SolidJS.
//
// `breaks: false` IS THE LOAD-BEARING SETTING. It keeps a single newline a
// markdown SOFT break, which is what makes a hard-wrapped paragraph one
// paragraph. Turning it on would reintroduce the exact defect this module
// was created to end.
//
// ── THE BYTES ARE SOMEBODY ELSE'S ─────────────────────────────────────────
//
// A skill can be installed from a URL, and this is a Wails WKWebView with IPC
// reach. So the posture is two layers, and the FIRST is the important one:
//
// INERT BY CONSTRUCTION. A link never becomes an `<a>` and an image never
// becomes an `<img>`. The renderer emits their text and their target as
// characters. There is therefore no `href` to police, no `src` to fetch, and
// no unprompted GET to whatever host the skill's author named — which is a
// real leak with no script in it at all, and the one a sanitiser alone would
// have let through if it kept `img[src]`. A reader still sees the words AND
// the URL, because hiding the target from the person deciding whether to
// trust the skill would be the wrong trade.
//
// DOMPURIFY IS THE SECOND GATE, not the policy. `html: false` means
// markdown-it never parses author HTML at all; the sanitiser is what catches
// a bug in that assumption or in this file. Deny-by-default: the allowlist
// names the semantic tags this renderer itself emits, and everything else —
// script, iframe, object, form, style, link — is absent by not being listed.
// No attribute carrying a URI or an event survives it, and `class` is
// permitted only because the classes below are written HERE, never
// interpolated from the document.
//
// A FENCE'S LANGUAGE COMES FROM A CLOSED SET. The info string is the
// author's, so it is looked up, never used. ```sh" onload="alert(1)`
// resolves to nothing and the block renders plain.
// ═══════════════════════════════════════════════════════════════════════════

import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

/** The languages a fence may name, mapped to the class this file writes for
 *  it. A closed set: anything absent renders plain, so the info string never
 *  reaches the DOM. The names are the ones people actually write in a
 *  procedure, and the values are OURS. */
const FENCE_LANGUAGE: Readonly<Record<string, string>> = {
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
  shell: 'shell',
  console: 'shell',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  py: 'python',
  python: 'python',
  md: 'markdown',
  markdown: 'markdown',
}

/** Everything this renderer emits, and nothing else. Deny-by-default: a tag
 *  absent from this list cannot appear, whatever produced it. */
const ALLOWED_TAGS = [
  'p',
  'br',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'ul',
  'ol',
  'li',
  'blockquote',
  'pre',
  'code',
  'em',
  'strong',
  'del',
  'hr',
  'table',
  'thead',
  'tbody',
  'tr',
  'th',
  'td',
  'span',
  'div',
]

/** `class` because every class here is written by this file, and `start`
 *  because an ordered list that begins at 3 must say 3. Nothing that carries
 *  a URI, an event handler, a style or an author-chosen id. */
const ALLOWED_ATTR = ['class', 'start']

const md = new MarkdownIt({
  // markdown-it never parses author HTML. DOMPurify below is the second
  // gate, not the first.
  html: false,
  // A bare URL stays text. Turning this on would manufacture links the
  // author did not even write.
  linkify: false,
  typographer: false,
  // See the header: this is what makes a hard-wrapped paragraph one
  // paragraph.
  breaks: false,
})

/** A link, rendered inert: its words, then its target, both as text.
 *
 *  The target is kept deliberately. A person auditing a skill needs to see
 *  where it points, and rendering only the words would hide exactly the
 *  thing they opened the file to judge — "see the runbook" says nothing
 *  about which host it names.
 *
 *  `link_open` emits nothing; `link_close` writes the target, because by
 *  then markdown-it has already emitted the link's own text. The href is
 *  found by walking back to the matching `link_open` — the rules are
 *  stateless functions, so the token stream is the only place to read it,
 *  and nesting is counted rather than assumed since a link's text may
 *  contain no link but the stream may hold several in a row. */
md.renderer.rules.link_open = () => ''
md.renderer.rules.link_close = (tokens, idx) => {
  let depth = 0
  for (let i = idx - 1; i >= 0; i--) {
    if (tokens[i].type === 'link_close') depth++
    else if (tokens[i].type === 'link_open') {
      if (depth === 0) {
        const href = tokens[i].attrGet('href') ?? ''
        return href === ''
          ? ''
          : `<span class="ui-md-inert">${md.utils.escapeHtml(` (${href})`)}</span>`
      }
      depth--
    }
  }
  return ''
}

/** An image never becomes an element, so the document can start no request.
 *  Its alt text and its source are shown as text for the same reason a
 *  link's target is. */
md.renderer.rules.image = (tokens, idx) => {
  const token = tokens[idx]
  const alt = token.content || ''
  const src = token.attrGet('src') ?? ''
  return `<span class="ui-md-inert">${md.utils.escapeHtml(`[image: ${alt}${src ? ` — ${src}` : ''}]`)}</span>`
}

md.renderer.rules.fence = (tokens, idx) => {
  const token = tokens[idx]
  const info = (token.info || '').trim().split(/\s+/)[0].toLowerCase()
  const language = Object.prototype.hasOwnProperty.call(FENCE_LANGUAGE, info)
    ? FENCE_LANGUAGE[info]
    : ''
  const cls = language ? `ui-md-code-block language-${language}` : 'ui-md-code-block'
  return `<pre class="ui-md-pre"><code class="${cls}">${md.utils.escapeHtml(token.content)}</code></pre>`
}

md.renderer.rules.code_block = (tokens, idx) =>
  `<pre class="ui-md-pre"><code class="ui-md-code-block">${md.utils.escapeHtml(tokens[idx].content)}</code></pre>`

/**
 * The lines of a `---`-delimited frontmatter block at byte 0, and where the
 * body begins. Absent when the file does not open with one: a `---` after
 * any other line is a thematic break and markdown-it draws it as one.
 *
 * It is handled HERE rather than left to the renderer because markdown-it
 * would read it as a rule, a paragraph and a setext underline — three
 * wrong answers — and because the block is the file's ENVELOPE. Rendering
 * `name: deploy` as the document's opening sentence, above its own title,
 * is what the owner saw. Nothing is hidden: it is drawn, set apart.
 */
function splitFrontmatter(text: string): { head: readonly string[]; body: string } {
  const lines = text.split('\n')
  if (lines[0]?.trim() !== '---') return { head: [], body: text }
  for (let i = 1; i < lines.length; i++) {
    if (lines[i].trim() === '---') {
      return { head: lines.slice(1, i), body: lines.slice(i + 1).join('\n') }
    }
  }
  return { head: [], body: text }
}

/** The frontmatter as named facts, built element by element with
 *  `textContent` — no html, so nothing in it can be markup. A line carrying
 *  no `key: value` is shown as it stands: this is a reader, and a line it
 *  cannot parse is still a line in the file. */
function frontmatterElement(head: readonly string[]): HTMLElement {
  const box = document.createElement('div')
  box.className = 'ui-md-front'
  for (const line of head) {
    if (line.trim() === '') continue
    const row = document.createElement('div')
    row.className = 'ui-md-front__row'
    const colon = line.indexOf(':')
    if (colon > 0) {
      const key = document.createElement('span')
      key.className = 'ui-md-front__key'
      key.textContent = line.slice(0, colon)
      const value = document.createElement('span')
      value.className = 'ui-md-front__value'
      value.textContent = line.slice(colon + 1).trim()
      row.append(key, value)
    } else {
      row.textContent = line
    }
    box.appendChild(row)
  }
  return box
}

/**
 * Render `text` into `host`, replacing everything that was there.
 *
 * Replacing rather than patching is the contract: a preview that merged into
 * what was already on screen could leave a fragment of the previous document
 * under the new one's name, which in a viewer is the one lie that matters.
 */
export function renderMarkdown(host: HTMLElement, text: string): void {
  host.replaceChildren()
  const { head, body } = splitFrontmatter(text)
  if (head.length > 0) host.appendChild(frontmatterElement(head))

  const clean = DOMPurify.sanitize(md.render(body), {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    // No `<template>`, no shadow DOM, no data URIs anywhere.
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
    RETURN_DOM_FRAGMENT: true,
  })
  const article = document.createElement('div')
  article.className = 'ui-md-body'
  article.appendChild(clean)
  host.appendChild(article)
}
