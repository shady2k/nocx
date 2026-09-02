/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sessions.inventory.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Sessions currently held by the authenticated helper generations known to this coordinator.
 */
export interface SessionsInventoryResult {
  /**
   * The helper-owned sessions. An empty array is an answered empty inventory, never a missing answer.
   */
  sessions: SessionEntry[]
}
export interface SessionEntry {
  hostSessionId: {
    generation: string
    session: string
  }
  workspace: string
  startedAt: string
  launch: {
    shell: string
    cwd: string
    pid: number
    pgid: number
    cols: number
    rows: number
    windowBytes: number
  }
  observed: {
    source: string
    cwd?: string
    argv?: string[]
    foregroundPgid?: number
    foregroundCommand?: string
    unavailable: string[]
  } | null
  window: {
    base: number
    written: number
  }
  lifecycleWindow: {
    base: number
    written: number
  }
  writer: string | null
  writerEpoch: number
  exit: {
    code: number
    signal?: number
    at: string
  } | null
}
