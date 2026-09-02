/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/lifecycle.submitAttempt.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The lifecycle.submitAttempt JSON-RPC result: the app-originated ExecutionAttempt as the kernel created it at editor submit (ADR-0024 decision 5, docs/lifecycle-protocol.md). The call runs synchronously BEFORE the bytes that can cause the shell's own start event are written to the pty, so the later authenticated start attaches to this attempt and replaces nothing — the attempt id, the app-owned command text (references intact, never the resolved send line), cwd and host all stay app-owned. The attempt is always created open and its origin is always app; the schema pins both. The state and lifecycle of the domain are not echoed here — the running lifecycle.changed fact carries them, and the renderer keys its state machine on that fact alone.
 */
export interface LifecycleSubmitAttempt {
  /**
   * The attempt id, server-assigned. The renderer correlates nothing with it directly — the published running fact carries the same id and the block projection binds on it.
   */
  id: string
  /**
   * The domain the attempt was opened on — the id the caller submitted to. An attempt belongs to exactly one domain and cannot cross an activation boundary.
   */
  domain: string
  /**
   * The attempt's status. Always open: the kernel creates the attempt open and only an authenticated start or completion moves it.
   */
  state: 'open'
  /**
   * The app-owned command text as submitted — the reference-intact record line. The shell's wire line, which may carry vault-resolved secrets, is ignored on attachment (decision 5's privacy rule).
   */
  command: string
  /**
   * The working directory the command runs in, captured at submit time. May be empty when the cwd is unknown (a remote environment).
   */
  cwd: string
  /**
   * The host the command runs on, captured at submit time. Empty for a local session.
   */
  host: string
  /**
   * Where the attempt came from. Always app: this call is the app-originated half of decision 5 — a shell-originated attempt is created only by an authenticated start with nothing pending.
   */
  origin: 'app'
  /**
   * When the attempt was created, RFC 3339 with sub-second precision. The block model derives duration from startedAt and completedAt.
   */
  startedAt: string
  /**
   * The submit's correlation token, echoed from the lifecycle.submitAttempt params that created this attempt. The renderer binds the ledger record it opened at submit to this attempt by equality on this value, rather than searching for a record by position. Absent on a shell-originated attempt, which has no submit behind it, and absent when the submit carried no token. It is a correlation token and never an identity: the attempt id is the backend's (ADR-0024 decision 5).
   */
  submitId?: string
}
