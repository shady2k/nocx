/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ledger.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the ledger.get JSON-RPC method (nocx-rtg0.20, design §6.2) — one entry with its relations and the METADATA of what its runs captured. The bodies are deliberately absent: the recall read must not haul bytes (ADR-0019 §6), so an artifact's content is fetched one artifact at a time by whoever actually wants it. The entry is the same shape a ledger.query page row has, declared once there.
 */
export interface LedgerGet {
  entry: Entry
  /**
   * Every edge touching this entry, in either direction — the difference between a log and a memory (design §3.4). Never null: no relation is [].
   */
  edges: Edge[]
  /**
   * The metadata of every artifact of every execution of this entry, in execution order. Never null: no capture is [].
   */
  artifacts: Artifact[]
  /**
   * Whether the prose of THIS RUN is no longer kept: retention took the bodies of its `text` children (ADR-0040's retention rule, ADR-0019 §7). It is the ONE place a reader asks that question, and it is a fact about the RUN because the run is the unit — the prose of one run is retained or evicted together, so a turn cut into seven pieces and a turn written in one report the same single answer, and the renderer drawing the turn has one sentence to say rather than one per hole. False on every kind that has no prose, which includes a command whose own terminal body was evicted — that block says its own sentence, and a turn does not say it for it.
   */
  proseEvicted: boolean
  /**
   * Everything this entry caused, in the causal order the turn assigned (nocx-h1l4o) — the `caused-by` edges above, resolved. The join and the order are the ledger's: a reader that resolved raw edges itself would own the arrangement a second time (AD-8). Never null: an entry that caused nothing is [], which is also what a reader gets when the relation is missing, and it draws plain ledger order.
   */
  caused: Caused[]
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
export interface Edge {
  /**
   * The entry id the relation points from.
   */
  from: string
  /**
   * The entry id the relation points to.
   */
  to: string
  /**
   * The edge vocabulary (design §3.4). Closed set, mirroring the store's CHECK constraint.
   */
  rel: 'rerun-of' | 'supersedes' | 'caused-by' | 'cites' | 'in-span' | 'references'
  /**
   * The edge's sparse extension — for a `references` edge, the region inside the frame it points at. The store never interprets it; an edge that carries none is {}.
   */
  payload: {}
}
/**
 * One capture of one execution, with the provenance ADR-0019 §6 requires: how the text was taken, at which version, from what geometry and stream position, and what is missing from it. There is no field for the content, and that absence is the contract.
 */
export interface Artifact {
  /**
   * The artifact's id — the address the content read takes.
   */
  id: string
  /**
   * The run this artifact belongs to. Artifacts attach to the execution, never to the intent (ADR-0020 §4): a rerun and a retry are executions of one entry and each captured its own output.
   */
  executionId: number
  /**
   * What the content is.
   */
  mediaType: 'application/vt' | 'text/plain' | 'text/markdown' | 'application/json'
  /**
   * The artifact this one was derived from, or null when it is the original capture.
   */
  derivedFrom: string | null
  /**
   * open while the capture may still grow, sealed once it cannot.
   */
  state: 'open' | 'sealed'
  /**
   * Logical content bytes — the retention budget's unit (§5.4), not the physical size on disk.
   */
  byteLen: number
  /**
   * How many chunks the content arrived in. An artifact is never one BLOB.
   */
  chunkCount: number
  /**
   * Whether retention's background eviction is forbidden to take this artifact. A pin does not protect against an explicit delete.
   */
  pinned: boolean
  /**
   * Why this artifact does not hold the whole stream: a cap dropped the middle, a gap lost a range, or capture was refused by policy. Null when nothing was lost.
   */
  truncated: 'cap' | 'gap' | 'suppressed' | null
  /**
   * Where the text came from — terminal cells, raw output, serialized block HTML, or nothing at all. Derived text must be able to say how it was taken.
   */
  captureMethod: 'terminal-cells' | 'raw-output' | 'serialized-html' | 'none'
  /**
   * The version of the capture method, so text taken by an older serializer is not read as though it were today's.
   */
  captureVersion: number
  /**
   * Terminal width at capture time, or null when the capture had no geometry.
   */
  terminalCols: number | null
  /**
   * Terminal height at capture time, or null when the capture had no geometry.
   */
  terminalRows: number | null
  /**
   * Which stream was captured, or null when the capture does not distinguish them.
   */
  stream: 'stdout' | 'stderr' | 'combined' | null
  /**
   * Where this capture starts in the run's byte stream, or null when it is not positioned in one.
   */
  byteOffset: number | null
  /**
   * Where this capture ends in the run's byte stream, or null when it is not positioned in one.
   */
  byteEnd: number | null
  /**
   * The content's encoding, so bytes are never decoded by guess.
   */
  encoding: string
  /**
   * The dropped byte ranges of the captured stream. Never null: no gap is [].
   */
  gaps: Gap[]
  /**
   * The artifact's sparse extension. The store never interprets it; an artifact that carries none is {}.
   */
  payload: {}
}
export interface Gap {
  /**
   * First dropped byte offset.
   */
  start: number
  /**
   * Offset just past the last dropped byte.
   */
  end: number
  /**
   * Why the range is missing.
   */
  reason: string
}
/**
 * One CHILD of this entry, at its seat among its siblings (ADR-0040). There is deliberately no `at`: the offset said how much of one stored answer had been written when the cause happened, and it existed only while the unit that was DRAWN (a run of prose) and the unit that was STORED (the whole answer) were different things. They are the same thing now — prose is a `text` child with a seat of its own — so `position` IS the place and there is nothing left to cut. A command the turn ran is a block the page already carries and is placed by this; a tool call is an action entry that opened no block of its own and is drawn as a child naming its tool and its arguments; a run of prose is a `text` child whose body is fetched like any other.
 */
export interface Caused {
  /**
   * The caused entry's id — the same id a page row carries for a command, and the address ledger.get takes.
   */
  entryId: string
  /**
   * Where it sits inside the turn: a causal index the turn assigned, 0 for the first thing it caused. NOT a timestamp and NOT ingest_seq, which is commit order and never causality (ADR-0019 §2).
   */
  position: number
  /**
   * Whether this cause's work became a TOP-LEVEL BLOCK of its own — the tool declaration's fact (internal/agenttools Declaration.OpensBlock), stored on the ACTION row with its attempt and read back here. True only for an action entry whose tool opens a block (`run`): the command's block, its output and its exit status are the account of that call, so the turn draws nothing beside it, and a second child would restate what the block already shows. False for every other action, whose own child is the only trace it left, and false for a shell entry — a command a turn ran IS a block and does not also say it opened one. Read rather than matched on `intent`, so a reader is never a second copy of the tool table.
   */
  opensBlock: boolean
  /**
   * What kind of entry it is. Closed set, mirroring the store's CHECK constraint — `text` included, because a run of assistant prose is a child like any other since ADR-0040 and the read returns it. `ask` is a TURN, which is never a child of a turn; the member is here because the enum mirrors the store.
   */
  kind: 'shell' | 'ask' | 'action' | 'text'
  /**
   * Who submitted the child's content or intent — the same entries.source fact a page row carries, so a restored turn's badge never guesses it from the child's kind.
   */
  source: 'user' | 'assistant'
  /**
   * The child's own intent: the command line for a shell entry, the declared tool name for an action, and EMPTY for a `text` child — prose has no intent, which is a clause of its CHECK rather than a convention a reader has to know.
   */
  intent: string
  /**
   * What the model asked for, as the tool's schema validated it, read back off the ACTION row's own record (content.ActionFacts). Null on every other kind — a command a turn ran is not a tool call and asked for nothing. It is here for the reason it is on agent.runToolCall: the arguments are what tell two calls of one tool apart, and a restored call naming only its tool and its derived resource would say LESS than the live one did — which is the defect ADR-0040 was written against, arriving one restart later. Stored rather than re-derived, like the effect and the resource beside it.
   */
  args: {
    [k: string]: unknown
  } | null
  /**
   * The effect class the gate decided for an ACTION entry, read back off that row's own record. Null on every other kind — a command a turn ran is not a tool call and has no effect class.
   */
  effect:
    | (
        | 'observe'
        | 'mutate-reversible'
        | 'mutate-destructive'
        | 'privilege-change'
        | 'disclose'
        | 'cross-boundary'
        | 'delegate'
      )
    | null
  /**
   * What the call named, as the backend derived it at the moment it decided about the call — never re-derived by a reader. Null when the tool names no resource in its parameters at all, and null for a non-action entry.
   */
  resource: {
    kind: string
    id: string
  } | null
}
