/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.dropped.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.dropped JSON-RPC notification: files were dropped onto the window, and the backend has minted a source ticket for each of them. Server-initiated and unsolicited, so it has no request to correlate against and no caller checking its shape — which is exactly where an addressing defect would hide, and why it is declared here like a result. In the Wails window a drop delivers absolute paths to the Go side, not File objects; the renderer must never see those paths (design R2), so Go mints and sends tickets, names and sizes instead. Read what this notification does NOT carry as part of the contract: there is no bindingId, no destDir and no path, because the native side resolves NO destination. The renderer takes these tickets and calls files.upload with its own bindingId, like any other caller, so the drop gesture joins the same authorised route rather than becoming a second addressing scheme that skips the connection's session set. The sessionId is a routing hint — which tab the drop landed on — and authorises nothing on its own.
 */
export interface FilesDropped {
  /**
   * The session the drop target belonged to, read from the drop element's data-session-id attribute and held to the shape a server-minted session id has (32 lowercase hex characters) before it goes back out on the wire. It tells the renderer which tab was dropped on; what actually authorises the write is the binding the renderer names in files.upload, re-checked against the requesting connection.
   */
  sessionId: string
  /**
   * One entry per dropped file that could be read. Never empty: a drop where nothing could be minted emits nothing at all, and a drop where some members were unusable (a directory, which is out of scope) emits the rest and reports the refusal in the backend's log rather than silently claiming everything arrived.
   */
  sources: {
    /**
     * The one-shot source ticket for this file, 32 lowercase hex characters from crypto/rand. Unlike dialog.openFileForUpload's, it is never empty — a drop that minted nothing is not sent. Same bearer-credential rules: never logged, never in an error string, claimed exactly once.
     */
    sourceTicket: string
    /**
     * The dropped file's BASE name, for display and for the default destination name. Never a path: the directory it came from stays in the backend's address space.
     */
    name: string
    /**
     * The dropped file's size in bytes at the moment of the drop. Advisory, like every stat.
     */
    size: number
  }[]
}
