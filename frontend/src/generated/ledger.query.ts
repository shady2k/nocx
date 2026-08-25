/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ledger.query.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the ledger.query JSON-RPC method (nocx-rtg0.20, design §6.2) — one page of the one authoritative ledger, newest first, already filtered to the requested rung of the recall ladder (§10.6). This method is the ONLY ordering implementation: the frontend cache renders what it holds and never answers a recall query with an ordering of its own, or the same keystroke returns different results depending on which pane it came from. The order is seq DESC — the backend-assigned total order (§6.3) — because two windows submit inside the same millisecond and a wall clock cannot separate them. The page can be narrowed to ONE pane with the paneId param — the read restore is made of (nocx-ycla4): a block's durable anchor is its pane, which outlives the session it ran in.
 */
export interface LedgerQuery {
  /**
   * The page, newest first by seq. Never null: no matches is [].
   */
  entries: Entry[]
  /**
   * The rung this page was drawn from, echoed back. The caller asks for a rung and the server answers from it — it never silently widens, because a ladder whose rung you cannot see is a filter.
   */
  scope: 'directory' | 'host' | 'everywhere'
  /**
   * True when this rung has no further entries beyond this page. The overlay uses it to decide whether the next Up climbs to a wider rung rather than paging further down this one.
   */
  exhausted: boolean
  /**
   * Whether the ledger holds any entry at all, read in the same transaction as the page. It separates 'the store answered and had nothing' (an empty page with hasRows true) from 'the store has nothing to answer from' (hasRows false) — the distinction history.query renders as source=store versus source=session. An empty answer and an unanswerable question must not look alike: collapsing them ships a UI that says 'no history' when it means 'history is off'.
   */
  hasRows: boolean
  /**
   * How far back the answer can see: the oldest retained entry's ended_at in Unix milliseconds, store-wide — independent of the rung and of every filter, because retention is store-wide so the horizon is too. With retention set, a search can only see part of history; the overlay renders this line so a partial answer is not presented as the whole one (§5.4). Null when nothing has completed — there is no horizon to state.
   */
  coverage: number | null
}
/**
 * One row of recall: the ledger's identity for the entry plus every fact a block or a history row is rendered from.
 */
export interface Entry {
  /**
   * The entry's client-minted UUIDv7 — the ledger's own key, and the address ledger.get takes.
   */
  id: string
  /**
   * The backend-assigned ingest_seq: the ledger's only total order, and the cursor a next page passes as `before`. Commit order, not causality (ADR-0019 §2).
   */
  seq: number
  /**
   * The environment identity the entry ran in, derived from its facets and never from a session (design §3.1). It is the coordinate the directory and host rungs filter on.
   */
  environmentId: string
  /**
   * The host the entry ran on, as the resolved environment reports it: the endpoint for a remote environment, empty for the local machine. Null when no environment row carries the entry's environmentId — which is 'unknown', and must never be rendered as 'local'.
   */
  host: string | null
  /**
   * Working directory at submit time.
   */
  cwd: string
  /**
   * What this ledger ROW is — the discriminator of the row, not of a visual block (the brief's decision). Closed set, mirroring the store's CHECK constraint. `ask` is a TURN — the word the renderer's BlockKind already uses for it; `frame` is a captured frame, a row that is never drawn as a block of its own (kind is what lets the ask's reference check tell a frame from a turn by the discriminated column rather than by comparing intent against a magic string); `text` is one run of assistant prose (ADR-0040) — the only member that is not an intent, because it was PRINTED rather than attempted. WHO submitted the row is NOT here: that is the `source` field. It was missing here until nocx-dc2fr.7: the store gained the kind and this shared definition did not, so ledger.get on a prose block — which is exactly what the restore reads, per entry — answered a payload that violated its own contract.
   */
  kind: 'shell' | 'ask' | 'action' | 'text' | 'frame'
  /**
   * The IMMEDIATE subject that submitted the content or the intent this entry represents — entries.source, never derived from the kind. Initiation is NOT transitive: the command the assistant ran was submitted by the assistant, so it stays 'assistant' even though a person started the assistant. Approval does not change it: a call the assistant proposed stays 'assistant' after a person allows it. The restore badge is painted from this (frontend/src/restore-client.ts), which is the whole point: a command the assistant ran is kind=shell AND source=assistant, and both halves must survive a restart.
   */
  source: 'user' | 'assistant'
  /**
   * The intent as recorded — for a shell entry, the command line. Secrets are masked before the row is written: the durable text is always the masked one, and maskedCount/maskedKinds say what was removed. Never truncated here.
   */
  intent: string
  /**
   * The entry lifecycle: open until execution is confirmed, bound while a run is live, closed once the outcome is known. Monotonic (design §6.3).
   */
  phase: 'open' | 'bound' | 'closed'
  /**
   * How it ended. 'unknown' is honest and must not be rendered as success; 'pending' and 'running' are entries that have not ended.
   */
  status: 'pending' | 'running' | 'success' | 'failure' | 'interrupted' | 'unknown'
  /**
   * The store's wall clock when the row was created, in Unix milliseconds. Display only — a duration is never a difference of wall clocks.
   */
  submittedAt: number
  /**
   * The renderer's wall clock at submit, or null when it was never observed. Null renders as unknown, never as the epoch.
   */
  startedAt: number | null
  /**
   * The store's wall clock at the close, in Unix milliseconds, or null while the entry has not ended. Null renders as running, never as the epoch.
   */
  endedAt: number | null
  /**
   * The renderer's own measurement of how long the command took, or null when it measured none. Never derived from the difference of two wall clocks (design §3.2).
   */
  durationMs: number | null
  /**
   * The shell arm's exit status, or null when the entry produced none — still running, interrupted, or not a shell entry at all. Null is not zero.
   */
  exitCode: number | null
  /**
   * How many secret-shaped regions were redacted from intent before this row was written. Read back off the row's receipt, never re-derived by running the detector over the stored text — which is already masked. 0 means nothing was masked.
   */
  maskedCount: number
  /**
   * The kinds that were masked, deduplicated in first-occurrence order, from the closed vocabulary of internal/secrets. Never the secret's value — kind and count are the fact, the matched text is the thing being removed. Never null: no mask is [].
   */
  maskedKinds: string[]
  /**
   * The row's structured redaction segments, in row order, offsets in UTF-16 code units into intent. The renderer draws an unresolved chip at each segment and refuses to run the command as written; a segment the user saved to a vault reference is gone from this list and the reference sits in intent instead. Never null: no redaction is [].
   */
  redactions: Redaction[]
}
/**
 * One structured redaction segment on a recorded intent. The single declaration of this shape: history.query points at it, so the two cannot drift.
 */
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
   * Inclusive UTF-16 code-unit offset into the recorded text.
   */
  start: number
  /**
   * Exclusive UTF-16 code-unit offset into the recorded text.
   */
  end: number
  /**
   * The head of the value the mask shows (the first 4 characters), or "" when the mask shows no material. Exactly the text already visible in the masked text.
   */
  prefix: string
  /**
   * The tail of the value the mask shows (the last 4 characters), or "" when the mask shows no material. Exactly the text already visible in the masked text.
   */
  suffix: string
}
