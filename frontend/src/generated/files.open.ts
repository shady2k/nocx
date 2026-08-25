/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.open.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the files.open JSON-RPC method: the binding that names a filesystem for every later files.* call, plus the root it starts at. sessionId appears exactly once on the wire — here — which is what keeps the wrong pairing inexpressible: no later parameter can ask for the local filesystem of an SSH session, and no caller can name a filesystem the backend did not hand out (design §5.2).
 */
export interface FilesOpenResult {
  /**
   * The backend-issued id every later files.* call echoes. Minted from crypto/rand so it cannot be guessed or enumerated; it is an address, not a bearer token — every later call re-checks that the binding's session is in the requesting connection's connState, one map lookup, and the check is what holds if an id ever reaches a log, a screenshot or a crash report.
   */
  bindingId: string
  /**
   * The backend's attestation of the resolved SSH destination and route ("v1:" + base64url(SHA-256 of the structured hop record)), or null for a local binding — a local tab has no remote to attest. Never absent: local sends null, remote sends the attestation. The renderer compares it after a reconnect to decide whether Reload may be offered: enabled only when a live session's endpointId matches (D6), because a profile is editable and a rebind on profile id could refresh a viewer labelled one machine from another.
   */
  endpointId: string | null
  /**
   * The directory the binding starts at. Navigation scope, not a sandbox (D8): no '..' row, and a symlink may leave the root and is rendered plainly.
   */
  root: {
    /**
     * The root's absolute path in provider syntax — the address the tree is rooted at.
     */
    path: string
    /**
     * The root as shown in the panel header, ~-abbreviated for the local provider. Display is for the user, canonical is for comparisons — the two are never conflated.
     */
    display: string
    /**
     * True when the root was not explicitly requested — the composition layer passed no usable rootPath (no verified OSC 7 cwd, or one the provider rejected), so the provider fell back to its default Root().
     */
    inferred: boolean
    /**
     * Why the root was inferred; empty when inferred is false. 'no rootPath' when the caller omitted the parameter, else what made the supplied rootPath unusable. The renderer can surface this as a hint that the panel is not showing what the session's cwd claimed.
     */
    inferredReason: string
  }
  /**
   * Whether this backend has a file-manager revealer wired (files.reveal). False on a platform nocx does not ship a reveal for, where files.reveal would answer -32601; the renderer must not offer 'Show in Finder' for a capability the backend refuses (nocx-ngf3u). A build fact, constant for the life of the process, repeated on every open so the binding carries its own capability.
   */
  revealAvailable: boolean
}
