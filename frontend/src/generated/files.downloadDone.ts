/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.downloadDone.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.downloadDone JSON-RPC notification: one transfer's terminal account, and the only thing on the download surface that may not be lost. It is addressed the way files.uploadDone is and for its reason, not the way files.downloadProgress is: a transfer is bounded by its session and not by the WebSocket that started it, so a terminal outcome emitted into a connection that has gone would leave the UI saying 'downloading' for the rest of the session about a transfer that ended ten minutes ago. When there is no subscriber — or the send fails on a socket that is going down — the outcome is RETAINED against the session and flushed on the next attach, bounded, and cleared as each one is delivered. Exactly one of these arrives per transfer. It carries neither a finalName nor a stranded list, and both absences are the direction rather than an economy: nothing is renamed because nothing on the far host is written, and nothing can be left behind because nothing was created. What replaces them is bytes, which on a failed download is the whole of the account — an upload can be undone and a download cannot, so 'how much did they actually get' is the only honest thing left to say.
 */
export interface FilesDownloadDone {
  /**
   * Which transfer ended — the id files.download returned, 32 lowercase hex characters. After a reconnect this may name a transfer the renderer has no row for, because the row lived in a page that was reloaded; that is the case retention exists to serve and the correct handling is to say the download finished, not to discard the frame.
   */
  transferId: string
  /**
   * How it ended, as a closed set. Three rather than the upload's four, because a download has no 'skipped': nothing on the far host is being replaced, so there is no collision question and nothing for a person to decline. 'sent' — every byte reached the client's connection. 'cancelled' — somebody asked, through files.downloadCancel or by closing the binding or the session, or nobody ever came to fetch it and the ticket expired; it is NOT a failure and must never be reported as one. 'failed' — the transfer could not complete, and error says why.
   */
  outcome: 'sent' | 'cancelled' | 'failed'
  /**
   * The base name of the file this transfer was for. Always present, including on a failure, because a person shown 'the download failed' and no name cannot tell which of two downloads it was.
   */
  name: string
  /**
   * How many bytes reached the client's connection before the transfer ended. On a 'sent' outcome it equals total. On a 'cancelled' or 'failed' one it is the honest account of a partial transfer that cannot be taken back: those bytes are at the far end, and the fetch's own framing — a body short of the Content-Length it declared — is what tells the client the file it holds is incomplete.
   */
  bytes: number
  /**
   * The size the transfer was framed for, in bytes, measured on the open handle at mint time. Present on every outcome so a renderer can say '3 of 12 MB' without having kept the result of files.download, which after a reconnect it will not have.
   */
  total: number
  /**
   * Why it failed, in the words of the error itself rather than a transport paraphrase. Present only on a 'failed' outcome: a cancelled transfer's underlying error is a context cancellation, which is not a fault and must not be shown to a person as one. Absent, never null.
   */
  error?: string
}
