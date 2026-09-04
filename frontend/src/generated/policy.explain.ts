/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.explain.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the policy.explain JSON-RPC method: what the ONE global agent policy decides about a command, and every step the evaluator took to decide it, IN THE ORDER IT TOOK THEM. The trace is recorded by the evaluator as it evaluates; nothing derives one from the outcome, on either side of the wire. A renderer that worked the order out from the policy document would be a second implementation of it, agreeing everywhere anyone looked and disagreeing somewhere nobody did.
 */
export interface PolicyExplain {
  /**
   * The effect class the explanation was asked for — the row that governs the call.
   */
  effect:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * What the policy decides for this call: the outcome the trace explains, and the same decision the LAST trace step carries.
   */
  decision: 'permit' | 'ask' | 'refuse'
  /**
   * Why a resource fell outside, when one did. Absent when the resource layer found nothing outside. The two values are different products: 'row-scope' is a question a person can answer by widening the row, 'fence' is an immutable bound no answer reaches, so a surface offers an expansion for one and never for the other.
   */
  cause?: 'row-scope' | 'fence'
  resource?: Scope
  /**
   * The steps the evaluator took, in the order it took them. It is complete: it opens on the first thing the evaluator did and closes where the verdict returned, so a reader never has to wonder whether a step is missing off the end. Always an array.
   *
   * @minItems 1
   */
  trace: [Step, ...Step[]]
}
/**
 * The ONE resource that fell outside, in the scope form a row states it in — so a widening answer can be written from this result alone. Present exactly when 'cause' is.
 */
export interface Scope {
  /**
   * The resource kind from the ledger's closed set. 'tool' is deliberately absent: the policy is over resources and effects, never over tool names (ADR-0028 decision 4), and an explanation of it inherits that vocabulary.
   */
  kind: 'environment' | 'session' | 'path' | 'credential' | 'destination'
  /**
   * The scope's id: an absolute path for kind 'path', an endpoint 'scheme://host[:port]' or the universal '*' for kind 'destination', an opaque identity otherwise.
   */
  id: string
  /**
   * Destination scopes only, and absent when false.
   */
  includeSubdomains?: boolean
}
/**
 * ONE thing the evaluator did. A step names effects, rules and resources, and NEVER a tool. Every field but 'kind' is absent when this step is not about it.
 */
export interface Step {
  /**
   * What the evaluator did, in the closed set of things it can do — three groups, in the order the layers are crossed. EFFECT layer: 'unparsed' (the command could not be read, so no row and no rule could speak about it); 'effect-row' (the matrix row for this effect was consulted, carrying the decision it holds); 'row-refuses' (the row refuses, and a rule is an exception to the effect layer alone, so the rules were NOT READ — a standing permit was never reached, which is a different fact from losing); 'disqualified' (the command's own text puts it beyond a rule's reach, so it takes the row's answer and the rules were NOT READ). SHAPE layer: 'rule-matched' (a rule covered this invocation and counted); 'rule-stale' (a loose permit saved under an earlier reading of commands, inert until a person re-reads what it now means); 'rule-other-effect' (a widening permit granted under a different effect than this call classified as, so it does not reach the call). RESOURCE layer: 'resource-inside' (every named resource lay inside the fence and the row's scopes); 'resource-outside-fence' (outside an immutable bound — refuse, and no answer widens it); 'resource-outside-row-scope' (outside the row's own editable scopes — ask, and widening the row would make the same call run); 'resource-not-reached' (the decision was already a refusal, and the resource layer can only narrow, so no resource was compared).
   */
  kind:
    | 'unparsed'
    | 'effect-row'
    | 'row-refuses'
    | 'disqualified'
    | 'rule-matched'
    | 'rule-stale'
    | 'rule-other-effect'
    | 'resource-inside'
    | 'resource-outside-fence'
    | 'resource-outside-row-scope'
    | 'resource-not-reached'
  /**
   * The rule this step is about, on the three rule steps. It is the same id policy.setRule minted and policy.forgetRule names, so a surface can offer to act on the rule it just explained.
   */
  ruleId?: string
  /**
   * The row's effect on 'effect-row' and on the resource steps, and the effect a widening permit was GRANTED UNDER on 'rule-other-effect' — which is the half a person needs in order to see the gap between what was granted and what was called.
   */
  effect?:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * What this step decided or read: the row's decision on 'effect-row', the rule's own on a rule step, the standing decision on a resource step.
   */
  decision?: 'permit' | 'ask' | 'refuse'
  /**
   * Prose for what the typed fields cannot carry — the two readings of commands a stale rule sits between, or why a bound cannot be widened. It never names a tool.
   */
  detail?: string
}
