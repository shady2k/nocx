/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.request.move.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.request.move JSON-RPC method: one request file was moved to another path INSIDE the same collection — a rename on the backend, never a write-then-delete, so the file is at exactly one of the two paths from before the call until after it. The result carries the NEW relPath, because the caller's next act is to address the file again and deriving it itself would be a second answer to "where is this request now". The bytes that moved were the bytes at the source — the file is the truth (design §6.4); nothing echoes them. The tree is re-read by the caller through the listing.
 */
export interface ApiRequestMoveResult {
  /**
   * The request's path WITHIN the collection after the move — the value every later api.request.* call names it by. It is carried rather than left to be reassembled: a renderer joining a folder and a stem itself would be the second answer this surface exists to refuse (design §13.1).
   */
  relPath: string
}
