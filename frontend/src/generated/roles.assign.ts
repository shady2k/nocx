/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/roles.assign.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the roles.assign JSON-RPC method (bead nocx-e6kn2): the full role table AFTER the write — the same shape roles.list declares, referenced cross-file. The write upserts one role's (endpoint, model) pair, or CLEARS it when both endpointId and model are absent; the result is the single table the renderer renders from, so the row and the response can never disagree.
 */
export interface RolesAssignResult {
  /**
   * Every role of the closed set, in product order, with the assignment just written (or cleared).
   */
  roles: Role[]
  /**
   * The one (endpoint, model) pair every role with no assignment of its own resolves through (bead nocx-rikz5). Null when the person has chosen none — which is the state a fresh profile is in, and the state in which the assistant is not ready. It is never a pair the product picked: a default the product invented is the silent fallback nocx-e6kn2 forbids. Declared here once and referenced cross-file by roles.assign, whose result is this same table after a write; roles.setDefault returns this shape too.
   */
  default: {
    /**
     * The default endpoint's backend-minted id. The endpoint EXISTS at the moment of the write — roles.setDefault refuses an id naming no endpoint, because a dangling default breaks every unassigned role at once with nothing on screen naming which choice did it.
     */
    endpointId: string
    /**
     * The default model id the endpoint's API understands (never the picker alias). A model that later disappears leaves the default unresolvable and says so; it is never repaired into a neighbouring model.
     */
    model: string
  } | null
}
export interface Role {
  /**
   * The role name, a closed enum: 'answering' is the model the assistant speaks with; 'classifier' is the second model judging proposed tool calls (its own bead).
   */
  role: 'answering' | 'classifier'
  /**
   * The assigned endpoint's backend-minted id, or null when the role has no assignment.
   */
  endpointId: string | null
  /**
   * The assigned model id the endpoint's API understands (never the picker alias), or null when the role has no assignment.
   */
  model: string | null
}
