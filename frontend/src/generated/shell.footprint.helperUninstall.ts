/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/shell.footprint.helperUninstall.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * What removing the remote helper from a host did (remote-helper design D25). The whole ~/.nocx/helper tree is removed — every installed version and any directory an interrupted install left incomplete — and the observation row is forgotten so the never-connect footprint surface stops listing it. removed reports whether a helper tree existed at all: a host with nothing installed uninstalls cleanly — the no-op that succeeds — so a user clicking remove twice sees removed=false, never a failure.
 */
export interface ShellFootprintHelperUninstallResult {
  /**
   * True when a helper tree existed on the host and was removed; false when nothing was installed (the idempotent no-op).
   */
  removed: boolean
}
