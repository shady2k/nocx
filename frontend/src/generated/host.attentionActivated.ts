/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/host.attentionActivated.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the host.attentionActivated notification (nocx-uo1k6, design D3): a person clicked a desktop notification the client presented for the coordinator. The click lands in the client, which is where the OS delivers it, and the client reports it here rather than acting on it: WHICH window is raised and WHICH pane is focused are the coordinator's to decide, because only the coordinator knows which connection holds the session (AD-3 keeps the shell an implementer, never an owner of the decision). sessionId is the addressing identity the banner carried (AD-7 — session-id is server-authoritative).
 */
export interface HostAttentionActivated {
  /**
   * The session the activated banner was about.
   */
  sessionId: string
}
