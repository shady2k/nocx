// Resolution happens at fire time, once, whatever the destination. CM6 ships
// snippet tab-stops and they were tempting for a field in the editor with a
// form elsewhere — rejected, because that is two implementations of "fill in
// the blanks" that would agree in every case anyone tried. Design §8.
//
// This module DERIVES nothing about the grammar: parse.ts reads the body and
// this one applies the reading, in the order the design fixes — refuse a
// malformed body, ask for what is visible, cut the conditions, and only then
// check the facts that survived the cut.
import { findReferences } from '../secret-reference'
import { parse, type ConditionRef, type Diagnostic, type Field, type SnippetParse } from './parse'
import type { SessionFacts } from './session-facts'

export type { SessionFacts } from './session-facts'

/** A flag's answer. A flag never reaches the text, so its value is a token
 *  and not a substitution — and a name is either a flag or a parameter,
 *  never both (parse reports the overlap as condition-on-parameter), so this
 *  can never collide with something a person typed. */
export const FLAG_ON = 'on'

export type ResolveOutcome =
  | { kind: 'resolved'; text: string }
  | { kind: 'needs-fields'; fields: Field[] }
  | { kind: 'refused'; reason: 'env-unavailable'; keys: string[] }
  | { kind: 'refused'; reason: 'malformed'; diagnostics: Diagnostic[] }

const satisfied = (cond: ConditionRef, answers: ReadonlyMap<string, string>): boolean =>
  (answers.get(cond.name) === FLAG_ON) !== cond.negated

/** The fields a person should be looking at right now: a field inside a
 *  block they switched off is not one of them (design §7 step 2). An
 *  unanswered flag reads as off, so a form opens showing the ticks and the
 *  fields outside them, and reveals the rest as the ticks go on. */
export function visibleFields(parsed: SnippetParse, answers: ReadonlyMap<string, string>): Field[] {
  return parsed.fields.filter((f) => f.inside === null || satisfied(f.inside, answers))
}

/** Whether firing this body should open the form at all. Kept here rather
 *  than at each surface so that no caller learns the grammar (AD-8): the
 *  palette, the completion dropdown and the clipboard route all ask this
 *  one question instead of each counting spans for itself. */
export function needsForm(body: string): boolean {
  return parse(body).fields.length > 0
}

/** True when the text carries a vault reference. Read-only use of the vault's
 *  own scan — this module never resolves one. */
export function hasSecretReference(text: string): boolean {
  return findReferences(text).length > 0
}

/** The closed env table (design §7.4) — extended by adding a row, never by a
 *  parameter or a mode flag (AD-8). A key that is not a row cannot be
 *  answered, exactly like a null fact.
 *
 *  The value of each row is what the settings page's preview line says the
 *  key will become. It is a phrase rather than a live value on purpose: the
 *  facts are the ACTIVE PANE's and are read at fire time, and while the
 *  settings tab is in front there is no pane to read — a preview showing
 *  "unavailable" there would be a statement about the wrong moment. */
export const ENV_KEYS = {
  cwd: "the pane's working directory",
  host: "the pane's host",
  // The SSH user, which a local shell does not have — so {{env:user}} in a
  // local pane refuses rather than substituting nothing (§11.2), and the
  // preview says which key it is before the first fire finds out.
  user: 'the ssh user (a local shell has none)',
  branch: 'the checked-out git branch',
} as const

export type EnvKey = keyof typeof ENV_KEYS

function envValue(key: string, facts: SessionFacts): string | null {
  return key in ENV_KEYS ? facts[key as EnvKey] : null
}

/** How much a tag takes with it when it goes. Alone on its line, the whole
 *  line goes — including its newline — so a body with two conditions does
 *  not arrive full of blank holes where its tags used to be. Sharing its
 *  line, only the tag goes.
 *
 *  This is Handlebars' standalone-line rule, adopted because it is what an
 *  author expects without being told; Go templates make you write `{{- -}}`
 *  by hand and everybody forgets. */
function tagCut(body: string, from: number, to: number): { from: number; to: number } {
  const lineStart = body.lastIndexOf('\n', from - 1) + 1
  const nl = body.indexOf('\n', to)
  const lineEnd = nl < 0 ? body.length : nl
  if (body.slice(lineStart, from).trim() === '' && body.slice(to, lineEnd).trim() === '') {
    return { from: lineStart, to: Math.min(lineEnd + 1, body.length) }
  }
  return { from, to }
}

interface Edit {
  from: number
  to: number
  text: string
}

export function resolveBody(
  body: string,
  facts: SessionFacts,
  answers: ReadonlyMap<string, string>,
): ResolveOutcome {
  const parsed = parse(body)
  // 1. A malformed body refuses before anything else, and refuses HERE as
  //    well as in the preview: a snippet arrives through backup/restore and
  //    may never have been opened in Settings (design §7 step 1).
  if (parsed.diagnostics.length > 0) {
    return { kind: 'refused', reason: 'malformed', diagnostics: [...parsed.diagnostics] }
  }

  // 2-3. Only a VISIBLE field is owed an answer. Asking for a value the
  //      text is about to drop is asking somebody to do work that has no
  //      effect, and it is how a form comes to demand three answers for a
  //      body that will use one.
  const pending = visibleFields(parsed, answers).filter((f) => !answers.has(f.name))
  if (pending.length > 0) return { kind: 'needs-fields', fields: pending }

  // 4. The cuts. A dropped block takes everything from its opening tag's cut
  //    to its closing tag's; a kept one loses only its two tags.
  const edits: Edit[] = []
  const dropped: Array<{ from: number; to: number }> = []
  for (const b of parsed.blocks) {
    const openCut = tagCut(body, b.openFrom, b.openTo)
    const closeCut = tagCut(body, b.closeFrom, b.closeTo)
    if (satisfied({ name: b.name, negated: b.negated }, answers)) {
      edits.push({ ...openCut, text: '' }, { ...closeCut, text: '' })
      continue
    }
    const cut = { from: openCut.from, to: closeCut.to }
    edits.push({ ...cut, text: '' })
    dropped.push(cut)
  }
  const isDropped = (from: number): boolean => dropped.some((d) => from >= d.from && from < d.to)

  // 5. Only NOW are env keys checked, and only the ones that survived the
  //    cut. An unavailable fact inside a switched-off paragraph is not the
  //    fire's problem, and refusing on it would make a body unfireable over
  //    text it was never going to send (design §7 step 5).
  const missing: string[] = []
  const seen = new Set<string>()
  for (const span of parsed.spans) {
    if (span.kind !== 'env' || isDropped(span.from)) continue
    if (envValue(span.arg, facts) === null && !seen.has(span.arg)) {
      seen.add(span.arg)
      missing.push(span.arg)
    }
  }
  if (missing.length > 0) return { kind: 'refused', reason: 'env-unavailable', keys: missing }

  // 6. Substitution and un-escaping join the same edit list, and that is
  //    what makes an ANSWER safe: every edit position is computed against
  //    the ORIGINAL body, so text a person typed is inserted after the
  //    parsing is over and can never be read as grammar. Somebody whose
  //    answer contains {{x}} or {%% gets those characters.
  for (const span of parsed.spans) {
    if (isDropped(span.from)) continue
    if (span.kind === 'env') {
      edits.push({ from: span.from, to: span.to, text: envValue(span.arg, facts) ?? '' })
    } else if (span.kind === 'param') {
      const name = span.arg.split('=', 1)[0]
      edits.push({ from: span.from, to: span.to, text: answers.get(name) ?? '' })
    }
    // 'secret' is left intact — not ours (design §3, snippets §11.1).
    // 'unrecognised' is sent as it is, which is the point of reporting it.
  }
  for (const e of parsed.escapes) {
    if (isDropped(e.from)) continue
    edits.push({ from: e.from, to: e.to, text: e.text })
  }

  // Right to left, so an earlier edit's offsets are still the body's.
  edits.sort((a, z) => z.from - a.from)
  let text = body
  for (const e of edits) text = text.slice(0, e.from) + e.text + text.slice(e.to)
  return { kind: 'resolved', text }
}
