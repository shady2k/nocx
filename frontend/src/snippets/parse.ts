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

export interface SnippetParse {
  readonly spans: readonly ValueSpan[]
}

export function parse(body: string): SnippetParse {
  return { spans: valueSpans(body) }
}
