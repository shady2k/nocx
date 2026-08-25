/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/dialog.openDirectory.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the dialog.openDirectory JSON-RPC method: the native directory picker, reached through the backend because the renderer has no path to the Wails runtime (AD-1). It is the same capability as dialog.openFile — one native dialog at a time — and answers the same shape: the chosen ABSOLUTE path, or an empty string when the user cancelled. The method reports itself unavailable (-32601) when no native runtime exists — the dev-web harness has no Wails at all, and that is the ordinary configuration — and the surface degrades to typing the path by hand.
 */
export interface DialogOpenDirectory {
  /**
   * The absolute path of the chosen directory, or "" when the picker was cancelled.
   */
  path: string
}
