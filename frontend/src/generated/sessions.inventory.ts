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
/**
 * One session a helper is holding. Its process is described by a UNION of two launch records — `launch` for a PTY the helper forked on its own machine, `remoteLaunch` for a shell channel it dialed — and exactly one is present. The other is ABSENT rather than filled in with zeros: a `launch` record beside a remote one would carry pid 0, which is the kernel's scheduler rather than a process on this machine, and a reader that trusted it would ask the OS about something nobody started (nocx-s8mfn, D10's 'the launch record is the authority').
 */
export interface SessionEntry {
  hostSessionId: {
    generation: string
    session: string
  }
  workspace: string
  startedAt: string
  /**
   * The LOCAL branch: what the helper recorded at the moment it forked this session's process. Absent when `remoteLaunch` is present.
   */
  launch?: {
    shell: string
    cwd: string
    pid: number
    pgid: number
    cols: number
    rows: number
    windowBytes: number
  }
  /**
   * The SSH branch: the destination the helper resolved and dialed, echoed so a reader of the inventory knows which machine this pane is on. The identity is a REFERENCE and never material — the credential's opaque handle, which the helper does not interpret.
   */
  remoteLaunch?: {
    host: string
    port: number
    user: string
    identityRef: string
    /**
     * The tier this session was launched FOR, from the closed set internal/helper/proto's SSHShellKind owns: `auto` when the profile pinned nothing, which is the honest value because the far side's own dispatcher decides which tier runs and its answer is not reported back. The set is deliberately NOT repeated here — one owner, and a copy would be a second vocabulary to keep in step.
     */
    shell: string
    /**
     * Empty, always, in this generation: the far login shell's directory is the far side's answer and this helper neither asks for it nor changes it. Present rather than omitted so a reader cannot mistake 'nobody resolved a directory here' for a wire that does not carry one.
     */
    cwd: string
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
