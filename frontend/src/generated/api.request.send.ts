/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.request.send.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.request.send JSON-RPC method: what came back from one exchange, bounded. The body is captured behind the 2 MiB ceiling files.read already uses (design §12.3), so a response larger than that arrives truncated rather than not at all.
 */
export interface ApiRequestSendResult {
  response: Response
  /**
   * The NAME of the environment this exchange actually went out under, as the environment FILE declares it — and "" when the send named none, which is the request as written on the direct route. It is the backend's own account rather than an echo of the caller's `envRelPath`: the renderer names an environment by its path and the name is read out of the file at the moment the address and the route are (capability.SendInputs), so this is the one field that says which record answered. A run list drawn from what the renderer BELIEVED it asked for is the vault.status defect in reverse — a value written by one side and never read back from the other.
   */
  environment: string
  /**
   * HOW this exchange got there: the route read off the same environment record the address came from (design §6.5), never an echo of anything the caller sent. `direct` left from this machine; `connection` left through the named SSH profile's pooled connection — the same one a terminal tab uses, authorized the same way. A file that omits the route reads as direct, and the zero value is spelled out here rather than sent as "", so the renderer meets one spelling of one state. The profileId is an ID and not a name: the name belongs to whoever owns connections, and the renderer turns one into the other for display.
   */
  route: {
    kind: 'direct' | 'connection'
    profileId: string
    /**
     * Send WITHOUT verifying the server's certificate — for a development host with a self-signed certificate. It is on the ROUTE because it is part of how a destination is reached, and it is per ENVIRONMENT rather than per app on purpose: a person turns it on for dev and cannot thereby turn it off for production, which is what a global switch would do the moment they forgot it was set. It is written into the collection file, so a colleague sees it in review, and every run that went out under it says so on the run — it can never be quietly on.
     */
    insecureTls: boolean
  }
}
export interface Response {
  status: number
  /**
   * Never null — a response with no headers is [].
   */
  headers: Header[]
  /**
   * The decoded body, always valid UTF-8, and EMPTY when binary: the run says "binary body, N bytes" and never base64.
   */
  text: string
  /**
   * A NUL byte among the bytes actually read — the files.read heuristic, labelled as one.
   */
  binary: boolean
  /**
   * The bytes read were not valid UTF-8 and invalid sequences were replaced. Distinct from binary: a NUL-free latin-1 body is lossy text, not a binary body. Three separate facts because they are three separate sentences in the run, and collapsing any two of them loses one.
   */
  lossy: boolean
  /**
   * The ceiling was reached and one further byte was readable — the body shown is a prefix.
   */
  truncated: boolean
  /**
   * Body bytes actually read and kept, which is the ceiling when truncated. Not a claim about what the server holds: a Content-Length can lie and a chunked response declares nothing.
   */
  size: number
  timings: Timings
  tlsVersion: string
  remoteAddr: string
  raw: Exchange
  /**
   * The negotiated cipher suite's name, and "" when the exchange was not over TLS.
   */
  tlsCipherSuite: string
  /**
   * The chain the SERVER PRESENTED, leaf first — not the chain that was verified, which is a different list and is empty when verification was off. It is the answer to the only question left once an environment accepts self-signed certificates: which certificate did I actually just trust. Described by the side that saw the bytes and never sent as DER: a renderer that parsed certificates would be a second X.509 implementation, and the fingerprint — the field somebody compares against a value read out over the phone — must be computed once. Never null: a plain http exchange presents none and that is [].
   */
  certificates: Certificate[]
}
export interface Header {
  name: string
  value: string
  /**
   * A disabled row is a row the user keeps: deleting it to turn it off loses the value they will want back.
   */
  enabled: boolean
}
/**
 * The phases of one exchange, in milliseconds. On a redirect chain dns, connect and tls describe the LAST hop — the one the body came from — ttfb is measured from the start of the exchange, and total adds the body read. A reused connection has a zero dns and connect, which is the honest answer: nothing was resolved and nothing was dialled.
 */
export interface Timings {
  dnsMs: number
  connectMs: number
  tlsMs: number
  ttfbMs: number
  totalMs: number
}
/**
 * The raw text of both sides, segmented (design §11). It rides on the send result rather than on a method of its own because the raw text belongs to a PARTICULAR run — this exchange, these substitutions, this truncation — so a second round trip could only fetch the raw of a different send. The two sides are two fields because they are two mechanisms with two different guarantees (§11.3): the request side verifies placements the sender itself made, the response side runs a bounded known-plaintext search.
 */
export interface Exchange {
  request: Raw
  response: Raw
}
/**
 * One side of the exchange, already segmented. A secret's VALUE never appears here in any state: the bytes are elided and a placeholder naming the secret takes their place, so a renderer that ignores spans entirely still shows no credential (§11.2, ADR-0011).
 */
export interface Raw {
  /**
   * The text as it crosses, AFTER elision. Bounded by the same 2 MiB ceiling as the captured body.
   */
  text: string
  /**
   * Never null — a side with nothing to mark is []. The spans tile the text in order with neither gap nor overlap, so a renderer draws the whole payload by walking them.
   */
  spans: Span[]
}
/**
 * One run of the raw text. Three kinds, never two (§11.1): the bytes still equal the secret (secret, named — the badge is evidence rather than a curtain); our span but the bytes differ (secret-damaged, naming the SHAPE of the damage and never its bytes, because a truncated token is a prefix of a live one); or not our span at all (text).
 */
export interface Span {
  /**
   * Byte offset into text where this run starts.
   */
  from: number
  /**
   * Byte offset into text where this run ends.
   */
  to: number
  /**
   * Which of the three states this run is.
   */
  kind: 'text' | 'secret' | 'secret-damaged'
  /**
   * The NAME of the secret, never its value. Empty for a text run.
   */
  name: string
  /**
   * The shape of the damage — "truncated, 24 of 214 bytes" — carrying only lengths, because the surviving bytes are the beginning of a live credential. Empty unless the kind is secret-damaged.
   */
  damage: string
}
export interface Certificate {
  subject: string
  issuer: string
  /**
   * RFC 3339, UTC.
   */
  notBefore: string
  /**
   * RFC 3339, UTC.
   */
  notAfter: string
  /**
   * The SANs, which is what a host name is actually checked against — the CN in the subject has not been the answer since 2017, and showing it alone is how somebody concludes a certificate is fine when the host is not on it. Never null.
   */
  dnsNames: string[]
  /**
   * The IP SANs. Never null.
   */
  ipAddresses: string[]
  /**
   * Subject and issuer are the same name. A description of THIS certificate and never a verdict about the connection: a self-signed leaf is exactly what an environment that accepts self-signed certificates is for.
   */
  selfSigned: boolean
  /**
   * SHA-256 of the DER, lower-case hex in colon-separated pairs — the spelling `openssl x509 -fingerprint -sha256` prints, so the value on screen compares with the one in a terminal without either being reformatted.
   */
  fingerprint: string
}
