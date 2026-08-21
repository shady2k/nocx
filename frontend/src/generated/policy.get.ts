/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.get JSON-RPC method (ADR-0020 §7 as amended 2026-08-16, accepted): the ONE global agent policy, resolved (a workspace override will participate in the same content.ResolvePolicy the run mint uses). The policy is a MATRIX: one row per effect class of decision 6, each row one of permit | ask | refuse plus the resource scopes it applies within. The wire always carries all seven rows with their EFFECTIVE decisions (an unstated row decides ask) and a scopes array, never a null — a renderer can draw the whole matrix from the bytes.
 */
export interface PolicyGet {
  /**
   * The effect classes at least one DECLARED tool carries — the registry's declaration table is the only thing that knows. A row outside this list governs nothing yet, and the surface says so rather than offering it as an equal to the rows that do. Always an array, never a null; deduplicated, in the lattice's order.
   */
  live: (
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  )[]
  policy: {
    observe: Row
    'mutate-reversible': Row1
    'mutate-destructive': Row2
    'privilege-change': Row3
    disclose: Row4
    'cross-boundary': Row5
    delegate: Row6
  }
}
/**
 * observe — reading files and screens.
 */
export interface Row {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
export interface Scope {
  /**
   * The resource kind from the ledger's closed set. 'tool' is deliberately absent: a tool-kind scope is a rule over a tool name.
   */
  kind: 'environment' | 'session' | 'path' | 'credential' | 'destination'
  /**
   * The scope's id: an absolute path for kind 'path', an opaque identity otherwise.
   */
  id: string
}
/**
 * mutate-reversible — changes that can be undone.
 */
export interface Row1 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
/**
 * mutate-destructive — changes that cannot be undone.
 */
export interface Row2 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
/**
 * privilege-change — elevation, su/sudo domains.
 */
export interface Row3 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
/**
 * disclose — secrets, credentials, vault material.
 */
export interface Row4 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
/**
 * cross-boundary — reaching a host the lane was not on.
 */
export interface Row5 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
/**
 * delegate — launching further agents or workers.
 */
export interface Row6 {
  /**
   * What a run may do for this effect class within the row's scopes. Unset rows decide ask (fail toward asking); never permit.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The resource scopes the decision applies within: a call naming a resource outside the row's scopes is refused. The rule that survives any amount of flexibility: no scope may name a TOOL — the policy is over resources and effects, never over tool names (ADR-0028 decision 4).
   */
  scopes: Scope[]
}
