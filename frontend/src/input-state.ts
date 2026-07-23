// Input-ownership state machine (ADR-0004 §1). nocx owns keyboard input ONLY in
// PROMPT_READY; every other state routes keys raw to the PTY. Enhanced states are
// reachable ONLY from OSC 133 markers and the xterm alt-buffer event — never
// inferred from bytes/termios. Fail-open: anything unexpected falls back to RAW.
export type InputState = 'RAW' | 'PROMPT_READY' | 'RUNNING_RAW' | 'ALT_SCREEN'

export type InputEvent =
  | { type: 'marker'; kind: 'A' | 'B' | 'C' | 'D' }
  | { type: 'buffer'; buffer: 'normal' | 'alternate' }
  | { type: 'submit' }
  | { type: 'reset' }
  | { type: 'exit' }

export interface Machine {
  state: InputState
  // The current prompt→C→D cycle reached C through a clean A/B. Consumers gate
  // block actions (e.g. re-run) on this; anomalies clear it (Task 3).
  trusted: boolean
}

export function initialMachine(): Machine {
  return { state: 'RAW', trusted: false }
}

export function reduce(m: Machine, e: InputEvent): Machine {
  switch (e.type) {
    case 'buffer':
      return e.buffer === 'alternate'
        ? { state: 'ALT_SCREEN', trusted: false }
        : { state: 'RAW', trusted: false }
    case 'reset':
    case 'exit':
      return { state: 'RAW', trusted: false }
    case 'submit':
      return { state: 'RUNNING_RAW', trusted: m.trusted }
    case 'marker':
      switch (e.kind) {
        case 'A':
          return { state: 'PROMPT_READY', trusted: true }
        case 'B':
          return { state: 'PROMPT_READY', trusted: m.trusted }
        case 'C':
          return { state: 'RUNNING_RAW', trusted: m.trusted }
        case 'D':
          return { state: 'RAW', trusted: m.trusted }
      }
  }
}
