/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/attach.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the attach JSON-RPC method: the outcome of a client claiming a live session and resuming its output stream (AD-9 reconnect, and the claim of the nocx-server design D5). ONE METHOD, TWO CALLERS, deliberately: a reconnecting client attaches at the offset it last received, and a fresh client — one whose renderer memory holds nothing — attaches at the replayFrom that sessions.live handed it. Giving the fresh case its own method would be a second reattach, and the ring, the offsets and the reset are already this one's. The two booleans are both always present rather than one being omitted: the contract is exact only when a reader can tell 'the server said no reset' from 'the server did not mention reset', and exactly one of them is true.
 */
export interface AttachResult {
  /**
   * The requested offset was still in the ring and the stream continues from it: no bytes were lost and the renderer keeps what it has drawn.
   */
  resumed: boolean
  /**
   * The requested offset is older than anything the ring still holds, so there is a gap. The renderer must clear its decoder and its screen and resync from `from` — replay cannot begin inside a UTF-8 sequence spliced onto a different stream position (D7).
   */
  reset: boolean
  /**
   * The byte offset the stream resumes at. Equal to the requested offset when resumed; the ring's current end when reset, because everything before it is gone.
   */
  from: number
}
