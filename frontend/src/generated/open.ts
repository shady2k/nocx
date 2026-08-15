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
 * Result of the open JSON-RPC method: a session is created and acknowledged (AD-7 — the server assigns the authoritative session id, so the id is minted by the backend, never the renderer). cwd is the session's starting directory, the tab's name until a program sets a title. shellIntegrationReason is deliberately NOT here (nocx-dvql): it could only answer at open time, and the two integration failures that matter most arrive later — a handshake that expires ten seconds in, and a channel lost mid-session. That question is answered by the session.integrationChanged notification, as a state the backend keeps revising, and keeping a second answer in this ack is the defect AD-8 names.
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
   * The resolved destination mode for this session (nocx-mlm7): the connection-scope default the tab's capability control starts from. auto (the default — ADR-0033) means the user has not answered: it wraps and installs the scripts exactly as script does (N3), and additionally permits the relay to be OFFERED where a surface reaches for it (D8). script is that same answer given explicitly, and is never upgraded. raw adds nothing. relay is the explicit choice of the deployed binary. The mode is never proof that integration succeeded — that is what session.integrationChanged reports, and it is the only thing that reports it.
   */
  desiredMode: 'auto' | 'raw' | 'script' | 'relay'
}
