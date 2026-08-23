/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.downloadProgress.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.downloadProgress JSON-RPC notification: how far one running download has got. It carries the same three fields as files.uploadProgress on purpose — the question is the same question, and two shapes saying {transferId, bytes, total} would be two places for one answer to drift — while the METHOD differs because the surfaces that draw them differ and a renderer must be able to tell which row moved. It is an INDICATOR and never a ledger, and the renderer's own state must not be derived from having seen one. Addressing says why: the destination is the binding's session's CURRENT subscriber, resolved at emit time and never stored, so an AD-9 reconnect redirects it — and with nobody attached the notification is dropped rather than kept, because the useful thing about 'we are at 40%' expires in about a second and a queue of expired ones is worse than silence. It is also coalesced to at most one in flight per transfer, so a fast link produces one frame carrying the newest value rather than thousands carrying every value. In-flight state comes from files.download's result and files.downloadDone.
 */
export interface FilesDownloadProgress {
  /**
   * Which transfer advanced — the id files.download returned, 32 lowercase hex characters. A renderer with no row for it has nothing to draw and ignores the frame; that is the ordinary case after a reconnect, not an error.
   */
  transferId: string
  /**
   * The running total handed to the client's connection, in bytes. It is the count after the last chunk that was written and flushed to the socket, which is not a receipt: TCP may still be carrying some of it, and a client that vanishes mid-transfer will have been counted a chunk further than it got. It can only move forwards and can sit still for as long as one remote read takes. Because frames are coalesced and dropped, consecutive notifications may skip arbitrarily far ahead; a renderer must treat this as the current value and never as an increment.
   */
  bytes: number
  /**
   * The size measured on the open handle when the transfer was minted, in bytes, repeated on every frame on purpose: a client that missed every earlier notification — because it was not attached, or because they were coalesced away — can still draw a complete bar from the one frame it did receive. Zero is legitimate; an empty file is a file.
   */
  total: number
}
