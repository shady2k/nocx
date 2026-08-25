// The URL and the parameter table are ONE fact in two shapes.
//
// The model keeps them in two fields — `url` and `query` (design §6.4) — and
// the sender concatenates: whatever query the URL already carries, then the
// enabled rows, in the user's order (internal/apisend/sender.go). That is
// correct on the wire and was invisible in the product: a person added
// `page=2` in the table, the URL field went on reading `{{baseUrl}}/users`,
// and the only way to find out the parameter existed at all was to send and
// read the run's raw request. Two shapes of one fact, and only one of them on
// screen.
//
// This module is the one derivation between them, and every rule it keeps is
// here rather than spread across the form:
//
//  1. NOTHING IS ENCODED OR DECODED on the way through. The field holds the
//     person's own text and `{{baseUrl}}` must survive it — percent-encoding
//     the braces would break the substitution that makes the environment
//     work at all. The SENDER encodes, once, at the wire (url.QueryEscape),
//     and that is the only place a value is ever escaped.
//  2. A row with an empty value renders as the bare name. `?q` and `?q=`
//     both parse to the same row, so the shape somebody typed is the shape
//     they get back; the sender writes `q=` either way.
//  3. A DISABLED ROW IS NOT IN THE URL and survives an edit to it. It is a
//     row the user keeps (request-form.tsx says why), so typing in the URL
//     replaces the enabled rows and leaves the disabled ones at the foot of
//     the table rather than deleting what the text could not mention.

import type { ApiParam, ApiRequest } from './api-model'

/** What is before the first `?`, and the pairs after it. */
export function splitTypedUrl(typed: string): { base: string; pairs: ApiParam[] } {
  const cut = typed.indexOf('?')
  if (cut < 0) return { base: typed, pairs: [] }
  const base = typed.slice(0, cut)
  const rest = typed.slice(cut + 1)
  const pairs: ApiParam[] = []
  for (const piece of rest.split('&')) {
    if (piece === '') continue
    const eq = piece.indexOf('=')
    pairs.push(
      eq < 0
        ? { name: piece, value: '', enabled: true }
        : { name: piece.slice(0, eq), value: piece.slice(eq + 1), enabled: true },
    )
  }
  return { base, pairs }
}

/** The URL a person sees: the address with its ENABLED parameters after it.
 *  This is what the field renders and what the send will amount to. */
export function urlWithParams(url: string, query: readonly ApiParam[]): string {
  const { base } = splitTypedUrl(url)
  const on = query.filter((p) => p.enabled && p.name !== '')
  if (on.length === 0) return base
  return `${base}?${on.map((p) => (p.value === '' ? p.name : `${p.name}=${p.value}`)).join('&')}`
}

/**
 * Fold any query already in `url` into the parameter rows.
 *
 * Run when a request is ADOPTED, on the draft and on the saved snapshot
 * alike, so the two stay equal and opening a file does not report itself
 * dirty. It is what makes the table the one owner: after it, `url` never
 * carries a query and the rows are the whole answer — including for the
 * requests that arrive with one, which is nearly every Postman export and
 * every converted curl line.
 *
 * The folded rows go FIRST, because that is the order the wire already had:
 * the sender writes the URL's own query before the rows.
 */
export function foldQueryIntoParams(request: ApiRequest): ApiRequest {
  const { base, pairs } = splitTypedUrl(request.url)
  if (pairs.length === 0) return request
  return { ...request, url: base, query: [...pairs, ...request.query] }
}

/**
 * Apply what was typed in the URL field.
 *
 * The typed text is the truth for the address and for every ENABLED
 * parameter; the disabled rows are kept, at the foot, in their own order.
 * Anything else would make editing the URL a way to silently delete rows a
 * person had switched off rather than thrown away.
 */
export function applyTypedUrl(request: ApiRequest, typed: string): ApiRequest {
  const { base, pairs } = splitTypedUrl(typed)
  const off = request.query.filter((p) => !p.enabled)
  return { ...request, url: base, query: [...pairs, ...off] }
}
