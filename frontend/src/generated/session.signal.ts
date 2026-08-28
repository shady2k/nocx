/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.signal.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The session.signal JSON-RPC result: what happened when a person's UI addressed a signal to the command running in one session (nocx-23rph, nocx-92gfl.4). THE GESTURE IS ADDRESSED TO THE SESSION, NOT TO WHATEVER HOLDS FOCUS. The primary route is the PTY foreground process group from TIOCGPGRP. Some foreground programs share the protected launcher-shell group; when that guard says nothing-running but the authenticated lifecycle names an exact open attempt, the backend delivers the terminal's ordinary 0x03 byte instead and never signals the shell group directly.
 */
export interface SessionSignal {
  /**
   * The signal that was asked for, echoed back. interrupt is one SIGINT — through the foreground process group, or the terminal's equivalent 0x03 byte for a lifecycle-confirmed shared shell group. stop uses the established SIGINT -> SIGTERM -> SIGKILL ladder when an independent foreground group exists. On the shared-group fallback it sends 0x03 and waits, bounded by the same cooperative grace, for that exact lifecycle attempt to end; it never escalates into the launcher shell.
   */
  signal: 'interrupt' | 'stop'
  /**
   * delivered: the signal/process-group route succeeded, or the lifecycle-confirmed terminal interrupt was written (and, for stop, the exact attempt ended before the bound). nothing-running: the PTY foreground group is the interactive shell's own and no authenticated running attempt exists. unsupported: the session is remote and this process cannot reach its host-side group. unreconciled: lifecycle still names an execution, but nocx could not prove it ended through the only route that preserves the launcher-shell guard. The set is closed because the renderer branches on it to decide what to say.
   */
  outcome: 'delivered' | 'nothing-running' | 'unsupported' | 'unreconciled'
}
