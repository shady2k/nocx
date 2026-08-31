// The one reader of the snippet grammar. Preview, the ask form and the fire
// adapter are its consumers and derive NOTHING themselves — the legend an
// author reads in Settings and the refusal a fire produces come from this
// one computation, so they cannot disagree (design §6, AD-8).
//
// The vault's namespace is not parsed here. `findReferences` owns "this is a
// reference to a secret" and is called for it, because a second derivation
// of that predicate is the failure the snippets spec §7 was written against.
import { findReferences } from '../secret-reference'
import { REFERENCE_NAMESPACES, type ReferenceNamespace } from './reference-namespaces'

/** Exported when a consumer needs to NAME it; until then the interface below
 *  is the only thing that has to. */
type SpanKind = 'param' | 'env' | 'secret' | 'unrecognised'

export interface ValueSpan {
  readonly from: number
  readonly to: number
  readonly kind: SpanKind
  /** param: the raw declaration ("w=a|b"). env: the key. secret: the vault
   *  name, from findReferences. unrecognised: the quoted text. */
  readonly arg: string
}

/** Every `{{…}}` whose content carries no `}`. Deliberately open: the
 *  classification below decides what it is, and an opening that matches
 *  nothing here is reported as unrecognised by the second pass. */
const VALUE_RE = /\{\{([^}]*)\}\}/g

/** Whether a colon may commit a span to a namespace — ASKED OF THE REGISTRY
 *  rather than answered again here. A local list of the owners would be a
 *  second spelling of reference-namespaces.ts: the two would agree the day
 *  they were written and disagree the day somebody adds an owner to one.
 *
 *  Anything not registered stays literal, which is what keeps `{{ask:port}}`
 *  visible after the `ask` namespace was retired. `secret` IS registered, so
 *  a secret-shaped span the vault refuses falls through to the second pass
 *  and is quoted — one owner for that predicate, and it is not this file. */
function owned(ns: string): ns is ReferenceNamespace {
  return ns in REFERENCE_NAMESPACES
}

const OPENING = '{{'

/** How much of an unrecognised opening to quote back: to the first `}}`,
 *  else to the first `}`, else to the end of the line — never across lines,
 *  because a runaway quote would be the rest of the body. */
function unrecognisedText(body: string, at: number): string {
  const nl = body.indexOf('\n', at)
  const line = body.slice(at, nl >= 0 ? nl : body.length)
  const both = line.indexOf('}}')
  if (both >= 0) return line.slice(0, both + 2)
  const one = line.indexOf('}')
  if (one >= 0) return line.slice(0, one + 1)
  return line
}

/**
 * Every `{{…}}` in the body, classified, in first-occurrence order.
 *
 * Two passes rather than one, and the second is the point: the first says
 * what a WELL-FORMED opening is, and the second walks every `{{` in the text
 * so that an opening the first pass never matched is still reported. A
 * single pass over the regex would leave `{{ask:port` — one brace, a real
 * typo — as invisible literal text, which is how a body that cannot fire
 * looks identical to one that can.
 */
export function valueSpans(body: string): ValueSpan[] {
  const secrets = new Map(findReferences(body).map((r) => [r.from, r]))
  const recognised = new Map<number, ValueSpan>()
  for (const m of body.matchAll(VALUE_RE)) {
    const from = m.index
    const to = from + m[0].length
    const secret = secrets.get(from)
    if (secret !== undefined) {
      recognised.set(from, { from, to, kind: 'secret', arg: secret.name })
      continue
    }
    const content = m[1]
    const colon = content.indexOf(':')
    if (colon < 0) {
      recognised.set(from, { from, to, kind: 'param', arg: content })
      continue
    }
    const ns = content.slice(0, colon)
    if (ns === 'env') {
      recognised.set(from, { from, to, kind: 'env', arg: content.slice(colon + 1) })
      continue
    }
    // A namespace nobody owns is literal text, quoted whole. One that IS
    // owned and did not land above belongs to the vault and was refused by
    // it — left for the second pass, which quotes it the same way.
    if (!owned(ns)) {
      recognised.set(from, { from, to, kind: 'unrecognised', arg: m[0] })
    }
  }

  const out: ValueSpan[] = []
  for (let at = body.indexOf(OPENING); at >= 0; at = body.indexOf(OPENING, at + 1)) {
    const hit = recognised.get(at)
    if (hit !== undefined) {
      out.push(hit)
      // Skip past it: a `{{` inside a span belongs to that span.
      at += hit.to - hit.from - 1
      continue
    }
    const text = unrecognisedText(body, at)
    out.push({ from: at, to: at + text.length, kind: 'unrecognised', arg: text })
  }
  return out
}

// ── The LOGIC half of the grammar ────────────────────────────────────────
//
// Borrowed notation, parsed here: `{% if x %}` … `{% endif %}` is Jinja's
// spelling and nothing else about Jinja is adopted. The spelling was taken
// rather than invented because an author has almost certainly seen it, and
// no templating engine we surveyed could be used instead — the vault's
// `{{secret:…}}` renders to nothing under Handlebars and refuses to parse
// under Nunjucks, and none of them can express an inline option list.
//
// These four are declared and not EXPORTED. Nothing outside this module
// names them yet, and the frontend's dead-exports ratchet is right to say
// so: they are exported by the task that gives them a consumer, not by the
// one that writes them. `SnippetParse` exposes them structurally, so a
// consumer can already read a block without naming its type.

interface Block {
  readonly openFrom: number
  readonly openTo: number
  readonly closeFrom: number
  readonly closeTo: number
  readonly name: string
  readonly negated: boolean
}

interface Escape {
  readonly from: number
  readonly to: number
}

type DiagnosticKind =
  'unclosed-block' | 'stray-endif' | 'nested-block' | 'unterminated-tag' | 'unknown-tag'

interface Diagnostic {
  readonly from: number
  readonly to: number
  readonly kind: DiagnosticKind
  readonly detail: string
}

const TAG_OPEN = '{%'
const TAG_CLOSE = '%}'
/** The escape: `{%%` is a literal `{%`. ONE escaping mechanism, applied to
 *  the delimiter that gained a meaning — `|` inside a default is knowingly
 *  not expressible (design §4.1). */
const ESCAPE = '{%%'

interface OpenTag {
  readonly from: number
  readonly to: number
  readonly name: string
  readonly negated: boolean
}

interface TagScan {
  readonly blocks: Block[]
  readonly escapes: Escape[]
  readonly diagnostics: Diagnostic[]
}

/**
 * Every tag in the body, paired into blocks or reported as a defect.
 *
 * A body with a structural defect is not half-parsed: whatever survives here
 * is still returned, and it is `diagnostics` being non-empty that the
 * consumers refuse on. That is deliberate — the settings preview has to draw
 * something while an author is mid-keystroke, and the fire has to name what
 * is wrong rather than say only "no".
 *
 * NESTING IS REFUSED RATHER THAN SUPPORTED. One level covers the case this
 * work was asked for (a sentence kept or dropped), and a nested condition is
 * far more likely to be a missing `{% endif %}` than an intention.
 */
function scanTags(body: string): TagScan {
  const blocks: Block[] = []
  const escapes: Escape[] = []
  const diagnostics: Diagnostic[] = []
  let open: OpenTag | null = null

  // `at` advances only at two points — past an escape, and past a
  // well-formed tag's `%}` — so a `{%` inside a tag's own text can never be
  // re-entered, and `tagFrom` is captured before `at` moves.
  let at = body.indexOf(TAG_OPEN)
  while (at >= 0) {
    if (body.startsWith(ESCAPE, at)) {
      escapes.push({ from: at, to: at + ESCAPE.length })
      at = body.indexOf(TAG_OPEN, at + ESCAPE.length)
      continue
    }
    const tagFrom = at
    const close = body.indexOf(TAG_CLOSE, tagFrom + TAG_OPEN.length)
    if (close < 0) {
      // The rest of the body is inside a tag that never closes, so there is
      // nothing after it to scan — reporting one defect and stopping beats
      // reporting the remainder as a cascade of consequences.
      diagnostics.push({
        from: tagFrom,
        to: body.length,
        kind: 'unterminated-tag',
        detail: 'this tag has no closing %}',
      })
      break
    }
    const tagTo = close + TAG_CLOSE.length
    const words = body
      .slice(tagFrom + TAG_OPEN.length, close)
      .trim()
      .split(/\s+/)
    at = body.indexOf(TAG_OPEN, tagTo)

    if (words.length === 1 && words[0] === 'endif') {
      if (open === null) {
        diagnostics.push({
          from: tagFrom,
          to: tagTo,
          kind: 'stray-endif',
          detail: 'there is no {% if %} open here',
        })
        continue
      }
      blocks.push({
        openFrom: open.from,
        openTo: open.to,
        closeFrom: tagFrom,
        closeTo: tagTo,
        name: open.name,
        negated: open.negated,
      })
      open = null
      continue
    }

    const isIf =
      words[0] === 'if' && (words.length === 2 || (words.length === 3 && words[1] === 'not'))
    if (isIf) {
      if (open !== null) {
        // The OUTER block stays open: the inner tag is the defect, and
        // treating it as the opener would make the next {% endif %} close
        // the wrong one and turn one mistake into two.
        diagnostics.push({
          from: tagFrom,
          to: tagTo,
          kind: 'nested-block',
          detail: 'a condition inside another condition is not supported',
        })
        continue
      }
      open = {
        from: tagFrom,
        to: tagTo,
        name: words[words.length - 1],
        negated: words.length === 3,
      }
      continue
    }

    diagnostics.push({
      from: tagFrom,
      to: tagTo,
      kind: 'unknown-tag',
      detail: 'only {% if %}, {% if not %} and {% endif %} exist',
    })
  }

  if (open !== null) {
    // Reported at the OPENING, not at the end of the body: the author's
    // mistake is where the block was opened, and that is where a cursor
    // should land.
    diagnostics.push({
      from: open.from,
      to: open.to,
      kind: 'unclosed-block',
      detail: 'this condition has no {% endif %}',
    })
  }
  return { blocks, escapes, diagnostics }
}

export interface SnippetParse {
  readonly spans: readonly ValueSpan[]
  readonly blocks: readonly Block[]
  readonly escapes: readonly Escape[]
  readonly diagnostics: readonly Diagnostic[]
}

export function parse(body: string): SnippetParse {
  const tags = scanTags(body)
  return {
    spans: valueSpans(body),
    blocks: tags.blocks,
    escapes: tags.escapes,
    diagnostics: tags.diagnostics,
  }
}
