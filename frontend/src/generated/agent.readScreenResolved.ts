/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.readScreenResolved.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.readScreenResolved RPC (nocx-ljfwz, design §2.4): the renderer answers an agent.readScreenRequest with a closed outcome. outcome=frame carries renderer-owned text rows, cursor, buffer identity and range; outcome=failed carries why the frame could not be produced, so the run receives an honest answer instead of hanging. requestId is the broker-minted id from the request, echoed back.
 */
export type AgentReadScreenResolved = {
  [k: string]: unknown
} & {
  /**
   * The request id from agent.readScreenRequest.
   */
  requestId: string
  /**
   * frame: the capture succeeded and the frame fields follow. failed: the capture could not be produced and error names why.
   */
  outcome: 'frame' | 'failed'
  /**
   * The failure sentence, present only when outcome is failed.
   */
  error?: string
  /**
   * The frame's rows, in order; each is the row's TEXT — the cells' characters joined in column order, blanks kept, so the row's width is the screen's. The renderer keeps the cells locally to DRAW with; no consumer of this resolution reads a cell attribute (nocx-u3vxd).
   */
  rows?: {
    kind: 'text'
    /**
     * The row's characters. Bounded at eight runes per column of the widest permitted geometry.
     */
    text: string
  }[]
  /**
   * The cursor's absolute buffer line and column at capture time.
   */
  cursor?: {
    line: number
    col: number
  }
  /**
   * The capture identity: which buffer instance, the geometry, and the content generation the frame belongs to.
   */
  identity?: {
    buffer: {
      kind: 'normal' | 'alternate'
      altSession?: number | null
    }
    cols: number
    rows: number
    generation: number
  }
  /**
   * The absolute buffer row span the frame's rows cover: [start, end), end - start == rows.length.
   */
  range?: {
    start: number
    end: number
  }
}
