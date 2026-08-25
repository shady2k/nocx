// AnswerMarkdown — how ONE line of a model's answer is painted (nocx-swoje,
// ui/README table).
//
// Vanilla-emitted for the same reason ToolCallLine and ReasoningNote are:
// its only home is the scrollback's answer block, which is imperative DOM
// built by scrollback/blocks.ts.
//
// WHY A LINE AND NOT A DOCUMENT. The answer body is a stack of `.term-line`
// rows, one per line, because that is what the scrollback IS: the selection
// path freezes a row range into a reference chip, the copy path reads rows
// back as text, and the stream arrives in chunks that split mid-line. A
// markdown renderer that owned a document would have to own all three, and
// it would have to re-lay the answer out on every chunk. So this paints ONE
// COMPLETED LINE, which is the largest unit the stream can hand over
// finished — and the row model, the copy path and the chip path are all
// untouched.
//
// WHAT THAT COSTS, DELIBERATELY. Anything markdown expresses ACROSS lines is
// not rendered, and it is not rendered on purpose rather than by oversight:
//   - tables (a grid is not a stack of rows),
//   - setext headings (`===` under a line — the underline arrives after the
//     line it names, which this cannot reach back to),
//   - nested block quotes and lazy continuation,
//   - horizontal rules, images and task-list checkboxes.
// Consecutive list items are painted as consecutive rows with bullets rather
// than gathered into one `<ul>`, for the same reason.
//
// AND ONE THING IS OMITTED FOR SAFETY, NOT FOR EFFORT: a `[text](url)` is
// NEVER turned into an anchor. Every byte here is the model's, and an anchor
// is a navigation target the model chose — `javascript:`, a file:// path, an
// exfiltrating query string. The text and its URL are both shown, verbatim
// and inert. Nothing else in this module produces an element with an
// attribute the model can influence either: every `<span>`, `<code>`,
// `<strong>` and `<em>` carries a class this file wrote, and the model's
// bytes only ever become TEXT inside them.
//
// THE MODEL'S TEXT IS DATA. Every byte goes through escapeHtml (the one
// owner, ui/escape-html.ts — the same function highlightShellText uses,
// which is the other place a string of somebody else's bytes is assigned to
// innerHTML). A line with no structure is written with textContent and never
// reaches innerHTML at all, which is also what makes an ordinary answer
// render exactly as it did before this existed.

import { escapeHtml } from './escape-html'

/** `# ` through `###### `. The space is required: `#nocx` is a word and
 *  `#ff0000` is a colour, and neither is a heading. */
const HEADING = /^(#{1,6})[ \t]+(.*)$/

/** A bullet: `-`, `*` or `+`, then whitespace, then something. The
 *  whitespace is what keeps `---` a rule and `-` a stray rather than an
 *  empty list item. */
const BULLET = /^([ \t]*)[-*+][ \t]+(.*)$/

/** An ordered item: `1.` or `1)`. The NUMBER THE MODEL WROTE is kept —
 *  renumbering a list would be asserting a fact the model did not. */
const ORDERED = /^([ \t]*)(\d{1,9})[.)][ \t]+(.*)$/

/** A quote. One level only; `>>` renders as a quote whose text starts `>`. */
const QUOTE = /^[ \t]*>[ \t]?(.*)$/

/** Inline code, then bold, then emphasis — in that order, because code wins
 *  and its contents are not markup (a model explaining markdown writes
 *  `**not bold**` and means the asterisks).
 *
 *  `_` IS DELIBERATELY NOT AN EMPHASIS MARKER. A terminal's text is full of
 *  `some_file_name`, `$MY_VAR` and `__init__`, and mis-emphasising an
 *  identifier is a worse and far more frequent error than failing to
 *  italicise the rare `_word_`. Models emit `*` and `**` for emphasis in
 *  practice; underscores here would cost more than they buy. */
const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(\*[^*\n]+\*)/g

/** The same pattern without `g`, for the "is there any inline markup at
 *  all" question. A global regex carries `lastIndex` across `.test()` calls
 *  and would answer differently on the second identical line. */
const HAS_INLINE = new RegExp(INLINE.source)

/** Two spaces of indent per level, the width every model writes. Deeper
 *  indents round down rather than inventing a level. */
const INDENT_PER_LEVEL = 2

/** Escaped HTML for the inline spans in one run of text. */
function inlineHtml(text: string): string {
  let html = ''
  let pos = 0
  for (const m of text.matchAll(INLINE)) {
    const at = m.index
    html += escapeHtml(text.slice(pos, at))
    const [token] = m
    if (token.startsWith('`')) {
      html += `<code class="ui-md-code">${escapeHtml(token.slice(1, -1))}</code>`
    } else if (token.startsWith('**')) {
      // The span's contents are markup too: `**`repos`**` means code
      // inside bold, and only the inline pass can see that. Bounded: the
      // bold/em pattern admits no `*` inside its contents, so a nested
      // span can only be code, which is rendered without recursing.
      html += `<strong class="ui-md-strong">${inlineHtml(token.slice(2, -2))}</strong>`
    } else {
      html += `<em class="ui-md-em">${inlineHtml(token.slice(1, -1))}</em>`
    }
    pos = at + token.length
  }
  html += escapeHtml(text.slice(pos))
  return html
}

/** The marker span for a list item — the glyph, kept out of the
 *  accessibility tree because a list read aloud does not want "bullet"
 *  before every item. */
function markerHtml(marker: string): string {
  return `<span class="ui-md-marker" aria-hidden="true">${escapeHtml(marker)}</span>`
}

/**
 * Paint one COMPLETED line of an answer into `row`.
 *
 * The row keeps its own identity (`.term-line`) and gains the block role as
 * typed variance: `data-md` is the role, `data-md-depth` the nesting of a
 * list item. An ordinary line sets neither and is written with textContent,
 * so an answer with no structure is byte-for-byte the DOM it was before.
 */
export function paintAnswerLine(row: HTMLElement, text: string): void {
  const heading = HEADING.exec(text)
  if (heading) {
    row.dataset.md = `h${heading[1].length}`
    row.innerHTML = inlineHtml(heading[2])
    return
  }

  const ordered = ORDERED.exec(text)
  if (ordered) {
    row.dataset.md = 'li'
    row.dataset.mdDepth = String(Math.floor(ordered[1].length / INDENT_PER_LEVEL))
    row.innerHTML = markerHtml(`${ordered[2]}.`) + inlineHtml(ordered[3])
    return
  }

  const bullet = BULLET.exec(text)
  if (bullet) {
    row.dataset.md = 'li'
    row.dataset.mdDepth = String(Math.floor(bullet[1].length / INDENT_PER_LEVEL))
    row.innerHTML = markerHtml('•') + inlineHtml(bullet[2])
    return
  }

  const quote = QUOTE.exec(text)
  if (quote) {
    row.dataset.md = 'quote'
    row.innerHTML = inlineHtml(quote[1])
    return
  }

  // No structure. The inline pass still runs, because `**bold**` in an
  // ordinary sentence is the commonest markdown a model emits — but a line
  // with nothing in it at all never touches innerHTML.
  if (!HAS_INLINE.test(text)) {
    row.textContent = text
    return
  }
  row.innerHTML = inlineHtml(text)
}
