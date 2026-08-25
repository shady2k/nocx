// How JSON is laid out for reading. One owner, because two surfaces ask the
// same question about the same bytes: the API workbench's request body, where
// a person presses a control and the document they will SEND is rewritten,
// and its response body, where the answer is laid out for reading while the
// Raw tab keeps the octets. A second implementation would agree with the
// first everywhere anybody looked and disagree the day one of them changed
// its indent, its handling of a bare literal, or its idea of how big is too
// big — which is the shape AGENTS.md's rule about a second derivation names.
//
// It lives in the kit rather than beside the workbench for the reason
// format-bytes.ts does: it is a wording decision about machine output, the
// surfaces that render it are kit components, and a kit component cannot
// reach into a feature module for it.
//
// WHAT IT PROMISES, and the promise is narrow on purpose:
//
//   Only whitespace changes. The result is `JSON.parse`'s document written
//   back out, so it parses to the same value it was given. It is NOT a
//   round-trip of the BYTES — key order is preserved (JS objects keep
//   insertion order for string keys), but a number written `1.0` comes back
//   `1`, and duplicate keys collapse. That is what laying out JSON means, and
//   it is why the surface that must show the bytes shows them from somewhere
//   else: the raw view holds what went on the socket.
//
//   It is idempotent. Laying out an already laid-out document returns the
//   same string, so a person pressing the control twice sees nothing move.
//
//   It refuses rather than mangles. Text that is not JSON comes back as a
//   refusal with the text untouched, never as a best effort — a body a
//   person is about to send is the last place for a guess.

/**
 * The most text that is laid out without asking.
 *
 * A cap rather than a spinner, because the work is `JSON.parse` plus
 * `JSON.stringify` on the main thread and neither can be interrupted: a body
 * big enough to be slow freezes the pane it is being read in. 256 KiB is
 * comfortably above every hand-written request body and every answer a person
 * reads field by field, and comfortably below the megabyte-scale payloads the
 * backend will still hand over (a truncated body reports its size in
 * megabytes). A body past it is shown exactly as it arrived, and the surface
 * says so — a silent degrade the UI contradicts is how a feature that does
 * not exist survives a release.
 */
export const JSON_LAYOUT_LIMIT = 256 * 1024

/** Two spaces. One number, decided here, so the request body a person formats
 *  and the response body they read are indented the same way. */
const INDENT = 2

/**
 * The answer to "lay this out", which is three answers and never two.
 *
 * `unreadable` and `too-large` are different facts and a caller says
 * different things about them: one is "this is not JSON", the other is "this
 * IS JSON and laying it out would cost more than it is worth". Collapsing
 * them would tell a person their valid body was invalid.
 */
export type JSONLayout =
  | { readonly kind: 'laid-out'; readonly text: string }
  | { readonly kind: 'unreadable' }
  | { readonly kind: 'too-large'; readonly limit: number }

/**
 * Lay out JSON text: one field per line, indented, nested structure indented
 * again. Whitespace only — see the module note for exactly what that means
 * and what it deliberately does not promise.
 */
export function layOutJSON(text: string): JSONLayout {
  if (text.length > JSON_LAYOUT_LIMIT) return { kind: 'too-large', limit: JSON_LAYOUT_LIMIT }
  let value: unknown
  try {
    value = JSON.parse(text)
  } catch {
    return { kind: 'unreadable' }
  }
  const laidOut = JSON.stringify(value, null, INDENT)
  // `JSON.parse` accepts documents `JSON.stringify` will not write back:
  // `undefined` is not one of them, but a caller reading this in six months
  // should not have to prove that from the spec. An answer of undefined is
  // one this function has nothing to say about, so it says so.
  if (laidOut === undefined) return { kind: 'unreadable' }
  return { kind: 'laid-out', text: laidOut }
}
