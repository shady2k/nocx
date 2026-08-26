/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runToolCall.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runToolCall server-to-client notification (nocx-shxv0, design §7): the assistant is about to DO something, announced where it happens — a child of the turn, at the seat the turn had reached, never a top-level block of its own and never an inference from a stray blob in the answer text. Sent once per execution, immediately before the tool runs: a call that was refused, malformed or escalated has not happened, and an escalated one already has a surface of its own (agent.approvalRequested), which is why this is not sent for it. runId AND entryId are both here for the same reason they are on agent.runDelta — the renderer routes by entryId while two overlapping asks stream concurrently. There is no seq: unlike a delta this is not persisted and not replayed, so arrival order over the one socket is its only order. The tool's RESULT is deliberately absent — the attempt's outcome is in the ledger, the run tool's output is in the block the command really opened, and the egress gate (design §7.1) screens a result for the PROVIDER, so sending the same bytes to the renderer would be a second egress path. The ARGUMENTS are here, and they used to be absent on the argument that the derived resource is the readable half. That argument produced the defect ADR-0040 was written against: the resource is the readable half only when it DIFFERS between calls, and for every session-scoped tool it does not. One turn drew `readScreen`, `blocks.list`, `blocks.read` and `blocks.read` as four announcements naming the same pane, two of them reads of different finished commands and indistinguishable from each other. What tells two calls apart is what the model actually asked for, so the arguments ride the wire and the renderer names a call with them. They are the VALIDATED blob the tool really ran on, which is the same object the action entry's payload already stores, so this sends no fact that was not already recorded. The resource stays beside them and keeps its job: it is what says WHICH argument holds the session, so the renderer can put the pane's name where the id would have been without guessing from the key.
 */
export interface AgentRunToolCall {
  /**
   * The backend-minted run id the call belongs to.
   */
  runId: number
  /**
   * The TURN the call is an element of — agent.ask result's entryId, the same routing key agent.runDelta carries. Not the ledger entry the attempt was recorded under; that is actionEntryId.
   */
  entryId: string
  /**
   * The model's own id for this call. The renderer keys on it, so a call announced twice — an approved egress resume passes the same call through the pipeline again — renders once.
   */
  callId: string
  /**
   * The declared tool name the model called, e.g. 'files.read'.
   */
  tool: string
  /**
   * What the model asked for, as the tool's own schema validated it — the object the call really ran on, never the raw string. This is what tells two calls of one tool apart, and the renderer names the call from it: `blocks.read sessionId=… blockId=3` and `blocks.read sessionId=… blockId=4` are one tool, one resource and two different acts. Open by construction (`additionalProperties: true`): the shape is the TOOL's, declared in internal/agenttools and different for every one of them, so a contract that closed it here would be a second copy of every tool schema in the repo. Always present, and `{}` for a tool that takes no arguments — absent and empty are the same sentence about a call, and a reader should not have to tell them apart. Whether an argument may be SHOWN is not this field's question: the gate already refused, escalated or permitted the call before it is announced, and what is announced is what ran.
   */
  args: {
    [k: string]: unknown
  }
  /**
   * The effect class the policy gate decided on — the ledger's vocabulary. Sent by the backend because the renderer must never derive an effect from a tool name (ADR-0028 decision 4).
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
   * Whether this call's work becomes a TOP-LEVEL BLOCK of its own (nocx-9sqii) — true for `run` alone, which submits a command through the same orchestration a person's line takes. It decides WHAT the call is drawn as: a call that opens a block IS that block, standing in its own seat among the turn's children, and no announcement is drawn beside it — a second child restating the command, the output and the exit status the block already owns is the empty half of two positions one command used to occupy. A call that opens none is drawn as a child of its own, named by its tool and its arguments, which is then the only thing that says it occurred. Sent by the backend for the reason the effect is (ADR-0028 decision 4): it is a fact of the tool table (internal/agenttools Declaration.OpensBlock), and a renderer holding its own list of which tools open blocks would be a second copy of that table, disagreeing the day a tool is added.
   */
  opensBlock: boolean
  /**
   * The LEDGER action entry the attempt was recorded under (design §6.4). The thread joining question, run, attempt and answer — and the handle a later 'show me what it returned' reaches through, rather than a second copy of the bytes.
   */
  actionEntryId: string
  /**
   * What the call named, or null when the tool names no resource in its parameters at all (git.status's repository IS the grant's path scope). The ONE derivation, shared with the policy's scope check and the approval ask.
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
}
