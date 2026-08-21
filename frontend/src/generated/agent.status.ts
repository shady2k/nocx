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
 * Result of the agent.status JSON-RPC method (design §7): whether the ANSWERING ROLE resolves to an (endpoint, model) pair, the credential fact of the endpoint that resolution named, and the last probe result. Readiness is a fact about the ROLE, never about endpoint existence (nocx-rikz5): an endpoint with a valid key and no model chosen is not ready, and said so here rather than at the person's first question. The ask surface reads it before offering an ask; a soft degrade — no endpoint, no model chosen, an unresolvable credential, a failed probe — is visible in the product, never only in a log. The probe result shape is declared ONCE, in endpoints.probe.schema.json, and referenced here cross-file.
 */
export interface AgentStatusResult {
  /**
   * At least one AI endpoint is stored. Endpoint EXISTENCE, and nothing more: it is not readiness and must not be read as readiness — that is `answering`. Kept because the Endpoints page's own empty state is a different question from the ask surface's.
   */
  endpointConfigured: boolean
  /**
   * The one credential fact (ADR-0032) of the endpoint the answering role RESOLVED to, and of no other. Null whenever the role does not resolve — there is then no endpoint the question is about, and reporting some other endpoint's fact would be a sentence about an endpoint nobody chose. 'resolvable' means the vault can currently resolve that endpoint's credential; 'sealed' means the vault cannot answer right now (the unlock offer); 'deleted' means the referenced secret is gone; 'none' means the endpoint has no reference at all; 'unavailable' is the honest fallback for a store failure that is none of those.
   */
  credential: ('resolvable' | 'none' | 'deleted' | 'sealed' | 'unavailable') | null
  /**
   * The last endpoints.probe outcome, or null when none has run in this process lifetime, or when the one that ran does not describe the CURRENT resolution — a probe names one endpoint and one model, and 'Last test ok' about a different model is a lie the person cannot see through. Process-lifetime by design: a probe's meaning expires with the endpoint that produced it.
   */
  lastProbe: EndpointsProbeResult | null
  /**
   * The resolution of the role the ask will use (profile.RoleAnswering). Never absent: readiness is the question the ask surface asks, so every status carries either a resolution or the reason there is none, and the reason is the rung of the ladder the person is on.
   */
  answering: {
    /**
     * True when the answering role resolves to an (endpoint, model) pair. This — not endpointConfigured — is what the ask surface gates on.
     */
    ready: boolean
    /**
     * Why it does not resolve, null when it does. 'no-endpoints': nothing is configured, and sending a person to choose from an empty list is the one answer worse than saying nothing. 'no-models': an endpoint exists and offers zero models, so 'choose a model' would open an empty picker. 'unassigned': the role has no assignment and no default — the person has a model to choose and has not chosen it. 'endpoint-gone' / 'model-gone': what was chosen no longer exists, and the role is never silently re-pointed at a neighbour. 'unavailable': a store could not answer — a reported rung, never an RPC error, because an error toast leaves a person with nothing to do next.
     */
    reason:
      | (
          | 'no-endpoints'
          | 'no-models'
          | 'unassigned'
          | 'endpoint-gone'
          | 'model-gone'
          | 'unavailable'
        )
      | null
    /**
     * Display name of the endpoint that will answer, null when the role does not resolve. The name, not the id: it is shown to a person.
     */
    endpoint: string | null
    /**
     * Model id that will answer, null when the role does not resolve.
     */
    model: string | null
  }
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
