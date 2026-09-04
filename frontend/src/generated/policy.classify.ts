/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/policy.classify.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * One member of the closed feature vocabulary content owns, because content owns the rules that match it.
 */
export type Feature = 'writes-option-named-path'

/**
 * Result of the policy.classify JSON-RPC method: the READING of one command line that has not been run, so a widening permit can be minted from a classification rather than from a word somebody liked the look of. A permit over a loose selector is a claim about what a command does, and a person typing 'find' into a box has made no such claim — 'find . -delete' is a destructive call wearing a read-shaped word. The backend therefore reads the command with the same parser and the same classifier a run uses, and the rule the caller then writes carries the effect this reading found. It carries no decision: what the policy currently decides is policy.explain's question, and an answer from before the change the caller is about to make would be read as an answer about after it.
 */
export interface PolicyClassify {
  /**
   * The command word a 'program' selector would name — the one loose shape a permit may take. It comes from the backend rather than from the caller because splitting a command line into words is a parser, and a second parser in the renderer would offer a permit for one reading of the command while the enforcement had another. Empty exactly when 'eligible' is false.
   */
  program: string
  /**
   * The canonical parse an 'exact' selector would name: one array of tokens per subcommand, exactly as content.StandingRule would save it. Always an array; empty exactly when 'eligible' is false.
   */
  commands: [string, ...string[]][]
  /**
   * The row that governs this command, derived from the tool declaration table's reachable set — the same set policy.get sends as 'live'. It is what a widening permit is bound to in grantedUnder, and the evaluator checks that binding against the effect a CALL classified as, so the same rule does not reach the same program doing something more serious. Present exactly when 'eligible' is true: an effect for a command the parser could not resolve is a guess, and a permit must never be minted from a guess.
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
   * The semantic facts the classifier recorded about this command, from content's CLOSED vocabulary — what a narrowing rule matches instead of the spelling of a token, since '-o', '--output', '--output=file' and an attached short option are one fact written four ways. Always an array, and empty for a command carrying none.
   */
  features: Feature[]
  /**
   * Whether a standing rule may be written over this reading at all. It is content.StandingRule's answer and not a second opinion: a rule may only be saved over text whose meaning cannot change between the reading and the next match, so a wrapper, a compound, an unresolved token or a command form the parser does not recognise is refused here rather than becoming a permission a person believes they have and does not.
   */
  eligible: boolean
  /**
   * Why the reading was refused, in content's own words, and empty exactly when 'eligible' is true. A command refused without one is a surface that stopped and did not say why.
   */
  reason: string
}
