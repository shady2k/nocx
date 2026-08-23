/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.downloadCancel.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the files.downloadCancel JSON-RPC method: the transfer named by transferId is cancelled. The call is idempotent and deliberately so — cancelling a transfer that has already finished, or one that never existed, is not an error, because the person's cancel races the transfer's own completion every time and losing that race is not a failure to show them. It cancels a DOWNLOAD only: upload and download ids are the same shape and live in the same registry, so naming an upload here is expressible, and honouring it would stop a transfer on a surface the person was not looking at. An empty result is still a contract: additionalProperties: false on an empty shape is what makes 'returns nothing' enforceable, and a renderer that wants a field here cannot be written. What actually happened arrives as files.downloadDone, whose outcome is 'cancelled' when the cancel landed and 'sent' when it did not. Unlike an upload there is no window in which cancellation is refused — a download replaces nothing on the far host, so there is no moment at which stopping could leave somebody with no file. What it CAN leave is a partial file at the far end, because bytes already handed to the client cannot be recalled; the fetch's own framing is what makes that visible as an incomplete transfer.
 */
export interface FilesDownloadCancelResult {}
