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
   * The ids of the rules that are INERT until a person re-reads what they now mean: a loose permit saved under an earlier reading of commands (design §5.6, content.RulesNeedingConfirmation). The backend answers this for the same reason it answers 'live' — the predicate is three facts about a rule joined to the reading of commands running NOW, which is a version number no result carries and which a renderer must never compare for itself. A second implementation of it in the renderer would agree everywhere anyone looked and disagree somewhere nobody did, while telling a person their permission works. Always an array, never a null; ids in document order.
   */
  awaitingReview: string[]
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
    /**
     * Invocation-specific standing answers. Rules never name tools and match the canonical command invocation.
     */
    rules?: Rule[]
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
   * The scope's id: an absolute path for kind 'path', an endpoint 'scheme://host[:port]' or the universal '*' for kind 'destination', an opaque identity otherwise.
   */
  id: string
  /**
   * Destination scopes only, and absent when false: the grant covers the endpoint's subdomains as well, matched label-wise so 'notgithub.com' and 'github.com.evil.example' stay outside a grant over 'github.com' (design §5.4). Refused on an IP literal, which has no subdomains, and on '*', which already covers every address.
   */
  includeSubdomains?: boolean
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
/**
 * One standing answer over the canonical command invocation, with the provenance that makes it an object a person can take back (design §5.6). A rule NEVER names a tool (ADR-0028 decision 4): it names a command word in a parsed invocation, which is a different thing.
 */
export interface Rule {
  /**
   * The rule's stable identity, minted by the backend and never supplied by the renderer (AD-7). It is what a page changes, confirms or forgets the rule by.
   */
  id: string
  /**
   * WHICH invocations the rule speaks about, and nothing about what it decides: exactly one of the three is set. 'exact' names a fixed command line positionally and is the only form a person's answer to a prompt can save; 'program' covers every argument list of one command word and may only permit while bound to the effect it was granted under; 'hasFeature' matches a semantic fact the CLASSIFIER recorded and may never permit.
   */
  selector: {
    /**
     * A fixed number of subcommands and a fixed number of tokens in each, matched positionally. A '*' in a token matches any contents of that one token and never spans a token boundary or a shell separator.
     *
     * @minItems 1
     */
    exact?: [[string, ...string[]], ...[string, ...string[]][]]
    /**
     * One command word, carrying ANY arguments.
     */
    program?: string
    /**
     * One command word carrying one feature of the classifier's CLOSED vocabulary. Both halves are required: a feature alone would speak for every program that can carry it.
     */
    hasFeature?: {
      program: string
      /**
       * The closed feature vocabulary content owns. 'writes-option-named-path': the command writes a file to a path named by one of its own options rather than by an operand or a shell redirection.
       */
      feature: 'writes-option-named-path'
    }
  }
  /**
   * What the rule decides for the invocations its selector covers. Among overlapping matching rules the most restrictive wins, and a rule is an exception to the EFFECT layer alone — never to a refusing row, and never past the resource layer.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * The effect class a widening permit was granted for, checked against the effect the CALL classified as. Absent on a rule that does not widen. It is what stops a permit written while a program was reading from reaching the same program deleting.
   */
  grantedUnder?:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * When the rule came into being, RFC 3339. The zero time ('0001-01-01T00:00:00Z') means a document stated none and none was invented.
   */
  createdAt: string
  /**
   * Where the rule came from: 'answered' — minted from a person's answer to a prompt, over the exact command line they were shown; 'written' — written into the policy document, which is what an unstated source parses as. The two are different objects with different trust and a page must be able to tell them apart.
   */
  source: 'answered' | 'written'
  /**
   * The reading of commands the rule was saved under. A rule with a LOOSE selector (program or hasFeature) whose version is not the current one does not apply: it was agreed to on an account of what the command does that no longer holds, and it is shown as needing confirmation until a person re-confirms it. An exact rule is unaffected — it names the literal command line the person was shown.
   */
  evaluatorVersion: number
}
