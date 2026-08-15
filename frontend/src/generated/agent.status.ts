/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agent.status JSON-RPC method (design §7): endpoint configured, the credential fact, last probe result. The ask surface reads it before offering an ask; a soft degrade — no endpoint, an unresolvable credential, a failed probe — is visible in the product, never only in a log. The probe result shape is declared ONCE, in endpoints.probe.schema.json, and referenced here cross-file.
 */
export interface AgentStatusResult {
  /**
   * At least one AI endpoint is stored (design §7: 'endpoint configured').
   */
  endpointConfigured: boolean
  /**
   * The one credential fact (ADR-0032): which of the distinguishable states is true, so each gets its own sentence instead of all three reading 'the vault may be locked'. Null only when no endpoint is configured — there is nothing to ask about. 'resolvable' means at least one stored endpoint's credential the vault can currently resolve; 'sealed' means the vault cannot answer right now (the unlock offer); 'deleted' means the referenced secret is gone; 'none' means the endpoint has no reference at all; 'unavailable' is the honest fallback for a store failure that is none of those.
   */
  credential: ('resolvable' | 'none' | 'deleted' | 'sealed' | 'unavailable') | null
  /**
   * The last endpoints.probe outcome, or null when none has run in this process lifetime. Process-lifetime by design: a probe's meaning expires with the endpoint that produced it.
   */
  lastProbe: EndpointsProbeResult | null
}
/**
 * Result of the endpoints.probe JSON-RPC method — the Test button. TWO checks live behind that button (nocx-q27y) and `kind` names which one ran: with a model, a real streaming completion through the same engine the ask transaction uses; without one, a connection check that only establishes the endpoint is reachable and the credential accepted, which needs no model and is the only question askable of an endpoint nobody has typed a model into yet. The params are the form's DRAFT values (name, baseUrl, key, model, endpointId) — the endpoint may not be saved yet, the key is an input that never crosses back (ADR-0030), and when the key is blank the backend resolves the named endpoint's own credential. This schema is the SINGLE declaration of the probe result shape: agent.status's lastProbe references this whole file cross-file.
 */
export interface EndpointsProbeResult {
  /**
   * The probed draft's display name. Historical fact: agent.status reports the last probe whatever the endpoint list says now.
   */
  name: string
  /**
   * The model id that was probed — the form tests its first model. Empty for a connection check, which has none by definition.
   */
  model: string
  /**
   * Which check produced this result. 'model' streamed a real completion from the named model; 'connection' only established that the endpoint is reachable and the credential accepted. They are different facts and a person acts on them differently, so the result states which it is rather than leaving it to be inferred from an empty model.
   */
  kind: 'model' | 'connection'
  /**
   * True when the check succeeded: for 'model', the endpoint streamed at least one content chunk; for 'connection', the endpoint was reached and did not reject the credential. A 404 to GET /models is a SUCCESSFUL connection check — GET /models is not universally implemented, and reaching a server that has no such route is not a failure to reach it.
   */
  ok: boolean
  /**
   * Model ids a connection check found the endpoint offering. ALWAYS an addition, never a gate: an endpoint that lists nothing is reachable, usable, and must stay configurable by hand. Absent for a model check, and absent for an endpoint that lists nothing.
   */
  models?: string[]
  /**
   * What went wrong when ok is false: the dial failure, the HTTP status, the refused stream, zero content. Absent when ok.
   */
  error?: string
  /**
   * Total wall time of the probe, dial to end of stream.
   */
  elapsedMs: number
  /**
   * When the probe finished, wall-clock (RFC 3339).
   */
  at: string
}
