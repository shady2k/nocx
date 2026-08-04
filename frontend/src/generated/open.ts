/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/open.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the open JSON-RPC method. Sandbox metadata is present only for a successfully opened sandboxed local session.
 */
export interface OpenResult {
  sessionId: string
  cwd: string
  sandbox?: {
    backend: 'landlock' | 'seatbelt'
    workspace: string
    writableRoots: string[]
  }
}
