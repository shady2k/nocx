/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/mutation.replayed.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The data payload of the -32005 refusal: a mutating call arrived with an identity the backend already used, and the record it retained does not say the first attempt failed to start, so running it again could apply one effect twice (ADR-8eej3e66 §4). It is the error a client that tries again without asking receives.
 */
export interface MutationReplayed {
  /**
   * The fixed discriminator the renderer matches on.
   */
  reason: 'mutation-replayed'
  /**
   * What the retained record says about the first attempt: the same closed set operation.outcome answers with.
   */
  outcome: 'not-started' | 'done' | 'outcome-unknown' | 'unknown'
  /**
   * The catalogue operation the record belongs to.
   */
  operation: string
}
