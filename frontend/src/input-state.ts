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
          // Fresh prompt. Trusted only when we arrived cleanly (from RAW after a
          // finished command or from initial). An A that interrupts RUNNING_RAW
          // (no D, or a nested prompt) is a resync: PROMPT_READY but untrusted.
          return { state: 'PROMPT_READY', trusted: m.state !== 'RUNNING_RAW' }
        case 'B':
          // B only means "input ready" when we are already at a prompt.
          return { state: 'PROMPT_READY', trusted: m.state === 'PROMPT_READY' && m.trusted }
        case 'C':
          // Command start. Trusted only if a clean prompt preceded it; an orphan
          // or nested C runs raw but disables downstream actions.
          return { state: 'RUNNING_RAW', trusted: m.state === 'PROMPT_READY' && m.trusted }
        case 'D':
          // Finished — only meaningful while a command is running. Orphan D
          // (e.g. empty Enter emits D with no preceding C) is ignored.
          return m.state === 'RUNNING_RAW' ? { state: 'RAW', trusted: m.trusted } : m
      }
  }
}
