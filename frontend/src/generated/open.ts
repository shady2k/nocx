/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/open.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the open JSON-RPC method: a session is created and acknowledged (AD-7 — the server assigns the authoritative session id, so the id is minted by the backend, never the renderer). cwd is the session's starting directory, the tab's name until a program sets a title. shellIntegrationReason reports why remote shell integration did not happen (nocx-r52q, nocx-xs1d): empty means integration succeeded or was never attempted, "unsupported-shell" and "no-secure-temp" are launcher refusals, "remote-command" a configured RemoteCommand, "unknown" a refusal the backend cannot classify (the adapter's fail-open for a reason its vocabulary does not yet know). The reason must reach the product, never only a log (AGENTS.md), so the field is always present and never omitted.
 */
export interface Open {
  /**
   * Backend-assigned session id (AD-7).
   */
  sessionId: string
  /**
   * Starting working directory of the session's shell, with the home directory abbreviated to ~.
   */
  cwd: string
  /**
   * Why remote shell integration did not happen for this session; empty when it succeeded or was never attempted.
   */
  shellIntegrationReason: '' | 'unsupported-shell' | 'no-secure-temp' | 'remote-command' | 'unknown'
  /**
   * The resolved destination mode for this session (nocx-mlm7): the connection-scope default the tab's capability control starts from. script (the default — N3) wraps and installs automatically, raw adds nothing, relay is consent-gated (inert until the relay lands). The mode is never proof that integration succeeded — shellIntegrationReason and the arrival of markers are what confirm or downgrade the tab's state.
   */
  desiredMode: 'raw' | 'script' | 'relay'
  /**
   * Immutable sandbox metadata for a sandboxed local session (ADR-0019). Absent for ordinary and SSH sessions.
   */
  sandbox?: {
    backend: 'landlock' | 'seatbelt'
    workspace: string
    writableRoots: string[]
  }
}
