// The variable grammar, in the renderer — one scan, shared by whatever
// decorates or reads `{{name}}` on this side.
//
// IT IS A MIRROR, AND THE ORIGINAL IS `internal/apicoll/substitute.go`. The
// backend decides what is a reference and what is text that happens to
// contain braces, because the backend is what substitutes; this scan exists
// so a surface can SHOW that decision before the send. The two must agree
// exactly or the chip lies in one of the two directions that matter: a chip
// over text the backend will send literally, or plain braces over a name it
// will resolve. Both are worse than no chip at all.
//
// So the rule is restated here in full rather than approximated:
//
//   - `{{` opens; the first `}}` after it closes. An unterminated `{{` is
//     text — a URL full of braces is a URL.
//   - the name is TRIMMED of surrounding whitespace.
//   - it must be 1…128 characters of `A-Za-z0-9_.-` — a superset of what the
//     Postman importer mints and a subset of what a JSON body contains,
//     which is what keeps `{{"a":1}}` a body rather than a variable nobody
//     bound.
//   - anything else is not a reference, and the scan RESUMES INSIDE the
//     opening braces, so a real reference nested in braces is still found.
//
// `{{secret:NAME}}` is not a variable by this rule — `:` is not in the
// allowed set — and that is deliberate on both sides: a vault reference has
// its own grammar and its own owner (`secret-reference.ts`).

/** The longest name the backend will treat as a reference. */
export const MAX_VAR_NAME_LENGTH = 128

/** One `{{name}}` span, in first-occurrence order. Offsets are UTF-16 code
 *  units — what CM6 counts in. */
export interface VariableSpan {
  from: number
  to: number
  name: string
}

/** Whether a name between braces is one the backend will resolve. */
export function isVariableName(name: string): boolean {
  if (name === '' || name.length > MAX_VAR_NAME_LENGTH) return false
  return /^[A-Za-z0-9_.-]+$/.test(name)
}

/**
 * Every well-formed `{{name}}` in `input`.
 *
 * The span covers the braces, not just the name: it is what a chip replaces
 * and what a caret steps over, and both of those are the whole reference.
 */
export function findVariables(input: string): VariableSpan[] {
  const out: VariableSpan[] = []
  let i = 0
  while (i < input.length) {
    const open = input.indexOf('{{', i)
    if (open < 0) return out
    const close = input.indexOf('}}', open + 2)
    if (close < 0) return out
    const raw = input.slice(open + 2, close)
    const name = raw.trim()
    if (isVariableName(name)) {
      out.push({ from: open, to: close + 2, name })
      i = close + 2
      continue
    }
    // Not a reference. Resume INSIDE the braces we just rejected, exactly as
    // the backend does, so `{{{{baseUrl}}` still finds the inner one.
    i = open + 2
  }
  return out
}
