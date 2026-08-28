/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.downloadSave.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Empty result of files.downloadSave. The request takes exactly one transferId naming a download already minted and owned by this connection's session. The backend claims its one-shot ticket before opening the shared native dialog, and the renderer receives neither the ticket nor a local destination path. Completion remains authoritative in files.downloadDone.
 */
export interface FilesDownloadSaveResult {}
