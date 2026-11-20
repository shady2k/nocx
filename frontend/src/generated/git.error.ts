/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/git.error.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Data payload of a git.* JSON-RPC error. Six distinct domain refusals share the -32602 code (gitErrorCode maps them all to invalid-params), and the renderer must tell them apart: an unknown binding means the store re-resolves through git.open, while a conflicted stage-all is a refusal a person should read. The code alone cannot discriminate, so the wire carries this fixed reason vocabulary (the vault's vaultErrorData and the control-saturated payload are the same pattern). A git invocation failure (-32603) carries no data at all. A call whose declared lifetime ran out answers -32005 with the declaration's own expired outcome as the reason: not-started (nothing was changed), outcome-unknown (a mutation may have happened) or retry-read (a read changed nothing).
 */
export interface GitError {
  /**
   * Fixed refusal vocabulary. `unknown-binding` is the single reason the store's isUnknownBinding treats as 'the binding is gone — re-resolve'; every other value is a refusal or an expiry that reaches the caller as itself. The three expiry reasons are the catalogue's declared outcomes (ADR-8eej3e66 §2), never a saturation class.
   */
  reason:
    | 'unknown-binding'
    | 'not-owned'
    | 'handle-released'
    | 'nothing-to-commit'
    | 'amend-unborn'
    | 'conflicted'
    | 'not-started'
    | 'outcome-unknown'
    | 'retry-read'
}
