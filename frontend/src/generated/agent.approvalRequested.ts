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
   * The effect class the policy gate decided on — the row a standing answer writes when the exact invocation rule is saved. Sent by the backend because the renderer must never derive an effect from a tool name (ADR-0028 decision 4).
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
   * Whether the prompt can offer a standing answer. For a command proposal, rule names the exact canonical invocation the answer would save; for a non-command proposal, rule is empty and the effect field names the policy row the answer covers.
   */
  standing: {
    available: boolean
    rule: string
    reason: string
  }
  /**
   * The resource the gate matched the call against, or null when the call named none. A fact for the person reading the question; a standing answer is over the exact invocation in standing, never over this.
   */
  resource?: {
    /**
     * The resource kind, from the ledger's closed set.
     */
    kind: 'path' | 'session' | 'environment' | 'credential' | 'destination' | 'content'
    /**
     * The resource's id.
     */
    id: string
  } | null
  /**
   * Skill write only: the first static scan finding in the proposed body.
   */
  finding?: {
    patternId: string
    line: string
    lineNumber: number
  } | null
  /**
   * Skill write only: the classifier verdict or bounded failure fact.
   */
  classifier?: {
    consulted: boolean
    verdict?: 'clear' | 'suspect'
    model?: string
    reason: string
  } | null
  /**
   * Egress only: the findings are in an ERROR string the tool returned rather than in its result — the surface reads the two differently.
   */
  wasError?: boolean
  /**
   * Command proposals only (nocx-4h0m7.5): what the verbatim command's variables currently read as, carried BESIDE the verbatim string and never instead of it (nocx-y47mi SETTLED 1 — the verbatim string is what runs). Absent for a non-command proposal. Every part carries one of three states, and the last two are different facts the surface must never merge: 'unsafe' means reading it would have an effect ($(cmd), backticks, <(cmd), ${VAR:=x}, ${VAR:?msg}, $((x++))) so it is left exactly as written; 'unasked' means it is a pure read and no shell could be asked — a remote host without our integration, or a shell that did not answer.
   */
  expansion?: {
    /**
     * Whether a live shell was consulted at all. False is the remote host, the un-integrated session and the shell that did not answer; reason says which.
     */
    asked: boolean
    /**
     * Why no shell was consulted, in the words the person reads. Present only when asked is false.
     */
    reason?: string
    /**
     * The expanded DISPLAY form: the verbatim command with every answered span substituted and every other span left exactly as written. It is never submitted, never re-quoted and never handed to a shell — the verbatim command in arguments is what runs.
     */
    command: string
    /**
     * Every expansion site in the verbatim command, in the order it appears.
     */
    parts?: {
      /**
       * The verbatim source text of this expansion site.
       */
      text: string
      /**
       * The parameter's name when it has one — what a refusal sentence names.
       */
      name?: string
      /**
       * Which shell construct this is.
       */
      kind:
        | 'parameter'
        | 'tilde'
        | 'glob'
        | 'brace'
        | 'arithmetic'
        | 'command-substitution'
        | 'process-substitution'
      /**
       * expanded: a live shell answered. unsafe: reading it would have an effect, so it is left as written and reason says why. unasked: a pure read no shell could be asked for.
       */
      state: 'expanded' | 'unsafe' | 'unasked'
      /**
       * What the shell said this reads as. Present only for state 'expanded'.
       */
      value?: string
      /**
       * How many paths a glob matched. A pattern can match an enormous number of them, so the count is the fact and the list is available rather than inline.
       */
      count?: number
      /**
       * Why an unsafe expansion is left exactly as written.
       */
      reason?: string
    }[]
    /**
     * The NAME=value prefixes the command applies to a command of its own — `HOME=/tmp rm -rf $HOME/x`. The PARSE's business and never the shell's: the live shell knows nothing about them, so every expansion of an assigned name is reported unsafe rather than expanded.
     */
    assignments?: {
      name: string
      value: string
    }[]
    /**
     * What a live shell says each program word actually IS. It closes, partly, the hole the command parser names in its own header: nocx does not read rc files, so an alias or a function can make `ls` mean something else.
     */
    programs?: {
      word: string
      kind: 'alias' | 'function' | 'builtin' | 'file' | 'not-found'
      /**
       * What the alias expands to, or the path of the file.
       */
      target?: string
    }[]
  } | null
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
