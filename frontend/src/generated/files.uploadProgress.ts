/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.uploadProgress.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.uploadProgress JSON-RPC notification: how far one running upload has got. It is an INDICATOR and never a ledger, and the renderer's own state must not be derived from having seen one. Addressing says why: the destination is the binding's session's CURRENT subscriber, resolved at emit time and never stored, so an AD-9 reconnect redirects it — and with nobody attached the notification is dropped rather than kept, because the useful thing about 'we are at 40%' expires in about a second and a queue of expired ones is worse than silence. It is also coalesced to at most one in flight per transfer: a 256 KiB chunk on a fast local link comes round thousands of times a second, and one frame per chunk would fill the connection's refreshable queue and trip the stall notice the renderer treats as a cue to reconnect — so the transfer's byte count is overwritten in place and the next frame carries the newest value rather than the oldest. In-flight state comes from files.upload's result and files.uploadDone (design §5.5); this notification only moves the bar.
 */
export interface FilesUploadProgress {
  /**
   * Which transfer advanced — the id files.upload returned, 32 lowercase hex characters. A renderer with no row for it has nothing to draw and ignores the frame; that is the ordinary case after a reconnect, not an error.
   */
  transferId: string
  /**
   * The running total the sink has confirmed onto the server, in bytes. It is the count after the last chunk the REMOTE accepted, not the count handed to a buffer, so it can only move forwards and can sit still for as long as one lane call takes. Because frames are coalesced and dropped, consecutive notifications may skip arbitrarily far ahead; a renderer must treat this as the current value and never as an increment.
   */
  bytes: number
  /**
   * The size declared when the transfer was minted, in bytes, repeated on every frame on purpose: a client that missed every earlier notification — because it was not attached, or because they were coalesced away — can still draw a complete bar from the one frame it did receive. Zero is legitimate; an empty file is a file.
   */
  total: number
}
