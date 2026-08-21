// The API workbench's vocabulary — one model, assembled from the generated
// contract types and never re-declared beside them.
//
// Two schemas describe the request: `api.request.read` (what the file holds)
// and `api.import.curl` (what a pasted line converted to). Three describe a
// collection: `api.collections.list`, `api.collections.open` and
// `api.collections.create`. Design §6.4 says the file is the truth and every
// surface is a projection of it — "there is nothing between them to diverge"
// — and §10 says the two import entrances share one converter. Both claims
// are about SHAPE, and this module is where the renderer states them once, in
// the type system: `adoptImportedRequest`, `adoptOpenedCollection` and
// `adoptCreatedCollection` assign one door's type to the other's, so a schema
// that drifts stops the build here rather than at the first field somebody
// happens to read off the wrong result.
//
// Nothing here is hand-written structure. Every type below is an alias of a
// generated one; the aliases exist so the rest of the surface names the
// concept ("a header") rather than the method it happened to arrive from.

import type {
  Request as ReadRequest,
  Header as ReadHeader,
  Param as ReadParam,
  Body as ReadBody,
  Auth as ReadAuth,
} from '../generated/api.request.read'
import type {
  Request as CurlRequest,
  Header as CurlHeader,
  Param as CurlParam,
  Body as CurlBody,
  Auth as CurlAuth,
  Unsupported as CurlUnsupported,
} from '../generated/api.import.curl'
import type {
  OpenCollection,
  Collection as ListedCollection,
  RequestRef as ListedRequestRef,
  MalformedRef as ListedMalformedRef,
  EnvironmentRef as ListedEnvironmentRef,
} from '../generated/api.collections.list'
import type {
  Collection as OpenedCollection,
  RequestRef as OpenedRequestRef,
  MalformedRef as OpenedMalformedRef,
  EnvironmentRef as OpenedEnvironmentRef,
} from '../generated/api.collections.open'
import type {
  Collection as CreatedCollection,
  RequestRef as CreatedRequestRef,
  MalformedRef as CreatedMalformedRef,
  EnvironmentRef as CreatedEnvironmentRef,
} from '../generated/api.collections.create'
import type {
  Response as SendResponse,
  Header as SendHeader,
  Timings as SendTimings,
  Exchange,
  Raw,
} from '../generated/api.request.send'

// ── The one request model, and the one collection model ───────────────────

export type ApiRequest = ReadRequest
export type ApiHeader = ReadHeader
export type ApiParam = ReadParam
type ApiBody = ReadBody
type ApiAuth = ReadAuth

export type ApiCollection = ListedCollection
type ApiRequestRef = ListedRequestRef
type ApiMalformedRef = ListedMalformedRef
/** One environment a person can send under: the path that names it on
 *  `api.request.send`, and the name the FILE declares. There is no third
 *  field, and the contract says why — the values and the route stay in the
 *  file (§6.4), so the renderer names an environment and never holds one. */
export type ApiEnvironmentRef = ListedEnvironmentRef
export type ApiOpenCollection = OpenCollection

export type ApiResponse = SendResponse

/**
 * Both sides of one exchange, already segmented (design §11).
 *
 * Two fields rather than one because they are two MECHANISMS with two
 * different guarantees (§11.3): the request side verifies placements the
 * sender itself made, which is cheap because we know where we put them; the
 * response side runs a bounded known-plaintext search, because a placement in
 * the request says nothing about whether a server echoed those bytes back or
 * where. They render alike and are not the same fact.
 */
export type ApiExchange = Exchange
type ApiResponseHeader = SendHeader
type ApiTimings = SendTimings

/**
 * What an import did NOT carry over (design §10, §12.2).
 *
 * A union of the two importers' own declarations rather than one of them
 * standing in for both: they are one vocabulary — a feature named, and why —
 * and the union is what says so without either schema being made the
 * authority over the other's method.
 */
export type ApiImportNote = CurlUnsupported

// ── The doors ─────────────────────────────────────────────────────────────

/**
 * Adopt a curl-converted request as the workbench's model.
 *
 * The body is a straight assignment on purpose. Every annotation below is a
 * compile-time assertion that `api.import.curl`'s request and
 * `api.request.read`'s request are the same shape — §10's "two entrances,
 * one converter" checked by the compiler rather than trusted. Should the
 * schemas diverge, this is where it is caught.
 */
export function adoptImportedRequest(imported: CurlRequest): ApiRequest {
  const headers: ApiHeader[] = imported.headers satisfies CurlHeader[]
  const query: ApiParam[] = imported.query satisfies CurlParam[]
  const body: ApiBody = imported.body satisfies CurlBody
  const auth: ApiAuth = imported.auth satisfies CurlAuth
  return {
    id: imported.id,
    name: imported.name,
    method: imported.method,
    url: imported.url,
    headers,
    query,
    body,
    auth,
  }
}

/**
 * Adopt the collection `api.collections.open` answered with into the shape
 * `api.collections.list` uses.
 *
 * The store holds ONE list — a folder just opened and a folder opened an
 * hour ago are the same row — so the open result has to arrive in the
 * listing's vocabulary. Same compile-time assertion as above: the two
 * schemas declare one collection, and the annotations prove it.
 */
export function adoptOpenedCollection(opened: OpenedCollection): ApiCollection {
  const requests: ApiRequestRef[] = opened.requests satisfies OpenedRequestRef[]
  const malformed: ApiMalformedRef[] = opened.malformed satisfies OpenedMalformedRef[]
  const environments: ApiEnvironmentRef[] = opened.environments satisfies OpenedEnvironmentRef[]
  return { name: opened.name, requests, malformed, environments }
}

/**
 * Adopt the collection `api.collections.create` answered with — the same
 * shape again, and the same assertion.
 *
 * The create schema says the shape is the open's ON PURPOSE, "so the renderer
 * has one thing to do afterwards rather than two, and there is no moment at
 * which a freshly made collection is not addressable". A door is what this
 * module makes of that: the claim is checked by the compiler here, so a create
 * result that drifted from an open's would stop the build rather than reach
 * the tree as a row with a field nobody filled in.
 *
 * It is a separate function rather than a cast to the open door's parameter
 * because the two schemas are two documents. They agree today; the day one of
 * them grows a field, this is the line that says which.
 */
export function adoptCreatedCollection(created: CreatedCollection): ApiCollection {
  const requests: ApiRequestRef[] = created.requests satisfies CreatedRequestRef[]
  const malformed: ApiMalformedRef[] = created.malformed satisfies CreatedMalformedRef[]
  const environments: ApiEnvironmentRef[] = created.environments satisfies CreatedEnvironmentRef[]
  return { name: created.name, requests, malformed, environments }
}

// ── Reading a response, in the product's words ────────────────────────────

/**
 * What the run says about the body it holds — one sentence, and the four
 * facts stay four (design §12.3).
 *
 * `binary`, `truncated`, `lossy` and "nothing came back" are separate
 * states because they are separate sentences, and collapsing any two of
 * them loses one: an empty body and a body cut at the ceiling are not the
 * same answer, and a binary body is never base64 — the wire sends empty
 * text and the size, and this says "binary body, N bytes".
 */
export function bodySummary(response: ApiResponse): string {
  if (response.binary) return `binary body, ${response.size} bytes`
  if (response.truncated) return `truncated at ${response.size} bytes — this is a prefix`
  if (response.size === 0) return 'empty body'
  if (response.lossy) return `${response.size} bytes, not valid text — invalid sequences replaced`
  return `${response.size} bytes`
}

/** True when there is body text to render at all. Binary is the case that
 *  matters: the wire deliberately sends EMPTY text for it, so "no text" and
 *  "binary" must not both render as an empty pane. */
export function hasBodyText(response: ApiResponse): boolean {
  return !response.binary && response.text !== ''
}

/** Milliseconds as the run list shows them: whole ones under a second so a
 *  column of runs lines up, seconds above it so a slow call reads as slow. */
export function formatElapsed(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${Math.round(ms)}ms`
}

/** Bytes as the run list shows them. Never a claim about what the server
 *  holds — `size` is what was read and kept. */
export function formatSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)}MB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)}KB`
  return `${bytes}B`
}

/** The status line's tone: 2xx settled, 3xx/4xx a caution, 5xx an error. */
export function statusTone(status: number): 'ok' | 'warning' | 'error' {
  if (status >= 500) return 'error'
  if (status >= 300) return 'warning'
  return 'ok'
}

// ── Raw diagnostics (design §11) ──────────────────────────────────────────

/**
 * One run of the raw text: ordinary text, the secret we placed, or the secret
 * we placed whose bytes no longer equal it (design §11.1's three states,
 * never two).
 *
 * ADR-0021: the reference is what gets stored, sent and resolved, and only the
 * RENDERING is a chip. A value cannot reach here even by mistake — the wire
 * elides a secret's bytes and puts a placeholder in their place (§11.2), and
 * neither secret shape carries a field a value could arrive in. The damaged
 * shape carries the SHAPE of the damage and nothing else, because a truncated
 * token is a prefix of a live one.
 */
export type ApiRawSegment =
  | { readonly kind: 'text'; readonly text: string }
  | { readonly kind: 'secret'; readonly name: string }
  | { readonly kind: 'secret-damaged'; readonly name: string; readonly damage: string }

/**
 * Walk one side of the exchange into the runs the raw view draws.
 *
 * The contract's own sentence is the whole specification: the spans "tile the
 * text in order with neither gap nor overlap, so a renderer draws the whole
 * payload by walking them". Two things follow, and both are load-bearing.
 *
 * `from` and `to` are BYTE offsets into UTF-8, not JavaScript string indices.
 * A response body is arbitrary decoded text, so a walk that sliced by UTF-16
 * code units would place every span after the first non-ASCII character in the
 * wrong position — and place a chip over the wrong bytes, which in this
 * particular view is the difference between evidence and a lie. The text is
 * encoded once per side and each run decoded out of it.
 *
 * ANYTHING THE SPANS DO NOT COVER IS STILL RENDERED. A tiling has no gaps by
 * construction, so this can only fire on a payload the backend under-declared
 * — and the contract itself calls `spans: []` the ordinary empty case for "a
 * side with nothing to mark", which is exactly such a payload. The raw view is
 * the one surface whose entire purpose is to show everything, so text nobody
 * declared is text, never text dropped: a walker that emitted only what the
 * spans named would show an empty pane for a side that has nothing to mark,
 * and would silently swallow a run on the day a backend miscounted.
 */
export function rawSegments(raw: Raw): ApiRawSegment[] {
  const bytes = new TextEncoder().encode(raw.text)
  const decoder = new TextDecoder()
  const out: ApiRawSegment[] = []
  const text = (from: number, to: number): void => {
    if (to <= from) return
    out.push({ kind: 'text', text: decoder.decode(bytes.subarray(from, to)) })
  }

  let at = 0
  for (const span of raw.spans) {
    const from = clamp(span.from, 0, bytes.length)
    const to = clamp(span.to, from, bytes.length)
    // Whatever sits between the last run and this one.
    text(at, from)
    if (span.kind === 'secret') {
      out.push({ kind: 'secret', name: span.name })
    } else if (span.kind === 'secret-damaged') {
      out.push({ kind: 'secret-damaged', name: span.name, damage: span.damage })
    } else {
      text(from, to)
    }
    at = Math.max(at, to)
  }
  // And the tail, which is the whole text when there was nothing to mark.
  text(at, bytes.length)
  return out
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(Math.max(value, low), high)
}

/** The response's headers, as the pretty view lists them. They used to be
 *  visible only inside the raw text this renderer composed itself; now that
 *  the raw text comes off the wire, this is the one place the renderer says
 *  anything about headers — and it is a summary beside the body rather than a
 *  second account of the exchange. */
export function responseHeaderText(response: ApiResponse): string {
  const headers: ApiResponseHeader[] = response.headers
  return headers.map((h) => `${h.name}: ${h.value}`).join('\n')
}

/** Where it went and how long each phase took. A reused connection reports a
 *  zero dns and connect, which is the honest answer: nothing was resolved
 *  and nothing was dialled. */
export function connectionRawText(response: ApiResponse): string {
  const t: ApiTimings = response.timings
  const where = [response.remoteAddr, response.tlsVersion].filter((s) => s !== '').join('  ')
  const phases = `dns ${t.dnsMs}ms · connect ${t.connectMs}ms · tls ${t.tlsMs}ms · ttfb ${t.ttfbMs}ms · total ${t.totalMs}ms`
  return where === '' ? phases : `${where}\n${phases}`
}
