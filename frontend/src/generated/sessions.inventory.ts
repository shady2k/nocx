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
    startTime?: string
    ppid?: number
    /**
     * The kernel's process state, normalised by the helper into one closed vocabulary. See contracts/helper/identities.schema.json for why it is normalised rather than carried in each kernel's own spelling.
     */
    state?: 'running' | 'sleeping' | 'uninterruptible' | 'stopped' | 'zombie'
    /**
     * Every diagnostic above that the inspector was asked for and could not supply. Always present, [] when everything was answered. A reader that ignores it falls back to the launch record and presents a value that was true once as a current observation.
     */
    unavailable: ('cwd' | 'argv' | 'foregroundCommand' | 'startTime' | 'ppid' | 'state')[]
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
