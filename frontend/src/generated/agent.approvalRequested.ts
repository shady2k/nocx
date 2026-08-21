/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.approvalRequested.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.approvalRequested server-to-client notification (nocx-z9hj4, design §7.2/§7.3): a question reached a person. One kind of question whether the risk was an effect coming in (a policy escalation) or a secret going out (an egress finding). Carries the full binding — run, attempt, tool, call id and the canonical-argument hash — what the person's answer on agent.approve must name, the arguments being decided about, the reason the gate asked, and the egress findings when the gate that asked was the egress gate. Findings are facts — which detector fired, the kind, where — never the secret material itself.
 */
export interface AgentApprovalRequested {
  /**
   * The backend-minted run id the question belongs to — the value agent.approve echoes back.
   */
  runId: string
  /**
   * The run's attempt — part of the binding; the answer echoes it back.
   */
  attempt: number
  /**
   * The tool the model proposed calling.
   */
  tool: string
  /**
   * The model's call id for the proposed call — part of the binding.
   */
  callId: string
  /**
   * The canonical-argument hash of the binding — what distinguishes one proposal from a changed one. The answer echoes it back; a changed argument never resumes under the old approval.
   */
  argHash: string
  /**
   * The proposed call's arguments, as the model produced them — what the person is deciding about.
   */
  arguments: string
  /**
   * Which gate asked: the policy gate (an effect coming in) or the egress gate (a secret going out).
   */
  reason: 'policy' | 'egress'
  /**
   * The effect class the policy gate decided on — the row a standing answer writes. Sent by the backend because the renderer must never derive an effect from a tool name (ADR-0028 decision 4).
   */
  effect:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * The resource the gate matched the call against, or null when the call named none. A fact for the person reading the question; a standing answer is over the effect, never over this.
   */
  resource?: {
    /**
     * The resource kind, from the ledger's closed set.
     */
    kind: 'path' | 'session' | 'environment' | 'credential' | 'destination'
    /**
     * The resource's id.
     */
    id: string
  } | null
  /**
   * Egress only: the findings are in an ERROR string the tool returned rather than in its result — the surface reads the two differently.
   */
  wasError?: boolean
  /**
   * Egress only: what was found and where. Facts, never the material.
   */
  findings?: {
    /**
     * Which detector fired: known vault material or a heuristic match.
     */
    source: 'known' | 'heuristic'
    /**
     * The recognizer's closed kind for a heuristic finding.
     */
    kind?: string
    /**
     * The vault catalogue name of the matched secret for a known finding (ADR-0016). Display metadata, never material.
     */
    secretName?: string
    /**
     * Byte offset of the match into the tool result, inclusive.
     */
    start: number
    /**
     * Byte offset of the match into the tool result, exclusive.
     */
    end: number
  }[]
}
