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
 * Result of the api.request.send JSON-RPC method: ONE EXCHANGE, which exists from the moment Send is pressed and ends `answered`, `failed` or `stopped`. It is not "the response", and the difference is the whole shape of this file. A run that WAS its answer could not exist before the answer did, so a request in flight had nothing to show, a running exchange had nothing to name it by, and a failure was not a run at all — it came back as a JSON-RPC error, one sentence, while the sender was holding the composed request, the route, the phases and the remote address at the moment it failed. Everything the attempt reached is here whatever the outcome; only `response` waits for an answer. The body is captured behind the 2 MiB ceiling files.read already uses (design §12.3), so a response larger than that arrives truncated rather than not at all. What is still a JSON-RPC error is what is not an exchange at all: an unknown handle, a request file that will not read, an auth variable nothing can resolve.
 */
export interface ApiRequestSendResult {
  /**
   * How this exchange ended. `answered` a response came back and was read; `failed` it did not; `stopped` the person who started it stopped it. Three and not two, because a stop is not a failure — it is the answer arriving on purpose — and a surface that had to infer the difference from a reason string would word and tone somebody's own Stop as something that went wrong.
   */
  outcome: 'answered' | 'failed' | 'stopped'
  request: Raw
  /**
   * What came back — NULL unless the outcome is `answered`. Null is the honest shape: a failed exchange has no status, no headers and no body, and a zeroed response would render as an HTTP 0 with an empty body, which is a lie the renderer cannot tell from a real one.
   */
  response: Response | null
  /**
   * Why it ended — NULL exactly when the outcome is `answered`, and present for `failed` and `stopped` alike. See $defs/failure: the outcome decides how it reads, the phase says how far it got.
   */
  failure: Failure | null
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
  /**
   * The address that actually answered the dial, and "" when nothing did. It is on the EXCHANGE rather than on the response because it is a fact about the attempt: a run that failed at `tls` still reached a machine, and which machine it reached is the first thing anybody asks. Its presence is also what distinguishes a dial that never landed from a connection that broke later, which is how the phase is classified.
   */
  remoteAddr: string
  /**
   * What the resolver ANSWERED for the host, in the order it answered — the order the dialler tries, so re-sorting it would describe a lookup nobody made. Never null: no lookup to make (an address literal, or a route that resolves on the far side) and a lookup that FAILED are both []. It is beside remoteAddr rather than folded into it because the two answer different questions: remoteAddr is which address answered, and this is what the name stands for — a name with four A records says the one that answered was one of four, and a name that resolves to a stale address says why a request went somewhere nobody expected. Recorded from the attempt rather than looked up again by whoever reads the run: a second lookup a second later can legitimately differ.
   */
  dnsAddresses: string[]
  timings: Timings
  /**
   * The chain the SERVER PRESENTED, leaf first, described by the side that saw the bytes. Never null: a plain http exchange presents none and that is []. It is EMPTY for a failure at phase `tls`, and that is a real limit rather than an oversight — net/http hands the trace an empty connection state when the handshake fails, and recovering the chain from a rejected handshake would mean turning verification off and re-implementing it here, which is the second X.509 implementation apisend.Certificate exists to refuse.
   */
  certificates: Certificate[]
}
/**
 * ONE SIDE of the exchange, already segmented. A secret's VALUE never appears here in any state: the bytes are elided and a placeholder naming the secret takes their place, so a renderer that ignores spans entirely still shows no credential (§11.2, ADR-0011).
 *
 * The two sides live at two LEVELS rather than in a pair, because they are known at two different times. `request` is on the exchange: the sender composes it and places its spans BEFORE it dials, so it is PRESENT WHATEVER THE OUTCOME — dropping it on the failure path is what left a person reading "connection refused" with no way to see the address, the headers or the body they had just sent. It is empty, with no spans, only at phase `compose`, where there was nothing to compose. `response.raw` is the other side and exists only when something answered, which is why it is inside `response`: a pair type would have had to sit at one level or the other, and either way it would have taken the request text away from exactly the runs that need it most (§11.3).
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
  tlsVersion: string
  raw: Raw
  /**
   * The negotiated cipher suite's name, and "" when the exchange was not over TLS.
   */
  tlsCipherSuite: string
  trust: Trust
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
 * WHAT VERIFICATION SAYS about the chain this exchange accepted — never the environment's SETTING, which is what `route.insecureTls` is and what this replaces on the run.
 *
 * The badge a surface draws from it used to come from that setting, so it appeared on every run under an environment with verification off — including a public host with an ordinary chain, in the same colour and words a self-signed development host would get. A warning that is on most of the time is a warning nobody reads, and the one run where it matters looks exactly like the twenty where it did not.
 *
 * What it means now is WE ACCEPTED SOMETHING THAT WOULD OTHERWISE HAVE BEEN REFUSED, which is knowable: the handshake completed, the chain is in hand, and the backend asks crypto/x509's own verifier — the presented intermediates, the host name, this build's roots — whether it verifies. It USES the verifier and does not become a second one; the renderer still parses no X.509 and reads a state and a sentence.
 *
 * It is HERE, beside tlsVersion and tlsCipherSuite, because like both of those it is a fact of a handshake that COMPLETED and exists exactly when they do. One level up it would be the only TLS fact to survive a run whose exchange broke after its handshake, with no version beside it to attach to.
 */
export interface Trust {
  /**
   * Four answers, because there are four different things to say. `none` there is nothing to say — no TLS, or a handshake that never completed. `verified` the handshake verified the chain before it would speak at all: the ordinary case, and nothing to report. `unchecked-trusted` verification was OFF and the chain verifies anyway — worth a quiet line beside the TLS version and never a warning, because nothing was accepted that would not have been accepted regardless. `unchecked-untrusted` verification was OFF and the chain does NOT verify: this is the one a warning is for, and `reason` says which.
   */
  state: 'none' | 'verified' | 'unchecked-trusted' | 'unchecked-untrusted'
  /**
   * Why the chain would have been refused, in the verifier's own words — "certificate signed by unknown authority", "certificate has expired or is not yet valid", "certificate is valid for a, not b". Empty in every other state. Passed through rather than reworded: the verifier's sentence IS the sentence a person wants, and a second vocabulary would be one more thing to keep in step with the standard library.
   */
  reason: string
}
/**
 * How the attempt ended when it did not answer. It is present for a `failed` AND for a `stopped` exchange, because both are asks about the same thing — how far did it get — and only the outcome says how to read it: a phase is a POSITION on the way to an answer, not a verdict. The reason is the backend's own words, already redacted (apisend.redact): userinfo and the query string never reach it.
 */
export interface Failure {
  /**
   * WHERE it stopped, as a closed set, so the renderer picks its own sentence rather than parsing prose. `compose` the request could not be built at all — an address that will not parse — and it is the one phase with no request text, because there was none to compose. `resolve` the name did not resolve. `dial` nothing accepted a connection. `connection` the route itself was unavailable: no lease on the SSH profile, or a name only the far side can resolve. `tls` the handshake did not complete. `exchange` the connection was open and the exchange broke on it — a truncated body, a malformed response. `timeout` a bound elapsed. `stopped` the person pressed Stop.
   */
  phase: 'compose' | 'resolve' | 'dial' | 'connection' | 'tls' | 'exchange' | 'timeout' | 'stopped'
  /**
   * What went wrong, in the backend's words. A sentence for a person and never a code to branch on — that is what `phase` is for.
   */
  reason: string
}
/**
 * The phases of the attempt, AS FAR AS IT GOT — a phase never reached is 0, which is the honest answer rather than an absence. On the exchange for the same reason remoteAddr is: a failed run took time too, and how long it spent before it gave up is the difference between a refusal and a hang.
 */
export interface Timings {
  dnsMs: number
  connectMs: number
  tlsMs: number
  ttfbMs: number
  totalMs: number
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
