/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/endpoints.update.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the endpoints.update JSON-RPC method: the stored endpoint after the replace (design §4.5.4, ADR-0030). A key sent in the params rotates the material behind the endpoint's own secret — same reference, never orphaned — and the result carries the unchanged row handle. The shape is the single declaration in endpoints.list.schema.json, referenced cross-file.
 */
export interface EndpointsUpdateResult {
  endpoint: Endpoint
}
export interface Endpoint {
  /**
   * Backend-minted identity: endpoint:custom:<slug>:<uuid>. The renderer may hold it but never mints it.
   */
  id: string
  /**
   * The display name the picker shows.
   */
  name: string
  /**
   * Absolute http(s) URL. Parse-level validated only this pass; the loopback/private policy is nocx-edio (design §4.5).
   */
  baseUrl: string
  /**
   * The wire schema, a closed enum: 'openai-compatible' is the ONE value this pass knows (design §4.5, decision 2). A select in the UI appears when the second value does.
   */
  schema: 'openai-compatible'
  /**
   * True only when the person explicitly declared that this endpoint needs no API key. False preserves the credential-required default; this is not inferred from the URL.
   */
  noKey: boolean
  /**
   * Row handle (secrow:...) of the endpoint's own vault secret, or null when no key is set (created without one, or the key was deleted on the Secrets page). A reference never crosses the wire.
   */
  credential: string | null
  /**
   * One or more models the endpoint offers. Never null: an endpoint without models is invalid.
   */
  models: {
    /**
     * The model id the API understands.
     */
    name: string
    /**
     * The picker label, or null to show the model id.
     */
    alias: string | null
  }[]
  /**
   * The endpoint's custom HTTP headers (nocx-lyyk), sent on every request it makes. Never null: an endpoint with no custom headers sends []. Each row names a header and carries EXACTLY ONE value source: a literal value, or the row handle (secrow:...) of a vault secret — never the material, and never the reference itself (ADR-0017 §1).
   */
  headers: {
    /**
     * The header name. Refused names (Authorization, Host, Content-Length, Content-Type, the hop-by-hop set) are rejected at write time with the reason.
     */
    name: string
    /**
     * The literal header value, or null when the value is a vault secret. An empty literal is legal HTTP and stays "".
     */
    value: string | null
    /**
     * Row handle of the vault secret the value resolves to at request time, or null for a literal value. A reference never crosses the wire.
     */
    secret: string | null
  }[]
}
