// What the parser recognised in a body, and what it did not — the render
// behind the settings page's preview line (design §10.4).
//
// It exists for one failure it makes impossible: a mistyped `{{ask:port}`
// with one closing brace matches nothing, so it is not a span, and without
// this line the author has no signal at all until the malformed literal is
// fired into somebody's agent session. Recognition is also the honest place
// to report an unknown namespace: `{{cwd}}` and `{{evn:cwd}}` appear here as
// unrecognised text rather than as substitutions.
//
// This module DECIDES nothing. Every recognition it reports comes from the
// scan that already owns it — valueSpans for env and parameters, findReferences
// for the vault's secret, ENV_KEYS for what an env key may be — so the
// preview cannot tell the author one thing and the fire do another (AD-8).
import { findReferences } from '../secret-reference'
import { valueSpans } from './parse'
import { ENV_KEYS, splitAsk } from './resolve'

export type PreviewPart =
  /** An `{{env:key}}` span. `known` is false for a key outside the table:
   *  it parses, so no scan can catch it as a typo, and the fire refuses on
   *  it (design §11.2) — the author learns it here instead. */
  | { kind: 'env'; text: string; key: string; known: boolean }
  /** An `{{ask:name=default}}` span — a question asked at fire time. */
  | { kind: 'ask'; text: string; name: string; defaultValue: string }
  /** A vault reference. Saving one is allowed and unremarked (§11.1), but
   *  the preview still says it was recognised: the author needs to know it
   *  is not literal text. */
  | { kind: 'secret'; text: string; name: string }
  /** Something that opens like a span and is not one. It will be sent
   *  literally, which is the whole reason this line exists. */
  | { kind: 'unrecognised'; text: string }

/** Every `{{` in the body — the candidate openings a reader means as a span.
 *  A recognised span starts at one of these; anything left over is the
 *  unrecognised half of the report. */
const OPENING = '{{'

/** How much of an unrecognised opening to quote back. It ends at the first
 *  `}` (a mistyped `{{ask:port}` ends there), else at the end of the line —
 *  never across lines, because a runaway quote would be the rest of the
 *  body. */
function unrecognisedText(body: string, at: number): string {
  const lineEnd = body.indexOf('\n', at) >= 0 ? body.indexOf('\n', at) : body.length
  const line = body.slice(at, lineEnd)
  // A well-formed-looking `{{cwd}}` is quoted whole, braces and all: the
  // author is looking for the thing they typed. A `{{ask:port}` with one
  // brace ends at that brace — quoting to the end of the line would report
  // the rest of the command as part of the mistake.
  const both = line.indexOf('}}')
  if (both >= 0) return line.slice(0, both + 2)
  const one = line.indexOf('}')
  if (one >= 0) return line.slice(0, one + 1)
  return line
}

/**
 * The body's spans in first-occurrence order, each with what it will become.
 * Text between spans is not reported: this is a legend for the field, not a
 * second rendering of the body.
 */
export function describeBody(body: string): PreviewPart[] {
  const recognised = new Map<number, PreviewPart>()
  for (const span of valueSpans(body)) {
    if (span.kind !== 'env' && span.kind !== 'param') continue
    const text = body.slice(span.from, span.to)
    if (span.kind === 'env') {
      recognised.set(span.from, {
        kind: 'env',
        text,
        key: span.arg,
        known: span.arg in ENV_KEYS,
      })
    } else {
      const field = splitAsk(span.arg)
      recognised.set(span.from, {
        kind: 'ask',
        text,
        name: field.name,
        defaultValue: field.defaultValue,
      })
    }
  }
  for (const ref of findReferences(body)) {
    recognised.set(ref.from, {
      kind: 'secret',
      text: body.slice(ref.from, ref.to),
      name: ref.name,
    })
  }

  const parts: PreviewPart[] = []
  for (let at = body.indexOf(OPENING); at >= 0; at = body.indexOf(OPENING, at + 1)) {
    const hit = recognised.get(at)
    if (hit !== undefined) {
      parts.push(hit)
      // Skip past the span: a `{{` inside one belongs to it, not to a
      // second opening.
      at += hit.text.length - 1
      continue
    }
    parts.push({ kind: 'unrecognised', text: unrecognisedText(body, at) })
  }
  return parts
}
