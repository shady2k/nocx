/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/roles.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Every product role for one agent. A null model uses the agent default, pinned on the first conversation turn.
 */
export interface RolesListResult {
  /**
   * Every role of the closed set, in product order. Never null: an empty store still lists the roles, null-assigned.
   */
  roles: Role[]
  agentId: string
}
export interface Role {
  /**
   * The role name, a closed enum: 'answering' is the model the assistant speaks with; 'classifier' is the second model judging proposed tool calls; 'summarizing' reads a transcript and writes a short text from it; 'auditing' reads a document somebody else wrote and describes it back to the person. The classifier, summarizing and auditing roles stay declared with no consumer in this build, so a feature that asks for one later adds its resolution rather than the table.
   */
  role: 'answering' | 'classifier' | 'summarizing' | 'auditing'
  /**
   * The chosen model id for this agent, or null for its own default.
   */
  model: string | null
}
