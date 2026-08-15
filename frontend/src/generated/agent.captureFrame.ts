/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.captureFrame.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the agent.captureFrame JSON-RPC method (nocx-f4s5, design §7). The renderer ingests the frame FIRST — cells, attributes, cursor, capture identity and source — and the backend answers with the BACKEND-MINTED frame id (AD-7: ids are server-authoritative, the renderer cannot invent one). The frame lands as its own entry in the one ledger (ADR-0019) with capture provenance; a frame that is never referenced is an orphan and is swept. The same capture retried with the same captureId answers the same frameId — the idempotency the lost-response retry needs.
 */
export interface AgentCaptureFrame {
  /**
   * The backend-minted id of the captured frame. Opaque to the renderer: it is carried back into agent.ask as a reference, never parsed or guessed.
   */
  frameId: string
}
