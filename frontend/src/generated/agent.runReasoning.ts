/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.runReasoning.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.runReasoning server-to-client notification (nocx-s92so, design §7): one chunk of the model's THINKING — eino's schema.Message.ReasoningContent, which the OpenAI-compatible adapter fills from the wire's reasoning_content. Its own notification, never mixed into agent.runDelta: an answer that concatenates thinking with the answer is the same defect as a tool result rendered as prose (nocx-bshm2). A model that returns no reasoning sends none of these, and the renderer must show nothing at all for it — no empty section, no placeholder. Deliberately NOT persisted: the durable answer is the answer, and appending the thinking to the answer artifact would put it back inside the answer by another route. So this is a live-view notification with no seq — arrival order over the one socket is its only order — and a chunk the wire refuses is a live-view gap the run's terminal agent.runState reports through droppedDeltas.
 */
export interface AgentRunReasoning {
  /**
   * The backend-minted run id the reasoning belongs to.
   */
  runId: number
  /**
   * The TURN the reasoning belongs to — agent.ask result's entryId, the same routing key agent.runDelta carries. The reasoning is NOT persisted (nocx-4em1z, the owner's call): it is streamed, drawn and gone, so a restored turn has no reasoning note at all rather than an empty one.
   */
  entryId: string
  /**
   * The chunk of reasoning text to append.
   */
  text: string
}
