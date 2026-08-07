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
 * Result of the dialog.openDirectory JSON-RPC method: the native folder picker behind the sandboxed-shell workspace action (ADR-0019 §3.2, §4.3). The chosen ABSOLUTE directory, or an empty string when the user cancelled. The method reports itself unavailable (-32601) when no native runtime exists — the dev-web harness has no Wails at all — and the action surface renders the unavailable row instead.
 */
export interface DialogOpenDirectory {
  /**
   * The absolute path of the chosen directory, or "" when the picker was cancelled.
   */
  path: string
}
