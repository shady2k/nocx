/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ports.error.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The data in a ports.* invalid-params refusal for an unknown pane or a known pane whose helper is unavailable.
 */
export interface PortsError {
  reason: 'unknown-pane' | 'helper-unavailable'
}
