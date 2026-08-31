// What the parser recognised in a body, and what it did not — the render
// behind the settings page's preview line (design §10.4).
//
// It exists for one failure it makes impossible: a mistyped `{{ask:port}`
// with one closing brace matches nothing, so it is not a span, and without
// this line the author has no signal at all until the malformed literal is
// fired into somebody's agent session. Recognition is also the honest place
// to report a name nobody owns: `{{evn:cwd}}` appears here as unrecognised
// text rather than as a substitution.
//
// This module DECIDES nothing, and after nocx-22y46 that is literally true
// rather than nearly true: every part below is a position and a label read
// off `parse`. It contains no regular expression, no brace literal and no
// second reading of the grammar, so the legend an author reads and the
// refusal a fire produces cannot disagree (AD-8).
import { parse, splitDeclaration } from './parse'
import { ENV_KEYS } from './resolve'

export type PreviewPart =
  /** An `{{env:key}}` span. `known` is false for a key outside the table:
   *  it parses, so no scan can catch it as a typo, and the fire refuses on
   *  it (design §11.2) — the author learns it here instead. */
  | { kind: 'env'; text: string; key: string; known: boolean }
  /** A `{{name}}`, `{{name=default}}` or `{{name=a|b}}` span — a question
   *  asked at fire time, with what it will offer. */
  | {
      kind: 'param'
      text: string
      name: string
      defaultValue: string
      options: readonly string[]
    }
  /** A condition's opening tag — a tick at fire time. */
  | { kind: 'flag'; text: string; name: string; negated: boolean }
  /** A vault reference, which this feature never resolves. */
  | { kind: 'secret'; text: string; name: string }
  /** Text that looked like a span and is not one. It is sent as it is. */
  | { kind: 'unrecognised'; text: string }
  /** A body that cannot be fired at all, and why. */
  | { kind: 'problem'; text: string; detail: string }

/**
 * The body's spans in first-occurrence order, each with what it will become.
 * Text between spans is not reported: this is a legend for the field, not a
 * second rendering of the body.
 */
export function describeBody(body: string): PreviewPart[] {
  const parsed = parse(body)
  const at = new Map<number, PreviewPart>()

  for (const span of parsed.spans) {
    const text = body.slice(span.from, span.to)
    if (span.kind === 'env') {
      at.set(span.from, { kind: 'env', text, key: span.arg, known: span.arg in ENV_KEYS })
    } else if (span.kind === 'secret') {
      at.set(span.from, { kind: 'secret', text, name: span.arg })
    } else if (span.kind === 'param') {
      const d = splitDeclaration(span.arg)
      at.set(span.from, {
        kind: 'param',
        text,
        name: d.name,
        defaultValue: d.defaultValue,
        options: d.options,
      })
    } else {
      at.set(span.from, { kind: 'unrecognised', text })
    }
  }
  for (const b of parsed.blocks) {
    at.set(b.openFrom, {
      kind: 'flag',
      text: body.slice(b.openFrom, b.openTo),
      name: b.name,
      negated: b.negated,
    })
  }
  // A problem takes the position it is about, REPLACING whatever the scan
  // made of it: an author reading the line needs the refusal, not the
  // classification that will never be used.
  for (const d of parsed.diagnostics) {
    at.set(d.from, { kind: 'problem', text: body.slice(d.from, d.to), detail: d.detail })
  }

  return [...at.entries()].sort((a, z) => a[0] - z[0]).map(([, part]) => part)
}
