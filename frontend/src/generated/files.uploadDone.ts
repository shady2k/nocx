/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.uploadDone.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.uploadDone JSON-RPC notification: one transfer's terminal account, and the only thing on the upload surface that may not be lost. It is deliberately NOT addressed the way files.uploadProgress is. A transfer is bounded by its session and not by the WebSocket that started it, so a person can start a 400 MB upload, close the laptop, and have it finish on its own lease with nothing attached; a terminal outcome emitted into that would leave the UI saying 'uploading' for the rest of the session about a transfer that ended ten minutes ago. So when there is no subscriber — or the send fails on a socket that is going down — the outcome is RETAINED against the session and flushed on the next attach, the way files.changed accumulates a dirty set. Retention is bounded and each outcome is cleared as it is delivered, so a reconnect replays what was missed exactly once. Exactly one of these arrives per transfer.
 */
export interface FilesUploadDone {
  /**
   * Which transfer ended — the id files.upload returned, 32 lowercase hex characters. After a reconnect this may name a transfer the renderer has no row for, because the row lived in a page that was reloaded; that is the case retention exists to serve and the correct handling is to say the upload finished, not to discard the frame.
   */
  transferId: string
  /**
   * How it ended, as a closed set, because these four are told to a person in four different ways. 'written' — the bytes are on the server under finalName. 'skipped' — the destination existed and the person chose to keep it, so nothing moved. 'cancelled' — somebody asked, through files.uploadCancel or by closing the binding or the session; it is NOT a failure and must never be reported as one. 'failed' — the transfer could not complete, and error says why.
   */
  outcome: 'written' | 'skipped' | 'cancelled' | 'failed'
  /**
   * The name actually written, one path component, which the keepBoth decision may have changed from the name that was asked for — that renaming is the whole point of keepBoth, and a person told only the requested name would look for a file that is not there. Empty when nothing was written, which is every outcome but 'written'. Present rather than omitted so a renderer reads one field in one way whatever happened.
   */
  finalName: string
  /**
   * Why it failed, in the words of the error itself rather than a transport paraphrase. Present only on a 'failed' outcome: a cancelled transfer's underlying error is a context cancellation, which is not a fault and must not be shown to a person as one. Absent, never null.
   */
  error?: string
  /**
   * Paths the transfer created or moved and could not clean up, in the provider's syntax. A LIST and never a single field, because the promote fallback can leave two at once — the upload temp holding the new content and the backup holding the old — and naming one of them would leave a person with an unmentioned file on their disk. It is orthogonal to the outcome: a 'written' transfer whose backup unlink failed succeeded AND stranded a path. Always an array and never null, so a client can iterate it without a guard; empty means nothing was left behind.
   */
  stranded: string[]
}
