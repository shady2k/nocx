// The API workbench's vocabulary — one model, assembled from the generated
// contract types and never re-declared beside them.
//
// Two schemas describe the request: `api.request.read` (what the file holds)
// and `api.import.curl` (what a pasted line converted to). Four describe a
// collection: `api.collections.list`, `api.collections.open`,
// `api.collections.create` and `api.collections.createFolder`. Design §6.4
// says the file is the truth and every surface is a projection of it — "there
// is nothing between them to diverge" — and §10 says the two import entrances
// share one converter. Both claims are about SHAPE, and this module is where
// the renderer states them once, in the type system: `adoptImportedRequest`,
// `adoptOpenedCollection`, `adoptCreatedCollection` and
// `adoptFolderCollection` assign one door's type to the other's, so a schema
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
  Collection as FolderedCollection,
  RequestRef as FolderedRequestRef,
  MalformedRef as FolderedMalformedRef,
  EnvironmentRef as FolderedEnvironmentRef,
} from '../generated/api.collections.createFolder'
import type {
  Environment as ReadEnvironment,
  Route as ReadRoute,
} from '../generated/api.environment.read'
import type {
  ApiRequestSendResult,
  Certificate as SendCertificate,
  Response as SendResponse,
  Header as SendHeader,
  Timings as SendTimings,
  Trust as SendTrust,
  Failure as SendFailure,
  Raw,
} from '../generated/api.request.send'

// ── The one request model, and the one collection model ───────────────────

export type ApiRequest = ReadRequest
export type ApiHeader = ReadHeader
export type ApiParam = ReadParam
type ApiBody = ReadBody
type ApiAuth = ReadAuth

export type ApiCollection = ListedCollection
/** One request as a LISTING names it: where its file is, what it is called,
 *  and its verb. Exported because the tree and the folder page both hold
 *  rows of them — never the request itself, which lives in its file. */
export type ApiRequestRef = ListedRequestRef
type ApiMalformedRef = ListedMalformedRef
/** One environment a person can send under: the path that names it on
 *  `api.request.send`, and the name the FILE declares. There is no third
 *  field, and the contract says why — the values and the route stay in the
 *  file (§6.4), so the renderer names an environment and never holds one. */
export type ApiEnvironmentRef = ListedEnvironmentRef

/** ONE environment whole, as the editor reads and writes it: the name, the
 *  plain values, the names of the secret variables, and the route. The ref
 *  above names an environment so a person can CHOOSE one; this is what an
 *  ask holds while it is open, which is the only interval in which the
 *  renderer legitimately has a copy of a file's contents (§6.4). */
export type ApiEnvironment = ReadEnvironment
export type ApiRoute = ReadRoute
export type ApiOpenCollection = OpenCollection

export type ApiResponse = SendResponse
/** How an exchange got there, off the SEND RESULT — the backend's account of
 *  the record it read, never an echo of what the panel asked for. The schema
 *  inlines it, so it is named here off the result rather than declared a
 *  second time: a hand-written copy is the shape contracts/ exists against. */
export type ApiSentRoute = ApiRequestSendResult['route']

/** One certificate of the chain a server presented, already described by the
 *  backend: the renderer reads strings and parses no X.509. */
export type ApiCertificate = SendCertificate

/**
 * WHAT VERIFICATION SAID about the chain a run accepted — the backend's
 * answer, never the environment's setting.
 *
 * The difference is the whole of nocx-6hg2w.19: `route.insecureTls` is true
 * of every run under an environment with the switch on, so a badge drawn
 * from it fired on a public host with an ordinary chain in the same words
 * and colour a self-signed development host would get. A warning that is on
 * most of the time is a warning nobody reads.
 */
export type ApiTrust = SendTrust

/**
 * ONE SIDE of an exchange, already segmented (design §11).
 *
 * There is no pair type any more, and the reason is what this whole change
 * is about: the two sides are two MECHANISMS with two different guarantees
 * (§11.3), and they now arrive at two different levels because they are
 * known at two different times. The request side is composed before the
 * dial, so it is on the exchange and a run that never got an answer still
 * has it; the response side exists only when something answered, so it is on
 * the response. A pair type would have had to sit at one of those levels and
 * would have taken the request text away from exactly the runs that need it.
 */
export type ApiRaw = Raw
/** Why an exchange ended without an answer: WHERE it stopped, from a closed
 *  set the renderer words itself, and the backend's own reason. */
export type ApiFailure = SendFailure
/** WHERE an exchange stopped — the closed vocabulary, named so a surface can
 *  exhaust it rather than matching on prose. */
export type ApiPhase = ApiFailure['phase']
/** One exchange whole, as api.request.send answers it. */
export type ApiSendResult = ApiRequestSendResult
export type ApiTimings = SendTimings
type ApiResponseHeader = SendHeader

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
  // The request's OWN variables, carried by the same assertion as the rest:
  // a curl line can spell `{{name}}` as readily as a Postman export can, and
  // a converter that dropped the table would be one entrance with a shorter
  // model than the other's.
  const variables: ApiParam[] = imported.variables satisfies CurlParam[]
  const body: ApiBody = imported.body satisfies CurlBody
  const auth: ApiAuth = imported.auth satisfies CurlAuth
  return {
    id: imported.id,
    name: imported.name,
    method: imported.method,
    url: imported.url,
    headers,
    query,
    variables,
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
  // `folders` rides through the same door as the rest, and its absence here
  // was caught by the compiler the moment the schema grew it — which is the
  // whole reason this function exists rather than a cast.
  return {
    name: opened.name,
    requests,
    folders: opened.folders,
    variableFolders: opened.variableFolders,
    malformed,
    environments,
  }
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
  return {
    name: created.name,
    requests,
    folders: created.folders,
    variableFolders: created.variableFolders,
    malformed,
    environments,
  }
}

/**
 * Adopt the collection `api.collections.createFolder` answered with — the
 * fourth account of one collection, and the same assertion again.
 *
 * A folder that has just been made is invisible in `requests` — that is the
 * state a folder spends its first minutes in — so the result carries the
 * collection AS IT IS NOW, `folders` included, precisely so the tree can be
 * drawn from it. A caller that listed afterwards instead would be reading one
 * folder twice, at two moments.
 *
 * Its own function rather than `adoptCreatedCollection` reused, for the
 * reason that one is not `adoptOpenedCollection` reused: they are four
 * documents, they agree today, and this is the line that says which one grew
 * a field the day one of them does.
 */
export function adoptFolderCollection(made: FolderedCollection): ApiCollection {
  const requests: ApiRequestRef[] = made.requests satisfies FolderedRequestRef[]
  const malformed: ApiMalformedRef[] = made.malformed satisfies FolderedMalformedRef[]
  const environments: ApiEnvironmentRef[] = made.environments satisfies FolderedEnvironmentRef[]
  return {
    name: made.name,
    requests,
    folders: made.folders,
    variableFolders: made.variableFolders,
    malformed,
    environments,
  }
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

/**
 * Whether the body should be painted as JSON.
 *
 * Decided by the CONTENT TYPE THE SERVER SENT, never by parsing the bytes. A
 * server that declares `application/json` and sends something else is a
 * server whose answer is worth seeing exactly as it is — and the raw view is
 * one tab away for that case. Suffixed types count (`application/problem+json`
 * is JSON), and parameters after the semicolon do not.
 */
export function isJSONResponse(response: ApiResponse): boolean {
  const headers: ApiResponseHeader[] = response.headers
  const found = headers.find((h) => h.name.toLowerCase() === 'content-type')
  if (!found) return false
  const media = found.value.split(';')[0].trim().toLowerCase()
  return media === 'application/json' || media.endsWith('+json')
}

/** Where it went and how long each phase took. A reused connection reports a
 *  zero dns and connect, which is the honest answer: nothing was resolved
 *  and nothing was dialled. */
/** The chain as the Raw view shows it: one block per certificate, leaf
 *  first, each field on its own line. Text rather than a table because it
 *  sits in the raw view beside the request and the response, and because a
 *  fingerprint is a thing people COPY — out of a pane, into a terminal. */
export function certificateText(cert: ApiCertificate, index: number): string {
  const lines = [
    `#${index + 1}${index === 0 ? '  (leaf)' : ''}${cert.selfSigned ? '  self-signed' : ''}`,
    `subject     ${cert.subject}`,
    `issuer      ${cert.issuer}`,
    `valid       ${cert.notBefore}  →  ${cert.notAfter}`,
  ]
  if (cert.dnsNames.length > 0) lines.push(`dns         ${cert.dnsNames.join(', ')}`)
  if (cert.ipAddresses.length > 0) lines.push(`ip          ${cert.ipAddresses.join(', ')}`)
  lines.push(`sha-256     ${cert.fingerprint}`)
  return lines.join('\n')
}

/**
 * Where it went and how long each phase took.
 *
 * It takes the ATTEMPT rather than the response, because that is where these
 * facts now live: an exchange that failed at the handshake still reached a
 * machine and still took time, and those are the two things a person asks
 * about it first. The TLS fields are the response's, because a handshake
 * that did not complete negotiated nothing — so they are optional here and
 * simply absent on a run with no answer.
 */
export function connectionRawText(where: {
  remoteAddr: string
  dnsAddresses: readonly string[]
  /** How the request left this machine — a `connection` route resolves the
   *  name on the FAR side, which is why this line can be about somebody
   *  else's resolver. */
  routedThrough?: string
  tlsVersion?: string
  tlsCipherSuite?: string
  trust?: ApiTrust
}): string {
  const head = [address(where.remoteAddr), where.tlsVersion ?? '', where.tlsCipherSuite ?? '']
    .filter((s) => s !== '')
    .join('  ')
  // VERIFICATION OFF OVER A CHAIN THAT WOULD HAVE PASSED belongs here and
  // not in a badge: the setting is on, and nothing was accepted that would
  // not have been accepted anyway. It is a FACT about this connection,
  // printed beside the version and the suite that describe the same
  // handshake — a warning for it would be the noise this whole change
  // removes, and saying nothing at all would hide a switch somebody left on.
  const verification =
    where.trust?.state === 'unchecked-trusted'
      ? 'verified  no — the certificate was not checked, but it would have passed'
      : ''
  return [head, resolvedLine(where), verification].filter((line) => line !== '').join('\n')
}

/**
 * WHAT THE NAME RESOLVED TO — or where it was resolved, when it was not here.
 *
 * A request routed through a connection cannot resolve on this side at all:
 * the far side does it (apisend's ErrNameResolvedRemotely says so by name), so
 * this machine's resolver answered nothing and there is no list to print. That
 * was rendering as an absent line, which reads as "we did not look" rather
 * than "it was not ours to look" — the second is a fact about the route and
 * worth a sentence.
 */
function resolvedLine(where: { dnsAddresses: readonly string[]; routedThrough?: string }): string {
  if (where.dnsAddresses.length > 0) return `resolved  ${where.dnsAddresses.join(', ')}`
  if (where.routedThrough !== undefined && where.routedThrough !== '') {
    return `resolved  on ${where.routedThrough}, which is where this request left from`
  }
  return ''
}

/**
 * The address that answered, or '' when there is nothing to say.
 *
 * A TUNNELLED connection has no local socket to name and reports the zero
 * address, which was printing as `0.0.0.0:0` — a value that looks like an
 * answer and is the absence of one. The wildcard host with port 0 is the only
 * shape this rejects: a real address never has port 0.
 */
function address(remote: string): string {
  if (remote === '') return ''
  return /:0$/.test(remote) && /^(0\.0\.0\.0|\[::\]|::)/.test(remote) ? '' : remote
}

/**
 * WHETHER A RUN ACCEPTED SOMETHING IT WOULD OTHERWISE HAVE REFUSED.
 *
 * The one state a warning is for. The other three are not warnings and must
 * not be drawn as one: `verified` is the ordinary case, `none` had no chain
 * to judge, and `unchecked-trusted` is the quiet line in the connection
 * block above.
 *
 * A function rather than a comparison at the call site because it is the
 * predicate the badge exists for, and naming it is what stops the next
 * surface reaching for `route.insecureTls` again.
 */
export function acceptedUntrusted(trust: ApiTrust | undefined): boolean {
  return trust?.state === 'unchecked-untrusted'
}

/**
 * The badge's words: what was accepted, and why it would have been refused.
 *
 * The verifier's own sentence is passed through — it is already the sentence
 * a person wants, and a second vocabulary here would be one more thing to
 * keep in step with crypto/x509. Only the library's `x509: ` prefix comes
 * off: it names the package that spoke, which is not a fact about this
 * connection.
 */
export function untrustedSentence(trust: ApiTrust): string {
  const reason = trust.reason.replace(/^x509:\s*/, '').trim()
  return reason === '' ? 'unverified TLS' : `unverified TLS — ${reason}`
}

/**
 * WHAT THE PHASE MEANS, in the product's words rather than the wire's.
 *
 * The contract deliberately sends a closed vocabulary and not a sentence, so
 * that this exists: one place that turns a position into something a person
 * can act on. A run reading `dial` tells them nothing; "nothing accepted a
 * connection" tells them the host is not listening, which is a different
 * afternoon from "the name did not resolve".
 *
 * The switch is exhaustive over ApiPhase with no default, so a phase added
 * to the schema stops the build here rather than rendering as a raw token.
 */
export function phaseSentence(phase: ApiPhase): string {
  switch (phase) {
    case 'compose':
      return 'the request could not be built'
    case 'resolve':
      return 'the name did not resolve'
    case 'dial':
      return 'nothing accepted a connection'
    case 'connection':
      return 'the connection it routes through was not available'
    case 'tls':
      return 'the TLS handshake did not complete'
    case 'exchange':
      return 'the connection broke during the exchange'
    case 'timeout':
      return 'it ran out of time'
    case 'stopped':
      return 'you stopped it'
  }
}
