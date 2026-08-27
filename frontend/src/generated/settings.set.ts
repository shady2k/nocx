/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/settings.set.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the settings.set JSON-RPC method: the acknowledgement that one non-secret setting was validated, persisted and published to every listener. It is the method the notification routing matrix travels on (nocx-3mniv D1) — the epic adds no wire method of its own because the settings methods already carry the choice, so the wire criterion is honoured on the method that actually carries the feature. A rejected value is a JSON-RPC error and never a result, which is why there is no failure shape here: 'ok' is present, true, and alone. Secret-class settings do not use this method at all (settings.secretSet does; ADR-0011 §2).
 */
export interface SettingsSet {
  /**
   * Always true. The field exists because a JSON-RPC result must be a value, and an empty object would leave a later 'partially applied' variant expressible without a contract change.
   */
  ok: true
}
