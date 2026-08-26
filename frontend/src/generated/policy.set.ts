/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.set.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.set JSON-RPC method (ADR-0020 §7 as amended 2026-08-16): acknowledges that the ONE global agent policy was validated and persisted. The params side carries the same effect-keyed matrix policy.get returns and is checked by the handler (content.ParseEffectPolicy — unknown keys, a tool name as a row key or a tool-kind scope are invalid params, so no configuration path can express a rule over a tool name).
 */
export interface PolicySet {
  /**
   * Always true on a successful set; an error response means the policy was not accepted.
   */
  ok: true
}
