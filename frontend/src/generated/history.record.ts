/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/history.record.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the history.record JSON-RPC method (nocx-rtg0.13) — the write half of the history family, and the seam where a submitted credential becomes a pending capture. The ack: the request was accepted and handed to the store. It claims nothing more — whether a row appears is decided by the live History policy (history.enabled) and is answered by history.query, never by this ack. MaskedCount and MaskedKinds report what was redacted from the command text before it was handed to the store: the durable command is always the masked one, and the block can say "3 secrets masked: openai, jwt" from this ack alone. EntryID is the row's stable identity; Redactions are the structured segments the row keeps (kind, span, the head/tail the mask shows — never secret material); Captures is the offer list — one opaque capture id plus display metadata per detected credential, empty when there is nothing to offer.
 */
export interface HistoryRecord {
  /**
   * How many secret-shaped regions were masked out of the command before recording. 0 when there was nothing to mask — an honest redaction that says nothing is indistinguishable from there having been nothing to redact, so the count is always carried.
   */
  maskedCount: number
  /**
   * The kinds that were masked, deduplicated in first-occurrence order, from the closed vocabulary of internal/secrets: openai, github-pat, slack, aws-access-key, gitlab, jwt, private-key, url-userinfo, db-connstring, auth-header, env-assignment, high-entropy. Never the secret's value — kind and count are the fact, the matched text is the thing being removed. Never null: no mask is [].
   */
  maskedKinds: string[]
  /**
   * The stable row id of the recorded entry, as history.query reports it — the address a later save rewrites by. Empty when the live History policy wrote no row (history.enabled off).
   */
  entryId: string
  /**
   * Who submitted the command, in the ledger's own vocabulary (entries.kind): 'shell' is the human, 'agent' is the assistant's lane. Minted at submit by the submitting InputTarget on the renderer and carried on the request (design §3.1, nocx-iadtt); the ack echoes the author the record was accepted under, so the renderer can verify the backend kept the fact it minted — the two sides never derive the same thing twice. A block whose author is not the human is visibly marked in the flow.
   */
  author: 'shell' | 'agent'
  /**
   * The row's structured redaction segments, in row order. The renderer draws an unresolved chip at each segment and refuses to run the command as written; a segment the user saved to a vault reference is absent here and the reference sits in the command instead. Offsets are UTF-16 code units into maskedCommand. A segment never carries secret material — prefix/suffix are exactly the text already visible in the masked command. Never null: no redaction is [].
   */
  redactions: Redaction[]
  /**
   * The command exactly as the store keeps it — every secret replaced by its mask, every already-saved value by its reference. This is the text the renderer shows on the frozen block and the text a block copy carries; the redaction offsets are UTF-16 code units into it. Never secret material: it is the durable row's text.
   */
  maskedCommand: string
  /**
   * The pending-capture offers, one per detected credential. Each carries an opaque, single-use capture id (the only way to save or dismiss the plaintext the backend holds), the entry it first attached to, this entry's redaction segment, and the backend-derived suggested vault name. Never null: nothing to offer is [].
   */
  captures: {
    /**
     * Opaque capture id. Holding it is the only way to save or dismiss the capture; it carries no secret material.
     */
    id: string
    /**
     * The history row the capture will rewrite on save. Empty when no row was written.
     */
    entryId: string
    redaction: Redaction
    /**
     * The backend-derived vault name the offer suggests: the host of the command invocation containing the credential, else the environment variable name, else the kind. The renderer may edit it; the vault resolves collisions and the real name comes back on save.
     */
    suggestedName: string
  }[]
}
export interface Redaction {
  /**
   * The closed vocabulary of internal/secrets.
   */
  kind:
    | 'openai'
    | 'github-pat'
    | 'slack'
    | 'aws-access-key'
    | 'gitlab'
    | 'jwt'
    | 'private-key'
    | 'url-userinfo'
    | 'db-connstring'
    | 'auth-header'
    | 'env-assignment'
    | 'high-entropy'
  /**
   * Inclusive UTF-16 code-unit offset into the recorded command.
   */
  start: number
  /**
   * Exclusive UTF-16 code-unit offset into the recorded command.
   */
  end: number
  /**
   * The head of the value the mask shows (the first 4 characters), or "" when the mask shows no material. Exactly the text already visible in the masked command.
   */
  prefix: string
  /**
   * The tail of the value the mask shows (the last 4 characters), or "" when the mask shows no material. Exactly the text already visible in the masked command.
   */
  suffix: string
}
