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
 * Result of the open JSON-RPC method: a session is created and acknowledged (AD-7 — the server assigns the authoritative session id, so the id is minted by the backend, never the renderer). cwd is the session's starting directory, the tab's name until a program sets a title. instanceId + sessionEpoch (nocx-3oupk) name the session's incarnation: the renderer learns them here and compares every later observation of this session against them, so a record or message from a previous backend instance is told from this one. shellIntegrationReason is deliberately NOT here (nocx-dvql): it could only answer at open time, and the two integration failures that matter most arrive later — a handshake that expires ten seconds in, and a channel lost mid-session. That question is answered by the session.integrationChanged notification, as a state the backend keeps revising, and keeping a second answer in this ack is the defect AD-8 names.
 */
export interface Open {
  /**
   * Backend-assigned session id (AD-7).
   */
  sessionId: string
  /**
   * The backend instance that minted this session (AD-7 — server-authoritative, never minted by the renderer): minted once per backend start, so two instances are never equal. Together with sessionEpoch it names this session's incarnation — a restored record or a late message naming the same sessionId from another instance (or an earlier epoch of this one) is a different incarnation and must not be applied to this session.
   */
  instanceId: string
  /**
   * This session's epoch within its backend instance: minted by the backend per session, monotonic and never reused (the rule internal/lifecycle states for its own epochs, decision 8). Distinguishes a later session that reuses a sessionId from the incarnation a record names, even within one instance. Named sessionEpoch to stand apart from the lifecycle domain epoch the renderer already sees on lifecycle.changed.
   */
  sessionEpoch: number
  /**
   * The workspace this session belongs to (nocx-fraus). NEVER empty and never absent: a tab is always in exactly one workspace, and there is no null (.internal/specs/2026-08-15-workspaces-ux-design.md §4.2). The open REQUEST may omit a workspace — the renderer has no name for the default because the default never renders — and the backend registry then supplies it, which is why this result field is required while the request field is not. It CARRIES NO BEHAVIOUR: nothing reads authority, addressability or reachability from it, the fence that later consults membership is a separate epic (§5), and §5.5 forbids any surface before that epic from describing a workspace as safe, isolated or contained.
   */
  workspaceId: string
  /**
   * Starting working directory of the session's shell, with the home directory abbreviated to ~.
   */
  cwd: string
  /**
   * The resolved destination mode for this session (nocx-mlm7): the connection-scope default the tab's capability control starts from. script (the default — N3) wraps and installs automatically, raw adds nothing, relay is consent-gated (inert until the relay lands). The mode is never proof that integration succeeded — that is what session.integrationChanged reports, and it is the only thing that reports it.
   */
  desiredMode: 'raw' | 'script' | 'relay'
  /**
   * The session that opened this one (nocx-9hu9d), or null for a root session. Always present: null is the answer for a root, and a missing key would make "this session has no parent" indistinguishable from "this backend does not say". It is the edge the backend RECORDED — the renderer's claim in the open params is checked against the live registry and refused (-32602) if it cannot be true, so what comes back here is an admitted fact, not an echo. The full identity is carried, never a bare sessionId: an id alone re-resolves to whatever holds it now, which is the ambiguity instanceId + sessionEpoch exist to remove (nocx-3oupk), and this edge is written once and never revisited. PROVENANCE ONLY — it says "A created B" and confers nothing: the parent gains no right to observe or control the child by appearing here, and continuing authority is a separate, revocable object (ADR-0020 §5). The edge outlives its subject: a parent that exits leaves this value exactly as it was, because a parent's death never closes or rewrites its children (design D6).
   */
  parent: {
    /**
     * The opener's session id.
     */
    sessionId: string
    /**
     * The backend instance that minted the opener. Equal to this session's own instanceId for every edge the backend will admit — a claim naming another instance is refused, because a record out of a previous backend can never resolve to a session here.
     */
    instanceId: string
    /**
     * The opener's epoch within that instance: which incarnation of that session id created this one. A later session reusing the id is a different incarnation and is not this parent.
     */
    sessionEpoch: number
  } | null
  /**
   * Immutable realized sandbox metadata for a sandboxed local session. Absent for ordinary and SSH sessions. The backend returns a deep copy of the enforced policy after native readiness; request deltas and settings revisions are never echoed.
   */
  sandbox?: {
    backend: 'landlock' | 'seatbelt'
    workspace: string
    writableRoots: string[]
    readOnlyRoots: string[]
    /**
     * Disposable discoverability aliases from the isolated runtime HOME to exact canonical host roots. These objects carry no access class; writableRoots and readOnlyRoots remain authoritative.
     *
     * @maxItems 129
     */
    homeProjections: {
      hostPath: string
      relativePath: string
    }[]
  }
}
